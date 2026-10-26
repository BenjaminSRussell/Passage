package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

const (
	CodeTeamTypeCodeDuplicate = "ORG-TEAM-TYPE-CODE-DUPLICATE"
	CodeTeamTypeNotFound      = "ORG-TEAM-TYPE-NOT-FOUND"
	teamTypeColumns           = "id, code, name, member_kinds, created_at, updated_at"

	CodeTeamTypeInvalid          = "ORG-TEAM-TYPE-INVALID"
	CodeTeamOperatingUnitInvalid = "ORG-TEAM-OPERATING-UNIT-INVALID"
	CodeTeamLimit                = "ORG-TEAM-LIMIT"
	CodeTeamCodeDuplicate        = "ORG-TEAM-CODE-DUPLICATE"
	CodeTeamNotFound             = "ORG-TEAM-NOT-FOUND"
	teamColumns                  = `id, code, name, description, team_type_id, operating_unit_id, to_char(effective_from, 'YYYY-MM-DD'),
		to_char(effective_to, 'YYYY-MM-DD'), status, version, created_at, updated_at`
)

const (
	CodeTeamMemberKindInvalid = "ORG-TEAM-MEMBER-KIND-INVALID"
	CodeTeamMemberRange       = "ORG-TEAM-MEMBER-RANGE"
	CodeTeamExpired           = "ORG-TEAM-EXPIRED"
	CodeTeamMemberLimit       = "ORG-TEAM-MEMBER-LIMIT"
	CodeTeamMemberDuplicate   = "ORG-TEAM-MEMBER-DUPLICATE"
	CodeTeamInactive          = "ORG-TEAM-INACTIVE"
	CodeTeamMemberNotFound    = "ORG-TEAM-MEMBER-NOT-FOUND"
	CodeTeamMemberRuleSourced = "ORG-TEAM-MEMBER-RULE-SOURCED"
	teamDateLayout            = "2006-01-02"
	teamMemberColumns         = `id, team_id, member_kind, member_id, member_role, to_char(valid_from, 'YYYY-MM-DD'),
		to_char(valid_to, 'YYYY-MM-DD'), source, created_at, updated_at`
)

var (
	MaxTeamsPerTenant = 5000
	MaxMembersPerTeam = 2000
)

