package store

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

const (
	TransactionPending = "pending"
	TransactionPosted  = "posted"
	TransactionRefused = "refused"
)

type NewLineTransaction struct {
	ReconciliationID string
	LineNo           int
	Kind             string
}

type LineTransaction struct {
	ID               string  `json:"id"`
	ReconciliationID string  `json:"reconciliationId"`
	LegalEntityID    string  `json:"legalEntityId"`
	BankAccountID    string  `json:"bankAccountId"`
	StatementID      string  `json:"statementId"`
	LineNo           int     `json:"lineNo"`
	Kind             string  `json:"kind"`
	Amount           string  `json:"amount"`
	Currency         string  `json:"currency"`
	PostingDate      string  `json:"postingDate"`
	Status           string  `json:"status"`
	LedgerEntryID    *string `json:"ledgerEntryId,omitempty"`
	MatchID          *string `json:"matchId,omitempty"`
	Version          int     `json:"version"`

	Replayed bool `json:"-"`
}

const lineTransactionColumns = `id, reconciliation_id, legal_entity_id, bank_account_id, statement_id, line_no, kind, amount::text, currency,
	posting_date::text, status, ledger_entry_id, match_id::text, version`

func scanLineTransaction(row pgx.Row) (LineTransaction, error) {
	var t LineTransaction
	err := row.Scan(&t.ID, &t.ReconciliationID, &t.LegalEntityID, &t.BankAccountID, &t.StatementID, &t.LineNo, &t.Kind, &t.Amount, &t.Currency,
		&t.PostingDate, &t.Status, &t.LedgerEntryID, &t.MatchID, &t.Version)
	return t, err
}

func lineNotFound() error {
	return notFound(CodeLineNotFound, "the statement line is not on this reconciliation")
}

func loadLine(ctx context.Context, tx pgx.Tx, tenantID, statementID string, lineNo int) (StatementLine, error) {
	var l StatementLine
	err := tx.QueryRow(ctx, `select line_no, booking_date::text, value_date::text, amount::text, currency, btc_code, end_to_end_ref, reference, counterparty_name, status
		from bank_statement_line where tenant_id = $1 and statement_id = $2 and line_no = $3`, tenantID, statementID, lineNo).
		Scan(&l.LineNo, &l.BookingDate, &l.ValueDate, &l.Amount, &l.Currency, &l.BTCCode, &l.EndToEndRef, &l.Reference, &l.CounterpartyName, &l.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, lineNotFound()
	}
	return l, err
}

func lockedReconciliationStatement(ctx context.Context, tx pgx.Tx, tenantID, id string) (statementID string, err error) {
	var state string
	err = tx.QueryRow(ctx, `select statement_id::text, state from bank_reconciliation where tenant_id = $1 and id = $2 for share`, tenantID, id).Scan(&statementID, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notFound(CodeReconNotFound, "reconciliation not found")
	}
	if err == nil && state == StateClosed {
		err = closedProblem()
	}
	return statementID, err
}

func hasPostingInFlight(ctx context.Context, tx pgx.Tx, tenantID, statementID string, lineNo int) (bool, error) {
	var n int
	err := tx.QueryRow(ctx, `select count(*) from bank_line_transaction where tenant_id = $1 and statement_id = $2 and line_no = $3 and status in ('pending', 'posted')`,
		tenantID, statementID, lineNo).Scan(&n)
	return n > 0, err
}

