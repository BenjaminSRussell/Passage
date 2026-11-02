package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

// Skill problem codes (FR-42.10, FR-42.18). None is in the 8.42.18 catalog;
// the service README lists them. Rating and scale errors are
// domain.CodeSkillRating.
const (
	CodeSkillNotFound          = "HCM-SKL-NOT-FOUND"
	CodeSkillDuplicate         = "HCM-SKL-DUPLICATE"
	CodeSkillStale             = "HCM-SKL-STALE"
	CodeRequirementNotFound    = "HCM-SKL-REQ-NOT-FOUND"
	CodeCertificationDuplicate = "HCM-CERT-DUPLICATE"

	// MaxSearchResults bounds a skill search (8.42.19: limit 500 results).
	MaxSearchResults = 500
	// maxWorkerSkillRows bounds a worker's profile and certificate lists.
	maxWorkerSkillRows = 500
)

// Skill is a catalog entry with its rating scale 1..ScaleMax (FR-42.10).
type Skill struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	ScaleMax  int       `json:"scaleMax"`
	CreatedAt time.Time `json:"createdAt"`
}

// SkillPage is one keyset page of the catalog ordered by code.
type SkillPage struct {
	Items     []Skill
	NextAfter string
}

// WorkerSkill is a worker's current rating on one skill.
type WorkerSkill struct {
	WorkerID   string    `json:"workerId"`
	SkillID    string    `json:"skillId"`
	SkillCode  string    `json:"skillCode"`
	SkillName  string    `json:"skillName"`
	ScaleMax   int       `json:"scaleMax"`
	Rating     int       `json:"rating"`
	AssessedOn string    `json:"assessedOn"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Certification is a certificate a worker holds, valid on [IssuedOn,
// ExpiresOn). Valid is computed as-of the read and is not stored.
type Certification struct {
	ID        string  `json:"id"`
	WorkerID  string  `json:"workerId"`
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Issuer    *string `json:"issuer,omitempty"`
	IssuedOn  string  `json:"issuedOn"`
	ExpiresOn *string `json:"expiresOn,omitempty"`
	Valid     bool    `json:"valid"`
}

// ExpiringCertification is one row of the "certifications expiring" inquiry
// (8.42.14): the certificate with its holder.
type ExpiringCertification struct {
	Certification
	PersonnelNumber string `json:"personnelNumber"`
	LegalName       string `json:"legalName"`
	DaysLeft        int    `json:"daysLeft"`
}

// CertificationPage is one keyset page of expiring certificates ordered by
// expiry, then id.
type CertificationPage struct {
	Items     []ExpiringCertification
	NextAfter string
}

// PositionRequirement is one skill a position needs at a minimum rating.
type PositionRequirement struct {
	PositionID string `json:"positionId"`
	SkillID    string `json:"skillId"`
	SkillCode  string `json:"skillCode"`
	SkillName  string `json:"skillName"`
	ScaleMax   int    `json:"scaleMax"`
	MinRating  int    `json:"minRating"`
}

// SkillGap compares a worker with a position's skill profile (FR-42.18).
// Comparison has every requirement; Gaps only the unmet ones (AC-42.16).
type SkillGap struct {
	WorkerID   string          `json:"workerId"`
	PositionID string          `json:"positionId,omitempty"`
	JobID      string          `json:"jobId,omitempty"`
	Met        bool            `json:"met"`
	Comparison []domain.GapRow `json:"comparison"`
	Gaps       []domain.GapRow `json:"gaps"`
}

// SkillMatch is one worker found by a skill search, with each searched skill
// next to the worker's rating.
type SkillMatch struct {
	WorkerID        string          `json:"workerId"`
	PersonnelNumber string          `json:"personnelNumber"`
	LegalName       string          `json:"legalName"`
	Skills          []domain.GapRow `json:"skills"`
}

// SkillSearch is the result of a skill search. Truncated is true when more
// than MaxSearchResults workers matched.
type SkillSearch struct {
	Items     []SkillMatch `json:"items"`
	Truncated bool         `json:"truncated"`
}

const skillColumns = `id::text, code, name, scale_max, created_at`

func sklScan(row pgx.Row) (Skill, error) {
	var k Skill
	err := row.Scan(&k.ID, &k.Code, &k.Name, &k.ScaleMax, &k.CreatedAt)
	k.CreatedAt = utc(k.CreatedAt)
	return k, err
}

func sklByCode(ctx context.Context, tx pgx.Tx, tenantID, code string) (Skill, error) {
	k, err := sklScan(tx.QueryRow(ctx, `select `+skillColumns+` from hr_skill where tenant_id = $1 and code = $2`, tenantID, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, problem(http.StatusNotFound, CodeSkillNotFound, "no skill with code "+code+" in the catalog")
	}
	return k, err
}

func sklRequireWorker(ctx context.Context, tx pgx.Tx, tenantID, workerID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, tenantID, workerID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	return nil
}

// sklRequirePosition checks the position is visible: the legal-entity scope
// hides positions of other entities, which then read as not found.
func sklRequirePosition(ctx context.Context, tx pgx.Tx, tenantID, positionID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from hr_position where tenant_id = $1 and id = $2)`, tenantID, positionID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return problem(http.StatusNotFound, CodePositionNotFound, "position not found")
	}
	return nil
}

