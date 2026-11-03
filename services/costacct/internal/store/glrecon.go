package store

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
)

const (
	CodeLedgerCurrency    = "CST-0029"
	CodeLedgerRefused     = "CST-0030"
	CodeLedgerUnavailable = "CST-0031"
	CodeLedgerNotMapped   = "CST-0032"
	CodeLedgerForeign     = "CST-0033"

	YearEndClosingSource = "year_end_closing"
)

// GLBalances reads account balances, the year-end closing lines and the ledger owner from the general ledger.
type GLBalances interface {
	Balances(ctx context.Context, q ledgerclient.BalanceQuery) (ledgerclient.AccountBalances, error)
	SourceBalances(ctx context.Context, q ledgerclient.SourceQuery) ([]ledgerclient.AccountBalance, error)
	LegalEntity(ctx context.Context, legalEntityID, ledgerID string) (string, error)
}

// GLReconcileRequest names the legal entity, its ledger and the period to reconcile. An empty Currency takes the ledger's.
type GLReconcileRequest struct {
	LegalEntityID string
	LedgerID      string
	Period        string
	Currency      string
}

// ReconcileToGL reads the period's balance of every primary element's account range from the general ledger and compares it
// with Σ primary cost entries per element (FR-38.11, AC-38.6). The journal side comes from the ledger only.
func (s *Postgres) ReconcileToGL(ctx context.Context, gl GLBalances, in GLReconcileRequest) (domain.Reconciliation, error) {
	var ranges []domain.AccountRange
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		ranges, err = primaryRanges(ctx, tx, tenantID, in.LegalEntityID)
		return err
	})
	if err != nil {
		return domain.Reconciliation{}, err
	}
	if err := checkLedgerOwner(ctx, gl, in.LegalEntityID, in.LedgerID); err != nil {
		return domain.Reconciliation{}, err
	}
	journal := []domain.JournalBalance{}
	if len(ranges) > 0 {
		journal, in.Currency, err = ledgerJournal(ctx, gl, in, ranges)
		if err != nil {
			return domain.Reconciliation{}, err
		}
	}
	return s.Reconcile(ctx, ReconcileRequest{LegalEntityID: in.LegalEntityID, Period: in.Period, Currency: in.Currency, Journal: journal})
}

func checkLedgerOwner(ctx context.Context, gl GLBalances, legalEntityID, ledgerID string) error {
	owner, err := gl.LegalEntity(ctx, legalEntityID, ledgerID)
	switch {
	case ledgerclient.Rejected(err):
		slog.ErrorContext(ctx, "general ledger lookup rejected", "ledger", ledgerID, "err", err, "correlationId", middleware.GetReqID(ctx))
		return problem(http.StatusUnprocessableEntity, CodeLedgerForeign, "the ledger is unknown or not readable for this legal entity")
	case err != nil:
		return ledgerProblem(ctx, err)
	case owner != legalEntityID:
		return problem(http.StatusConflict, CodeLedgerForeign, "the ledger belongs to another legal entity")
	}
	return nil
}

// ledgerJournal reads one balance call over the span of the primary ranges and takes the year-end closing lines out of it, so a
// fiscal year-end month compares the period's own movements and not the entry that closes the profit and loss accounts. Accounts
// the ranges do not map stay in the result for Reconcile to list.
func ledgerJournal(ctx context.Context, gl GLBalances, in GLReconcileRequest, ranges []domain.AccountRange) ([]domain.JournalBalance, string, error) {
	from, to := ranges[0].From, ranges[0].To
	for _, r := range ranges {
		from, to = min(from, r.From), max(to, r.To)
	}
	set, err := gl.Balances(ctx, ledgerclient.BalanceQuery{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID,
		Account: from, AccountTo: to, PeriodFrom: in.Period, PeriodTo: in.Period})
	if err != nil {
		return nil, "", ledgerProblem(ctx, err)
	}
	if in.Currency != "" && set.Currency != in.Currency {
		return nil, "", problem(http.StatusUnprocessableEntity, CodeLedgerCurrency, "the ledger reports "+set.Currency+", not "+in.Currency)
	}
	first, err := time.Parse("2006-01", in.Period)
	if err != nil {
		return nil, "", problem(http.StatusBadRequest, "COM-0001", "period must be YYYY-MM")
	}
	closing, err := gl.SourceBalances(ctx, ledgerclient.SourceQuery{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, SourceType: YearEndClosingSource,
		AccountFrom: from, AccountTo: to, DateFrom: first.Format(time.DateOnly), DateTo: first.AddDate(0, 1, -1).Format(time.DateOnly)})
	if err != nil {
		return nil, "", ledgerProblem(ctx, err)
	}
	closed := map[string]decimal.Decimal{}
	for _, c := range closing {
		closed[c.Account] = closed[c.Account].Add(c.Net())
	}
	journal := make([]domain.JournalBalance, 0, len(set.Rows))
	for _, row := range set.Rows {
		journal = append(journal, domain.JournalBalance{MainAccount: row.Account, Amount: row.Net().Sub(closed[row.Account])})
	}
	return journal, set.Currency, nil
}

// LegalEntities lists the legal entities that hold cost elements for the caller's tenant.
func (s *Postgres) LegalEntities(ctx context.Context) ([]string, error) {
	out := []string{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select distinct legal_entity_id::text from cost_element where tenant_id = $1 order by 1`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

func ledgerProblem(ctx context.Context, err error) error {
	code := ledgerclient.CodeOf(err)
	slog.ErrorContext(ctx, "general ledger call failed", "ledgerCode", code, "err", err, "correlationId", middleware.GetReqID(ctx))
	detail := "the general ledger did not answer"
	if code != "" {
		detail = "the general ledger refused the request (" + code + ")"
	}
	return problem(http.StatusBadGateway, CodeLedgerRefused, detail)
}
