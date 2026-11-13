package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

// CodeReportConflict is raised when a report id was imported before with other content.
const CodeReportConflict = "TRS-0525"

// IntradayImport is one parsed camt.052 report to store against a bank account.
type IntradayImport struct {
	BankAccountID string
	FileHash      string
	Parsed        domain.IntradayReport
}

// IntradayBalance is a stored balance of a report; Amount is signed.
type IntradayBalance struct {
	Code   string    `json:"code"`
	Amount string    `json:"amount"`
	AsOf   time.Time `json:"asOf"`
}

// IntradayReport is the stored header of an imported report with its balances.
type IntradayReport struct {
	ID            string            `json:"id"`
	BankAccountID string            `json:"bankAccountId"`
	LegalEntityID string            `json:"legalEntityId"`
	ReportID      string            `json:"reportId"`
	Format        string            `json:"format"`
	Currency      string            `json:"currency"`
	CreatedAtBank time.Time         `json:"createdAtBank"`
	ImportedAt    time.Time         `json:"importedAt"`
	Balances      []IntradayBalance `json:"balances"`
}

// IntradayResult reports whether the call stored a new report or replayed one already imported.
type IntradayResult struct {
	Report   IntradayReport
	Replayed bool
}

// PositionAccount is an active bank account with what its cash position is built from.
type PositionAccount struct {
	Account   BankAccount
	Statement *domain.StatementBalance
	Intraday  *domain.IntradayReport
	Opening   *domain.OpeningBalance
}

const intradayColumns = `id, bank_account_id, legal_entity_id, report_id, format, currency, created_at_bank, imported_at`

func scanIntraday(row pgx.Row) (IntradayReport, error) {
	var r IntradayReport
	err := row.Scan(&r.ID, &r.BankAccountID, &r.LegalEntityID, &r.ReportID, &r.Format, &r.Currency, &r.CreatedAtBank, &r.ImportedAt)
	r.Balances = []IntradayBalance{}
	return r, err
}

func loadIntradayBalances(ctx context.Context, tx pgx.Tx, tenantID string, r *IntradayReport) error {
	rows, err := tx.Query(ctx, `select code, amount::text, as_of from intraday_balance where tenant_id = $1 and report_id = $2 order by seq`, tenantID, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b IntradayBalance
		if err := rows.Scan(&b.Code, &b.Amount, &b.AsOf); err != nil {
			return err
		}
		r.Balances = append(r.Balances, b)
	}
	return rows.Err()
}

// ImportIntradayReport stores a camt.052 report against an active bank account of the file's own account and
// currency. The same bytes, or the same report id with the same balances, replay the stored report.
func (s *Postgres) ImportIntradayReport(ctx context.Context, in IntradayImport) (IntradayResult, error) {
	res, err := s.importIntraday(ctx, in)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return s.importIntraday(ctx, in)
	}
	return res, err
}

