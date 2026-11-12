// Package store persists revenue contracts with their performance obligations
// and schedule lines, recognition runs and the deferred revenue roll-forward.
// Every call runs in dbx.WithTenantTx so row-level security applies, and every
// state change writes its event to the outbox in the same transaction.
package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	pevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	rev "github.com/BenjaminSRussell/passage/services/revrec/internal/events"
)

const (
	// CodeNotFound is returned for an unknown contract or obligation.
	CodeNotFound = "REV-0100"
	// CodeKeyInFlight is returned when a concurrent request holds the same
	// Idempotency-Key, or a concurrent run holds the same period.
	CodeKeyInFlight = "idempotency_in_progress"
	// CodeVersion is returned when If-Match names a version the contract no longer has.
	CodeVersion = "REV-0103"

	eventSource       = "revrec"
	aggregateContract = "revenue_contract"
	dateLayout        = "2006-01-02"
	periodLayout      = "2006-01"
	uniqueViolation   = "23505"
	deferredAccount   = "2400"
	revenueAccount    = "4000"
)

// Postgres is the revrec repository.
type Postgres struct{ pool *pgxpool.Pool }

// New wraps a pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping reports whether the database answers (readiness).
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Contract is a revenue contract created from one order (FR-29.7).
type Contract struct {
	ID               string
	LegalEntityID    string
	OrderID          string
	CustomerID       string
	Currency         string
	TransactionPrice decimal.Decimal
	ResidualRule     string
	// RatableBasis is DAILY or MONTHLY (§5.29.13); it shaped the RATABLE lines.
	RatableBasis   string
	ContractDate   time.Time
	Status         string
	Version        int
	IdempotencyKey string
	RequestHash    string
	CreatedAt      time.Time
	// Recognized is the revenue recognized so far (all methods); the list
	// shows it with the remaining amount (§5.29.14 revenue contracts inquiry).
	Recognized  decimal.Decimal
	Obligations []Obligation
}

// Obligation is one performance obligation with its schedule lines.
type Obligation struct {
	ID          string
	ContractID  string
	LineNo      int
	ProductID   string
	SSP         decimal.Decimal
	SSPSource   string
	Allocated   decimal.Decimal
	Method      string
	TermStart   *time.Time
	TermEnd     *time.Time
	SatisfiedOn *time.Time
	Schedule    []Schedule
}

// Schedule is one revenue schedule line.
type Schedule struct {
	ID           string
	Seq          int
	Date         *time.Time
	Amount       decimal.Decimal
	Status       string
	RecognizedOn *time.Time
	// Credited is what credit notes took off the line (credited_amount).
	Credited decimal.Decimal
}

// ContractFilter narrows ListContracts; empty fields do not filter. Method
// keeps contracts with at least one obligation of that recognition method.
// After is the id of the last contract of the previous page (keyset cursor).
type ContractFilter struct {
	LegalEntityID string
	CustomerID    string
	OrderID       string
	Status        string
	Method        string
	After         string
	Limit         int
}

// ContractPage is one page of contract headers; NextAfter is the id to pass as
// After for the next page, empty on the last page.
type ContractPage struct {
	Items     []Contract
	NextAfter string
}

// Run is one recognition run: one per legal entity and period end.
type Run struct {
	ID            string
	LegalEntityID string
	PeriodEnd     time.Time
	LineCount     int
	CreatedAt     time.Time
}

// Satisfaction is the outcome of satisfying a milestone obligation. Replayed
// is true when the milestone was already satisfied on the same date.
type Satisfaction struct {
	ContractID      string
	ContractVersion int
	ObligationID    string
	ScheduleID      string
	Currency        string
	Amount          decimal.Decimal
	RecognizedOn    time.Time
	Replayed        bool
}

