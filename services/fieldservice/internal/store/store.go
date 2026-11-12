// Package store persists field-service aggregates in Postgres under row-level
// security. Every transaction is scoped to the caller's tenant with dbx.WithTenantTx.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/objectstore"
)

// Problem codes returned by the store. Each is listed in the service README.
// The PRD message catalog (8.45.18) owns SVC-SLA-001/002, SVC-WO-001/002 and
// the other numbered codes it lists; codes here avoid those numbers.
const (
	CodeProfileDuplicate  = "SVC-PROF-001"
	CodeProfileNotFound   = "SVC-PROF-002"
	CodeNoTarget          = "SVC-SLA-003"
	CodeInvalidCalendar   = "SVC-SLA-004"
	CodeAgreementUnknown  = "SVC-AGR-001"
	CodeProfileUnknown    = "SVC-AGR-002"
	CodeAgreementCustomer = "SVC-AGR-003"
	CodeAgreementNotFound = "SVC-AGR-004"
	CodeNoEntitlement     = "SVC-ENT-002"
	CodeCaseUnknown       = "SVC-CASE-001"
	CodeCaseCustomer      = "SVC-CASE-002"
	CodeCaseTransition    = "SVC-CASE-003"
	CodeWOUnknown         = "SVC-WO-004"
	CodeWOInvalid         = "SVC-WO-011"
	CodeWOFollowUp        = "SVC-WO-012"
	CodeVersionConflict   = "conflict_version"
	CodeIdempotencyReuse  = "idempotency_key_reuse"
)

const (
	uniqueViolation = "23505"
	eventSource     = "fieldservice"
	dateLayout      = "2006-01-02"
)

// Postgres is the store backed by a pgx pool.
type Postgres struct {
	pool *pgxpool.Pool
	// objects holds the bytes of captures; nil keeps them in Postgres.
	objects objectstore.Store
}

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// WithObjects keeps the bytes of new captures in an object store instead of Postgres (#45). Captures already in Postgres
// stay readable and move over with OffloadCaptures.
func (s *Postgres) WithObjects(o objectstore.Store) *Postgres {
	s.objects = o
	return s
}

// Ping checks the database for the readiness probe.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func tenantOf(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.TenantID
}

func unprocessable(code, detail string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, code, detail)
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

// nextNumber allocates the next document number of a kind for the tenant. The
// counter row is locked by the upsert, so numbers are unique but may have gaps
// after a rollback; gap-free allocation is a later slice.
func nextNumber(ctx context.Context, tx pgx.Tx, tenantID, kind, prefix string) (string, error) {
	var n int64
	err := tx.QueryRow(ctx, `insert into service_number_counter (tenant_id, kind, next) values ($1, $2, 1)
		on conflict (tenant_id, kind) do update set next = service_number_counter.next + 1 returning next`, tenantID, kind).Scan(&n)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%06d", prefix, n), nil
}

// replayOf reports the row id of an earlier request that carried the same
// Idempotency-Key. A different request body under the same key is refused.
func replayOf(ctx context.Context, tx pgx.Tx, table, tenantID, key, requestHash string) (string, bool, error) {
	if key == "" {
		return "", false, nil
	}
	var id, hash string
	err := tx.QueryRow(ctx, fmt.Sprintf(`select id::text, request_hash from %s where tenant_id = $1 and idempotency_key = $2`, table), tenantID, key).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if hash != requestHash {
		return "", false, unprocessable(CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return id, true, nil
}

// SLAProfile is a set of business-hour calendar rules and per-priority targets.
type SLAProfile struct {
	ID           string          `json:"id"`
	Code         string          `json:"code"`
	Name         string          `json:"name"`
	TimeZone     string          `json:"timeZone"`
	AllDay       bool            `json:"allDay"`
	OpenMinute   int             `json:"openMinute"`
	CloseMinute  int             `json:"closeMinute"`
	PauseOnParts bool            `json:"pauseOnParts"`
	Holidays     []string        `json:"holidays"`
	Targets      []domain.Target `json:"targets"`
}

// Calendar builds the business-time calendar of the profile.
func (p SLAProfile) Calendar() (domain.Calendar, error) {
	loc, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		return domain.Calendar{}, err
	}
	if p.AllDay {
		return domain.Calendar{Loc: loc, AllDay: true}, nil
	}
	cal, err := domain.MondayToFriday(loc, p.OpenMinute, p.CloseMinute).WithHolidays(p.Holidays...)
	if err != nil || !cal.Validate() {
		return domain.Calendar{}, errors.New("invalid calendar")
	}
	return cal, nil
}

// Target returns the target for a priority.
func (p SLAProfile) Target(priority int) (domain.Target, bool) {
	for _, t := range p.Targets {
		if t.Priority == priority {
			return t, true
		}
	}
	return domain.Target{}, false
}

// CreateSLAProfile stores a profile. The bool reports whether the call replayed an earlier request.
func (s *Postgres) CreateSLAProfile(ctx context.Context, in SLAProfile, key, requestHash string) (SLAProfile, bool, error) {
	tid := tenantOf(ctx)
	var out SLAProfile
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_sla_profile", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getProfile(ctx, tx, tid, id)
			return err
		}
		if _, err := in.Calendar(); err != nil {
			return unprocessable(CodeInvalidCalendar, "time zone or business hours are not valid")
		}
		targets, err := json.Marshal(in.Targets)
		if err != nil {
			return err
		}
		out = in
		out.ID = ids.New()
		if out.Holidays == nil {
			out.Holidays = []string{}
		}
		if out.AllDay {
			// A 24x7 profile has no opening hours; store the whole day so the
			// row satisfies the hour checks whatever the request carried.
			out.OpenMinute, out.CloseMinute = 0, 24*60
		}
		_, err = tx.Exec(ctx, `insert into service_sla_profile (tenant_id, id, code, name, time_zone, all_day, open_minute, close_minute, pause_on_parts,
			holidays, targets, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::date[], $11, $12, $13)`,
			tid, out.ID, in.Code, in.Name, in.TimeZone, in.AllDay, out.OpenMinute, out.CloseMinute, in.PauseOnParts, out.Holidays, targets, nullable(key), requestHash)
		return err
	})
	if isUnique(err) {
		return SLAProfile{}, false, httpx.NewProblem(http.StatusConflict, CodeProfileDuplicate, "an SLA profile with this code already exists")
	}
	return out, replayed, err
}

