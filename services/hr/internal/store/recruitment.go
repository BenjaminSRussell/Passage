package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

const (
	CodeRequisitionNotFound = "HCM-REQ-NOT-FOUND"
	CodeRequisitionDup      = "HCM-REQ-DUPLICATE"
	CodeCandidateNotFound   = "HCM-CAN-NOT-FOUND"
	CodeCandidateErased     = "HCM-CAN-ERASED"
	CodeOfferNotFound       = "HCM-OFR-NOT-FOUND"
	CodeOfferConverted      = "HCM-OFR-001"
	CodeConsentRequired     = "HCM-CNS-001"
	CodeOfferNoPlan         = "HCM-OFR-002"
	CodeOfferEntity         = "HCM-OFR-003"
	CodeConsentNotFound     = "HCM-CNS-NOT-FOUND"
	CodeSubjectNotFound     = "HCM-CNS-SUBJECT"

	PurposeRecruitment = "RECRUITMENT"
	ClassCandidate     = "CANDIDATE"

	fieldCandidateName  = "hr_candidate.full_name"
	fieldCandidateEmail = "hr_candidate.email"
	fieldCandidatePhone = "hr_candidate.phone"
	purgeBatch          = 500
)

type Requisition struct {
	ID                    string    `json:"id"`
	RequisitionNumber     string    `json:"requisitionNumber"`
	PositionID            string    `json:"positionId"`
	LegalEntityID         string    `json:"legalEntityId"`
	Headcount             int       `json:"headcount"`
	HiringManagerWorkerID string    `json:"hiringManagerWorkerId"`
	PlannedStartDate      string    `json:"plannedStartDate"`
	AllowOverfill         bool      `json:"allowOverfill"`
	Status                string    `json:"status"`
	Filled                int       `json:"filled"`
	CreatedAt             time.Time `json:"createdAt"`
}

const requisitionSelect = `select r.id::text, r.requisition_number, r.position_id::text, r.legal_entity_id::text, r.headcount, r.hiring_manager_worker_id::text,
	to_char(r.planned_start_date, 'YYYY-MM-DD'), r.allow_overfill, r.status,
	(select count(*) from hr_offer o where o.tenant_id = r.tenant_id and o.requisition_id = r.id and o.status = 'ACCEPTED')::int, r.created_at
	from hr_requisition r`

func scanRequisition(row pgx.Row) (Requisition, error) {
	var r Requisition
	err := row.Scan(&r.ID, &r.RequisitionNumber, &r.PositionID, &r.LegalEntityID, &r.Headcount, &r.HiringManagerWorkerID, &r.PlannedStartDate, &r.AllowOverfill, &r.Status, &r.Filled, &r.CreatedAt)
	r.CreatedAt = utc(r.CreatedAt)
	return r, err
}

func requisitionEvent(ctx context.Context, tx pgx.Tx, tenantID string, r Requisition) error {
	return enqueue(ctx, tx, "requisition", r.ID, hrevents.RequisitionChanged, tenantID, hrevents.RequisitionChangedData{
		ID: r.ID, PositionID: r.PositionID, Headcount: r.Headcount, Status: r.Status, Filled: r.Filled,
	})
}

func loadRequisition(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Requisition, error) {
	q := requisitionSelect + ` where r.tenant_id = $1 and r.id = $2`
	if lock {
		q += ` for update of r`
	}
	r, err := scanRequisition(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, problem(http.StatusNotFound, CodeRequisitionNotFound, "requisition not found")
	}
	return r, err
}