// CreateContract inserts a contract with its obligations and schedule lines and
// enqueues contract.allocated. A contract with nothing left to recognize is
// COMPLETE at once. A concurrent duplicate idempotency key surfaces as 409.
func (s *Postgres) CreateContract(ctx context.Context, c Contract) (Contract, error) {
	p, err := principal(ctx)
	if err != nil {
		return Contract{}, err
	}
	c.Status = domain.ContractOpen
	var methods, statuses []string
	c.Recognized = decimal.Zero
	for _, o := range c.Obligations {
		methods = append(methods, o.Method)
		for _, sc := range o.Schedule {
			statuses = append(statuses, sc.Status)
			if sc.Status == domain.ScheduleRecognized {
				c.Recognized = c.Recognized.Add(sc.Amount)
			}
		}
	}
	if domain.FullyRecognized(methods, statuses) {
		c.Status = domain.ContractComplete
	}
	c.Version = 1
	if c.RatableBasis == "" {
		c.RatableBasis = domain.BasisDaily
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `insert into revenue_contract (tenant_id, id, legal_entity_id, order_id, customer_id, currency,
			transaction_price, residual_rule, ratable_basis, contract_date, status, version, idempotency_key, request_hash)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) returning created_at`,
			p.TenantID, c.ID, c.LegalEntityID, c.OrderID, c.CustomerID, c.Currency, c.TransactionPrice.String(), c.ResidualRule,
			c.RatableBasis, c.ContractDate.Format(dateLayout), c.Status, c.Version, c.IdempotencyKey, c.RequestHash).Scan(&c.CreatedAt); err != nil {
			return writeErr(err)
		}
		for _, o := range c.Obligations {
			if err := insertObligation(ctx, tx, p.TenantID, c.ID, o); err != nil {
				return err
			}
		}
		return enqueueAllocated(ctx, tx, p.TenantID, c)
	})
	if err != nil {
		return Contract{}, err
	}
	return c, nil
}

// FindContractByKey returns the contract an idempotency key created, if any.
func (s *Postgres) FindContractByKey(ctx context.Context, key string) (Contract, bool, error) {
	var c Contract
	found := true
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		c, err = loadTree(ctx, tx, "idempotency_key = $1", key)
		if errors.Is(err, pgx.ErrNoRows) {
			found = false
			return nil
		}
		return err
	})
	return c, found, err
}

// GetContract returns a contract with its obligations and schedule lines.
func (s *Postgres) GetContract(ctx context.Context, id string) (Contract, error) {
	var c Contract
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		c, err = loadTree(ctx, tx, "id = $1", id)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("contract")
		}
		return err
	})
	return c, err
}

// ListContracts returns one page of contract headers (without obligations) with
// the revenue recognized so far, newest first. The page is keyed on (created_at,
// id) of the After contract, so rows created while the caller pages are neither
// repeated nor skipped; an After the caller cannot see yields an empty page.
func (s *Postgres) ListContracts(ctx context.Context, f ContractFilter) (ContractPage, error) {
	if f.Limit < 1 {
		return ContractPage{}, errors.New("store: list limit must be at least 1")
	}
	var out []Contract
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+contractColumns+`,
			  (select coalesce(sum(s.amount), 0)::text from revenue_schedule s
			     join performance_obligation o on o.tenant_id = s.tenant_id and o.id = s.obligation_id
			    where o.tenant_id = c.tenant_id and o.contract_id = c.id and s.status = 'RECOGNIZED')
			from revenue_contract c
			where ($1 = '' or c.legal_entity_id::text = $1) and ($2 = '' or c.customer_id::text = $2) and ($3 = '' or c.status = $3)
			  and ($5 = '' or exists (select 1 from performance_obligation o
			        where o.tenant_id = c.tenant_id and o.contract_id = c.id and o.method = $5))
			  and ($6 = '' or (c.created_at, c.id) < (select a.created_at, a.id from revenue_contract a where a.id = nullif($6, '')::uuid))
			  and ($7 = '' or c.order_id::text = lower($7))
			order by c.created_at desc, c.id desc limit $4`, f.LegalEntityID, f.CustomerID, f.Status, f.Limit+1, f.Method, f.After, f.OrderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Contract
			var price, recognized string
			if err := rows.Scan(append(contractDest(&c, &price), &recognized)...); err != nil {
				return err
			}
			if c.TransactionPrice, err = decimal.NewFromString(price); err != nil {
				return err
			}
			if c.Recognized, err = decimal.NewFromString(recognized); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return ContractPage{}, err
	}
	page := ContractPage{Items: out}
	if len(out) > f.Limit {
		page.Items = out[:f.Limit]
		page.NextAfter = page.Items[f.Limit-1].ID
	}
	return page, nil
}