const profileColumns = `id::text, code, name, time_zone, all_day, open_minute, close_minute, pause_on_parts, holidays::text[], targets`

func scanProfile(row pgx.Row) (SLAProfile, error) {
	var p SLAProfile
	var targets []byte
	if err := row.Scan(&p.ID, &p.Code, &p.Name, &p.TimeZone, &p.AllDay, &p.OpenMinute, &p.CloseMinute, &p.PauseOnParts, &p.Holidays, &targets); err != nil {
		return p, err
	}
	if p.Holidays == nil {
		p.Holidays = []string{}
	}
	return p, json.Unmarshal(targets, &p.Targets)
}

// getProfile loads a profile that a command references; a missing one is a 422.
func getProfile(ctx context.Context, tx pgx.Tx, tenantID, id string) (SLAProfile, error) {
	p, err := scanProfile(tx.QueryRow(ctx, `select `+profileColumns+` from service_sla_profile where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, unprocessable(CodeProfileUnknown, "SLA profile not found")
	}
	return p, err
}

// GetSLAProfile reads one profile; a missing one is a 404.
func (s *Postgres) GetSLAProfile(ctx context.Context, id string) (SLAProfile, error) {
	tid := tenantOf(ctx)
	var p SLAProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		p, err = scanProfile(tx.QueryRow(ctx, `select `+profileColumns+` from service_sla_profile where tenant_id = $1 and id = $2`, tid, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return p, httpx.NewProblem(http.StatusNotFound, CodeProfileNotFound, "SLA profile not found")
	}
	return p, err
}

// Agreement is a customer's service agreement with its entitlement lines.
type Agreement struct {
	ID              string                 `json:"id"`
	Number          string                 `json:"number"`
	CustomerPartyID string                 `json:"customerPartyId"`
	LegalEntityID   string                 `json:"legalEntityId"`
	ValidFrom       string                 `json:"validFrom"`
	ValidTo         *string                `json:"validTo,omitempty"`
	SLAProfileID    string                 `json:"slaProfileId"`
	BillingModel    string                 `json:"billingModel"`
	PriorityRank    int                    `json:"priorityRank"`
	Status          string                 `json:"status"`
	Lines           []domain.AgreementLine `json:"lines"`
	// HoursConsumed is the included labor consumed by posted work orders (FR-45.10).
	HoursConsumed decimal.Decimal `json:"hoursConsumed"`
	// VisitsConsumed counts the recorded visits over all lines (FR-45.3); the
	// per-line count is in the visit list.
	VisitsConsumed int `json:"visitsConsumed"`
	// StatusReason is the reason given for the last suspension or cancellation.
	StatusReason *string `json:"statusReason,omitempty"`
	// Version is the If-Match / ETag version, bumped by every status change.
	Version int `json:"version"`
}

// CreateAgreement stores an agreement after checking its SLA profile exists.
func (s *Postgres) CreateAgreement(ctx context.Context, in Agreement, key, requestHash string) (Agreement, bool, error) {
	tid := tenantOf(ctx)
	var out Agreement
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_agreement", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getAgreement(ctx, tx, tid, id, false)
			return err
		}
		if _, err := getProfile(ctx, tx, tid, in.SLAProfileID); err != nil {
			return err
		}
		number, err := nextNumber(ctx, tx, tid, "agreement", "SA")
		if err != nil {
			return err
		}
		lines, err := json.Marshal(in.Lines)
		if err != nil {
			return err
		}
		out = in
		out.ID = ids.New()
		out.Number = number
		out.HoursConsumed = decimal.Zero
		out.Version = 1
		_, err = tx.Exec(ctx, `insert into service_agreement (tenant_id, id, number, customer_party_id, legal_entity_id, valid_from, valid_to, sla_profile_id,
			billing_model, priority_rank, status, lines, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6::date, $7::date, $8, $9, $10, $11, $12, $13, $14)`,
			tid, out.ID, number, in.CustomerPartyID, in.LegalEntityID, in.ValidFrom, in.ValidTo, in.SLAProfileID,
			in.BillingModel, in.PriorityRank, in.Status, lines, nullable(key), requestHash)
		return err
	})
	return out, replayed, err
}

// GetAgreement loads one agreement with its consumed hours; a missing one is a 404.
func (s *Postgres) GetAgreement(ctx context.Context, id string) (Agreement, error) {
	tid := tenantOf(ctx)
	var a Agreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		a, err = getAgreement(ctx, tx, tid, id, false)
		return err
	})
	var p *httpx.Problem
	if errors.As(err, &p) && p.Code == CodeAgreementUnknown {
		return a, httpx.NewProblem(http.StatusNotFound, CodeAgreementNotFound, "service agreement not found")
	}
	return a, err
}

const agreementColumns = `a.id::text, a.number, a.customer_party_id::text, a.legal_entity_id::text, a.valid_from::text, a.valid_to::text,
	a.sla_profile_id::text, a.billing_model, a.priority_rank, a.status, a.lines,
	(select coalesce(sum(u.hours), 0)::text from service_entitlement_usage u where u.tenant_id = a.tenant_id and u.agreement_id = a.id),
	(select count(*)::int from service_agreement_visit v where v.tenant_id = a.tenant_id and v.agreement_id = a.id), a.status_reason, a.version`

func scanAgreement(row pgx.Row) (Agreement, error) {
	var a Agreement
	var lines []byte
	var consumed string
	if err := row.Scan(&a.ID, &a.Number, &a.CustomerPartyID, &a.LegalEntityID, &a.ValidFrom, &a.ValidTo, &a.SLAProfileID, &a.BillingModel,
		&a.PriorityRank, &a.Status, &lines, &consumed, &a.VisitsConsumed, &a.StatusReason, &a.Version); err != nil {
		return a, err
	}
	var err error
	if a.HoursConsumed, err = decimal.NewFromString(consumed); err != nil {
		return a, err
	}
	return a, json.Unmarshal(lines, &a.Lines)
}

// getAgreement loads an agreement that a command references; a missing one is
// a 422. forUpdate locks the row so concurrent posts consume included hours in
// turn rather than both reading the same remaining balance.
func getAgreement(ctx context.Context, tx pgx.Tx, tenantID, id string, forUpdate bool) (Agreement, error) {
	lock := ""
	if forUpdate {
		lock = " for update of a"
	}
	a, err := scanAgreement(tx.QueryRow(ctx, `select `+agreementColumns+` from service_agreement a where a.tenant_id = $1 and a.id = $2`+lock, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, unprocessable(CodeAgreementUnknown, "service agreement not found")
	}
	return a, err
}

// customerAgreements loads the ACTIVE agreements a customer holds with one
// legal entity as entitlement candidates: a case opened in one legal entity
// never resolves to another entity's agreement.
func customerAgreements(ctx context.Context, tx pgx.Tx, tenantID, customer, legalEntity string) ([]domain.Agreement, error) {
	rows, err := tx.Query(ctx, `select id::text, sla_profile_id::text, status, valid_from::text, valid_to::text, priority_rank, lines
		from service_agreement where tenant_id = $1 and customer_party_id = $2 and legal_entity_id = $3 and status = 'ACTIVE' order by id`,
		tenantID, customer, legalEntity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Agreement{}
	for rows.Next() {
		var a domain.Agreement
		var from string
		var to *string
		var lines []byte
		if err := rows.Scan(&a.ID, &a.SLAProfileID, &a.Status, &from, &to, &a.PriorityRank, &lines); err != nil {
			return nil, err
		}
		if a.ValidFrom, err = time.Parse(dateLayout, from); err != nil {
			return nil, err
		}
		if to != nil {
			t, err := time.Parse(dateLayout, *to)
			if err != nil {
				return nil, err
			}
			a.ValidTo = &t
		}
		if err := json.Unmarshal(lines, &a.Lines); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }
