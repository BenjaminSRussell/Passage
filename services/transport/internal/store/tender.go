package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
)

// SweepLimit caps how many overdue tenders one expiry sweep advances.
const SweepLimit = 500

const tenderColumns = `id, load_id, status, carriers, rates, coalesce(currency, ''), timeout_seconds, attempts,
	coalesce(accepted_carrier_id::text, ''), planner_task, version, created_at, request_hash`

// TenderFilter narrows the tender list. Empty fields match everything.
type TenderFilter struct {
	Status string
	LoadID string
}

// CreateTender stores a tender whose first attempt the caller has already sent.
// Tenders are serialised per load with a transaction advisory lock. A load with
// an ACCEPTED tender, or with a PENDING tender that is still inside its deadline
// at now, refuses another (TMS-0114). A PENDING tender past its deadline is
// cascaded first, so a tender that has failed by time no longer blocks a new one.
// A load whose latest DG check FAILed is refused with TMS-0130 and nothing is
// stored (AC-34.13, dg_check_mode BLOCK); so is a load with an OPEN screening
// hold, with TMS-0133 (BR-34.17, AC-34.16).
func (s *Postgres) CreateTender(ctx context.Context, in domain.Tender, idem Idem, now time.Time) (domain.Tender, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Tender
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay := func() (bool, error) {
			if idem.Key == "" {
				return false, nil
			}
			t, hash, err := scanTender(tx.QueryRow(ctx, `select `+tenderColumns+` from tender where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idem.Key))
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if hash != idem.Fingerprint {
				return false, reuse()
			}
			out = t
			return true, nil
		}
		if done, err := replay(); done || err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('transport.tender:' || $1 || ':' || $2))`, p.TenantID, in.LoadID); err != nil {
			return err
		}
		// A concurrent request with the same key may have committed while this one waited.
		if done, err := replay(); done || err != nil {
			return err
		}
		if err := dgGate(ctx, tx, p.TenantID, in.LoadID); err != nil {
			return err
		}
		if err := screeningGate(ctx, tx, p.TenantID, in.LoadID); err != nil {
			return err
		}
		open, _, err := scanTender(tx.QueryRow(ctx, `select `+tenderColumns+` from tender
			where tenant_id = $1 and load_id = $2 and status in ('PENDING', 'ACCEPTED') for update`, p.TenantID, in.LoadID))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case open.Status == domain.TenderAccepted:
			return httpx.NewProblem(http.StatusConflict, domain.CodeTenderOpen, "load already has accepted tender "+open.ID)
		default:
			if err := advance(ctx, tx, p.TenantID, &open, open.TickAt(now)); err != nil {
				return err
			}
			if open.Status == domain.TenderPending {
				return httpx.NewProblem(http.StatusConflict, domain.CodeTenderOpen, "load already has pending tender "+open.ID)
			}
		}
		carriers, rates, attempts, err := marshalTender(in)
		if err != nil {
			return err
		}
		out, _, err = scanTender(tx.QueryRow(ctx, `insert into tender (tenant_id, id, load_id, status, carriers, rates, currency, timeout_seconds,
			attempts, planner_task, pending_expires_at, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9::jsonb, $10, $11, $12, $13)
			returning `+tenderColumns,
			p.TenantID, ids.New(), in.LoadID, in.Status, carriers, rates, nullable(in.Currency), int(in.Timeout/time.Second),
			attempts, in.PlannerTask, pendingDeadline(in), nullable(idem.Key), nullable(idem.Fingerprint)))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeTenderOpen, "load already has an open tender")
		}
		return err
	})
	return out, err
}