// SatisfyMilestone recognizes the single schedule line of a MILESTONE obligation
// on `on` (FR-29.8), bumps the contract version and enqueues
// schedule.recognized. A non-zero expectedVersion must match the contract
// (If-Match). A retry on the same date returns the recorded satisfaction with
// Replayed=true and changes nothing.
func (s *Postgres) SatisfyMilestone(ctx context.Context, obligationID string, on time.Time, expectedVersion int) (Satisfaction, error) {
	p, err := principal(ctx)
	if err != nil {
		return Satisfaction{}, err
	}
	out := Satisfaction{ObligationID: obligationID}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var method, legalEntityID string
		var contractDate time.Time
		// The contract row is locked first, then the milestone's line: the
		// recognition run locks ratable lines and then contracts, and never a
		// milestone line, so the two cannot deadlock.
		err := tx.QueryRow(ctx, `select o.contract_id::text, c.legal_entity_id::text, c.currency::text, c.contract_date, c.version, o.method
			from performance_obligation o
			join revenue_contract c on c.tenant_id = o.tenant_id and c.id = o.contract_id
			where o.id = $1
			for update of c`, obligationID).
			Scan(&out.ContractID, &legalEntityID, &out.Currency, &contractDate, &out.ContractVersion, &method)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("obligation")
		}
		if err != nil {
			return err
		}
		if method != domain.MethodMilestone {
			_, err := domain.CheckSatisfy(method, "", nil, on, contractDate)
			return err
		}
		var status, amount string
		var recognizedOn *time.Time
		if err := tx.QueryRow(ctx, `select id::text, status, amount::text, recognized_on from revenue_schedule
			where obligation_id = $1 order by seq limit 1 for update`, obligationID).
			Scan(&out.ScheduleID, &status, &amount, &recognizedOn); err != nil {
			return err
		}
		if out.Amount, err = decimal.NewFromString(amount); err != nil {
			return err
		}
		replay, err := domain.CheckSatisfy(method, status, recognizedOn, on, contractDate)
		if err != nil {
			return err
		}
		out.RecognizedOn = on
		if replay {
			out.Replayed = true
			return nil
		}
		if expectedVersion != 0 && expectedVersion != out.ContractVersion {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "the contract changed since it was read; reload and review it")
		}
		day := on.Format(dateLayout)
		if _, err := tx.Exec(ctx, `update revenue_schedule set status = $2, recognized_on = $3::date, scheduled_date = $3::date where id = $1`,
			out.ScheduleID, domain.ScheduleRecognized, day); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update performance_obligation set satisfied_on = $2::date where id = $1`, obligationID, day); err != nil {
			return err
		}
		versions, err := touchContracts(ctx, tx, []string{out.ContractID})
		if err != nil {
			return err
		}
		out.ContractVersion = versions[out.ContractID]
		return enqueueRecognized(ctx, tx, p.TenantID, rev.ScheduleRecognizedData{
			ScheduleID: out.ScheduleID, ObligationID: obligationID, ContractID: out.ContractID, LegalEntityID: legalEntityID,
			PeriodID: on.Format(periodLayout), RecognizedOn: day, Currency: out.Currency, Amount: fixed(out.Amount, out.Currency),
			DeferredAccount: deferredAccount, RevenueAccount: revenueAccount,
		})
	})
	if err != nil {
		return Satisfaction{}, err
	}
	return out, nil
}

// RunRecognition recognizes every SCHEDULED RATABLE line of a legal entity dated
// on or before periodEnd (FR-29.8), on periodEnd. The run is keyed by (legal
// entity, period end): a run already recorded is returned unchanged with
// created=false, so a rerun recognizes nothing twice.
func (s *Postgres) RunRecognition(ctx context.Context, legalEntityID string, periodEnd time.Time) (Run, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Run{}, false, err
	}
	end := periodEnd.Format(dateLayout)
	run := Run{LegalEntityID: legalEntityID, PeriodEnd: periodEnd}
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select id::text, line_count, created_at from recognition_run where legal_entity_id = $1::uuid and period_end = $2::date`,
			legalEntityID, end).Scan(&run.ID, &run.LineCount, &run.CreatedAt)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		lines, err := recognizeRatable(ctx, tx, legalEntityID, end)
		if err != nil {
			return err
		}
		run.ID = ids.New()
		run.LineCount = len(lines)
		if err := tx.QueryRow(ctx, `insert into recognition_run (tenant_id, id, legal_entity_id, period_end, line_count)
			values ($1,$2,$3,$4::date,$5) returning created_at`, p.TenantID, run.ID, legalEntityID, end, run.LineCount).Scan(&run.CreatedAt); err != nil {
			return writeErr(err)
		}
		contracts := map[string]bool{}
		var touched []string
		for _, l := range lines {
			if !contracts[l.contractID] {
				contracts[l.contractID] = true
				touched = append(touched, l.contractID)
			}
			if err := enqueueRecognized(ctx, tx, p.TenantID, rev.ScheduleRecognizedData{
				ScheduleID: l.scheduleID, ObligationID: l.obligationID, ContractID: l.contractID, LegalEntityID: legalEntityID,
				PeriodID: periodEnd.Format(periodLayout), RecognizedOn: end, Currency: l.currency, Amount: fixed(l.amount, l.currency),
				DeferredAccount: deferredAccount, RevenueAccount: revenueAccount,
			}); err != nil {
				return err
			}
		}
		if _, err := touchContracts(ctx, tx, touched); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return Run{}, false, err
	}
	return run, created, nil
}