func (s *Postgres) ExcludeLine(ctx context.Context, reconciliationID string, lineNo int, reason string) (StatementLine, error) {
	p, _ := tenant.FromContext(ctx)
	var out StatementLine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		statementID, err := lockedReconciliationStatement(ctx, tx, p.TenantID, reconciliationID)
		if err != nil {
			return err
		}
		var status, legalEntityID, amount string
		err = tx.QueryRow(ctx, `select status, legal_entity_id::text, amount::text from bank_statement_line where tenant_id = $1 and statement_id = $2 and line_no = $3 for update`,
			p.TenantID, statementID, lineNo).Scan(&status, &legalEntityID, &amount)
		if errors.Is(err, pgx.ErrNoRows) {
			return lineNotFound()
		}
		if err != nil {
			return err
		}
		switch status {
		case "excluded":
		case "matched", "created":
			return httpx.NewProblem(http.StatusConflict, CodeLineMatched, "the statement line is matched; unmatch it before excluding it")
		default:
			if busy, err := hasPostingInFlight(ctx, tx, p.TenantID, statementID, lineNo); err != nil || busy {
				if err != nil {
					return err
				}
				return postingProblem()
			}
			if _, err := tx.Exec(ctx, `update bank_statement_line set status = 'excluded', version = version + 1 where tenant_id = $1 and statement_id = $2 and line_no = $3`,
				p.TenantID, statementID, lineNo); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into reconciliation_line_exclusion (tenant_id, statement_id, line_no, legal_entity_id, amount, reason, excluded_by)
				values ($1, $2, $3, $4, $5, $6, $7)
				on conflict (tenant_id, statement_id, line_no) do update set reason = excluded.reason, excluded_by = excluded.excluded_by, active = true, updated_at = now()`,
				p.TenantID, statementID, lineNo, legalEntityID, amount, reason, createdBy(p)); err != nil {
				return err
			}
		}
		out, err = loadLine(ctx, tx, p.TenantID, statementID, lineNo)
		return err
	})
	return out, err
}

func (s *Postgres) IncludeLine(ctx context.Context, reconciliationID string, lineNo int) (StatementLine, error) {
	p, _ := tenant.FromContext(ctx)
	var out StatementLine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		statementID, err := lockedReconciliationStatement(ctx, tx, p.TenantID, reconciliationID)
		if err != nil {
			return err
		}
		var status string
		err = tx.QueryRow(ctx, `select status from bank_statement_line where tenant_id = $1 and statement_id = $2 and line_no = $3 for update`,
			p.TenantID, statementID, lineNo).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return lineNotFound()
		}
		if err != nil {
			return err
		}
		if status == "excluded" {
			if _, err := tx.Exec(ctx, `update bank_statement_line set status = 'unmatched', version = version + 1 where tenant_id = $1 and statement_id = $2 and line_no = $3`,
				p.TenantID, statementID, lineNo); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update reconciliation_line_exclusion set active = false, updated_at = now() where tenant_id = $1 and statement_id = $2 and line_no = $3`,
				p.TenantID, statementID, lineNo); err != nil {
				return err
			}
		}
		out, err = loadLine(ctx, tx, p.TenantID, statementID, lineNo)
		return err
	})
	return out, err
}

