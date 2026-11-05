package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	kevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
	"github.com/BenjaminSRussell/passage/services/expense/internal/events"
)

// Problem codes raised by the cash advance store (listed in the service README).
const (
	CodeAdvanceLimit       = "EXP-ADV-001"
	CodeAdvanceBlocked     = "EXP-ADV-002"
	CodeAdvanceRequisition = "EXP-ADV-004"
	CodeAdvanceState       = "EXP-ADV-005"
	CodeAdvanceNotFound    = "EXP-ADV-006"
	CodeAdvanceReturn      = "EXP-ADV-007"
	CodeAdvanceVersion     = "EXP-ADV-008"
)

const outboxAdvance = "cash_advance"

// AdvancePolicy is the legal-entity parameter set that gates a new request (8.43.13):
// the maximum per request (default 2,000.00) and the settlement due days after trip
// end (default 30).
type AdvancePolicy struct {
	MaxAmount decimal.Decimal
	DueDays   int
}

// Advance is a persisted cash advance with its settlement amounts (BR-43.9).
type Advance struct {
	ID                  string
	LegalEntityID       string
	WorkerID            string
	Amount              decimal.Decimal
	Currency            string
	Purpose             string
	TravelRequisitionID string
	TripEnd             time.Time
	DueDate             time.Time
	Status              string
	Paid                decimal.Decimal
	Settled             decimal.Decimal
	Returned            decimal.Decimal
	Recovered           decimal.Decimal
	APPrepaymentID      string
	RequestedBy         string
	DecidedBy           string
	Version             int
	CreatedAt           time.Time
}

// Domain returns the advance as the pure domain type for the settlement arithmetic.
func (a Advance) Domain() domain.CashAdvance {
	return domain.CashAdvance{
		ID: a.ID, WorkerID: a.WorkerID, Currency: a.Currency, Amount: a.Amount, Paid: a.Paid,
		Settled: a.Settled, Returned: a.Returned, Recovered: a.Recovered, TripEnd: a.TripEnd,
	}
}

// Overdue is true when the advance still has an open balance after its stored due date.
func (a Advance) Overdue(asOf time.Time) bool {
	return a.Domain().Open().IsPositive() && utcDay(asOf).After(a.DueDate)
}

// NewAdvance is the input to RequestAdvance.
type NewAdvance struct {
	LegalEntityID       string
	WorkerID            string
	Amount              decimal.Decimal
	Currency            string
	Purpose             string
	TravelRequisitionID string
	TripEnd             time.Time
}

// AdvanceFilter narrows ListAdvances. Empty fields do not filter.
type AdvanceFilter struct {
	WorkerID string
	Status   string
}

const advanceColumns = `id::text, legal_entity_id::text, worker_id::text, amount::text, currency::text, purpose,
  coalesce(travel_requisition_id::text, ''), trip_end, due_settlement_date, status, paid_amount::text,
  settled_amount::text, returned_amount::text, recovered_via_payroll_amount::text, coalesce(ap_prepayment_id::text, ''),
  requested_by, coalesce(decided_by, ''), version, created_at`

// RequestAdvance records a SUBMITTED advance (requesting it routes it for approval).
// It refuses an amount over the policy maximum (EXP-ADV-001), a worker with a PAID or
// PARTIALLY_SETTLED advance still open after its due date on asOf (EXP-ADV-002), and a
// travel requisition of another worker or legal entity, or one that is REJECTED or
// CLOSED (EXP-ADV-004).
// A replayed key with the same body returns the original with replayed=true.
func (s *Store) RequestAdvance(ctx context.Context, key, hash string, in NewAdvance, pol AdvancePolicy, asOf time.Time) (Advance, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Advance{}, false, err
	}
	if !tenant.InLegalEntityScope(p, in.LegalEntityID) {
		return Advance{}, false, outOfScope()
	}
	var out Advance
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id, stored string
		err := tx.QueryRow(ctx, `select id::text, request_hash from cash_advance where tenant_id=$1 and create_key=$2`, p.TenantID, key).Scan(&id, &stored)
		if err == nil {
			if stored != hash {
				return reuse()
			}
			replayed = true
			out, err = loadAdvance(ctx, tx, p.TenantID, id)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.Amount.GreaterThan(pol.MaxAmount) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAdvanceLimit, "advance exceeds the limit "+pol.MaxAmount.StringFixed(money.MinorUnits(in.Currency)))
		}
		if in.TravelRequisitionID != "" {
			rq, err := loadRequisition(ctx, tx, p.TenantID, in.TravelRequisitionID)
			if err != nil {
				return err
			}
			if rq.WorkerID != in.WorkerID || rq.LegalEntityID != in.LegalEntityID ||
				rq.Status == string(domain.RequisitionRejected) || rq.Status == string(domain.RequisitionClosed) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAdvanceRequisition,
					"the travel requisition must belong to the same worker and legal entity and be neither rejected nor closed")
			}
		}
		// Lock the worker's open advances so two concurrent requests see the same block.
		open, err := queryAdvances(ctx, tx, `select `+advanceColumns+` from cash_advance
			where tenant_id=$1 and worker_id=$2 and status in ('PAID', 'PARTIALLY_SETTLED') for update`, p.TenantID, in.WorkerID)
		if err != nil {
			return err
		}
		for _, a := range open {
			if a.Overdue(asOf) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAdvanceBlocked,
					"You have advances past settlement due; new advances blocked")
			}
		}
		id = ids.New()
		_, err = tx.Exec(ctx, `insert into cash_advance(tenant_id, id, legal_entity_id, worker_id, amount, currency, purpose,
			travel_requisition_id, trip_end, due_settlement_date, requested_by, create_key, request_hash)
			values($1,$2,$3,$4,$5::numeric,$6,$7,nullif($8, '')::uuid,$9,$10,$11,$12,$13)`,
			p.TenantID, id, in.LegalEntityID, in.WorkerID, in.Amount.String(), in.Currency, in.Purpose,
			in.TravelRequisitionID, in.TripEnd, domain.DueDate(in.TripEnd, pol.DueDays), p.UserID, key, hash)
		if err != nil {
			return translate(err, CodeIdempotencyReuse)
		}
		out, err = loadAdvance(ctx, tx, p.TenantID, id)
		return err
	})
	if err != nil {
		return Advance{}, false, err
	}
	return out, replayed, nil
}