// ListRuns returns recognition runs, latest period first.
func (s *Postgres) ListRuns(ctx context.Context, legalEntityID string, limit int) ([]Run, error) {
	var out []Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, period_end, line_count, created_at from recognition_run
			where ($1 = '' or legal_entity_id::text = $1) order by period_end desc, created_at desc limit $2`, legalEntityID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Run
			if err := rows.Scan(&r.ID, &r.LegalEntityID, &r.PeriodEnd, &r.LineCount, &r.CreatedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// RollForward returns one currency's deferred revenue movement for the legal
// entity between start and end inclusive (FR-29.9). Additions are the deferred
// obligations of contracts dated in the window; recognized are their lines
// recognized in it; opening is what remained deferred before start.
func (s *Postgres) RollForward(ctx context.Context, legalEntityID, currency string, start, end time.Time) (domain.RollForward, error) {
	var out domain.RollForward
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = rollForward(ctx, tx, legalEntityID, "", currency, start, end)
		return err
	})
	return out, err
}

// ContractRollForward is the roll-forward of one contract for the window
// (FR-29.9 "per period and contract", the drill-down of the entity
// roll-forward). It is stated in the contract's currency, returned with it; the
// contract's lines for the window are its schedule by period.
func (s *Postgres) ContractRollForward(ctx context.Context, contractID string, start, end time.Time) (ContractRoll, error) {
	var out ContractRoll
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select legal_entity_id::text, currency::text from revenue_contract where id = $1::uuid`, contractID).
			Scan(&out.LegalEntityID, &out.Currency)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("contract")
		}
		if err != nil {
			return err
		}
		out.RollForward, err = rollForward(ctx, tx, out.LegalEntityID, contractID, out.Currency, start, end)
		return err
	})
	return out, err
}

// ContractRoll is one contract's roll-forward with the entity and currency it is stated in.
type ContractRoll struct {
	LegalEntityID string
	Currency      string
	domain.RollForward
}

// rollForward sums the deferred obligations of the entity's contracts in the
// currency, or of the one contract when contractID is set, with the credit-note
// reversals of those contracts.
func rollForward(ctx context.Context, tx pgx.Tx, legalEntityID, contractID, currency string, start, end time.Time) (domain.RollForward, error) {
	from, to := start.Format(dateLayout), end.Format(dateLayout)
	var raw [8]string
	if err := tx.QueryRow(ctx, rollForwardAdditions, legalEntityID, currency, from, to, domain.DeferredMethods(), contractID).Scan(&raw[0], &raw[1]); err != nil {
		return domain.RollForward{}, err
	}
	if err := tx.QueryRow(ctx, rollForwardRecognized, legalEntityID, currency, from, to, domain.DeferredMethods(), contractID).Scan(&raw[2], &raw[3]); err != nil {
		return domain.RollForward{}, err
	}
	if err := tx.QueryRow(ctx, rollForwardCredits, legalEntityID, currency, from, to, contractID).Scan(&raw[4], &raw[5], &raw[6], &raw[7]); err != nil {
		return domain.RollForward{}, err
	}
	var v [8]decimal.Decimal
	for i, r := range raw {
		d, err := decimal.NewFromString(r)
		if err != nil {
			return domain.RollForward{}, err
		}
		v[i] = d
	}
	// Opening: allocated before the window, less recognized, plus reversed, less the credit notes' deferred amounts.
	opening := v[0].Sub(v[2]).Add(v[6]).Sub(v[4])
	out := domain.NewRollForward(opening, v[1], v[3]).WithCredits(v[7], v[5].Neg())
	var decreases string
	// Billing credits each product whose allocation fell (the negative lines of the billing request), not the net price change: a
	// modification of -300 on one product and +100 on another is a 300 credit note and a 100 invoice.
	if err := tx.QueryRow(ctx, `select coalesce(sum(-(l ->> 'amount')::numeric), 0)::text from contract_modification m
		join modification_billing b on b.tenant_id = m.tenant_id and b.modification_id = m.id
		join revenue_contract c on c.tenant_id = m.tenant_id and c.id = m.contract_id
		cross join lateral jsonb_array_elements(b.body -> 'lines') l
		where c.legal_entity_id = $1::uuid and c.currency = $2 and (l ->> 'amount')::numeric < 0 and m.effective_date <= $3::date
		  and ($4 = '' or c.id = nullif($4, '')::uuid)`, legalEntityID, currency, to, contractID).Scan(&decreases); err != nil {
		return domain.RollForward{}, err
	}
	out.ModificationCredits = decimal.RequireFromString(decreases)
	return out, nil
}

