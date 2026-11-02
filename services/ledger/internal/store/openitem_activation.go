package store

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// Activation run error codes and aggregate (FR-11.19, BR-11.18).
const (
	CodeOIAlreadyActive = "LEDGER_OI_ALREADY_ACTIVE"

	activationAggregate = "open_item_activation"
)

// ActivationInput is one activation run request. Hash covers the ledger, account and date.
type ActivationInput struct {
	LedgerID string
	Account  string
	Date     time.Time
	Key      string
	Hash     []byte
}

// ActivationRun is the stored result of an activation run. Totals are decimal strings in the ledger currency.
type ActivationRun struct {
	ID             string `json:"id"`
	LedgerID       string `json:"ledgerId"`
	Account        string `json:"account"`
	AccountName    string `json:"accountName"`
	ActivationDate string `json:"activationDate"`
	ItemsCreated   int    `json:"itemsCreated"`
	// LineItemsCreated counts the lines dated after the activation date that became open items of their own.
	LineItemsCreated int       `json:"lineItemsCreated"`
	TotalDebit       string    `json:"totalDebit"`
	TotalCredit      string    `json:"totalCredit"`
	CreatedBy        string    `json:"createdBy"`
	CreatedAt        time.Time `json:"createdAt"`
	hash             []byte
}

const activationSelect = `select r.id::text, r.ledger_id::text, a.account_number, a.name, r.activation_date::text, r.items_created, r.line_items_created,
		r.total_debit::text, r.total_credit::text, r.created_by, r.created_at, r.payload_hash
	from open_item_activation_run r
	join main_account a on a.tenant_id = r.tenant_id and a.id = r.main_account_id`

// activationLines selects the lines of the account in the ledger that have no open item yet
// (lines of an earlier managed spell already have items, all cleared because none may be
// open) and splits them at the activation date ($4): opening holds one net balance per
// (dimension combination, transaction currency) of the lines dated on or before it,
// across all periods; later holds each line dated after it, which becomes an ordinary
// open item. Every amount is signed, debit positive, in the ledger currency. Lines must
// be in the ledger currency until wave 12 (LEDGER_CURRENCY_MISMATCH), so the currency
// split yields one item per combination today. $1 tenant, $2 ledger, $3 account.
const activationLines = `with lines as (
		select jl.id, jl.dimension_combination_id, jl.currency_txn, je.posting_date, jl.source_line_ref,
			case when jl.debit_credit = 'D' then jl.amount_acct else -jl.amount_acct end as net
		from journal_line jl
		join journal_entry je on je.tenant_id = jl.tenant_id and je.id = jl.entry_id
		where jl.tenant_id = $1 and jl.main_account_id = $3 and je.ledger_id = $2 and jl.amount_acct <> 0
			and not exists (select 1 from open_item i where i.tenant_id = jl.tenant_id and i.journal_line_id = jl.id)
	), opening as (
		select dimension_combination_id, currency_txn, sum(net) as net
		from lines where posting_date <= $4::text::date
		group by dimension_combination_id, currency_txn
		having sum(net) <> 0
	), later as (
		select id, dimension_combination_id, posting_date, source_line_ref, net
		from lines where posting_date > $4::text::date
	)`

func scanActivation(row pgx.Row, currency string) (ActivationRun, error) {
	var r ActivationRun
	var debit, credit string
	if err := row.Scan(&r.ID, &r.LedgerID, &r.Account, &r.AccountName, &r.ActivationDate, &r.ItemsCreated, &r.LineItemsCreated, &debit, &credit,
		&r.CreatedBy, &r.CreatedAt, &r.hash); err != nil {
		return r, err
	}
	d, err := decimal.NewFromString(debit)
	if err != nil {
		return r, err
	}
	c, err := decimal.NewFromString(credit)
	if err != nil {
		return r, err
	}
	r.TotalDebit = d.StringFixed(money.MinorUnits(currency))
	r.TotalCredit = c.StringFixed(money.MinorUnits(currency))
	return r, nil
}

