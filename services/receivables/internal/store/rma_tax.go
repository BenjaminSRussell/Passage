package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/billingclient"
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

func (s *Postgres) WithBilling(b billingclient.Reader) *Postgres {
	s.billing = b
	return s
}

func (s *Postgres) billedTax(ctx context.Context, in RMACredit) (map[string]decimal.Decimal, error) {
	if len(in.TaxLines) == 0 {
		return nil, nil
	}
	if s.billing == nil {
		return nil, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "billing is not configured; the invoice tax cannot be verified")
	}
	basis, err := s.billing.TaxBasis(ctx, in.LegalEntityID, in.InvoiceID)
	if errors.Is(err, billingclient.ErrNotFound) {
		return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice, "billing has no posted invoice with this invoiceId")
	}
	if err != nil {
		slog.ErrorContext(ctx, "billing invoice tax not read", "invoice", in.InvoiceNumber, "err", err, "correlationId", middleware.GetReqID(ctx))
		return nil, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "billing could not be read; retry later")
	}
	le, _ := ids.Canonical(basis.LegalEntityID)
	want, _ := ids.Canonical(in.LegalEntityID)
	if basis.Number != in.InvoiceNumber || le != want {
		return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice, "the invoiceId is not the invoice named by invoiceNumber in this legal entity")
	}
	return basis.Remaining, nil
}

func checkTaxAgainstInvoice(ctx context.Context, tx pgx.Tx, tenantID, invoiceItemID string, billed map[string]decimal.Decimal, lines []RMATaxLine) error {
	if len(lines) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `select t.tax_code, sum(t.amount)::text from rma_credit_tax t
		join rma_credit c on c.tenant_id = t.tenant_id and c.id = t.rma_credit_id
		where c.tenant_id = $1 and c.invoice_open_item_id = $2 group by t.tax_code`, tenantID, invoiceItemID)
	if err != nil {
		return err
	}
	defer rows.Close()
	earlier := map[string]decimal.Decimal{}
	for rows.Next() {
		var code, sum string
		if err := rows.Scan(&code, &sum); err != nil {
			return err
		}
		earlier[code] = decimal.RequireFromString(sum)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range lines {
		left, ok := billed[t.Code]
		if !ok {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice, fmt.Sprintf("tax code %s was not billed on the invoice", t.Code))
		}
		if left = left.Sub(earlier[t.Code]); t.Amount.GreaterThan(left) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice,
				fmt.Sprintf("tax %s of code %s exceeds the %s the invoice still allows", t.Amount, t.Code, left))
		}
	}
	return nil
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
