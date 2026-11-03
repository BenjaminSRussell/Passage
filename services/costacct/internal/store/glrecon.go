package store

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
)

const (
	CodeLedgerCurrency    = "CST-0029"
	CodeLedgerRefused     = "CST-0030"
	CodeLedgerUnavailable = "CST-0031"
	CodeLedgerNotMapped   = "CST-0032"
)

// GLBalances reads account balances from the general ledger.
type GLBalances interface {
	Balances(ctx context.Context, q ledgerclient.BalanceQuery) (ledgerclient.AccountBalances, error)
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
	journal := []domain.JournalBalance{}
	for _, r := range ranges {
		set, err := gl.Balances(ctx, ledgerclient.BalanceQuery{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID,
			Account: r.From, AccountTo: r.To, PeriodFrom: in.Period, PeriodTo: in.Period})
		if err != nil {
			return domain.Reconciliation{}, ledgerProblem(ctx, err)
		}
		if in.Currency == "" {
			in.Currency = set.Currency
		}
		if set.Currency != in.Currency {
			return domain.Reconciliation{}, problem(http.StatusBadGateway, CodeLedgerCurrency, "the ledger reports "+set.Currency+", not "+in.Currency)
		}
		for _, row := range set.Rows {
			journal = append(journal, domain.JournalBalance{MainAccount: row.Account, Amount: row.Net()})
		}
	}
	return s.Reconcile(ctx, ReconcileRequest{LegalEntityID: in.LegalEntityID, Period: in.Period, Currency: in.Currency, Journal: journal})
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