// rollForwardAdditions sums the deferred allocations: each obligation's original allocation (what is allocated now less the changes
// of contract modifications) on the contract date, and each modification's change on its effective date.
const rollForwardAdditions = `select (coalesce(sum(o.allocated_amount - d.total) filter (where c.contract_date < $3::date), 0)
         + coalesce(sum(d.before_window), 0))::text,
       (coalesce(sum(o.allocated_amount - d.total) filter (where c.contract_date between $3::date and $4::date), 0)
         + coalesce(sum(d.in_window), 0))::text
from performance_obligation o
join revenue_contract c on c.tenant_id = o.tenant_id and c.id = o.contract_id
left join lateral (select coalesce(sum(x.delta), 0) as total,
         coalesce(sum(x.delta) filter (where x.effective_date < $3::date), 0) as before_window,
         coalesce(sum(x.delta) filter (where x.effective_date between $3::date and $4::date), 0) as in_window
       from allocation_delta x where x.tenant_id = o.tenant_id and x.obligation_id = o.id) d on true
where c.legal_entity_id = $1::uuid and c.currency = $2 and o.method = any($5::text[])
  and ($6 = '' or c.id = nullif($6, '')::uuid)`

const rollForwardRecognized = `select coalesce(sum(s.amount) filter (where s.recognized_on < $3::date), 0)::text,
       coalesce(sum(s.amount) filter (where s.recognized_on between $3::date and $4::date), 0)::text
from revenue_schedule s
join performance_obligation o on o.tenant_id = s.tenant_id and o.id = s.obligation_id
join revenue_contract c on c.tenant_id = o.tenant_id and c.id = o.contract_id
where c.legal_entity_id = $1::uuid and c.currency = $2 and o.method = any($5::text[]) and s.status = 'RECOGNIZED'
  and ($6 = '' or c.id = nullif($6, '')::uuid)`

// rollForwardCredits sums the credit-note reversals: the deferred amounts billing debited to 2400 (adjustments) and the
// recognized revenue reversed, before and within the window, by the credit note's posting date; and the revenue contract
// modifications took back (reversed, no adjustment: the modification's allocation change is in the additions), by effective date.
const rollForwardCredits = `select coalesce(sum(r.deferred_amount) filter (where r.posting_date < $3::date), 0)::text,
       coalesce(sum(r.deferred_amount) filter (where r.posting_date between $3::date and $4::date), 0)::text,
       coalesce(sum(r.revenue_reversed) filter (where r.posting_date < $3::date), 0)::text,
       coalesce(sum(r.revenue_reversed) filter (where r.posting_date between $3::date and $4::date), 0)::text
from (select tenant_id, contract_id, posting_date, deferred_amount, revenue_reversed from credit_note_reversal
      union all
      select tenant_id, contract_id, posting_date, 0, revenue_reversed from modification_reversal) r
join revenue_contract c on c.tenant_id = r.tenant_id and c.id = r.contract_id
where c.legal_entity_id = $1::uuid and c.currency = $2 and ($5 = '' or c.id = nullif($5, '')::uuid)`

type recognizedLine struct {
	scheduleID   string
	obligationID string
	contractID   string
	currency     string
	amount       decimal.Decimal
}

