package store

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/taxclient"
)

func (s *Postgres) WithRMATax(accounts map[string]map[string]string, recorder taxclient.Recorder) *Postgres {
	s.taxAccounts, s.tax = accounts, recorder
	return s
}

type rmaTaxRow struct {
	id, code, account string
	amount            decimal.Decimal
}

func (s *Postgres) rmaTaxAccountsFor(in RMACredit) (map[string]string, error) {
	if len(in.TaxLines) == 0 {
		return nil, nil
	}
	if s.tax == nil {
		return nil, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "the tax service is not configured; a credit with tax lines cannot be recorded")
	}
	out := make(map[string]string, len(in.TaxLines))
	for _, t := range in.TaxLines {
		account := s.taxAccounts[in.LegalEntityID][t.Code]
		if account == "" {
			return nil, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable,
				fmt.Sprintf("the legal entity has no GL tax account for tax code %s", t.Code))
		}
		out[t.Code] = account
	}
	return out, nil
}

func (s *Postgres) loadRMATax(ctx context.Context, tenantID string, row rmaCreditRow) ([]rmaTaxRow, error) {
	if !row.tax.IsPositive() {
		return nil, nil
	}
	var out []rmaTaxRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, tax_code, tax_account, amount::text from rma_credit_tax
			where tenant_id = $1 and rma_credit_id = $2 order by tax_code`, tenantID, row.id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t rmaTaxRow
			var amount string
			if err := rows.Scan(&t.id, &t.code, &t.account, &amount); err != nil {
				return err
			}
			t.amount = decimal.RequireFromString(amount)
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) recordTax(ctx context.Context, row rmaCreditRow, lines []rmaTaxRow) error {
	if len(lines) == 0 {
		return nil
	}
	if s.tax == nil {
		return fmt.Errorf("tax service is not configured")
	}
	txns := make([]taxclient.Txn, 0, len(lines))
	for _, t := range lines {
		txns = append(txns, taxclient.Txn{ID: t.id, Code: t.code, Date: row.postingDate, Amount: t.amount.Neg()})
	}
	return s.tax.Record(ctx, row.legalEntityID, txns)
}