// GetAdvance loads one advance.
func (s *Store) GetAdvance(ctx context.Context, id string) (Advance, error) {
	p, err := principal(ctx)
	if err != nil {
		return Advance{}, err
	}
	var out Advance
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadAdvance(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// ListAdvances returns up to limit advances after the cursor (an id), in id order.
// The next cursor is empty on the last page.
func (s *Store) ListAdvances(ctx context.Context, f AdvanceFilter, after string, limit int) ([]Advance, string, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, "", err
	}
	if after == "" {
		after = ids.Nil
	}
	var out []Advance
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = queryAdvances(ctx, tx, `select `+advanceColumns+` from cash_advance
			where tenant_id=$1 and id > $2::uuid and ($3 = '' or worker_id::text = $3) and ($4 = '' or status = $4)
			order by id limit $5`, p.TenantID, after, f.WorkerID, f.Status, limit+1)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	out, next := pageOf(out, limit, func(a Advance) string { return a.ID })
	return out, next, nil
}

// DecideAdvance approves or rejects a SUBMITTED advance (hr.team.approve). The user who
// requested the advance may not decide it (EXP-SOD-001).
func (s *Store) DecideAdvance(ctx context.Context, id string, approve bool, c Command) (Advance, bool, error) {
	p, err := decider(ctx)
	if err != nil {
		return Advance{}, false, err
	}
	target := domain.AdvanceRejected
	if approve {
		target = domain.AdvanceApproved
	}
	return s.changeAdvance(ctx, p.TenantID, id, c, func(tx pgx.Tx, cur Advance) error {
		if !domain.CanTransitionAdvance(domain.AdvanceLifecycle(cur.Status), target) {
			return advanceState(cur.Status, "be decided")
		}
		if cur.RequestedBy != "" && cur.RequestedBy == p.UserID {
			return ownDecision("advance")
		}
		_, err := tx.Exec(ctx, `update cash_advance set status=$3, decided_by=$4, decided_at=now(), updated_at=now(), version=version+1
			where tenant_id=$1 and id=$2`, p.TenantID, id, string(target), p.UserID)
		return err
	})
}

// PayAdvance records that an APPROVED advance was paid in full by the AP prepayment
// prepaymentID (wave 18/19) and commits dev.passage.expense.cashadvance.paid.v1 to the
// outbox in the same transaction.
func (s *Store) PayAdvance(ctx context.Context, id, prepaymentID string, c Command) (Advance, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Advance{}, false, err
	}
	return s.changeAdvance(ctx, p.TenantID, id, c, func(tx pgx.Tx, cur Advance) error {
		if !domain.CanTransitionAdvance(domain.AdvanceLifecycle(cur.Status), domain.AdvancePaid) {
			return advanceState(cur.Status, "be paid")
		}
		if _, err := tx.Exec(ctx, `update cash_advance set status=$3, paid_amount=amount, ap_prepayment_id=$4::uuid,
			paid_at=now(), updated_at=now(), version=version+1 where tenant_id=$1 and id=$2`,
			p.TenantID, id, domain.AdvancePaid, prepaymentID); err != nil {
			return err
		}
		evt, err := kevents.New(events.CashAdvancePaid, events.Source, id, p.TenantID, events.CashAdvancePaidData{
			AdvanceID: id, WorkerID: cur.WorkerID, Amount: cur.Amount.StringFixed(money.MinorUnits(cur.Currency)),
			Currency: cur.Currency, PrepaymentID: prepaymentID,
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, outboxAdvance, id, evt)
	})
}

// ReturnAdvance records cash the worker handed back (BR-43.9 option a) against a PAID
// or PARTIALLY_SETTLED advance. The return may not exceed the open balance
// (EXP-ADV-007), and the status is re-derived from the amounts: a return that clears
// the balance makes the advance SETTLED.
func (s *Store) ReturnAdvance(ctx context.Context, id string, amount decimal.Decimal, c Command) (Advance, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Advance{}, false, err
	}
	return s.changeAdvance(ctx, p.TenantID, id, c, func(tx pgx.Tx, cur Advance) error {
		next, err := cur.Domain().Return(amount)
		switch {
		case errors.Is(err, domain.ErrAdvanceNotPaid):
			return advanceState(cur.Status, "take a return")
		case errors.Is(err, domain.ErrOverReturn):
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAdvanceReturn,
				"return is more than the open balance "+cur.Domain().Open().StringFixed(money.MinorUnits(cur.Currency)))
		case err != nil:
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAdvanceReturn, err.Error())
		}
		status, err := next.SettlementStatus()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update cash_advance set returned_amount=$3::numeric, status=$4, updated_at=now(), version=version+1
			where tenant_id=$1 and id=$2`, p.TenantID, id, next.Returned.String(), status)
		return err
	})
}

// changeAdvance runs one command against a locked advance: a replayed key returns the
// current advance, a stale If-Match is EXP-ADV-008, and a successful apply stores the
// key with the outcome.
func (s *Store) changeAdvance(ctx context.Context, tenantID, id string, c Command, apply func(pgx.Tx, Advance) error) (Advance, bool, error) {
	var out Advance
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanAdvance(tx.QueryRow(ctx, `select `+advanceColumns+` from cash_advance where tenant_id=$1 and id=$2 for update`, tenantID, id))
		if err != nil {
			return notFoundAdvance(err)
		}
		found, err := priorCommand(ctx, tx, tenantID, c)
		if err != nil {
			return err
		}
		if !found {
			if err := checkVersion(c, cur.Version, CodeAdvanceVersion, "cash advance"); err != nil {
				return err
			}
			if err := apply(tx, cur); err != nil {
				return err
			}
			if err := remember(ctx, tx, tenantID, c.Key, c.Hash, http.StatusOK, map[string]string{"advanceId": id}); err != nil {
				return err
			}
		}
		replayed = found
		out, err = loadAdvance(ctx, tx, tenantID, id)
		return err
	})
	if err != nil {
		return Advance{}, false, err
	}
	return out, replayed, nil
}

func queryAdvances(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]Advance, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Advance{}
	for rows.Next() {
		a, err := scanAdvance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func loadAdvance(ctx context.Context, tx pgx.Tx, tenantID, id string) (Advance, error) {
	a, err := scanAdvance(tx.QueryRow(ctx, `select `+advanceColumns+` from cash_advance where tenant_id=$1 and id=$2`, tenantID, id))
	if err != nil {
		return Advance{}, notFoundAdvance(err)
	}
	return a, nil
}

func scanAdvance(r pgx.Row) (Advance, error) {
	var a Advance
	var amount, paid, settled, returned, recovered string
	if err := r.Scan(&a.ID, &a.LegalEntityID, &a.WorkerID, &amount, &a.Currency, &a.Purpose, &a.TravelRequisitionID,
		&a.TripEnd, &a.DueDate, &a.Status, &paid, &settled, &returned, &recovered, &a.APPrepaymentID,
		&a.RequestedBy, &a.DecidedBy, &a.Version, &a.CreatedAt); err != nil {
		return Advance{}, err
	}
	a.TripEnd, a.DueDate = utcDay(a.TripEnd), utcDay(a.DueDate)
	for _, f := range []struct {
		raw string
		dst *decimal.Decimal
	}{{amount, &a.Amount}, {paid, &a.Paid}, {settled, &a.Settled}, {returned, &a.Returned}, {recovered, &a.Recovered}} {
		d, err := decimal.NewFromString(f.raw)
		if err != nil {
			return Advance{}, err
		}
		*f.dst = d
	}
	return a, nil
}

func notFoundAdvance(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewProblem(http.StatusNotFound, CodeAdvanceNotFound, "cash advance not found")
	}
	return err
}

func advanceState(status, action string) error {
	return httpx.NewProblem(http.StatusConflict, CodeAdvanceState, "cash advance is "+status+" and cannot "+action)
}