// GetTender returns one tender as stored. A pending attempt past its deadline is
// shown as stored until the expiry sweep or a response cascades it.
func (s *Postgres) GetTender(ctx context.Context, id string) (domain.Tender, error) {
	p, _ := tenant.FromContext(ctx)
	var t domain.Tender
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		t, _, err = scanTender(tx.QueryRow(ctx, `select `+tenderColumns+` from tender where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Tender{}, NotFound("tender")
	}
	return t, err
}

// ListTenders returns the newest tenders first (tender history, §6.34.14).
func (s *Postgres) ListTenders(ctx context.Context, f TenderFilter) ([]domain.Tender, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Tender{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+tenderColumns+` from tender
			where tenant_id = $1 and ($2::text = '' or status = $2) and ($3::text = '' or load_id::text = $3)
			order by created_at desc, id limit $4`, p.TenantID, f.Status, f.LoadID, ListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, _, err := scanTender(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// RespondTender applies a carrier's accept or decline under a row lock (BR-34.3).
// Deadlines that passed before now are cascaded first and saved even when the
// response is then rejected: a late acceptance is TMS-0105, a carrier the tender
// is not waiting on is TMS-0113. The same carrier repeating its last response
// gets the tender back unchanged. Events go to the outbox in the same transaction.
func (s *Postgres) RespondTender(ctx context.Context, id, carrierID string, accept bool, now time.Time) (domain.Tender, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Tender
	var rejected error
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, _, err := scanTender(tx.QueryRow(ctx, `select `+tenderColumns+` from tender where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("tender")
		}
		if err != nil {
			return err
		}
		if t.Responded(carrierID, accept) {
			out = t
			return nil
		}
		expired, ruleErr := t.RespondAt(carrierID, accept, now)
		var de *domain.Error
		switch {
		case errors.As(ruleErr, &de):
			rejected = httpx.NewProblem(http.StatusConflict, de.Code, "tender expired for this carrier; the acceptance is rejected")
		case errors.Is(ruleErr, domain.ErrNotPending):
			rejected = httpx.NewProblem(http.StatusConflict, domain.CodeTenderNotPending, "the tender is not waiting on this carrier")
		case ruleErr != nil:
			return ruleErr
		}
		if rejected != nil && len(expired) == 0 {
			return nil
		}
		if err := advance(ctx, tx, p.TenantID, &t, expired); err != nil {
			return err
		}
		out = t
		if rejected != nil {
			return nil
		}
		typ := tmsevents.TenderDeclined
		if accept {
			typ = tmsevents.TenderAccepted
		}
		return enqueueTender(ctx, tx, p.TenantID, t, lastAttemptFor(t, carrierID), typ)
	})
	if err != nil {
		return domain.Tender{}, err
	}
	if rejected != nil {
		return domain.Tender{}, rejected
	}
	return out, nil
}

// ExpireTenders is the expiry sweep: every PENDING tender whose pending attempt
// is past its deadline at now is cascaded to the next carrier, or fails with a
// planner task after the last one, and one tender.expired event is written per
// expired attempt. Rows another sweep holds are skipped. It returns the
// tenders it advanced; a Temporal TenderWorkflow timer will drive the same rule.
func (s *Postgres) ExpireTenders(ctx context.Context, now time.Time) ([]domain.Tender, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Tender{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+tenderColumns+` from tender
			where tenant_id = $1 and status = 'PENDING' and pending_expires_at <= $2
			order by pending_expires_at, id limit $3 for update skip locked`, p.TenantID, now, SweepLimit)
		if err != nil {
			return err
		}
		var due []domain.Tender
		for rows.Next() {
			t, _, err := scanTender(rows)
			if err != nil {
				rows.Close()
				return err
			}
			due = append(due, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range due {
			if err := advance(ctx, tx, p.TenantID, &due[i], due[i].TickAt(now)); err != nil {
				return err
			}
			out = append(out, due[i])
		}
		return nil
	})
	return out, err
}