// recognizeRatable flips the due RATABLE lines to RECOGNIZED and returns them.
func recognizeRatable(ctx context.Context, tx pgx.Tx, legalEntityID, end string) ([]recognizedLine, error) {
	rows, err := tx.Query(ctx, `update revenue_schedule s set status = $3, recognized_on = $2::date
from performance_obligation o join revenue_contract c on c.tenant_id = o.tenant_id and c.id = o.contract_id
where o.tenant_id = s.tenant_id and o.id = s.obligation_id and c.legal_entity_id = $1::uuid
  and o.method = $4 and s.status = $5 and s.scheduled_date <= $2::date
returning s.id::text, s.obligation_id::text, o.contract_id::text, c.currency::text, s.amount::text`,
		legalEntityID, end, domain.ScheduleRecognized, domain.MethodRatable, domain.ScheduleScheduled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []recognizedLine
	for rows.Next() {
		var l recognizedLine
		var amount string
		if err := rows.Scan(&l.scheduleID, &l.obligationID, &l.contractID, &l.currency, &amount); err != nil {
			return nil, err
		}
		if l.amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// touchContracts bumps the version of every changed contract and closes it
// (COMPLETE) when nothing is left to recognize (domain.FullyRecognized: no USAGE
// obligation and no SCHEDULED line). It returns the new versions.
func touchContracts(ctx context.Context, tx pgx.Tx, contractIDs []string) (map[string]int, error) {
	out := map[string]int{}
	if len(contractIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `update revenue_contract c set version = c.version + 1,
	  status = case when c.status = 'OPEN' and not exists (
	      select 1 from performance_obligation o
	      left join revenue_schedule s on s.tenant_id = o.tenant_id and s.obligation_id = o.id
	      where o.tenant_id = c.tenant_id and o.contract_id = c.id and (o.method = 'USAGE' or s.status = 'SCHEDULED'))
	    then 'COMPLETE' else c.status end
	where c.id = any($1::uuid[])
	returning c.id::text, c.version`, contractIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var v int
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

const contractColumns = `c.id::text, c.legal_entity_id::text, c.order_id::text, c.customer_id::text, c.currency::text,
  c.transaction_price::text, c.residual_rule, c.ratable_basis, c.contract_date, c.status, c.version, c.idempotency_key, c.request_hash,
  c.created_at`

func contractDest(c *Contract, price *string) []any {
	return []any{&c.ID, &c.LegalEntityID, &c.OrderID, &c.CustomerID, &c.Currency, price, &c.ResidualRule, &c.RatableBasis,
		&c.ContractDate, &c.Status, &c.Version, &c.IdempotencyKey, &c.RequestHash, &c.CreatedAt}
}

func loadTree(ctx context.Context, tx pgx.Tx, where string, arg any) (Contract, error) {
	var c Contract
	var price string
	err := tx.QueryRow(ctx, `select `+contractColumns+` from revenue_contract c where c.`+where, arg).Scan(contractDest(&c, &price)...)
	if err != nil {
		return Contract{}, err
	}
	if c.TransactionPrice, err = decimal.NewFromString(price); err != nil {
		return Contract{}, err
	}
	return c, loadObligations(ctx, tx, &c)
}

func loadObligations(ctx context.Context, tx pgx.Tx, c *Contract) error {
	rows, err := tx.Query(ctx, `select id::text, line_no, product_id::text, ssp_amount::text, ssp_source, allocated_amount::text, method,
		term_start, term_end, satisfied_on from performance_obligation where contract_id = $1::uuid order by line_no`, c.ID)
	if err != nil {
		return err
	}
	index := map[string]int{}
	for rows.Next() {
		var o Obligation
		var ssp, allocated string
		if err := rows.Scan(&o.ID, &o.LineNo, &o.ProductID, &ssp, &o.SSPSource, &allocated, &o.Method, &o.TermStart, &o.TermEnd, &o.SatisfiedOn); err != nil {
			rows.Close()
			return err
		}
		o.ContractID = c.ID
		if o.SSP, err = decimal.NewFromString(ssp); err != nil {
			rows.Close()
			return err
		}
		if o.Allocated, err = decimal.NewFromString(allocated); err != nil {
			rows.Close()
			return err
		}
		index[o.ID] = len(c.Obligations)
		c.Obligations = append(c.Obligations, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	srows, err := tx.Query(ctx, `select s.id::text, s.obligation_id::text, s.seq, s.scheduled_date, s.amount::text, s.status, s.recognized_on,
		  s.credited_amount::text
		from revenue_schedule s join performance_obligation o on o.tenant_id = s.tenant_id and o.id = s.obligation_id
		where o.contract_id = $1::uuid order by o.line_no, s.seq`, c.ID)
	if err != nil {
		return err
	}
	defer srows.Close()
	c.Recognized = decimal.Zero
	for srows.Next() {
		var sc Schedule
		var obligationID, amount, credited string
		if err := srows.Scan(&sc.ID, &obligationID, &sc.Seq, &sc.Date, &amount, &sc.Status, &sc.RecognizedOn, &credited); err != nil {
			return err
		}
		if sc.Amount, err = decimal.NewFromString(amount); err != nil {
			return err
		}
		if sc.Credited, err = decimal.NewFromString(credited); err != nil {
			return err
		}
		if sc.Status == domain.ScheduleRecognized {
			c.Recognized = c.Recognized.Add(sc.Amount)
		}
		if i, ok := index[obligationID]; ok {
			c.Obligations[i].Schedule = append(c.Obligations[i].Schedule, sc)
		}
	}
	return srows.Err()
}

func insertObligation(ctx context.Context, tx pgx.Tx, tenantID, contractID string, o Obligation) error {
	if _, err := tx.Exec(ctx, `insert into performance_obligation (tenant_id, id, contract_id, line_no, product_id, ssp_amount, ssp_source,
		allocated_amount, method, term_start, term_end, satisfied_on, deferred_account, revenue_account)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		tenantID, o.ID, contractID, o.LineNo, o.ProductID, o.SSP.String(), o.SSPSource, o.Allocated.String(), o.Method,
		dateArg(o.TermStart), dateArg(o.TermEnd), dateArg(o.SatisfiedOn), deferredAccount, revenueAccount); err != nil {
		return err
	}
	for _, sc := range o.Schedule {
		if _, err := tx.Exec(ctx, `insert into revenue_schedule (tenant_id, id, obligation_id, seq, scheduled_date, amount, status, recognized_on)
			values ($1,$2,$3,$4,$5,$6,$7,$8)`,
			tenantID, sc.ID, o.ID, sc.Seq, dateArg(sc.Date), sc.Amount.String(), sc.Status, dateArg(sc.RecognizedOn)); err != nil {
			return err
		}
	}
	return nil
}

func enqueueAllocated(ctx context.Context, tx pgx.Tx, tenantID string, c Contract) error {
	data := rev.ContractAllocatedData{
		ContractID: c.ID, LegalEntityID: c.LegalEntityID, OrderID: c.OrderID, CustomerID: c.CustomerID, Currency: c.Currency,
		TransactionPrice: fixed(c.TransactionPrice, c.Currency), ContractDate: c.ContractDate.Format(dateLayout),
	}
	for _, o := range c.Obligations {
		data.Obligations = append(data.Obligations, rev.ObligationAllocated{
			ID: o.ID, ProductID: o.ProductID, SSP: fixed(o.SSP, c.Currency), SSPSource: o.SSPSource,
			Allocated: fixed(o.Allocated, c.Currency), Method: o.Method,
		})
	}
	evt, err := pevents.New(rev.ContractAllocated, eventSource, c.ID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateContract, c.ID, evt)
}

// enqueueRecognized keys the event by contract so a contract's events stay ordered.
func enqueueRecognized(ctx context.Context, tx pgx.Tx, tenantID string, d rev.ScheduleRecognizedData) error {
	evt, err := pevents.New(rev.ScheduleRecognized, eventSource, d.ScheduleID, tenantID, d)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateContract, d.ContractID, evt)
}

func principal(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return tenant.Principal{}, errors.New("store: no tenant principal in context")
	}
	return p, nil
}

func dateArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(dateLayout)
}

func fixed(v decimal.Decimal, currency string) string {
	return v.StringFixed(money.MinorUnits(currency))
}

func notFound(what string) error {
	return httpx.NewProblem(http.StatusNotFound, CodeNotFound, what+" not found")
}

// writeErr maps a unique violation (a key or a period already in flight) to 409.
func writeErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == uniqueViolation {
		return httpx.NewProblem(http.StatusConflict, CodeKeyInFlight, "a request with this key is already in progress")
	}
	return err
}