type TeamType struct {
	ID          string    `json:"id"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	MemberKinds []string  `json:"memberKinds"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type TeamTypeFilter struct{ MemberKind string }

type TeamTypePage struct {
	Items    []TeamType
	NextCode string `json:"-"`
}

func scanTeamType(row pgx.Row, extra ...any) (TeamType, error) {
	var t TeamType
	err := row.Scan(append([]any{&t.ID, &t.Code, &t.Name, &t.MemberKinds, &t.CreatedAt, &t.UpdatedAt}, extra...)...)
	return t, err
}

func (s *Postgres) CreateTeamType(ctx context.Context, in TeamType, idempotencyKey string) (TeamType, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var t TeamType
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		if key != nil {
			replay, err := scanTeamType(tx.QueryRow(ctx, `select `+teamTypeColumns+` from team_type where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				t = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		t, err = scanTeamType(tx.QueryRow(ctx, `insert into team_type (tenant_id, id, code, name, member_kinds, idempotency_key)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, idempotency_key) do update set updated_at = team_type.updated_at
			returning `+teamTypeColumns+`, (xmax = 0)`, p.TenantID, ids.New(), in.Code, in.Name, in.MemberKinds, key), &inserted)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return TeamType{}, false, httpx.NewProblem(http.StatusConflict, CodeTeamTypeCodeDuplicate, "a team type with this code already exists")
	}
	if err == nil && key != nil && (t.Code != in.Code || t.Name != in.Name || !slices.Equal(t.MemberKinds, in.MemberKinds)) {
		return TeamType{}, false, idempotencyReuse()
	}
	return t, inserted, err
}

func (s *Postgres) GetTeamType(ctx context.Context, id string) (TeamType, error) {
	p, _ := tenant.FromContext(ctx)
	var t TeamType
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		t, err = scanTeamType(tx.QueryRow(ctx, `select `+teamTypeColumns+` from team_type where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamType{}, httpx.NewProblem(http.StatusNotFound, CodeTeamTypeNotFound, "team type not found")
	}
	return t, err
}

func (s *Postgres) ListTeamTypes(ctx context.Context, f TeamTypeFilter, afterCode string, limit int) (TeamTypePage, error) {
	p, _ := tenant.FromContext(ctx)
	page := TeamTypePage{Items: []TeamType{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+teamTypeColumns+` from team_type where tenant_id = $1 and code > $2
			and ($3::text = '' or $3 = any(member_kinds)) order by code limit $4`, p.TenantID, afterCode, f.MemberKind, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTeamType(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, t)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, err
}

type Team struct {
	ID              string    `json:"id"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	Description     *string   `json:"description,omitempty"`
	TeamTypeID      string    `json:"teamTypeId"`
	OperatingUnitID *string   `json:"operatingUnitId,omitempty"`
	EffectiveFrom   string    `json:"effectiveFrom"`
	EffectiveTo     *string   `json:"effectiveTo,omitempty"`
	Status          string    `json:"status"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type TeamFilter struct {
	TeamTypeID, OperatingUnitID, Status, MemberID string
	AsOf                                          *time.Time
}

type TeamPage struct {
	Items    []Team
	NextCode string `json:"-"`
}

func scanTeam(row pgx.Row, extra ...any) (Team, error) {
	var t Team
	err := row.Scan(append([]any{&t.ID, &t.Code, &t.Name, &t.Description, &t.TeamTypeID, &t.OperatingUnitID, &t.EffectiveFrom, &t.EffectiveTo, &t.Status, &t.Version, &t.CreatedAt, &t.UpdatedAt}, extra...)...)
	return t, err
}

func invalidTeamReferent(code, field, detail string) error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, code, detail)
	p.Errors = map[string]string{field: detail}
	return p
}

func checkTeamReferents(ctx context.Context, tx pgx.Tx, tenantID string, in Team) error {
	var exists bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from team_type where tenant_id = $1 and id = $2)`, tenantID, in.TeamTypeID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return invalidTeamReferent(CodeTeamTypeInvalid, "teamTypeId", "must be an existing team type")
	}
	if in.OperatingUnitID == nil {
		return nil
	}
	return checkTeamOperatingUnit(ctx, tx, tenantID, *in.OperatingUnitID)
}

func checkTeamOperatingUnit(ctx context.Context, tx pgx.Tx, tenantID, id string) error {
	var status string
	err := tx.QueryRow(ctx, `select status from operating_unit where tenant_id = $1 and id = $2`, tenantID, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && status != "active" {
		return invalidTeamReferent(CodeTeamOperatingUnitInvalid, "operatingUnitId", "must be an active operating unit")
	}
	return err
}

func checkTeamLimit(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('org-team-limit:' || $1::text, 0))`, tenantID); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `select count(*) from team where tenant_id = $1`, tenantID).Scan(&n); err != nil {
		return err
	}
	if n >= MaxTeamsPerTenant {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeTeamLimit, fmt.Sprintf("a tenant can have at most %d teams", MaxTeamsPerTenant))
	}
	return nil
}

