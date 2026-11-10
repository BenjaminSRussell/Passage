package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

// CodeConfirmationNotFound answers a run or confirmation the caller cannot see.
const CodeConfirmationNotFound = "AR-CONFIRMATION-NOT-FOUND"

// ConfirmationCounts tallies a run's replies.
type ConfirmationCounts struct {
	Selected  int `json:"selected"`
	Agreed    int `json:"agreed"`
	Disagreed int `json:"disagreed"`
	NoReply   int `json:"noReply"`
}

// ConfirmationRun is one balance confirmation run as of a key date (FR-30.20).
// Confirmations is filled on a single read and empty in a list.
type ConfirmationRun struct {
	ID            string             `json:"id"`
	LegalEntityID string             `json:"legalEntityId"`
	AsOfDate      string             `json:"asOfDate"`
	Threshold     decimal.Decimal    `json:"threshold"`
	SampleSize    *int               `json:"sampleSize,omitempty"`
	IncludeZero   bool               `json:"includeZeroBalances"`
	ReminderDays  int                `json:"reminderDays"`
	CreatedAt     time.Time          `json:"createdAt"`
	Counts        ConfirmationCounts `json:"counts"`
	Confirmations []Confirmation     `json:"confirmations,omitempty"`
}

// Confirmation is one customer and currency row of a run. A reply is immutable once recorded.
type Confirmation struct {
	ID              string           `json:"id"`
	RunID           string           `json:"runId"`
	LegalEntityID   string           `json:"legalEntityId"`
	CustomerID      string           `json:"customerId"`
	Currency        string           `json:"currency"`
	OurBalance      decimal.Decimal  `json:"ourBalance"`
	Status          string           `json:"status"`
	CustomerBalance *decimal.Decimal `json:"customerBalance,omitempty"`
	Difference      *decimal.Decimal `json:"difference,omitempty"`
	Comment         *string          `json:"comment,omitempty"`
	RepliedAt       *time.Time       `json:"repliedAt,omitempty"`
	CaseID          *string          `json:"caseId,omitempty"`
}

// ConfirmationReply is one customer reply to record.
type ConfirmationReply struct {
	ConfirmationID  string
	Reply           string
	CustomerBalance *decimal.Decimal
	Comment         *string
}

const (
	confRunColumns = `r.id, r.legal_entity_id, r.as_of_date::text, r.threshold::text, r.sample_size, r.include_zero, r.reminder_days,
		r.created_at, count(c.id), count(c.id) filter (where c.status = 'AGREED'), count(c.id) filter (where c.status = 'DISAGREED')`
	confRunFrom    = ` from balance_confirmation_run r left join balance_confirmation c on c.tenant_id = r.tenant_id and c.run_id = r.id`
	confRunGroupBy = ` group by r.tenant_id, r.id`
	confColumns    = `id, run_id, legal_entity_id, customer_id, currency, our_balance::text, status, customer_balance::text, comment, replied_at, case_id`
)

func scanConfRun(row rowScanner) (ConfirmationRun, error) {
	var r ConfirmationRun
	var threshold string
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.AsOfDate, &threshold, &r.SampleSize, &r.IncludeZero, &r.ReminderDays, &r.CreatedAt,
		&r.Counts.Selected, &r.Counts.Agreed, &r.Counts.Disagreed); err != nil {
		return ConfirmationRun{}, err
	}
	r.Counts.NoReply = r.Counts.Selected - r.Counts.Agreed - r.Counts.Disagreed
	var err error
	r.Threshold, err = decimal.NewFromString(threshold)
	return r, err
}

func scanConfirmation(row rowScanner) (Confirmation, error) {
	var c Confirmation
	var ours string
	var theirs *string
	if err := row.Scan(&c.ID, &c.RunID, &c.LegalEntityID, &c.CustomerID, &c.Currency, &ours, &c.Status, &theirs,
		&c.Comment, &c.RepliedAt, &c.CaseID); err != nil {
		return Confirmation{}, err
	}
	var err error
	if c.OurBalance, err = decimal.NewFromString(ours); err != nil {
		return Confirmation{}, err
	}
	if theirs != nil {
		v, err := decimal.NewFromString(*theirs)
		if err != nil {
			return Confirmation{}, err
		}
		diff := domain.BalanceDifference(c.OurBalance, v)
		c.CustomerBalance, c.Difference = &v, &diff
	}
	return c, nil
}

func readConfRun(ctx context.Context, tx pgx.Tx, id string) (ConfirmationRun, error) {
	run, err := scanConfRun(tx.QueryRow(ctx, `select `+confRunColumns+confRunFrom+` where r.id = $1`+confRunGroupBy, id))
	return run, notFound(err, CodeConfirmationNotFound, "balance confirmation run not found")
}