func (s *Postgres) BeginLineTransaction(ctx context.Context, in NewLineTransaction) (LineTransaction, error) {
	p, _ := tenant.FromContext(ctx)
	var out LineTransaction
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		statementID, err := lockedReconciliationStatement(ctx, tx, p.TenantID, in.ReconciliationID)
		if err != nil {
			return err
		}
		var legalEntityID, accountID, accountCurrency string
		if err := tx.QueryRow(ctx, `select r.legal_entity_id::text, r.bank_account_id::text, a.currency from bank_reconciliation r
			join bank_account a on a.tenant_id = r.tenant_id and a.id = r.bank_account_id where r.tenant_id = $1 and r.id = $2`,
			p.TenantID, in.ReconciliationID).Scan(&legalEntityID, &accountID, &accountCurrency); err != nil {
			return err
		}
		var status, currency, amountText, bookingDate string
		err = tx.QueryRow(ctx, `select status, currency, amount::text, booking_date::text from bank_statement_line where tenant_id = $1 and statement_id = $2 and line_no = $3 for update`,
			p.TenantID, statementID, in.LineNo).Scan(&status, &currency, &amountText, &bookingDate)
		if errors.Is(err, pgx.ErrNoRows) {
			return lineNotFound()
		}
		if err != nil {
			return err
		}
		existing, err := scanLineTransaction(tx.QueryRow(ctx, `select `+lineTransactionColumns+` from bank_line_transaction
			where tenant_id = $1 and statement_id = $2 and line_no = $3 for update`, p.TenantID, statementID, in.LineNo))
		switch {
		case err == nil:
			if existing.Kind != in.Kind {
				return httpx.NewProblem(http.StatusConflict, CodeLinePosting, "this statement line already has a "+strings.ReplaceAll(existing.Kind, "_", " ")+" transaction")
			}
			if existing.Status == TransactionPosted {
				existing.Replayed = true
				out = existing
				return nil
			}
			if existing.Status == TransactionRefused {
				if _, err := tx.Exec(ctx, `update bank_line_transaction set status = 'pending', refusal_code = null, version = version + 1, updated_at = now()
					where tenant_id = $1 and id = $2`, p.TenantID, existing.ID); err != nil {
					return err
				}
				existing.Status = TransactionPending
				existing.Version++
			}
			out = existing
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if status != "unmatched" && status != "suggested" {
			return httpx.NewProblem(http.StatusConflict, CodeLineMatched, "only an unmatched statement line can be turned into a bank transaction")
		}
		if currency != accountCurrency {
			return unprocessable(CodeLineKind, "the statement line is in "+currency+" but the bank account is in "+accountCurrency)
		}
		amount, err := decimal.NewFromString(amountText)
		if err != nil {
			return err
		}
		if !domain.LineKindFits(in.Kind, amount) {
			return unprocessable(CodeLineKind, "a "+strings.ReplaceAll(in.Kind, "_", " ")+" does not fit a statement line of "+amount.String()+" "+currency)
		}
		out, err = scanLineTransaction(tx.QueryRow(ctx, `insert into bank_line_transaction (tenant_id, id, legal_entity_id, bank_account_id, reconciliation_id, statement_id, line_no,
			kind, amount, currency, posting_date, created_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) returning `+lineTransactionColumns,
			p.TenantID, ids.New(), legalEntityID, accountID, in.ReconciliationID, statementID, in.LineNo, in.Kind, amountText, currency, bookingDate, createdBy(p)))
		return err
	})
	return out, AsGuardError(err)
}

func (s *Postgres) FinishLineTransaction(ctx context.Context, id, ledgerEntryID string) (LineTransaction, error) {
	p, _ := tenant.FromContext(ctx)
	var out LineTransaction
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := scanLineTransaction(tx.QueryRow(ctx, `select `+lineTransactionColumns+` from bank_line_transaction where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return lineNotFound()
		}
		if err != nil {
			return err
		}
		if t.Status == TransactionPosted {
			t.Replayed = true
			out = t
			return nil
		}
		if t.Status != TransactionPending {
			return postingProblem()
		}
		entryID := strings.ToLower(ledgerEntryID)
		matchID := ids.New()
		if _, err := tx.Exec(ctx, `update bank_line_transaction set status = 'posted', ledger_entry_id = $3, match_id = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id, entryID, matchID); err != nil {
			return err
		}
		reason := "created from statement line: " + strings.ReplaceAll(t.Kind, "_", " ")
		m := Match{ID: matchID, LegalEntityID: t.LegalEntityID, BankAccountID: t.BankAccountID, StatementID: t.StatementID, Kind: "manual", Status: "matched",
			Difference: "0", Reason: reason, CreatedBy: createdBy(p), Version: 1,
			Lines: []MatchLine{{LineNo: t.LineNo, Amount: t.Amount}},
			Items: []MatchItem{{Type: "ledger", ID: entryID, Currency: t.Currency, Amount: t.Amount}}}
		if _, err := writeMatches(ctx, tx, p.TenantID, t.StatementID, []Match{m}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update bank_reconciliation set book_balance = book_balance + $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, t.ReconciliationID, t.Amount); err != nil {
			return err
		}
		out, err = scanLineTransaction(tx.QueryRow(ctx, `select `+lineTransactionColumns+` from bank_line_transaction where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	return out, AsGuardError(err)
}

func (s *Postgres) RefuseLineTransaction(ctx context.Context, id, code string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update bank_line_transaction set status = 'refused', refusal_code = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and status = 'pending'`, p.TenantID, id, code)
		return err
	})
}