func (s *Postgres) CreateTeam(ctx context.Context, in Team, idempotencyKey string) (Team, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var t Team
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		if key != nil {
			replay, err := scanTeam(tx.QueryRow(ctx, `select `+teamColumns+` from team where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				t = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if err := checkTeamReferents(ctx, tx, p.TenantID, in); err != nil {
			return err
		}
		if err := checkTeamLimit(ctx, tx, p.TenantID); err != nil {
			return err
		}
		t, err = scanTeam(tx.QueryRow(ctx, `insert into team (tenant_id, id, code, name, description, team_type_id, operating_unit_id, effective_from, effective_to, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8::date, $9::date, $10)
			on conflict (tenant_id, idempotency_key) do update set updated_at = team.updated_at
			returning `+teamColumns+`, (xmax = 0)`, p.TenantID, ids.New(), in.Code, in.Name, in.Description, in.TeamTypeID, in.OperatingUnitID, in.EffectiveFrom, in.EffectiveTo, key), &inserted)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Team{}, false, httpx.NewProblem(http.StatusConflict, CodeTeamCodeDuplicate, "a team with this code already exists")
	}
	if err == nil && key != nil && (t.Code != in.Code || t.Name != in.Name || deref(t.Description) != deref(in.Description) || t.TeamTypeID != in.TeamTypeID ||
		deref(t.OperatingUnitID) != deref(in.OperatingUnitID) || t.EffectiveFrom != in.EffectiveFrom || deref(t.EffectiveTo) != deref(in.EffectiveTo)) {
		return Team{}, false, idempotencyReuse()
	}
	return t, inserted, err
}

func (s *Postgres) GetTeam(ctx context.Context, id string) (Team, error) {
	p, _ := tenant.FromContext(ctx)
	var t Team
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		t, err = scanTeam(tx.QueryRow(ctx, `select `+teamColumns+` from team where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, httpx.NewProblem(http.StatusNotFound, CodeTeamNotFound, "team not found")
	}
	return t, err
}

func (s *Postgres) ListTeams(ctx context.Context, f TeamFilter, afterCode string, limit int) (TeamPage, error) {
	p, _ := tenant.FromContext(ctx)
	page := TeamPage{Items: []Team{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+teamColumns+` from team where tenant_id = $1 and code > $2
			and ($3::text = '' or team_type_id::text = $3) and ($4::text = '' or operating_unit_id::text = $4) and ($5::text = '' or status = $5)
			and ($6::date is null or (effective_from <= $6::date and (effective_to is null or effective_to >= $6::date)))
			and ($8::text = '' or exists (select 1 from team_member m where m.tenant_id = team.tenant_id and m.team_id = team.id and m.member_id::text = $8
				and ($6::date is null or (m.valid_from <= $6::date and (m.valid_to is null or m.valid_to >= $6::date)))))
			order by code limit $7`, p.TenantID, afterCode, f.TeamTypeID, f.OperatingUnitID, f.Status, f.AsOf, limit+1, f.MemberID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTeam(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, t)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, err
}

type TeamMember struct {
	ID         string    `json:"id"`
	TeamID     string    `json:"teamId"`
	MemberKind string    `json:"memberKind"`
	MemberID   string    `json:"memberId"`
	MemberRole string    `json:"memberRole"`
	ValidFrom  string    `json:"validFrom"`
	ValidTo    *string   `json:"validTo,omitempty"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type TeamMemberFilter struct {
	AsOf                   *time.Time
	MemberKind, MemberRole string
}

type TeamMemberPage struct {
	Items  []TeamMember
	NextID string `json:"-"`
}

func scanTeamMember(row pgx.Row, extra ...any) (TeamMember, error) {
	var m TeamMember
	err := row.Scan(append([]any{&m.ID, &m.TeamID, &m.MemberKind, &m.MemberID, &m.MemberRole, &m.ValidFrom, &m.ValidTo, &m.Source, &m.CreatedAt, &m.UpdatedAt}, extra...)...)
	return m, err
}

func teamNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeTeamNotFound, "team not found")
}

func checkTeamMemberRange(in TeamMember, code string, from time.Time, to *time.Time, today time.Time) error {
	validFrom, err := time.Parse(teamDateLayout, in.ValidFrom)
	if err != nil {
		return err
	}
	var validTo *time.Time
	if in.ValidTo != nil {
		v, err := time.Parse(teamDateLayout, *in.ValidTo)
		if err != nil {
			return err
		}
		validTo = &v
	}
	switch domain.MemberRangeViolation(from, to, validFrom, validTo, today) {
	case "expired":
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeTeamExpired, fmt.Sprintf("Team %s has ended.", code))
	case "range":
		field := "validTo"
		if !domain.TeamInForce(from, to, validFrom) {
			field = "validFrom"
		}
		return invalidTeamReferent(CodeTeamMemberRange, field, "Member validity must be inside the team's effective range.")
	}
	return nil
}

func checkTeamMemberLimit(ctx context.Context, tx pgx.Tx, tenantID, teamID string, today time.Time) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('org-team-member-limit:' || $1::text, 0))`, teamID); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `select count(*) from team_member where tenant_id = $1 and team_id = $2 and (valid_to is null or valid_to >= $3::date)`,
		tenantID, teamID, today.Format(teamDateLayout)).Scan(&n); err != nil {
		return err
	}
	if n >= MaxMembersPerTeam {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeTeamMemberLimit, fmt.Sprintf("a team can have at most %d members", MaxMembersPerTeam))
	}
	return nil
}

func (s *Postgres) AddTeamMember(ctx context.Context, teamID string, in TeamMember, today time.Time, idempotencyKey string) (TeamMember, bool, error) {
	p, _ := tenant.FromContext(ctx)
	if in.MemberRole == "" {
		in.MemberRole = "member"
	}
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var m TeamMember
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		if key != nil {
			replay, err := scanTeamMember(tx.QueryRow(ctx, `select `+teamMemberColumns+` from team_member where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				m = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var code, status string
		var from time.Time
		var to *time.Time
		var kinds []string
		err = tx.QueryRow(ctx, `select t.code, t.status, t.effective_from, t.effective_to, tt.member_kinds from team t
			join team_type tt on tt.tenant_id = t.tenant_id and tt.id = t.team_type_id
			where t.tenant_id = $1 and t.id = $2 for update of t`, p.TenantID, teamID).Scan(&code, &status, &from, &to, &kinds)
		if errors.Is(err, pgx.ErrNoRows) {
			return teamNotFound()
		}
		if err != nil {
			return err
		}
		if status != "active" {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeTeamInactive, fmt.Sprintf("Team %s is inactive.", code))
		}
		if !slices.Contains(kinds, in.MemberKind) {
			return invalidTeamReferent(CodeTeamMemberKindInvalid, "memberKind", fmt.Sprintf("team %s does not accept %s members", code, in.MemberKind))
		}
		if err := checkTeamMemberRange(in, code, from, to, today); err != nil {
			return err
		}
		if err := checkTeamMemberLimit(ctx, tx, p.TenantID, teamID, today); err != nil {
			return err
		}
		m, err = scanTeamMember(tx.QueryRow(ctx, `insert into team_member (tenant_id, id, team_id, member_kind, member_id, member_role, valid_from, valid_to, source, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7::date, $8::date, 'manual', $9)
			on conflict (tenant_id, idempotency_key) do update set updated_at = team_member.updated_at
			returning `+teamMemberColumns+`, (xmax = 0)`, p.TenantID, ids.New(), teamID, in.MemberKind, in.MemberID, in.MemberRole, in.ValidFrom, in.ValidTo, key), &inserted)
		if err != nil || !inserted {
			return err
		}
		return enqueue(ctx, tx, "team", teamID, orgevents.TeamMembershipChanged, p.TenantID, orgevents.TeamMembershipChangedData{
			TeamID:  teamID,
			Added:   []orgevents.TeamMemberRef{{MemberKind: m.MemberKind, MemberID: m.MemberID, MemberRole: m.MemberRole}},
			Removed: []orgevents.TeamMemberRef{},
			AsOf:    m.ValidFrom,
		})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return TeamMember{}, false, httpx.NewProblem(http.StatusConflict, CodeTeamMemberDuplicate, "this member is already on the team from this date")
	}
	if err == nil && key != nil && (m.TeamID != teamID || m.MemberKind != in.MemberKind || m.MemberID != in.MemberID || m.MemberRole != in.MemberRole ||
		m.ValidFrom != in.ValidFrom || deref(m.ValidTo) != deref(in.ValidTo)) {
		return TeamMember{}, false, idempotencyReuse()
	}
	return m, inserted, err
}

func (s *Postgres) ListTeamMembers(ctx context.Context, teamID string, f TeamMemberFilter, afterID string, limit int) (TeamMemberPage, error) {
	p, _ := tenant.FromContext(ctx)
	page := TeamMemberPage{Items: []TeamMember{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from team where tenant_id = $1 and id = $2)`, p.TenantID, teamID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return teamNotFound()
		}
		rows, err := tx.Query(ctx, `select `+teamMemberColumns+` from team_member where tenant_id = $1 and team_id = $2
			and ($3::text = '' or id > $3::uuid) and ($4::text = '' or member_kind = $4) and ($5::text = '' or member_role = $5)
			and ($6::date is null or (valid_from <= $6::date and (valid_to is null or valid_to >= $6::date)))
			order by id limit $7`, p.TenantID, teamID, afterID, f.MemberKind, f.MemberRole, f.AsOf, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanTeamMember(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, m)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextID = page.Items[limit-1].ID
	}
	return page, err
}

type TeamPatch struct {
	Name            *string `json:"name"`
	Description     *string `json:"description"`
	OperatingUnitID *string `json:"operatingUnitId"`
	EffectiveTo     *string `json:"effectiveTo"`
	Status          *string `json:"status"`
}

func (in TeamPatch) apply(t Team) (Team, []string) {
	var changed []string
	var did bool
	if t.Description, did = applyID(t.Description, in.Description); did {
		changed = append(changed, "description")
	}
	if t.OperatingUnitID, did = applyID(t.OperatingUnitID, in.OperatingUnitID); did {
		changed = append(changed, "operating_unit_id")
	}
	if t.EffectiveTo, did = applyID(t.EffectiveTo, in.EffectiveTo); did {
		changed = append(changed, "effective_to")
	}
	if in.Name != nil && *in.Name != t.Name {
		t.Name = *in.Name
		changed = append(changed, "name")
	}
	if in.Status != nil && *in.Status != t.Status {
		t.Status = *in.Status
		changed = append(changed, "status")
	}
	return t, changed
}

func checkTeamEffectiveTo(ctx context.Context, tx pgx.Tx, tenantID, teamID, effectiveFrom, effectiveTo string) error {
	if effectiveTo < effectiveFrom {
		bad := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		bad.Errors = map[string]string{"effectiveTo": "must not be before effectiveFrom"}
		return bad
	}
	var n int
	if err := tx.QueryRow(ctx, `select count(*) from team_member where tenant_id = $1 and team_id = $2 and (valid_to is null or valid_to > $3::date)`,
		tenantID, teamID, effectiveTo).Scan(&n); err != nil || n == 0 {
		return err
	}
	return invalidTeamReferent(CodeTeamMemberRange, "effectiveTo", fmt.Sprintf("%d members are valid after %s; end them first.", n, effectiveTo))
}

func (s *Postgres) UpdateTeam(ctx context.Context, id string, version int, in TeamPatch) (Team, error) {
	p, _ := tenant.FromContext(ctx)
	var t Team
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanTeam(tx.QueryRow(ctx, `select `+teamColumns+` from team where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return teamNotFound()
		}
		if err != nil {
			return err
		}
		t = current
		if current.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, fmt.Sprintf("current version is %d", current.Version))
		}
		next, changed := in.apply(current)
		if len(changed) == 0 {
			return nil
		}
		if next.OperatingUnitID != nil && deref(next.OperatingUnitID) != deref(current.OperatingUnitID) {
			if err := checkTeamOperatingUnit(ctx, tx, p.TenantID, *next.OperatingUnitID); err != nil {
				return err
			}
		}
		if next.EffectiveTo != nil && deref(next.EffectiveTo) != deref(current.EffectiveTo) {
			if err := checkTeamEffectiveTo(ctx, tx, p.TenantID, id, current.EffectiveFrom, *next.EffectiveTo); err != nil {
				return err
			}
		}
		t, err = scanTeam(tx.QueryRow(ctx, `update team set name = $3, description = $4, operating_unit_id = $5, effective_to = $6::date, status = $7, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+teamColumns, p.TenantID, id, next.Name, next.Description, next.OperatingUnitID, next.EffectiveTo, next.Status))
		return err
	})
	if err != nil {
		return Team{}, err
	}
	return t, nil
}

func lockTeamMember(ctx context.Context, tx pgx.Tx, tenantID, teamID, memberRowID string) (TeamMember, error) {
	var locked string
	err := tx.QueryRow(ctx, `select id from team where tenant_id = $1 and id = $2 for update`, tenantID, teamID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamMember{}, teamNotFound()
	}
	if err != nil {
		return TeamMember{}, err
	}
	m, err := scanTeamMember(tx.QueryRow(ctx, `select `+teamMemberColumns+` from team_member where tenant_id = $1 and team_id = $2 and id = $3 for update`, tenantID, teamID, memberRowID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamMember{}, httpx.NewProblem(http.StatusNotFound, CodeTeamMemberNotFound, "team member not found")
	}
	return m, err
}

func enqueueMemberRemoved(ctx context.Context, tx pgx.Tx, tenantID string, m TeamMember, asOf string) error {
	return enqueue(ctx, tx, "team", m.TeamID, orgevents.TeamMembershipChanged, tenantID, orgevents.TeamMembershipChangedData{
		TeamID:  m.TeamID,
		Added:   []orgevents.TeamMemberRef{},
		Removed: []orgevents.TeamMemberRef{{MemberKind: m.MemberKind, MemberID: m.MemberID, MemberRole: m.MemberRole}},
		AsOf:    asOf,
	})
}

func (s *Postgres) EndTeamMember(ctx context.Context, teamID, memberRowID string, validTo time.Time) (TeamMember, bool, error) {
	p, _ := tenant.FromContext(ctx)
	end := validTo.Format(teamDateLayout)
	var m TeamMember
	var changed bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := lockTeamMember(ctx, tx, p.TenantID, teamID, memberRowID)
		if err != nil {
			return err
		}
		m = current
		if end < current.ValidFrom {
			return invalidTeamReferent(CodeTeamMemberRange, "validTo", "must not be before validFrom")
		}
		if current.ValidTo != nil && end == *current.ValidTo {
			return nil
		}
		if current.ValidTo != nil && end > *current.ValidTo {
			return invalidTeamReferent(CodeTeamMemberRange, "validTo", "an end date can only be moved earlier")
		}
		m, err = scanTeamMember(tx.QueryRow(ctx, `update team_member set valid_to = $4::date, updated_at = now()
			where tenant_id = $1 and team_id = $2 and id = $3 returning `+teamMemberColumns, p.TenantID, teamID, memberRowID, end))
		if err != nil {
			return err
		}
		changed = true
		return enqueueMemberRemoved(ctx, tx, p.TenantID, m, validTo.AddDate(0, 0, 1).Format(teamDateLayout))
	})
	if err != nil {
		return TeamMember{}, false, err
	}
	return m, changed, nil
}

func (s *Postgres) RemoveTeamMember(ctx context.Context, teamID, memberRowID string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		m, err := lockTeamMember(ctx, tx, p.TenantID, teamID, memberRowID)
		if err != nil {
			return err
		}
		if m.Source != "manual" {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeTeamMemberRuleSourced, "a rule-sourced member can only be removed by changing the rule")
		}
		if _, err := tx.Exec(ctx, `delete from team_member where tenant_id = $1 and team_id = $2 and id = $3`, p.TenantID, teamID, memberRowID); err != nil {
			return err
		}
		return enqueueMemberRemoved(ctx, tx, p.TenantID, m, m.ValidFrom)
	})
}