func queryConfirmations(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]Confirmation, error) {
	rows, err := tx.Query(ctx, `select `+confColumns+` from balance_confirmation where `+where+` order by customer_id, currency`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Confirmation{}
	for rows.Next() {
		c, err := scanConfirmation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// loadConfRun reads a run with its confirmations.
func loadConfRun(ctx context.Context, tx pgx.Tx, id string) (ConfirmationRun, error) {
	run, err := readConfRun(ctx, tx, id)
	if err != nil {
		return ConfirmationRun{}, err
	}
	run.Confirmations, err = queryConfirmations(ctx, tx, `run_id = $1`, id)
	return run, err
}

// sameRun compares the fields a client controls, for idempotent replay.
func sameRun(stored, in ConfirmationRun) bool {
	sample := func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	}
	return stored.LegalEntityID == in.LegalEntityID && stored.AsOfDate == in.AsOfDate && stored.Threshold.Equal(in.Threshold) &&
		sample(stored.SampleSize) == sample(in.SampleSize) && stored.IncludeZero == in.IncludeZero && stored.ReminderDays == in.ReminderDays
}

// CreateConfirmationRun selects the customers to confirm from the entity's
// balances as of the key date and records one NO_REPLY confirmation per customer
// and currency. The Idempotency-Key is the sampling seed, so a replay returns the
// same selection.
func (s *Postgres) CreateConfirmationRun(ctx context.Context, in ConfirmationRun, key string) (ConfirmationRun, error) {
	p, _ := tenant.FromContext(ctx)
	var out ConfirmationRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existing string
		err := tx.QueryRow(ctx, `select id from balance_confirmation_run where idempotency_key = $1`, key).Scan(&existing)
		if err == nil {
			run, err := loadConfRun(ctx, tx, existing)
			if err != nil {
				return err
			}
			if !sameRun(run, in) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out = run
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		balances, err := asOfBalances(ctx, tx, in.LegalEntityID, in.AsOfDate)
		if err != nil {
			return err
		}
		sample := 0
		if in.SampleSize != nil {
			sample = *in.SampleSize
		}
		picked := domain.SelectConfirmations(balances, in.Threshold, sample, in.IncludeZero, key)
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into balance_confirmation_run (tenant_id, id, legal_entity_id, as_of_date, threshold,
			sample_size, include_zero, reminder_days, idempotency_key)
			values ($1, $2, $3, $4::date, $5::numeric, $6, $7, $8, $9)`,
			p.TenantID, id, in.LegalEntityID, in.AsOfDate, in.Threshold.String(), in.SampleSize, in.IncludeZero, in.ReminderDays, key); err != nil {
			return err
		}
		for _, b := range picked {
			if _, err := tx.Exec(ctx, `insert into balance_confirmation (tenant_id, id, run_id, legal_entity_id, customer_id, currency, our_balance)
				values ($1, $2, $3, $4, $5, $6, $7::numeric)`,
				p.TenantID, ids.New(), id, in.LegalEntityID, b.CustomerID, b.Currency, b.Balance.String()); err != nil {
				return err
			}
		}
		out, err = loadConfRun(ctx, tx, id)
		return err
	})
	return out, mapWriteErr(err)
}

// asOfBalances is each customer's balance per currency as of the key date: every
// item documented on or before it, less the settlements and down payment
// clearings made on or before it, and less the deductions taken off it that
// were approved, written off, denied (the re-bill item carries the amount on)
// or split (the children carry it on) by then; a deduction still being worked
// is owed. Later movements are added back, so a run dated in the past reports
// the balance the customer had on that day.
// The balance is in the item currency; open_item has no entity-currency amount yet.
func asOfBalances(ctx context.Context, tx pgx.Tx, legalEntityID, asOf string) ([]domain.CustomerBalance, error) {
	rows, err := tx.Query(ctx, `select oi.customer_id::text, oi.currency, sum(oi.amount
			- coalesce((select sum(st.settled_amount + st.cash_discount_taken + st.write_off_amount) from settlement st
				where st.tenant_id = oi.tenant_id and st.open_item_id = oi.id and st.settled_at::date <= $2::date), 0)
			- coalesce((select sum(cs.amount) from credit_settlement cs
				where cs.tenant_id = oi.tenant_id and cs.debit_item_id = oi.id and cs.settled_at::date <= $2::date), 0)
			+ coalesce((select sum(cs.amount) from credit_settlement cs
				where cs.tenant_id = oi.tenant_id and cs.credit_item_id = oi.id and cs.settled_at::date <= $2::date), 0)
			- coalesce((select sum(cl.amount) from down_payment_clearing cl
				where cl.tenant_id = oi.tenant_id and cl.open_item_id = oi.id and cl.cleared_at::date <= $2::date), 0)
			- coalesce((select coalesce(sum(dd.amount) filter (where dd.parent_id is null), 0)
				- coalesce(sum(dd.amount) filter (where dd.resolved_at is null or dd.resolved_at::date > $2::date), 0)
				from deduction dd where dd.tenant_id = oi.tenant_id and dd.open_item_id = oi.id and dd.created_at::date <= $2::date), 0))::text
		from open_item oi
		where oi.legal_entity_id = $1 and oi.document_date <= $2::date and oi.status <> 'REVERSED'
		group by oi.customer_id, oi.currency`, legalEntityID, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CustomerBalance
	for rows.Next() {
		var customer, currency, sum string
		if err := rows.Scan(&customer, &currency, &sum); err != nil {
			return nil, err
		}
		v, err := decimal.NewFromString(sum)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.CustomerBalance{CustomerID: customer, Currency: currency, Balance: v})
	}
	return out, rows.Err()
}

// ListConfirmationRuns reads runs with their reply counts, newest first.
func (s *Postgres) ListConfirmationRuns(ctx context.Context, limit int) ([]ConfirmationRun, error) {
	out := []ConfirmationRun{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+confRunColumns+confRunFrom+confRunGroupBy+` order by r.created_at desc, r.id limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanConfRun(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetConfirmationRun reads a run with its confirmations.
func (s *Postgres) GetConfirmationRun(ctx context.Context, id string) (ConfirmationRun, error) {
	var out ConfirmationRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadConfRun(ctx, tx, id)
		return err
	})
	return out, err
}

// RecordConfirmationReply records a customer's reply once (FR-30.20). A
// DISAGREED reply opens a DISPUTE case with the customer's balance attached
// (BR-30.13) in the same transaction. Repeating the recorded reply returns it;
// any other reply to an answered confirmation is 409.
func (s *Postgres) RecordConfirmationReply(ctx context.Context, runID string, in ConfirmationReply) (Confirmation, error) {
	p, _ := tenant.FromContext(ctx)
	var out Confirmation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanConfirmation(tx.QueryRow(ctx, `select `+confColumns+` from balance_confirmation
			where id = $1 and run_id = $2 for update`, in.ConfirmationID, runID))
		if err != nil {
			return notFound(err, CodeConfirmationNotFound, "balance confirmation not found in this run")
		}
		theirs, derr := domain.ReplyBalance(in.Reply, cur.OurBalance, in.CustomerBalance)
		if derr != nil {
			return domainProblemFrom(derr)
		}
		if cur.Status != domain.ReplyNone {
			if cur.Status == in.Reply && cur.CustomerBalance != nil && cur.CustomerBalance.Equal(theirs) && eqStrPtr(cur.Comment, in.Comment) {
				out = cur
				return nil
			}
			return domainProblem(domain.CodeConfirmationReplied, "the customer's reply ("+cur.Status+") is already recorded; replies are immutable")
		}
		var caseRef *string
		if in.Reply == domain.ReplyDisagreed {
			opened, err := insertCase(ctx, tx, p.TenantID, Case{LegalEntityID: cur.LegalEntityID, CustomerID: cur.CustomerID,
				Category: domain.CaseCategoryDispute, Owner: domain.ConfirmationCaseOwner, Priority: domain.CasePriorityNormal,
				Reason: domain.BalanceDifferenceReason, Currency: cur.Currency, AttachedAmount: &theirs}, "confirmation:"+cur.ID)
			if err != nil {
				return err
			}
			caseRef = &opened.ID
		}
		out, err = scanConfirmation(tx.QueryRow(ctx, `update balance_confirmation set status = $3, customer_balance = $4::numeric,
			comment = $5, replied_at = now(), case_id = $6, version = version + 1
			where tenant_id = $1 and id = $2 returning `+confColumns,
			p.TenantID, cur.ID, in.Reply, theirs.String(), in.Comment, caseRef))
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "confirmation", out.ID, rcvevents.ConfirmationReplied, p.TenantID, rcvevents.ConfirmationRepliedData{
			ConfirmationID: out.ID, RunID: out.RunID, LegalEntityID: out.LegalEntityID, CustomerID: out.CustomerID, Currency: out.Currency,
			Reply: out.Status, OurBalance: out.OurBalance, CustomerBalance: theirs, Difference: domain.BalanceDifference(out.OurBalance, theirs),
			CaseID: caseRef,
		})
	})
	return out, mapWriteErr(err)
}

// ConfirmationDifferences is a run's difference list for audit (FR-30.20): every
// DISAGREED reply with both balances and the case it opened.
func (s *Postgres) ConfirmationDifferences(ctx context.Context, runID string) ([]Confirmation, error) {
	var out []Confirmation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := readConfRun(ctx, tx, runID); err != nil {
			return err
		}
		var err error
		out, err = queryConfirmations(ctx, tx, `run_id = $1 and status = 'DISAGREED'`, runID)
		return err
	})
	return out, err
}
