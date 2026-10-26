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

var MaxTeamsPerTenant = 5000

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
	TeamTypeID, OperatingUnitID, Status string
	AsOf                                *time.Time
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
	var status string
	err := tx.QueryRow(ctx, `select status from operating_unit where tenant_id = $1 and id = $2`, tenantID, *in.OperatingUnitID).Scan(&status)
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
			order by code limit $7`, p.TenantID, afterCode, f.TeamTypeID, f.OperatingUnitID, f.Status, f.AsOf, limit+1)
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