func (s *Postgres) importIntraday(ctx context.Context, in IntradayImport) (IntradayResult, error) {
	p, _ := tenant.FromContext(ctx)
	var res IntradayResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status, currency, legalEntityID string
		var accountHash, statementAccountHash []byte
		err := tx.QueryRow(ctx, `select status, currency, legal_entity_id, iban_hash, statement_account_hash from bank_account where tenant_id = $1 and id = $2`, p.TenantID, in.BankAccountID).
			Scan(&status, &currency, &legalEntityID, &accountHash, &statementAccountHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeAccountNotFound, "bank account not found")
		}
		if err != nil {
			return err
		}
		fileAccount := ibanHash(in.Parsed.Account)
		if string(accountHash) != string(fileAccount) && (statementAccountHash == nil || string(statementAccountHash) != string(fileAccount)) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeStatementAccount, "the report belongs to another account")
		}
		if existing, err := scanIntraday(tx.QueryRow(ctx, `select `+intradayColumns+` from intraday_report where tenant_id = $1 and bank_account_id = $2 and file_hash = $3`, p.TenantID, in.BankAccountID, in.FileHash)); err == nil {
			if err := loadIntradayBalances(ctx, tx, p.TenantID, &existing); err != nil {
				return err
			}
			res = IntradayResult{Report: existing, Replayed: true}
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := scanIntraday(tx.QueryRow(ctx, `select `+intradayColumns+` from intraday_report where tenant_id = $1 and bank_account_id = $2 and report_id = $3`, p.TenantID, in.BankAccountID, in.Parsed.ReportID)); err == nil {
			return httpx.NewProblem(http.StatusConflict, CodeReportConflict, "a report with this id was imported with other content")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if status != string(domain.AccountActive) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAccountInactive, "reports can only be imported into an active bank account")
		}
		if in.Parsed.Currency != currency {
			return &domain.Error{Code: domain.CodeStatementCurrency, Detail: "report currency " + in.Parsed.Currency + " differs from account currency " + currency}
		}
		stored, err := scanIntraday(tx.QueryRow(ctx, `insert into intraday_report (tenant_id, id, legal_entity_id, bank_account_id, report_id, format, file_hash, currency, created_at_bank)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning `+intradayColumns,
			p.TenantID, ids.New(), legalEntityID, in.BankAccountID, in.Parsed.ReportID, in.Parsed.Format, in.FileHash, in.Parsed.Currency, in.Parsed.CreatedAt))
		if err != nil {
			return err
		}
		for i, b := range in.Parsed.Balances {
			if _, err := tx.Exec(ctx, `insert into intraday_balance (tenant_id, report_id, seq, legal_entity_id, code, amount, as_of) values ($1, $2, $3, $4, $5, $6, $7)`,
				p.TenantID, stored.ID, i+1, legalEntityID, b.Code, b.Amount.String(), b.AsOf); err != nil {
				return err
			}
		}
		if err := loadIntradayBalances(ctx, tx, p.TenantID, &stored); err != nil {
			return err
		}
		res = IntradayResult{Report: stored}
		return nil
	})
	return res, err
}

// CashPositionAccounts lists the active bank accounts of a legal entity, each with the closing balance of its
// latest statement and its latest intraday report. The caller (domain.Position) decides which is fresher.
func (s *Postgres) CashPositionAccounts(ctx context.Context, legalEntityID string) ([]PositionAccount, error) {
	p, _ := tenant.FromContext(ctx)
	out := []PositionAccount{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+bankAccountColumns+` from bank_account where tenant_id = $1 and legal_entity_id = $2 and status = 'active' order by account_code`, p.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		var accounts []BankAccount
		for rows.Next() {
			a, err := scanBankAccount(rows)
			if err != nil {
				rows.Close()
				return err
			}
			accounts = append(accounts, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, a := range accounts {
			pa := PositionAccount{Account: a}
			var closingDate, closingAmount string
			err := tx.QueryRow(ctx, `select closing_date::text, closing_amount::text from bank_statement where tenant_id = $1 and bank_account_id = $2 order by closing_date desc, created_at desc limit 1`, p.TenantID, a.ID).
				Scan(&closingDate, &closingAmount)
			switch {
			case err == nil:
				amount, perr := decimal.NewFromString(closingAmount)
				if perr != nil {
					return perr
				}
				pa.Statement = &domain.StatementBalance{ClosingDate: closingDate, ClosingAmount: amount}
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
			var asOf string
			var minor int64
			err = tx.QueryRow(ctx, `select as_of::text, balance_minor from bank_opening_balance where tenant_id = $1 and bank_account_id = $2`, p.TenantID, a.ID).Scan(&asOf, &minor)
			switch {
			case err == nil:
				pa.Opening = &domain.OpeningBalance{AsOf: asOf, Amount: decimal.New(minor, -int32(money.MinorUnits(a.Currency)))}
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
			rep, err := scanIntraday(tx.QueryRow(ctx, `select `+intradayColumns+` from intraday_report where tenant_id = $1 and bank_account_id = $2 order by created_at_bank desc, imported_at desc limit 1`, p.TenantID, a.ID))
			switch {
			case err == nil:
				if err := loadIntradayBalances(ctx, tx, p.TenantID, &rep); err != nil {
					return err
				}
				dr := domain.IntradayReport{ReportID: rep.ReportID, CreatedAt: rep.CreatedAtBank, Currency: rep.Currency}
				for _, b := range rep.Balances {
					amount, perr := decimal.NewFromString(b.Amount)
					if perr != nil {
						return perr
					}
					dr.Balances = append(dr.Balances, domain.IntradayBalance{Code: b.Code, Amount: amount, AsOf: b.AsOf})
				}
				pa.Intraday = &dr
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
			out = append(out, pa)
		}
		return nil
	})
	return out, err
}