func sklProblem(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return problem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return err
}

// CreateSkill adds a catalog skill. A replayed key with the same skill returns
// the stored one with inserted false; with a different skill it is refused.
func (s *Postgres) CreateSkill(ctx context.Context, in Skill, idempotencyKey string) (Skill, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out Skill
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := sklScan(tx.QueryRow(ctx, `select `+skillColumns+` from hr_skill where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var err error
		out, err = sklScan(tx.QueryRow(ctx, `insert into hr_skill (tenant_id, id, code, name, scale_max, idempotency_key)
			values ($1, $2, $3, $4, $5, $6) returning `+skillColumns,
			p.TenantID, ids.New(), in.Code, in.Name, in.ScaleMax, key))
		if err != nil {
			return err
		}
		inserted = true
		return nil
	})
	if idempotencyRace(err) {
		return Skill{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return Skill{}, false, problem(http.StatusConflict, CodeSkillDuplicate, "a skill with this code already exists")
	}
	if err == nil && !inserted && (out.Code != in.Code || out.Name != in.Name || out.ScaleMax != in.ScaleMax) {
		return Skill{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// ListSkills pages the catalog by code.
func (s *Postgres) ListSkills(ctx context.Context, after string, limit int) (SkillPage, error) {
	p := principal(ctx)
	page := SkillPage{Items: []Skill{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+skillColumns+` from hr_skill where tenant_id = $1 and code > $2 order by code limit $3`,
			p.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			k, err := sklScan(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, k)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfter = page.Items[limit-1].Code
	}
	return page, err
}

const workerSkillSelect = `select ws.worker_id::text, k.id::text, k.code, k.name, k.scale_max, ws.rating,
	to_char(ws.assessed_on, 'YYYY-MM-DD'), ws.updated_at
	from worker_skill ws join hr_skill k on k.tenant_id = ws.tenant_id and k.id = ws.skill_id`

func sklScanWorkerSkill(row pgx.Row) (WorkerSkill, error) {
	var w WorkerSkill
	err := row.Scan(&w.WorkerID, &w.SkillID, &w.SkillCode, &w.SkillName, &w.ScaleMax, &w.Rating, &w.AssessedOn, &w.UpdatedAt)
	w.UpdatedAt = utc(w.UpdatedAt)
	return w, err
}

// RateWorkerSkill records the worker's current rating on a skill (FR-42.10),
// replacing the earlier one. The rating must sit on the skill's scale. An
// assessment older than the stored one is refused (HCM-SKL-STALE) so a late
// retry cannot overwrite a newer rating; the same rating and date again is a
// no-op that returns changed false and publishes nothing.
func (s *Postgres) RateWorkerSkill(ctx context.Context, workerID, skillCode string, rating int, assessedOn time.Time) (WorkerSkill, bool, error) {
	p := principal(ctx)
	var out WorkerSkill
	changed := false
	day := assessedOn.Format(absDateLayout)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialise rating writes per worker so the stale check and the
		// previous rating in the event are exact.
		if _, err := absLockWorker(ctx, tx, p.TenantID, workerID); err != nil {
			return err
		}
		skill, err := sklByCode(ctx, tx, p.TenantID, skillCode)
		if err != nil {
			return err
		}
		if err := domain.ValidateRating(rating, skill.ScaleMax); err != nil {
			return sklProblem(err)
		}
		var prevRating *int
		var prevDay string
		err = tx.QueryRow(ctx, `select rating, to_char(assessed_on, 'YYYY-MM-DD') from worker_skill where tenant_id = $1 and worker_id = $2 and skill_id = $3`,
			p.TenantID, workerID, skill.ID).Scan(&prevRating, &prevDay)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if prevRating != nil {
			if prevDay > day {
				return problem(http.StatusConflict, CodeSkillStale, "the worker has a newer assessment of this skill, dated "+prevDay)
			}
			if *prevRating == rating && prevDay == day {
				out, err = sklScanWorkerSkill(tx.QueryRow(ctx, workerSkillSelect+` where ws.tenant_id = $1 and ws.worker_id = $2 and ws.skill_id = $3`,
					p.TenantID, workerID, skill.ID))
				return err
			}
		}
		if _, err := tx.Exec(ctx, `insert into worker_skill (tenant_id, worker_id, skill_id, rating, assessed_on)
			values ($1, $2, $3, $4, $5::date)
			on conflict (tenant_id, worker_id, skill_id) do update
			set rating = excluded.rating, assessed_on = excluded.assessed_on, updated_at = now()`,
			p.TenantID, workerID, skill.ID, rating, day); err != nil {
			return err
		}
		out, err = sklScanWorkerSkill(tx.QueryRow(ctx, workerSkillSelect+` where ws.tenant_id = $1 and ws.worker_id = $2 and ws.skill_id = $3`,
			p.TenantID, workerID, skill.ID))
		if err != nil {
			return err
		}
		changed = true
		if prevRating != nil && *prevRating == rating {
			// A newer assessment confirming the same rating moves the date only.
			return nil
		}
		return enqueue(ctx, tx, "worker", workerID, hrevents.WorkerSkillChanged, p.TenantID, hrevents.WorkerSkillChangedData{
			WorkerID: workerID, SkillID: skill.ID, SkillCode: skill.Code, Rating: rating, PreviousRating: prevRating, AssessedOn: day,
		})
	})
	return out, changed, err
}

// ListWorkerSkills returns the worker's skill profile ordered by skill code.
func (s *Postgres) ListWorkerSkills(ctx context.Context, workerID string) ([]WorkerSkill, error) {
	p := principal(ctx)
	out := []WorkerSkill{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := sklRequireWorker(ctx, tx, p.TenantID, workerID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, workerSkillSelect+` where ws.tenant_id = $1 and ws.worker_id = $2 order by k.code limit $3`,
			p.TenantID, workerID, maxWorkerSkillRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			w, err := sklScanWorkerSkill(rows)
			if err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

const certificationColumns = `c.id::text, c.worker_id::text, c.code, c.name, c.issuer,
	to_char(c.issued_on, 'YYYY-MM-DD'), to_char(c.expires_on, 'YYYY-MM-DD')`

func sklScanCertification(row pgx.Row, extra ...any) (Certification, error) {
	var c Certification
	dest := append([]any{&c.ID, &c.WorkerID, &c.Code, &c.Name, &c.Issuer, &c.IssuedOn, &c.ExpiresOn}, extra...)
	err := row.Scan(dest...)
	return c, err
}

// validOn sets Valid as-of asOf.
func (c Certification) validOn(asOf time.Time) (Certification, error) {
	issued, err := time.Parse(absDateLayout, c.IssuedOn)
	if err != nil {
		return c, err
	}
	var expires *time.Time
	if c.ExpiresOn != nil {
		t, err := time.Parse(absDateLayout, *c.ExpiresOn)
		if err != nil {
			return c, err
		}
		expires = &t
	}
	c.Valid = domain.CertificationValid(issued, expires, asOf)
	return c, nil
}

func sameCertification(stored, in Certification) bool {
	return stored.WorkerID == in.WorkerID && stored.Code == in.Code && stored.Name == in.Name &&
		sameStringPtr(stored.Issuer, in.Issuer) && stored.IssuedOn == in.IssuedOn && sameStringPtr(stored.ExpiresOn, in.ExpiresOn)
}

// IssueCertification records a certificate for a worker (BR-42.8). The caller
// gives the expiry or none. A replayed key with the same certificate returns
// it with inserted false; with a different one (another worker, code or date)
// it is refused. The same worker, code and issue date twice is
// HCM-CERT-DUPLICATE (a unique constraint, so concurrent duplicates too).
func (s *Postgres) IssueCertification(ctx context.Context, in Certification, asOf time.Time, idempotencyKey string) (Certification, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out Certification
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := sklScanCertification(tx.QueryRow(ctx, `select `+certificationColumns+` from hr_certification c where c.tenant_id = $1 and c.idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if err := sklRequireWorker(ctx, tx, p.TenantID, in.WorkerID); err != nil {
			return err
		}
		var err error
		out, err = sklScanCertification(tx.QueryRow(ctx, `insert into hr_certification as c (tenant_id, id, worker_id, code, name, issuer, issued_on, expires_on, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7::date, $8::date, $9)
			returning `+certificationColumns,
			p.TenantID, ids.New(), in.WorkerID, in.Code, in.Name, in.Issuer, in.IssuedOn, in.ExpiresOn, key))
		if err != nil {
			return err
		}
		inserted = true
		return enqueue(ctx, tx, "certification", out.ID, hrevents.CertificationIssued, p.TenantID, hrevents.CertificationIssuedData{
			ID: out.ID, WorkerID: out.WorkerID, Code: out.Code, IssuedOn: out.IssuedOn, ExpiresOn: out.ExpiresOn,
		})
	})
	if idempotencyRace(err) {
		return Certification{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return Certification{}, false, problem(http.StatusConflict, CodeCertificationDuplicate, "the worker already has a "+in.Code+" certificate issued on "+in.IssuedOn)
	}
	if err == nil && !inserted && !sameCertification(out, in) {
		return Certification{}, false, idempotencyReuse()
	}
	if err != nil {
		return Certification{}, false, err
	}
	out, err = out.validOn(asOf)
	return out, inserted, err
}

// ListCertifications returns a worker's certificates, newest first, each with
// its validity as-of asOf.
func (s *Postgres) ListCertifications(ctx context.Context, workerID string, asOf time.Time) ([]Certification, error) {
	p := principal(ctx)
	out := []Certification{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := sklRequireWorker(ctx, tx, p.TenantID, workerID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+certificationColumns+` from hr_certification c where c.tenant_id = $1 and c.worker_id = $2
			order by c.issued_on desc, c.code, c.id limit $3`, p.TenantID, workerID, maxWorkerSkillRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := sklScanCertification(rows)
			if err != nil {
				return err
			}
			if c, err = c.validOn(asOf); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// ListExpiringCertifications is the "certifications expiring" inquiry
// (8.42.14, BR-42.8): certificates still valid on asOf that expire within
// withinDays after it, soonest first. A certificate the worker has already
// renewed (a later one of the same code that is valid longer) is left out.
// after is the cursor "YYYY-MM-DD/id" of the last row of the previous page.
func (s *Postgres) ListExpiringCertifications(ctx context.Context, asOf time.Time, withinDays int, after string, limit int) (CertificationPage, error) {
	p := principal(ctx)
	page := CertificationPage{Items: []ExpiringCertification{}}
	var afterDay, afterID *string
	if after != "" {
		d, id, ok := strings.Cut(after, "/")
		if _, err := time.Parse(absDateLayout, d); !ok || err != nil || !ids.Valid(id) {
			return page, problem(http.StatusBadRequest, "COM-0001", "cursor is not from a previous page")
		}
		afterDay, afterID = &d, &id
	}
	day := asOf.Format(absDateLayout)
	until := asOf.AddDate(0, 0, withinDays).Format(absDateLayout)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+certificationColumns+`, w.personnel_number, w.legal_name, (c.expires_on - $2::date)
			from hr_certification c join worker w on w.tenant_id = c.tenant_id and w.id = c.worker_id
			where c.tenant_id = $1 and c.issued_on <= $2::date and c.expires_on > $2::date and c.expires_on <= $3::date
			  and not exists (select 1 from hr_certification n where n.tenant_id = c.tenant_id and n.worker_id = c.worker_id
			      and n.code = c.code and n.issued_on > c.issued_on and (n.expires_on is null or n.expires_on > c.expires_on))
			  and ($4::date is null or (c.expires_on, c.id) > ($4::date, $5::uuid))
			order by c.expires_on, c.id limit $6`,
			p.TenantID, day, until, afterDay, afterID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e ExpiringCertification
			c, err := sklScanCertification(rows, &e.PersonnelNumber, &e.LegalName, &e.DaysLeft)
			if err != nil {
				return err
			}
			if e.Certification, err = c.validOn(asOf); err != nil {
				return err
			}
			page.Items = append(page.Items, e)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextAfter = *last.ExpiresOn + "/" + last.ID
	}
	return page, err
}

const requirementSelect = `select r.position_id::text, k.id::text, k.code, k.name, k.scale_max, r.min_rating
	from position_skill_requirement r join hr_skill k on k.tenant_id = r.tenant_id and k.id = r.skill_id`

func sklScanRequirement(row pgx.Row) (PositionRequirement, error) {
	var r PositionRequirement
	err := row.Scan(&r.PositionID, &r.SkillID, &r.SkillCode, &r.SkillName, &r.ScaleMax, &r.MinRating)
	return r, err
}

// SetRequirement sets the minimum rating a position needs in a skill
// (FR-42.18), replacing an earlier minimum for that skill.
func (s *Postgres) SetRequirement(ctx context.Context, positionID, skillCode string, minRating int) (PositionRequirement, error) {
	p := principal(ctx)
	var out PositionRequirement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := sklRequirePosition(ctx, tx, p.TenantID, positionID); err != nil {
			return err
		}
		skill, err := sklByCode(ctx, tx, p.TenantID, skillCode)
		if err != nil {
			return err
		}
		if err := domain.ValidateRating(minRating, skill.ScaleMax); err != nil {
			return sklProblem(err)
		}
		if _, err := tx.Exec(ctx, `insert into position_skill_requirement (tenant_id, position_id, skill_id, min_rating)
			values ($1, $2, $3, $4)
			on conflict (tenant_id, position_id, skill_id) do update set min_rating = excluded.min_rating, updated_at = now()`,
			p.TenantID, positionID, skill.ID, minRating); err != nil {
			return err
		}
		out = PositionRequirement{PositionID: positionID, SkillID: skill.ID, SkillCode: skill.Code, SkillName: skill.Name,
			ScaleMax: skill.ScaleMax, MinRating: minRating}
		return nil
	})
	return out, err
}

// RemoveRequirement drops a skill from a position's profile.
func (s *Postgres) RemoveRequirement(ctx context.Context, positionID, skillCode string) error {
	p := principal(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := sklRequirePosition(ctx, tx, p.TenantID, positionID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `delete from position_skill_requirement r using hr_skill k
			where r.tenant_id = $1 and r.position_id = $2 and k.tenant_id = r.tenant_id and k.id = r.skill_id and k.code = $3`,
			p.TenantID, positionID, skillCode)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(http.StatusNotFound, CodeRequirementNotFound, "the position does not require skill "+skillCode)
		}
		return nil
	})
}