// ActivateOpenItemAccount switches open-item management on for an account that
// already has a balance (FAGL_SWITCH_TO_OPEN_ITEM semantics, FR-11.19). In one
// transaction under the ledger lock it creates one opening open item (source
// type activation, dated the activation date, assignment ACTIVATION) for every
// (dimension combination, transaction currency) whose lines dated on or before
// the activation date do not net to zero, and an ordinary gl_line item for every
// line dated after it, so the open items add up to the account's balance; it
// stores the run and turns the setting on with the run id. The account must
// qualify under BR-11.18, must not be managed already (409
// LEDGER_OI_ALREADY_ACTIVE) and must have no item still open (409
// LEDGER_OI_OPEN_ITEMS_EXIST). No journal entry is written, so the period status
// of the activation date is not checked. A repeated Idempotency-Key on the
// ledger returns the stored run (replayed = true); the same key with another
// body is a 409.
func (s *Postgres) ActivateOpenItemAccount(ctx context.Context, in ActivationInput) (ActivationRun, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return ActivationRun{}, false, err
	}
	var out ActivationRun
	var replayed bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, true)
		if err != nil {
			return err
		}
		existing, err := scanActivation(tx.QueryRow(ctx, activationSelect+` where r.tenant_id = $1 and r.ledger_id = $2 and r.idempotency_key = $3`,
			p.TenantID, ledger.ID, in.Key), ledger.Currency)
		if err == nil {
			if !bytes.Equal(existing.hash, in.Hash) {
				return httpx.NewProblem(http.StatusConflict, CodeIdempotencyConflict, "Idempotency-Key was already used with a different request body")
			}
			out, replayed = existing, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var accountID, name, accountType, recon string
		var postable bool
		err = tx.QueryRow(ctx, `select id::text, name, account_type, is_postable, reconciliation_account_type
			from main_account where tenant_id = $1 and chart_code = $2 and account_number = $3`,
			p.TenantID, ledger.ChartCode, in.Account).Scan(&accountID, &name, &accountType, &postable, &recon)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeAccountNotFound, "account "+in.Account+" does not exist in chart "+ledger.ChartCode)
		}
		if err != nil {
			return err
		}
		if !oiEligible(accountType, postable, recon) {
			return oiViolation(CodeOIAccountInvalid, "account",
				"open-item management needs a postable balance-sheet account that is not a customer or vendor reconciliation account")
		}
		var active bool
		err = tx.QueryRow(ctx, `select enabled from open_item_setting where tenant_id = $1 and ledger_id = $2 and main_account_id = $3 for update`,
			p.TenantID, ledger.ID, accountID).Scan(&active)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if active {
			return httpx.NewProblem(http.StatusConflict, CodeOIAlreadyActive, "account "+in.Account+" is already open-item managed in this ledger")
		}
		// Items left over from an earlier activation or switch-off that are still open would double the balance.
		var stillOpen int
		if err := tx.QueryRow(ctx, `select count(*)::int from open_item where tenant_id = $1 and ledger_id = $2 and main_account_id = $3
			and status in ('open', 'reset_open')`, p.TenantID, ledger.ID, accountID).Scan(&stillOpen); err != nil {
			return err
		}
		if stillOpen > 0 {
			return httpx.NewProblem(http.StatusConflict, CodeOIItemsExist, "account "+in.Account+" still has open items; clear them first")
		}
		// The ledger row lock taken above is the one every posting takes before it writes lines
		// (writeEntry), so no line can land on the account between reading its lines here and
		// switching the setting on below: a concurrent posting waits, then sees the setting and
		// makes its own item. Lines are the source of truth (the balance projection is built from
		// them and has no transaction-currency split).
		date := dateText(in.Date)
		var count, lineCount int
		var debit, credit string
		if err := tx.QueryRow(ctx, activationLines+`, created as (select net from opening union all select net from later)
			select (select count(*)::int from opening), (select count(*)::int from later),
				coalesce((select sum(net) from created where net > 0), 0)::text, coalesce((select -sum(net) from created where net < 0), 0)::text`,
			p.TenantID, ledger.ID, accountID, date).Scan(&count, &lineCount, &debit, &credit); err != nil {
			return err
		}
		runID := ids.New()
		if _, err := tx.Exec(ctx, `insert into open_item_activation_run (tenant_id, id, legal_entity_id, ledger_id, main_account_id, activation_date,
				items_created, line_items_created, total_debit, total_credit, idempotency_key, payload_hash, created_by)
			values ($1, $2, $3, $4, $5, $6::text::date, $7, $8, $9::text::numeric, $10::text::numeric, $11, $12, $13)`,
			p.TenantID, runID, ledger.LegalEntityID, ledger.ID, accountID, date, count, lineCount, debit, credit, in.Key, in.Hash, p.UserID); err != nil {
			return idempotencyRace(err)
		}
		if _, err := tx.Exec(ctx, activationLines+`
			insert into open_item (tenant_id, id, legal_entity_id, ledger_id, main_account_id, dimension_combination_id, journal_line_id, source_type,
				amount, currency, assignment, posting_date, activation_run_id)
			select $1::uuid, gen_random_uuid(), $5::uuid, $2::uuid, $3::uuid, o.dimension_combination_id, null::uuid, 'activation', o.net, $6::text,
				'ACTIVATION', $4::text::date, $7::uuid
			from opening o
			union all
			select $1::uuid, gen_random_uuid(), $5::uuid, $2::uuid, $3::uuid, l.dimension_combination_id, l.id, 'gl_line', l.net, $6::text,
				left(l.source_line_ref, 50), l.posting_date, $7::uuid
			from later l`,
			p.TenantID, ledger.ID, accountID, date, ledger.LegalEntityID, ledger.Currency, runID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into open_item_setting (tenant_id, legal_entity_id, ledger_id, main_account_id, enabled, changed_by, activation_run_id)
			values ($1, $2, $3, $4, true, $5, $6)
			on conflict (tenant_id, ledger_id, main_account_id) do update set enabled = true, changed_by = excluded.changed_by,
				changed_at = now(), activation_run_id = excluded.activation_run_id`,
			p.TenantID, ledger.LegalEntityID, ledger.ID, accountID, p.UserID, runID); err != nil {
			return err
		}
		out, err = scanActivation(tx.QueryRow(ctx, activationSelect+` where r.tenant_id = $1 and r.id = $2`, p.TenantID, runID), ledger.Currency)
		if err != nil {
			return err
		}
		return enqueueFor(ctx, tx, activationAggregate, runID, ledgerevents.OpenItemActivated, p.TenantID, ledgerevents.OpenItemActivatedData{
			RunID: runID, LedgerID: ledger.ID, Account: in.Account, ActivationDate: dateText(in.Date), ItemsCreated: count, LineItemsCreated: lineCount,
			TotalDebit: out.TotalDebit, TotalCredit: out.TotalCredit, Actor: p.UserID,
		})
	})
	return out, replayed, err
}

// ListActivationRuns returns the activation runs of a ledger, newest first (at most 200).
func (s *Postgres) ListActivationRuns(ctx context.Context, ledgerID string) ([]ActivationRun, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []ActivationRun{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, activationSelect+` where r.tenant_id = $1 and r.ledger_id = $2 order by r.created_at desc, r.id limit 200`, p.TenantID, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanActivation(rows, ledger.Currency)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