func (s *Postgres) CreateRequisition(ctx context.Context, in Requisition, idempotencyKey string) (Requisition, bool, error) {
	p := principal(ctx)
	key := idemKey(idempotencyKey)
	var out Requisition
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanRequisition(tx.QueryRow(ctx, requisitionSelect+` where r.tenant_id = $1 and r.idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var legalEntity string
		err := tx.QueryRow(ctx, `select legal_entity_id::text from hr_position where tenant_id = $1 and id = $2`, p.TenantID, in.PositionID).Scan(&legalEntity)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodePositionNotFound, "position not found")
		}
		if err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, in.HiringManagerWorkerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeWorkerNotFound, "hiring manager not found")
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into hr_requisition (tenant_id, id, requisition_number, position_id, legal_entity_id, headcount, hiring_manager_worker_id, planned_start_date, allow_overfill, status, idempotency_key)
			values ($1, $2, $3, $4, $10, $5, $6, $7::date, $8, 'DRAFT', $9)`,
			p.TenantID, id, in.RequisitionNumber, in.PositionID, in.Headcount, in.HiringManagerWorkerID, in.PlannedStartDate, in.AllowOverfill, key, legalEntity); err != nil {
			return err
		}
		if out, err = loadRequisition(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		inserted = true
		return requisitionEvent(ctx, tx, p.TenantID, out)
	})
	if idempotencyRace(err) {
		return Requisition{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return Requisition{}, false, problem(http.StatusConflict, CodeRequisitionDup, "a requisition with this number already exists")
	}
	if err == nil && key != nil && !inserted && (out.RequisitionNumber != in.RequisitionNumber || out.PositionID != in.PositionID || out.Headcount != in.Headcount) {
		return Requisition{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

func (s *Postgres) RequisitionByKey(ctx context.Context, idempotencyKey string) (Requisition, bool, error) {
	p := principal(ctx)
	var out Requisition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanRequisition(tx.QueryRow(ctx, requisitionSelect+` where r.tenant_id = $1 and r.idempotency_key = $2`, p.TenantID, idempotencyKey))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Requisition{}, false, nil
	}
	return out, err == nil, err
}

func (s *Postgres) GetRequisition(ctx context.Context, id string) (Requisition, error) {
	p := principal(ctx)
	var out Requisition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadRequisition(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

func (s *Postgres) ListRequisitions(ctx context.Context, status, after string, limit int) ([]Requisition, string, error) {
	p := principal(ctx)
	out := []Requisition{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, requisitionSelect+` where r.tenant_id = $1 and r.requisition_number > $2 and ($3 = '' or r.status = $3) order by r.requisition_number limit $4`,
			p.TenantID, after, status, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRequisition(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].RequisitionNumber
	}
	return out, next, err
}

func (s *Postgres) MoveRequisition(ctx context.Context, id, to string) (Requisition, error) {
	p := principal(ctx)
	var out Requisition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadRequisition(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if cur.Status == to {
			out = cur
			return nil
		}
		if err := domain.RequisitionMove(cur.Status, to); err != nil {
			return problem(http.StatusConflict, domain.CodeReqState, err.(*domain.Error).Detail)
		}
		if to == "OPEN" {
			if err := checkVacancy(ctx, tx, p.TenantID, cur); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update hr_requisition set status = $3, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, to); err != nil {
			return err
		}
		if out, err = loadRequisition(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		return requisitionEvent(ctx, tx, p.TenantID, out)
	})
	return out, err
}

func checkVacancy(ctx context.Context, tx pgx.Tx, tenantID string, r Requisition) error {
	var fteText string
	var overfill, exists bool
	err := tx.QueryRow(ctx, `select fte::text, allow_overfill, validity @> $3::date::timestamptz from hr_position where tenant_id = $1 and id = $2 for update`,
		tenantID, r.PositionID, r.PlannedStartDate).Scan(&fteText, &overfill, &exists)
	if err != nil {
		return err
	}
	if !exists {
		return problem(http.StatusUnprocessableEntity, domain.CodeReqNoVacancy, "the position does not exist on the planned start date")
	}
	var assigned, claimed string
	if err := tx.QueryRow(ctx, `select coalesce(sum(fte_share), 0)::text from position_assignment where tenant_id = $1 and position_id = $2 and validity @> $3::date::timestamptz`,
		tenantID, r.PositionID, r.PlannedStartDate).Scan(&assigned); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `select coalesce(sum(greatest(r.headcount - (select count(*) from hr_offer o where o.tenant_id = r.tenant_id and o.requisition_id = r.id and o.status = 'ACCEPTED'), 0)), 0)::text
		from hr_requisition r where r.tenant_id = $1 and r.position_id = $2 and r.status = 'OPEN' and r.id <> $3`, tenantID, r.PositionID, r.ID).Scan(&claimed); err != nil {
		return err
	}
	fte := decimal.RequireFromString(fteText)
	claimedFTE := decimal.RequireFromString(claimed).Mul(fte)
	if err := domain.CheckVacancy(fte, decimal.RequireFromString(assigned), claimedFTE, r.Headcount, r.AllowOverfill || overfill); err != nil {
		return problem(http.StatusUnprocessableEntity, domain.CodeReqNoVacancy, err.(*domain.Error).Detail)
	}
	return nil
}

type Candidate struct {
	ID             string    `json:"id"`
	RequisitionID  string    `json:"requisitionId"`
	Stage          string    `json:"stage"`
	Source         *string   `json:"source,omitempty"`
	FullName       *string   `json:"fullName,omitempty"`
	Email          *string   `json:"email,omitempty"`
	Phone          *string   `json:"phone,omitempty"`
	Erased         bool      `json:"erased"`
	StageChangedAt time.Time `json:"stageChangedAt"`
}

type CandidateInput struct {
	RequisitionID  string
	FullName       string
	Email          string
	Phone          string
	Source         *string
	ConsentGranted bool
}

const candidateSelect = `select id::text, requisition_id::text, stage, source, full_name_sealed, email_sealed, phone_sealed, erased_at is not null, stage_changed_at from hr_candidate`

func (s *Postgres) scanCandidate(p tenant.Principal, row pgx.Row) (Candidate, error) {
	var c Candidate
	var name, email, phone []byte
	if err := row.Scan(&c.ID, &c.RequisitionID, &c.Stage, &c.Source, &name, &email, &phone, &c.Erased, &c.StageChangedAt); err != nil {
		return c, err
	}
	c.StageChangedAt = utc(c.StageChangedAt)
	open := func(field string, sealed []byte) (*string, error) {
		if sealed == nil {
			return nil, nil
		}
		v, err := s.sealer.Open(p.TenantID, field, sealed)
		return &v, err
	}
	var err error
	if c.FullName, err = open(fieldCandidateName, name); err != nil {
		return c, err
	}
	if c.Email, err = open(fieldCandidateEmail, email); err != nil {
		return c, err
	}
	c.Phone, err = open(fieldCandidatePhone, phone)
	return c, err
}

func auditCandidate(ctx context.Context, tx pgx.Tx, p tenant.Principal, action, candidateID string, fields []string, reason string) error {
	var r *string
	if reason != "" {
		r = &reason
	}
	_, err := tx.Exec(ctx, `insert into audit_event (tenant_id, id, actor, action, entity, entity_id, fields, reason)
		values ($1, $2, $3, $4, 'candidate', $5, $6, $7)`, p.TenantID, ids.New(), p.UserID, action, candidateID, fields, r)
	return err
}

var candidatePIIFields = []string{"full_name", "email", "phone"}

func (s *Postgres) CreateCandidate(ctx context.Context, in CandidateInput, idempotencyKey string) (Candidate, bool, error) {
	p := principal(ctx)
	key := idemKey(idempotencyKey)
	var out Candidate
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := s.scanCandidate(p, tx.QueryRow(ctx, candidateSelect+` where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return auditCandidate(ctx, tx, p, "p3_read", replay.ID, candidatePIIFields, "")
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if !in.ConsentGranted {
			return problem(http.StatusUnprocessableEntity, CodeConsentRequired, "the candidate's consent to process their data for recruitment is required")
		}
		req, err := loadRequisition(ctx, tx, p.TenantID, in.RequisitionID, false)
		if err != nil {
			return err
		}
		if req.Status != "OPEN" {
			return problem(http.StatusConflict, domain.CodeReqState, "candidates can apply only to an OPEN requisition")
		}
		name, err := s.sealer.Seal(p.TenantID, fieldCandidateName, in.FullName)
		if err != nil {
			return err
		}
		var email, phone []byte
		if in.Email != "" {
			if email, err = s.sealer.Seal(p.TenantID, fieldCandidateEmail, in.Email); err != nil {
				return err
			}
		}
		if in.Phone != "" {
			if phone, err = s.sealer.Seal(p.TenantID, fieldCandidatePhone, in.Phone); err != nil {
				return err
			}
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into hr_candidate (tenant_id, id, requisition_id, legal_entity_id, stage, source, full_name_sealed, email_sealed, phone_sealed, idempotency_key)
			values ($1, $2, $3, $9, 'APPLIED', $4, $5, $6, $7, $8)`, p.TenantID, id, in.RequisitionID, in.Source, name, email, phone, key, req.LegalEntityID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into hr_consent_record (tenant_id, id, subject_type, subject_id, legal_entity_id, purpose, granted_by) values ($1, $2, 'CANDIDATE', $3, $6, $4, $5)`,
			p.TenantID, ids.New(), id, PurposeRecruitment, p.UserID, req.LegalEntityID); err != nil {
			return err
		}
		if err := auditCandidate(ctx, tx, p, "p3_change", id, candidatePIIFields, ""); err != nil {
			return err
		}
		inserted = true
		out, err = s.scanCandidate(p, tx.QueryRow(ctx, candidateSelect+` where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if idempotencyRace(err) {
		return Candidate{}, false, idempotencyInFlight()
	}
	if err == nil && key != nil && !inserted && out.RequisitionID != in.RequisitionID {
		return Candidate{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

func (s *Postgres) GetCandidate(ctx context.Context, id string) (Candidate, error) {
	p := principal(ctx)
	var out Candidate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.scanCandidate(p, tx.QueryRow(ctx, candidateSelect+` where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeCandidateNotFound, "candidate not found")
		}
		if err != nil {
			return err
		}
		return auditCandidate(ctx, tx, p, "p3_read", id, candidatePIIFields, "")
	})
	return out, err
}

func (s *Postgres) ListCandidates(ctx context.Context, requisitionID, stage, after string, limit int) ([]Candidate, string, error) {
	p := principal(ctx)
	out := []Candidate{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, candidateSelect+` where tenant_id = $1 and requisition_id = $2 and id::text > $3 and ($4 = '' or stage = $4) order by id limit $5`,
			p.TenantID, requisitionID, after, stage, limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			c, err := s.scanCandidate(p, rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			if i < limit && !out[i].Erased {
				if err := auditCandidate(ctx, tx, p, "p3_read", out[i].ID, candidatePIIFields, ""); err != nil {
					return err
				}
			}
		}
		return nil
	})
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, err
}

func (s *Postgres) MoveCandidate(ctx context.Context, id, to string) (Candidate, error) {
	p := principal(ctx)
	var out Candidate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var stage string
		var erased bool
		err := tx.QueryRow(ctx, `select stage, erased_at is not null from hr_candidate where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&stage, &erased)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeCandidateNotFound, "candidate not found")
		}
		if err != nil {
			return err
		}
		if erased {
			return problem(http.StatusConflict, CodeCandidateErased, "the candidate's data has been erased")
		}
		if err := domain.CandidateStageMove(stage, to); err != nil {
			return problem(http.StatusConflict, domain.CodeCandidateMove, err.(*domain.Error).Detail)
		}
		if _, err := tx.Exec(ctx, `update hr_candidate set stage = $3, stage_changed_at = $4 where tenant_id = $1 and id = $2`, p.TenantID, id, to, s.now()); err != nil {
			return err
		}
		out, err = s.scanCandidate(p, tx.QueryRow(ctx, candidateSelect+` where tenant_id = $1 and id = $2`, p.TenantID, id))
		if err != nil {
			return err
		}
		return auditCandidate(ctx, tx, p, "p3_read", id, candidatePIIFields, "")
	})
	return out, err
}

type Consent struct {
	ID          string     `json:"id"`
	SubjectType string     `json:"subjectType"`
	SubjectID   string     `json:"subjectId"`
	Purpose     string     `json:"purpose"`
	GrantedAt   time.Time  `json:"grantedAt"`
	GrantedBy   string     `json:"grantedBy"`
	WithdrawnAt *time.Time `json:"withdrawnAt,omitempty"`
	WithdrawnBy *string    `json:"withdrawnBy,omitempty"`
}

const consentSelect = `select id::text, subject_type, subject_id::text, purpose, granted_at, granted_by, withdrawn_at, withdrawn_by from hr_consent_record`

func scanConsent(row pgx.Row) (Consent, error) {
	var c Consent
	err := row.Scan(&c.ID, &c.SubjectType, &c.SubjectID, &c.Purpose, &c.GrantedAt, &c.GrantedBy, &c.WithdrawnAt, &c.WithdrawnBy)
	c.GrantedAt, c.WithdrawnAt = utc(c.GrantedAt), utcPtr(c.WithdrawnAt)
	return c, err
}

func (s *Postgres) GrantConsent(ctx context.Context, subjectType, subjectID, purpose string) (Consent, bool, error) {
	p := principal(ctx)
	var out Consent
	granted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var legalEntity string
		err := tx.QueryRow(ctx, `select legal_entity_id::text from hr_candidate where tenant_id = $1 and id = $2`, p.TenantID, subjectID).Scan(&legalEntity)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeSubjectNotFound, "the consent subject does not exist")
		}
		if err != nil {
			return err
		}
		cur, err := scanConsent(tx.QueryRow(ctx, consentSelect+` where tenant_id = $1 and subject_type = $2 and subject_id = $3 and purpose = $4 and withdrawn_at is null`,
			p.TenantID, subjectType, subjectID, purpose))
		if err == nil {
			out = cur
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = scanConsent(tx.QueryRow(ctx, `insert into hr_consent_record (tenant_id, id, subject_type, subject_id, legal_entity_id, purpose, granted_by) values ($1, $2, $3, $4, $7, $5, $6)
			returning id::text, subject_type, subject_id::text, purpose, granted_at, granted_by, withdrawn_at, withdrawn_by`, p.TenantID, ids.New(), subjectType, subjectID, purpose, p.UserID, legalEntity))
		granted = err == nil
		return err
	})
	return out, granted, err
}

func (s *Postgres) WithdrawConsent(ctx context.Context, id string) (Consent, error) {
	p := principal(ctx)
	var out Consent
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanConsent(tx.QueryRow(ctx, consentSelect+` where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeConsentNotFound, "consent record not found")
		}
		if err != nil {
			return err
		}
		if cur.WithdrawnAt != nil {
			out = cur
			return nil
		}
		out, err = scanConsent(tx.QueryRow(ctx, `update hr_consent_record set withdrawn_at = now(), withdrawn_by = $3 where tenant_id = $1 and id = $2
			returning id::text, subject_type, subject_id::text, purpose, granted_at, granted_by, withdrawn_at, withdrawn_by`, p.TenantID, id, p.UserID))
		return err
	})
	return out, err
}

func (s *Postgres) ListConsents(ctx context.Context, subjectType, subjectID string) ([]Consent, error) {
	p := principal(ctx)
	out := []Consent{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from hr_candidate where tenant_id = $1 and id = $2)`, p.TenantID, subjectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeSubjectNotFound, "the consent subject does not exist")
		}
		rows, err := tx.Query(ctx, consentSelect+` where tenant_id = $1 and subject_type = $2 and subject_id = $3 order by granted_at, id`, p.TenantID, subjectType, subjectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanConsent(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

type RetentionPolicy struct {
	DataClass       string    `json:"dataClass"`
	RetentionMonths int       `json:"retentionMonths"`
	UpdatedBy       string    `json:"updatedBy"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func (s *Postgres) SetRetentionPolicy(ctx context.Context, dataClass string, months int) (RetentionPolicy, error) {
	p := principal(ctx)
	var out RetentionPolicy
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `insert into hr_retention_policy (tenant_id, data_class, retention_months, updated_by) values ($1, $2, $3, $4)
			on conflict (tenant_id, data_class) do update set retention_months = excluded.retention_months, updated_by = excluded.updated_by, updated_at = now()
			returning data_class, retention_months, updated_by, updated_at`, p.TenantID, dataClass, months, p.UserID).Scan(&out.DataClass, &out.RetentionMonths, &out.UpdatedBy, &out.UpdatedAt)
	})
	out.UpdatedAt = utc(out.UpdatedAt)
	return out, err
}

func (s *Postgres) ListRetentionPolicies(ctx context.Context) ([]RetentionPolicy, error) {
	p := principal(ctx)
	out := []RetentionPolicy{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select data_class, retention_months, updated_by, updated_at from hr_retention_policy where tenant_id = $1 order by data_class`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RetentionPolicy
			if err := rows.Scan(&r.DataClass, &r.RetentionMonths, &r.UpdatedBy, &r.UpdatedAt); err != nil {
				return err
			}
			r.UpdatedAt = utc(r.UpdatedAt)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) PurgeCandidates(ctx context.Context) (int, error) {
	p := principal(ctx)
	erased := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var months *int
		err := tx.QueryRow(ctx, `select retention_months from hr_retention_policy where tenant_id = $1 and data_class = 'CANDIDATE'`, p.TenantID).Scan(&months)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err := tx.Query(ctx, `select c.id::text from hr_candidate c
			where c.tenant_id = $1 and c.erased_at is null
			and (exists (select 1 from hr_consent_record k where k.tenant_id = c.tenant_id and k.subject_type = 'CANDIDATE' and k.subject_id = c.id and k.purpose = 'RECRUITMENT' and k.withdrawn_at is not null)
				and not exists (select 1 from hr_consent_record k where k.tenant_id = c.tenant_id and k.subject_type = 'CANDIDATE' and k.subject_id = c.id and k.purpose = 'RECRUITMENT' and k.withdrawn_at is null)
				or ($2::int is not null and c.stage in ('REJECTED', 'HIRED') and c.stage_changed_at < $3::timestamptz - make_interval(months => coalesce($2::int, 0))))
			order by c.id limit $4 for update of c`, p.TenantID, months, s.now(), purgeBatch)
		if err != nil {
			return err
		}
		doomed, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range doomed {
			if _, err := tx.Exec(ctx, `update hr_candidate set full_name_sealed = null, email_sealed = null, phone_sealed = null, erased_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
				return err
			}
			if err := auditCandidate(ctx, tx, p, "p3_change", id, candidatePIIFields, "erased by retention or withdrawn consent"); err != nil {
				return err
			}
		}
		erased = len(doomed)
		return nil
	})
	return erased, err
}

type Offer struct {
	ID            string  `json:"id"`
	CandidateID   string  `json:"candidateId"`
	RequisitionID string  `json:"requisitionId"`
	Amount        string  `json:"amount"`
	Currency      string  `json:"currency"`
	StartDate     string  `json:"startDate"`
	Status        string  `json:"status"`
	HireActionID  *string `json:"hireActionId,omitempty"`
}

const offerSelect = `select id::text, candidate_id::text, requisition_id::text, amount::text, currency, to_char(start_date, 'YYYY-MM-DD'), status, hire_action_id::text from hr_offer`

func scanOffer(row pgx.Row) (Offer, error) {
	var o Offer
	err := row.Scan(&o.ID, &o.CandidateID, &o.RequisitionID, &o.Amount, &o.Currency, &o.StartDate, &o.Status, &o.HireActionID)
	return o, err
}

func (s *Postgres) CreateOffer(ctx context.Context, in Offer, idempotencyKey string) (Offer, bool, error) {
	p := principal(ctx)
	key := idemKey(idempotencyKey)
	var out Offer
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanOffer(tx.QueryRow(ctx, offerSelect+` where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var reqID, stage string
		var erased bool
		err := tx.QueryRow(ctx, `select requisition_id::text, stage, erased_at is not null from hr_candidate where tenant_id = $1 and id = $2 for update`, p.TenantID, in.CandidateID).Scan(&reqID, &stage, &erased)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeCandidateNotFound, "candidate not found")
		}
		if err != nil {
			return err
		}
		if erased || stage != "OFFER" {
			return problem(http.StatusConflict, domain.CodeCandidateMove, "an offer needs a candidate at the OFFER stage whose data is still held")
		}
		req, err := loadRequisition(ctx, tx, p.TenantID, reqID, true)
		if err != nil {
			return err
		}
		if req.Status != "OPEN" {
			return problem(http.StatusConflict, domain.CodeReqState, "offers can be made only on an OPEN requisition")
		}
		out, err = scanOffer(tx.QueryRow(ctx, `insert into hr_offer (tenant_id, id, candidate_id, requisition_id, legal_entity_id, amount, currency, start_date, status, idempotency_key)
			values ($1, $2, $3, $4, $9, $5::numeric, $6, $7::date, 'DRAFT', $8)
			returning id::text, candidate_id::text, requisition_id::text, amount::text, currency, to_char(start_date, 'YYYY-MM-DD'), status, hire_action_id::text`,
			p.TenantID, ids.New(), in.CandidateID, reqID, in.Amount, in.Currency, in.StartDate, key, req.LegalEntityID))
		inserted = err == nil
		return err
	})
	if idempotencyRace(err) {
		return Offer{}, false, idempotencyInFlight()
	}
	if err == nil && key != nil && !inserted && (out.CandidateID != in.CandidateID || !sameDecimal(out.Amount, in.Amount) || out.Currency != in.Currency || out.StartDate != in.StartDate) {
		return Offer{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

func (s *Postgres) GetOffer(ctx context.Context, id string) (Offer, error) {
	p := principal(ctx)
	var out Offer
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanOffer(tx.QueryRow(ctx, offerSelect+` where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Offer{}, problem(http.StatusNotFound, CodeOfferNotFound, "offer not found")
	}
	return out, err
}

func lockOffer(ctx context.Context, tx pgx.Tx, tenantID, id string) (Offer, Requisition, error) {
	var reqID string
	err := tx.QueryRow(ctx, `select requisition_id::text from hr_offer where tenant_id = $1 and id = $2`, tenantID, id).Scan(&reqID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Offer{}, Requisition{}, problem(http.StatusNotFound, CodeOfferNotFound, "offer not found")
	}
	if err != nil {
		return Offer{}, Requisition{}, err
	}
	req, err := loadRequisition(ctx, tx, tenantID, reqID, true)
	if err != nil {
		return Offer{}, Requisition{}, err
	}
	o, err := scanOffer(tx.QueryRow(ctx, offerSelect+` where tenant_id = $1 and id = $2 for update`, tenantID, id))
	return o, req, err
}

func (s *Postgres) MoveOffer(ctx context.Context, id, to string) (Offer, error) {
	p := principal(ctx)
	var out Offer
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, req, err := lockOffer(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if cur.Status == to {
			out = cur
			return nil
		}
		if err := domain.OfferMove(cur.Status, to); err != nil {
			return problem(http.StatusConflict, domain.CodeOfferState, err.(*domain.Error).Detail)
		}
		if cur.HireActionID != nil {
			return problem(http.StatusConflict, CodeOfferConverted, "offer "+id+" has already been converted to hire action "+*cur.HireActionID)
		}
		if to == "ACCEPTED" && req.Status != "OPEN" {
			return problem(http.StatusConflict, domain.CodeReqState, "the requisition is no longer OPEN")
		}
		if _, err := tx.Exec(ctx, `update hr_offer set status = $3, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, to); err != nil {
			return err
		}
		out, err = scanOffer(tx.QueryRow(ctx, offerSelect+` where tenant_id = $1 and id = $2`, p.TenantID, id))
		if err != nil {
			return err
		}
		return s.settleRequisition(ctx, tx, p.TenantID, req.ID)
	})
	return out, err
}

func (s *Postgres) settleRequisition(ctx context.Context, tx pgx.Tx, tenantID, requisitionID string) error {
	req, err := loadRequisition(ctx, tx, tenantID, requisitionID, false)
	if err != nil {
		return err
	}
	next := req.Status
	switch {
	case req.Status == "OPEN" && req.Filled >= req.Headcount:
		next = "FILLED"
	case req.Status == "FILLED" && req.Filled < req.Headcount:
		next = "OPEN"
	}
	if next == req.Status {
		return nil
	}
	if _, err := tx.Exec(ctx, `update hr_requisition set status = $3, updated_at = now() where tenant_id = $1 and id = $2`, tenantID, requisitionID, next); err != nil {
		return err
	}
	req.Status = next
	return requisitionEvent(ctx, tx, tenantID, req)
}

func checkHireFits(ctx context.Context, tx pgx.Tx, tenantID string, req Requisition, in HireInput, start time.Time) error {
	var overlap bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from employment where tenant_id = $1 and worker_id = $2 and legal_entity_id = $3 and assignment_kind = 'PRIMARY'
		and validity && tstzrange($4::timestamptz, null, '[)'))`, tenantID, in.WorkerID, in.LegalEntityID, start).Scan(&overlap); err != nil {
		return err
	}
	if overlap {
		return problem(http.StatusConflict, CodeEmploymentOverlap, "the worker already has a primary employment in this legal entity from that date")
	}
	var fte, existing string
	var overfill bool
	err := tx.QueryRow(ctx, `select fte::text, allow_overfill from hr_position where tenant_id = $1 and id = $2 for update`, tenantID, req.PositionID).Scan(&fte, &overfill)
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `select coalesce(sum(fte_share), 0)::text from position_assignment where tenant_id = $1 and position_id = $2
		and validity && tstzrange($3::timestamptz, null, '[)')`, tenantID, req.PositionID, start).Scan(&existing); err != nil {
		return err
	}
	share := in.FTEShare
	if share == "" {
		share = defaultFTEShare
	}
	if err := domain.CheckFTE(decimal.RequireFromString(fte), overfill, decimal.RequireFromString(existing), decimal.RequireFromString(share)); err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return problem(http.StatusUnprocessableEntity, de.Code, de.Detail)
		}
		return err
	}
	return nil
}

type HireInput struct {
	WorkerID       string
	LegalEntityID  string
	EmploymentType string
	FTEShare       string
	CompPlanID     string
}

func (s *Postgres) HireFromOffer(ctx context.Context, id string, in HireInput) (PersonnelAction, error) {
	p := principal(ctx)
	var out PersonnelAction
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		offer, req, err := lockOffer(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if offer.HireActionID != nil {
			return problem(http.StatusConflict, CodeOfferConverted, "offer "+id+" has already been converted to hire action "+*offer.HireActionID)
		}
		if offer.Status != "ACCEPTED" {
			return problem(http.StatusConflict, domain.CodeOfferState, "only an ACCEPTED offer can be converted to a hire")
		}
		start, err := time.Parse("2006-01-02", offer.StartDate)
		if err != nil {
			return err
		}
		if in.CompPlanID == "" {
			return problem(http.StatusUnprocessableEntity, CodeOfferNoPlan, "a hire from an offer needs a compPlanId so the offered pay is recorded")
		}
		if in.LegalEntityID != req.LegalEntityID {
			return problem(http.StatusUnprocessableEntity, CodeOfferEntity, "the hire must be in the requisition's legal entity "+req.LegalEntityID)
		}
		if err := checkHireFits(ctx, tx, p.TenantID, req, in, start); err != nil {
			return err
		}
		payload := ActionPayload{
			LegalEntityID: in.LegalEntityID, EmploymentType: in.EmploymentType, PositionID: req.PositionID, FTEShare: in.FTEShare,
			CompPlanID: in.CompPlanID, Amount: offer.Amount, Currency: offer.Currency,
		}
		kind, src := sourceOffer, id
		out, err = s.submitActionTx(ctx, tx, p, ActionInput{
			ActionType: hrevents.ActionHire, WorkerID: in.WorkerID, EffectiveDate: start, Payload: payload,
			Reason: "hire from offer " + id, SourceKind: &kind, SourceID: &src,
		}, nil)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update hr_offer set hire_action_id = $3, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, out.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update hr_candidate set stage = 'HIRED', stage_changed_at = $3 where tenant_id = $1 and id = $2`, p.TenantID, offer.CandidateID, s.now())
		return err
	})
	return out, err
}
