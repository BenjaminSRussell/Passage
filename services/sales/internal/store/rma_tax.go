package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

func (s *Postgres) readTaxBasis(ctx context.Context, o ReturnOrder) (Invoice, error) {
	if s.invoices == nil {
		return Invoice{}, problem(http.StatusServiceUnavailable, CodeRMAUpstream, "billing is not configured; the invoice tax cannot be reversed")
	}
	invoice, err := s.invoices.ReadInvoice(ctx, o.LegalEntityID, o.OriginalInvoiceID)
	if err != nil {
		return Invoice{}, upstreamProblem(ctx, "billing", err)
	}
	return invoice, nil
}

func storeReversedTax(ctx context.Context, tx pgx.Tx, tenantID string, o ReturnOrder, invoice Invoice, plan creditPlan) (decimal.Decimal, error) {
	if invoice.ID == "" {
		return decimal.Zero, nil
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, tenantID+o.OriginalInvoiceID); err != nil {
		return decimal.Zero, err
	}
	returned := map[string]decimal.Decimal{}
	for i, idx := range plan.lines {
		product := o.Lines[idx].ProductID
		returned[product] = returned[product].Add(plan.qty[i])
	}
	prior, err := priorReversals(ctx, tx, tenantID, o)
	if err != nil {
		return decimal.Zero, err
	}
	basis := make([]domain.TaxBasisLine, 0, len(invoice.Lines))
	for _, l := range invoice.Lines {
		done := prior[l.LineNo]
		basis = append(basis, domain.TaxBasisLine{LineNo: l.LineNo, ProductID: l.ProductID, TaxCode: l.TaxCode, Qty: l.Qty, Tax: l.Tax,
			CreditedQty: l.CreditedQty, CreditedTax: l.CreditedTax, ReturnedQty: done.qty, ReturnedTax: done.tax})
	}
	total := decimal.Zero
	for _, r := range domain.ReverseTax(o.Currency, basis, returned) {
		if r.TaxCode == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `insert into return_order_tax (tenant_id, id, legal_entity_id, return_order_id, original_invoice_id, invoice_line_no, tax_code, qty, tax)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			tenantID, ids.New(), o.LegalEntityID, o.ID, o.OriginalInvoiceID, r.InvoiceLineNo, r.TaxCode, r.Qty.String(), r.Tax.String()); err != nil {
			return decimal.Zero, err
		}
		total = total.Add(r.Tax)
	}
	return total, nil
}

type reversed struct{ qty, tax decimal.Decimal }

func priorReversals(ctx context.Context, tx pgx.Tx, tenantID string, o ReturnOrder) (map[int]reversed, error) {
	rows, err := tx.Query(ctx, `select invoice_line_no, sum(qty)::text, sum(tax)::text from return_order_tax
		where tenant_id = $1 and original_invoice_id = $2 and return_order_id <> $3 group by invoice_line_no`, tenantID, o.OriginalInvoiceID, o.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]reversed{}
	for rows.Next() {
		var no int
		var qty, tax string
		if err := rows.Scan(&no, &qty, &tax); err != nil {
			return nil, err
		}
		out[no] = reversed{qty: decOrZero(&qty), tax: decOrZero(&tax)}
	}
	return out, rows.Err()
}

func (s *Postgres) creditTaxLines(ctx context.Context, returnOrderID string) ([]ARTaxLine, error) {
	p, _ := tenant.FromContext(ctx)
	var out []ARTaxLine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select tax_code, sum(tax)::text from return_order_tax where tenant_id = $1 and return_order_id = $2
			group by tax_code having sum(tax) > 0 order by tax_code`, p.TenantID, returnOrderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code, amount string
			if err := rows.Scan(&code, &amount); err != nil {
				return err
			}
			out = append(out, ARTaxLine{Code: code, Amount: decOrZero(&amount)})
		}
		return rows.Err()
	})
	return out, err
}