// advance saves the tender's cascade state and writes one tender.expired event
// per newly expired attempt. It bumps t.Version to the stored value.
func advance(ctx context.Context, tx pgx.Tx, tenantID string, t *domain.Tender, expired []domain.Attempt) error {
	_, _, attempts, err := marshalTender(*t)
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `update tender set status = $3, attempts = $4::jsonb, accepted_carrier_id = $5::uuid, planner_task = $6,
		pending_expires_at = $7, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2 returning version`,
		tenantID, t.ID, t.Status, attempts, nullable(t.AcceptedCarrier), t.PlannerTask, pendingDeadline(*t)).Scan(&t.Version); err != nil {
		return err
	}
	for _, a := range expired {
		if err := enqueueTender(ctx, tx, tenantID, *t, a, tmsevents.TenderExpired); err != nil {
			return err
		}
	}
	return nil
}

func enqueueTender(ctx context.Context, tx pgx.Tx, tenantID string, t domain.Tender, a domain.Attempt, typ string) error {
	data := tmsevents.TenderPayload{TenderID: t.ID, LoadID: t.LoadID, CarrierID: a.CarrierID, Rank: a.Rank, Response: a.Response}
	if rate, ok := t.Rates[a.CarrierID]; ok {
		data.Rate, data.Currency = rate.StringFixed(2), t.Currency
	}
	evt, err := events.New(typ, eventSource, t.ID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "tender", t.ID, evt)
}

// lastAttemptFor returns the most recent attempt sent to carrierID.
func lastAttemptFor(t domain.Tender, carrierID string) domain.Attempt {
	for i := len(t.Attempts) - 1; i >= 0; i-- {
		if t.Attempts[i].CarrierID == carrierID {
			return t.Attempts[i]
		}
	}
	return domain.Attempt{CarrierID: carrierID}
}

func pendingDeadline(t domain.Tender) *time.Time {
	if a, ok := t.Pending(); ok && t.Status == domain.TenderPending {
		return &a.ExpiresAt
	}
	return nil
}

func marshalTender(t domain.Tender) (carriers, rates, attempts string, err error) {
	c, a := t.Carriers, t.Attempts
	if c == nil {
		c = []string{}
	}
	if a == nil {
		a = []domain.Attempt{}
	}
	r := map[string]string{}
	for carrier, rate := range t.Rates {
		r[carrier] = rate.String()
	}
	cb, err := json.Marshal(c)
	if err != nil {
		return "", "", "", err
	}
	rb, err := json.Marshal(r)
	if err != nil {
		return "", "", "", err
	}
	ab, err := json.Marshal(a)
	if err != nil {
		return "", "", "", err
	}
	return string(cb), string(rb), string(ab), nil
}

func scanTender(row pgx.Row) (domain.Tender, string, error) {
	var t domain.Tender
	var carriers, rates, attempts []byte
	var seconds int
	var hash *string
	if err := row.Scan(&t.ID, &t.LoadID, &t.Status, &carriers, &rates, &t.Currency, &seconds, &attempts,
		&t.AcceptedCarrier, &t.PlannerTask, &t.Version, &t.CreatedAt, &hash); err != nil {
		return domain.Tender{}, "", err
	}
	t.Timeout = time.Duration(seconds) * time.Second
	if err := json.Unmarshal(carriers, &t.Carriers); err != nil {
		return domain.Tender{}, "", err
	}
	if err := json.Unmarshal(attempts, &t.Attempts); err != nil {
		return domain.Tender{}, "", err
	}
	var r map[string]string
	if err := json.Unmarshal(rates, &r); err != nil {
		return domain.Tender{}, "", err
	}
	if len(r) > 0 {
		t.Rates = make(map[string]decimal.Decimal, len(r))
		for carrier, rate := range r {
			d, err := decimal.NewFromString(rate)
			if err != nil {
				return domain.Tender{}, "", err
			}
			t.Rates[carrier] = d
		}
	}
	for i := range t.Attempts {
		t.Attempts[i].SentAt, t.Attempts[i].ExpiresAt = t.Attempts[i].SentAt.UTC(), t.Attempts[i].ExpiresAt.UTC()
	}
	t.CreatedAt = t.CreatedAt.UTC()
	return t, deref(hash), nil
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
