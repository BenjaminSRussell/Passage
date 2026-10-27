// Package store is the HR Postgres layer. Every write runs in a tenant-scoped
// transaction (RLS via dbx.WithTenantTx) and writes its outbox row in the same
// transaction as the state change.
package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

// Problem codes raised by the store. Catalog codes come from 8.42.18; the rest
// are listed in the service README and NOT-IMPLEMENTED.md.
const (
	CodeWorkerNotFound      = "HCM-WRK-NOT-FOUND"
	CodeWorkerDuplicate     = "HCM-WRK-DUPLICATE"
	CodeEmploymentOverlap   = "HCM-EMP-001"
	CodePositionNotFound    = "HCM-POS-NOT-FOUND"
	CodePositionDuplicate   = "HCM-POS-DUPLICATE"
	CodePositionParent      = "HCM-POS-PARENT-INVALID"
	CodeAssignmentOverlap   = "HCM-ASG-001"
	CodeAssignmentMismatch  = "HCM-ASG-002"
	CodeIdempotencyReuse    = "idempotency_key_reuse"
	CodeIdempotencyInFlight = "idempotency_key_in_flight"
	CodeAssignmentRange     = "HCM-ASG-003"

	uniqueViolation    = "23505"
	exclusionViolation = "23P01"
	eventSource        = "hr"
)

// Postgres is the HR store over a pgx pool.
type Postgres struct {
	pool   *pgxpool.Pool
	sealer *domain.Sealer
	depth  int
}

// New builds the store. maxDepth is the vacant-manager walk depth (BR-42.1).
func New(pool *pgxpool.Pool, sealer *domain.Sealer, maxDepth int) *Postgres {
	return &Postgres{pool: pool, sealer: sealer, depth: maxDepth}
}

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Worker is the non-PII view of a worker. Names are P2 and stay in the row.
type Worker struct {
	ID              string    `json:"id"`
	PersonnelNumber string    `json:"personnelNumber"`
	WorkerType      string    `json:"workerType"`
	PartyPersonID   string    `json:"partyPersonId"`
	LegalName       string    `json:"legalName"`
	PreferredName   *string   `json:"preferredName,omitempty"`
	UserID          *string   `json:"userId,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// WorkerInput carries the plaintext P3 values to be sealed into worker_pii.
type WorkerInput struct {
	Worker
	NationalID  string
	BankAccount string
}

// PIIView is what a PII read returns: masked unless the caller revealed it.
type PIIView struct {
	NationalID  string  `json:"nationalId"`
	BankAccount *string `json:"bankAccount,omitempty"`
	Masked      bool    `json:"masked"`
}

// Employment is one effective-dated employment at a legal entity.
type Employment struct {
	ID                string     `json:"id"`
	WorkerID          string     `json:"workerId"`
	LegalEntityID     string     `json:"legalEntityId"`
	EmploymentType    string     `json:"employmentType"`
	AssignmentKind    string     `json:"assignmentKind"`
	HomeLegalEntityID *string    `json:"homeLegalEntityId,omitempty"`
	PlannedEndDate    *string    `json:"plannedEndDate,omitempty"`
	HireDate          string     `json:"hireDate"`
	TerminationDate   *string    `json:"terminationDate,omitempty"`
	ValidFrom         time.Time  `json:"validFrom"`
	ValidTo           *time.Time `json:"validTo,omitempty"`
	// Status is derived from the validity range as-of the read (FR-42.2):
	// PENDING before the start, ACTIVE inside it, TERMINATED after it.
	Status string `json:"status"`
}

// statusAt derives the employment status as-of an instant.
func (e Employment) statusAt(at time.Time) Employment {
	e.Status = domain.EmploymentStatus(domain.Range{From: e.ValidFrom, To: e.ValidTo}, at)
	return e
}

const workerColumns = "id::text, personnel_number, worker_type, party_person_id::text, legal_name, preferred_name, user_id::text, created_at, updated_at"

func scanWorker(row pgx.Row) (Worker, error) {
	var w Worker
	err := row.Scan(&w.ID, &w.PersonnelNumber, &w.WorkerType, &w.PartyPersonID, &w.LegalName, &w.PreferredName, &w.UserID, &w.CreatedAt, &w.UpdatedAt)
	w.CreatedAt, w.UpdatedAt = utc(w.CreatedAt), utc(w.UpdatedAt)
	return w, err
}

const employmentColumns = "id::text, worker_id::text, legal_entity_id::text, employment_type, assignment_kind, home_legal_entity_id::text, to_char(planned_end_date, 'YYYY-MM-DD'), to_char(hire_date, 'YYYY-MM-DD'), to_char(termination_date, 'YYYY-MM-DD'), valid_from, valid_to"

func scanEmployment(row pgx.Row) (Employment, error) {
	var e Employment
	err := row.Scan(&e.ID, &e.WorkerID, &e.LegalEntityID, &e.EmploymentType, &e.AssignmentKind, &e.HomeLegalEntityID, &e.PlannedEndDate, &e.HireDate, &e.TerminationDate, &e.ValidFrom, &e.ValidTo)
	e.ValidFrom, e.ValidTo = utc(e.ValidFrom), utcPtr(e.ValidTo)
	return e, err
}

// utc normalises a timestamptz read: pgx returns the session's local zone,
// and validity bounds are UTC day starts in this slice.
func utc(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func pgCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func pgConstraint(err error, name string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == exclusionViolation && pgErr.ConstraintName == name
}

// idempotencyRace reports a unique violation on a (tenant_id, idempotency_key)
// constraint: a concurrent request with the same key committed first.
func idempotencyRace(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && strings.HasSuffix(pgErr.ConstraintName, "idempotency_key_key")
}

func idempotencyInFlight() error {
	return problem(http.StatusConflict, CodeIdempotencyInFlight, "a request with this Idempotency-Key is being processed; retry it")
}

func problem(status int, code, detail string) error {
	return httpx.NewProblem(status, code, detail)
}

func idempotencyReuse() error {
	return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
}

func principal(ctx context.Context) tenant.Principal {
	p, _ := tenant.FromContext(ctx)
	return p
}

// CreateWorker stores a worker, seals its P3 values into worker_pii and writes
// an audit row recording which P3 fields were set (never their values).
func (s *Postgres) CreateWorker(ctx context.Context, in WorkerInput, idempotencyKey string) (Worker, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var w Worker
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanWorker(tx.QueryRow(ctx, `select `+workerColumns+` from worker where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				w = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		nationalID, err := s.sealer.Seal(in.NationalID)
		if err != nil {
			return err
		}
		var bank []byte
		if in.BankAccount != "" {
			if bank, err = s.sealer.Seal(in.BankAccount); err != nil {
				return err
			}
		}
		workerID := ids.New()
		w, err = scanWorker(tx.QueryRow(ctx, `insert into worker (tenant_id, id, personnel_number, worker_type, party_person_id, legal_name, preferred_name, user_id, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning `+workerColumns,
			p.TenantID, workerID, in.PersonnelNumber, in.WorkerType, in.PartyPersonID, in.LegalName, in.PreferredName, in.UserID, key))
		if err != nil {
			return err
		}
		inserted = true
		if _, err := tx.Exec(ctx, `insert into worker_pii (tenant_id, worker_id, national_id_sealed, bank_account_sealed) values ($1, $2, $3, $4)`,
			p.TenantID, w.ID, nationalID, bank); err != nil {
			return err
		}
		changed := []string{"national_id"}
		if bank != nil {
			changed = append(changed, "bank_account")
		}
		if err := audit(ctx, tx, p, "p3_change", w.ID, changed, "", ""); err != nil {
			return err
		}
		return enqueue(ctx, tx, "worker", w.ID, hrevents.WorkerCreated, p.TenantID, hrevents.WorkerCreatedData{
			ID: w.ID, PersonnelNumber: w.PersonnelNumber, WorkerType: w.WorkerType, PartyPersonID: w.PartyPersonID,
		})
	})
	if idempotencyRace(err) {
		return Worker{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return Worker{}, false, problem(http.StatusConflict, CodeWorkerDuplicate, "a worker with this personnel number already exists")
	}
	if err == nil && key != nil && !inserted && (w.PersonnelNumber != in.PersonnelNumber || w.PartyPersonID != in.PartyPersonID ||
		w.WorkerType != in.WorkerType || w.LegalName != in.LegalName) {
		return Worker{}, false, idempotencyReuse()
	}
	return w, inserted, err
}

// GetWorker returns the non-PII view of a worker.
func (s *Postgres) GetWorker(ctx context.Context, id string) (Worker, error) {
	p := principal(ctx)
	var w Worker
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		w, err = scanWorker(tx.QueryRow(ctx, `select `+workerColumns+` from worker where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	return w, err
}

// ReadPII opens the vault for one worker. Every call writes an audit row in the
// same transaction and fails closed if that write fails. A reveal records the
// reason; a masked read records only the field names.
func (s *Postgres) ReadPII(ctx context.Context, workerID string, reveal bool, reason, requestID string) (PIIView, error) {
	p := principal(ctx)
	var view PIIView
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var nationalID []byte
		var bank []byte
		err := tx.QueryRow(ctx, `select national_id_sealed, bank_account_sealed from worker_pii where tenant_id = $1 and worker_id = $2`, p.TenantID, workerID).Scan(&nationalID, &bank)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		if err != nil {
			return err
		}
		nid, err := s.sealer.Open(nationalID)
		if err != nil {
			return err
		}
		view = PIIView{NationalID: domain.MaskNationalID(nid), Masked: !reveal}
		var bankPlain *string
		if bank != nil {
			b, err := s.sealer.Open(bank)
			if err != nil {
				return err
			}
			bankPlain = &b
		}
		if reveal {
			view.NationalID = nid
			view.BankAccount = bankPlain
		} else if bankPlain != nil {
			masked := domain.MaskBank(*bankPlain)
			view.BankAccount = &masked
		}
		action, fields := "p3_read", []string{"national_id", "bank_account"}
		if reveal {
			action = "p3_reveal"
		}
		return audit(ctx, tx, p, action, workerID, fields, reason, requestID)
	})
	return view, err
}

func audit(ctx context.Context, tx pgx.Tx, p tenant.Principal, action, entityID string, fields []string, reason, requestID string) error {
	var reasonPtr, requestPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	if requestID != "" {
		requestPtr = &requestID
	}
	_, err := tx.Exec(ctx, `insert into audit_event (tenant_id, id, actor, action, entity, entity_id, fields, reason, request_id)
		values ($1, $2, $3, $4, 'worker', $5, $6, $7, $8)`, p.TenantID, ids.New(), p.UserID, action, entityID, fields, reasonPtr, requestPtr)
	return err
}

// CreateEmployment starts an employment. The primary-per-entity rule is
// enforced by the exclusion constraint and reported as HCM-EMP-001.
func (s *Postgres) CreateEmployment(ctx context.Context, workerID string, in Employment, idempotencyKey string) (Employment, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var e Employment
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanEmployment(tx.QueryRow(ctx, `select `+employmentColumns+` from employment where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				e = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, workerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		var err error
		e, err = scanEmployment(tx.QueryRow(ctx, `insert into employment (tenant_id, id, worker_id, legal_entity_id, employment_type, assignment_kind,
			home_legal_entity_id, planned_end_date, hire_date, termination_date, valid_from, valid_to, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8::date, $9::date, $10::date, $11, $12, $13)
			returning `+employmentColumns,
			p.TenantID, ids.New(), workerID, in.LegalEntityID, in.EmploymentType, in.AssignmentKind, in.HomeLegalEntityID, in.PlannedEndDate,
			in.HireDate, in.TerminationDate, in.ValidFrom, in.ValidTo, key))
		if err != nil {
			return err
		}
		inserted = true
		return enqueue(ctx, tx, "employment", e.ID, hrevents.EmploymentChanged, p.TenantID, hrevents.EmploymentChangedData{
			ID: e.ID, WorkerID: workerID, LegalEntityID: e.LegalEntityID, EmploymentType: e.EmploymentType, AssignmentKind: e.AssignmentKind,
			Action: hrevents.ActionHire, ValidFrom: e.ValidFrom.UTC().Format(time.RFC3339), ValidTo: rfc3339Ptr(e.ValidTo),
		})
	})
	if idempotencyRace(err) {
		return Employment{}, false, idempotencyInFlight()
	}
	if pgConstraint(err, "employment_primary_no_overlap") {
		return Employment{}, false, problem(http.StatusConflict, CodeEmploymentOverlap, "the worker already has an active primary employment in this legal entity on that date")
	}
	if err == nil && key != nil && !inserted && (e.WorkerID != workerID || e.LegalEntityID != in.LegalEntityID || e.EmploymentType != in.EmploymentType ||
		e.AssignmentKind != in.AssignmentKind || e.HireDate != in.HireDate || !sameStringPtr(e.TerminationDate, in.TerminationDate)) {
		return Employment{}, false, idempotencyReuse()
	}
	return e.statusAt(time.Now()), inserted, err
}

func sameStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// MaxPageSize bounds list endpoints; the API defaults to it.
const MaxPageSize = 100

// WorkerPage is one keyset page of workers ordered by personnel number.
type WorkerPage struct {
	Items     []Worker
	NextAfter string
}

// ListWorkers pages workers by personnel number. search matches the personnel
// number prefix or any part of the legal or preferred name, case-insensitively.
func (s *Postgres) ListWorkers(ctx context.Context, search, after string, limit int) (WorkerPage, error) {
	p := principal(ctx)
	page := WorkerPage{Items: []Worker{}}
	pattern := ""
	if search != "" {
		pattern = "%" + likeEscape(search) + "%"
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+workerColumns+` from worker where tenant_id = $1 and personnel_number > $2
			and ($3::text = '' or personnel_number ilike $3 or legal_name ilike $3 or coalesce(preferred_name, '') ilike $3)
			order by personnel_number limit $4`, p.TenantID, after, pattern, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			w, err := scanWorker(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, w)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfter = page.Items[limit-1].PersonnelNumber
	}
	return page, err
}

// likeEscape makes user text literal inside an ILIKE pattern.
func likeEscape(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}

// ListEmployments returns every employment of a worker, oldest first, with
// the status derived as-of now.
func (s *Postgres) ListEmployments(ctx context.Context, workerID string) ([]Employment, error) {
	p := principal(ctx)
	out := []Employment{}
	now := time.Now()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, workerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		rows, err := tx.Query(ctx, `select `+employmentColumns+` from employment where tenant_id = $1 and worker_id = $2 order by valid_from, id`, p.TenantID, workerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEmployment(rows)
			if err != nil {
				return err
			}
			out = append(out, e.statusAt(now))
		}
		return rows.Err()
	})
	return out, err
}