// ListRequirements returns a position's skill profile ordered by skill code.
func (s *Postgres) ListRequirements(ctx context.Context, positionID string) ([]PositionRequirement, error) {
	p := principal(ctx)
	out := []PositionRequirement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := sklRequirePosition(ctx, tx, p.TenantID, positionID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, requirementSelect+` where r.tenant_id = $1 and r.position_id = $2 order by k.code`, p.TenantID, positionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := sklScanRequirement(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// SkillGapFor compares a worker's ratings with a position's requirements
// (FR-42.18, AC-42.16). A skill the worker is not rated on counts as 0.
func (s *Postgres) SkillGapFor(ctx context.Context, workerID, positionID string) (SkillGap, error) {
	p := principal(ctx)
	out := SkillGap{WorkerID: workerID, PositionID: positionID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := sklRequireWorker(ctx, tx, p.TenantID, workerID); err != nil {
			return err
		}
		if err := sklRequirePosition(ctx, tx, p.TenantID, positionID); err != nil {
			return err
		}
		reqs, err := sklCodeRatings(ctx, tx, `select k.code, r.min_rating from position_skill_requirement r
			join hr_skill k on k.tenant_id = r.tenant_id and k.id = r.skill_id
			where r.tenant_id = $1 and r.position_id = $2`, p.TenantID, positionID)
		if err != nil {
			return err
		}
		actual, err := sklCodeRatings(ctx, tx, `select k.code, ws.rating from worker_skill ws
			join hr_skill k on k.tenant_id = ws.tenant_id and k.id = ws.skill_id
			where ws.tenant_id = $1 and ws.worker_id = $2`, p.TenantID, workerID)
		if err != nil {
			return err
		}
		required := make([]domain.Requirement, 0, len(reqs))
		for code, min := range reqs {
			required = append(required, domain.Requirement{Skill: code, MinRating: min})
		}
		out.Comparison = domain.Compare(required, actual)
		out.Gaps = domain.Gap(required, actual)
		out.Met = len(out.Gaps) == 0
		return nil
	})
	return out, err
}

// SearchWorkers finds the workers employed on asOf who are rated at least the
// minimum on every required skill and hold every required certificate valid
// on asOf (FR-42.18), ordered by personnel number, at most MaxSearchResults.
// Employment visibility follows the caller's legal-entity scope. A skill code
// that is not in the catalog matches nobody.
func (s *Postgres) SearchWorkers(ctx context.Context, reqs []domain.Requirement, certs []string, asOf time.Time) (SkillSearch, error) {
	p := principal(ctx)
	out := SkillSearch{Items: []SkillMatch{}}
	reqs = domain.Normalize(reqs)
	codes := make([]string, len(reqs))
	mins := make([]int32, len(reqs))
	for i, r := range reqs {
		codes[i], mins[i] = r.Skill, int32(r.MinRating)
	}
	certSet := map[string]bool{}
	for _, c := range certs {
		certSet[c] = true
	}
	certCodes := make([]string, 0, len(certSet))
	for c := range certSet {
		certCodes = append(certCodes, c)
	}
	if len(codes) == 0 && len(certCodes) == 0 {
		return out, nil
	}
	day := asOf.Format(absDateLayout)
	instant := domain.AsOfDay(asOf)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with req as (
				select k.id as skill_id, r.min_rating
				from unnest($2::text[], $3::int[]) as r(code, min_rating)
				join hr_skill k on k.tenant_id = $1 and k.code = r.code
			), rated as (
				select ws.worker_id from req
				join worker_skill ws on ws.tenant_id = $1 and ws.skill_id = req.skill_id and ws.rating >= req.min_rating
				group by ws.worker_id having count(*) = cardinality($2::text[])
			), certified as (
				select c.worker_id from hr_certification c
				where c.tenant_id = $1 and c.code = any($4::text[]) and c.issued_on <= $5::date
				  and (c.expires_on is null or c.expires_on > $5::date)
				group by c.worker_id having count(distinct c.code) = cardinality($4::text[])
			)
			select w.id::text, w.personnel_number, w.legal_name from worker w
			where w.tenant_id = $1
			  and (cardinality($2::text[]) = 0 or w.id in (select worker_id from rated))
			  and (cardinality($4::text[]) = 0 or w.id in (select worker_id from certified))
			  and exists (select 1 from employment e where e.tenant_id = $1 and e.worker_id = w.id
			      and e.valid_from <= $6 and (e.valid_to is null or e.valid_to > $6))
			order by w.personnel_number limit $7`,
			p.TenantID, codes, mins, certCodes, day, instant, MaxSearchResults+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var m SkillMatch
			if err := rows.Scan(&m.WorkerID, &m.PersonnelNumber, &m.LegalName); err != nil {
				rows.Close()
				return err
			}
			out.Items = append(out.Items, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > MaxSearchResults {
			out.Items, out.Truncated = out.Items[:MaxSearchResults], true
		}
		if len(codes) == 0 || len(out.Items) == 0 {
			for i := range out.Items {
				out.Items[i].Skills = []domain.GapRow{}
			}
			return nil
		}
		workerIDs := make([]string, len(out.Items))
		for i, m := range out.Items {
			workerIDs[i] = m.WorkerID
		}
		ratings := map[string]map[string]int{}
		rrows, err := tx.Query(ctx, `select ws.worker_id::text, k.code, ws.rating from worker_skill ws
			join hr_skill k on k.tenant_id = ws.tenant_id and k.id = ws.skill_id
			where ws.tenant_id = $1 and ws.worker_id = any($2::uuid[]) and k.code = any($3::text[])`, p.TenantID, workerIDs, codes)
		if err != nil {
			return err
		}
		defer rrows.Close()
		for rrows.Next() {
			var worker, code string
			var rating int
			if err := rrows.Scan(&worker, &code, &rating); err != nil {
				return err
			}
			if ratings[worker] == nil {
				ratings[worker] = map[string]int{}
			}
			ratings[worker][code] = rating
		}
		if err := rrows.Err(); err != nil {
			return err
		}
		for i := range out.Items {
			out.Items[i].Skills = domain.Compare(reqs, ratings[out.Items[i].WorkerID])
		}
		return nil
	})
	return out, err
}

// sklCodeRatings runs a two-column (code, integer) query into a map.
func sklCodeRatings(ctx context.Context, tx pgx.Tx, query string, args ...any) (map[string]int, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var code string
		var n int
		if err := rows.Scan(&code, &n); err != nil {
			return nil, err
		}
		out[code] = n
	}
	return out, rows.Err()
}
