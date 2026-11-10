package store

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
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

func storeReversedTax(ctx context.Context, tx pgx.Tx, tenantID string, o ReturnOrder, invoice Invoice, plan creditPlan) (decimal.Decimal, []ARInvoiceLine, error) {
	if invoice.ID == "" {
		return decimal.Zero, nil, nil
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, tenantID+o.OriginalInvoiceID); err != nil {
		return decimal.Zero, nil, err
	}
	returned := map[string]decimal.Decimal{}
	for i, idx := range plan.lines {
		product := o.Lines[idx].ProductID
		returned[product] = returned[product].Add(plan.qty[i])
	}
	prior, err := priorReversals(ctx, tx, tenantID, o)
	if err != nil {
		return decimal.Zero, nil, err
	}
	basis := make([]domain.TaxBasisLine, 0, len(invoice.Lines))
	for _, l := range invoice.Lines {
		done := prior[l.LineNo]
		basis = append(basis, domain.TaxBasisLine{LineNo: l.LineNo, ProductID: l.ProductID, TaxCode: l.TaxCode, Qty: l.Qty, Tax: l.Tax,
			CreditedQty: l.CreditedQty, CreditedTax: l.CreditedTax, ReturnedQty: done.qty, ReturnedTax: done.tax})
	}
	total := decimal.Zero
	var lines []ARInvoiceLine
	allocated := map[string]decimal.Decimal{}
	productOf := map[int]string{}
	for _, l := range invoice.Lines {
		productOf[l.LineNo] = l.ProductID
	}
	for _, r := range domain.ReverseTax(o.Currency, basis, returned) {
		lines = append(lines, ARInvoiceLine{LineNo: r.InvoiceLineNo, Qty: r.Qty})
		allocated[productOf[r.InvoiceLineNo]] = allocated[productOf[r.InvoiceLineNo]].Add(r.Qty)
		if r.TaxCode == "" {
			if r.Tax.IsPositive() {
				return decimal.Zero, nil, problem(http.StatusUnprocessableEntity, CodeRMAInvoice, "an invoice line with tax has no tax code; its tax cannot be reversed")
			}
			continue
		}
		if _, err := tx.Exec(ctx, `insert into return_order_tax (tenant_id, id, legal_entity_id, return_order_id, original_invoice_id, invoice_line_no, tax_code, qty, tax)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			tenantID, ids.New(), o.LegalEntityID, o.ID, o.OriginalInvoiceID, r.InvoiceLineNo, r.TaxCode, r.Qty.String(), r.Tax.String()); err != nil {
			return decimal.Zero, nil, err
		}
		total = total.Add(r.Tax)
	}
	for product, want := range returned {
		if !allocated[product].Equal(want) {
			return decimal.Zero, nil, problem(http.StatusUnprocessableEntity, CodeRMAInvoice,
				"the invoice has fewer units of a returned product left to credit than this return holds")
		}
	}
	return total, lines, nil
}

type reversed struct{ qty, tax decimal.Decimal }

func priorReversals(ctx context.Context, tx pgx.Tx, tenantID string, o ReturnOrder) (map[int]reversed, error) {
	out := map[int]reversed{}
	taxRows, err := tx.Query(ctx, `select invoice_line_no, sum(tax)::text from return_order_tax
		where tenant_id = $1 and original_invoice_id = $2 and return_order_id <> $3 group by invoice_line_no`, tenantID, o.OriginalInvoiceID, o.ID)
	if err != nil {
		return nil, err
	}
	for taxRows.Next() {
		var no int
		var tax string
		if err := taxRows.Scan(&no, &tax); err != nil {
			taxRows.Close()
			return nil, err
		}
		out[no] = reversed{tax: decOrZero(&tax)}
	}
	taxRows.Close()
	if err := taxRows.Err(); err != nil {
		return nil, err
	}
	qtyRows, err := tx.Query(ctx, `select line_no, sum(qty)::text from (
			select (e->>'lineNo')::int as line_no, (e->>'qty')::numeric as qty from return_order r, jsonb_array_elements(r.credit_invoice_lines) e
			where r.tenant_id = $1 and r.original_invoice_id = $2 and r.id <> $3
			union all
			select t.invoice_line_no, t.qty from return_order_tax t join return_order r on r.tenant_id = t.tenant_id and r.id = t.return_order_id
			where t.tenant_id = $1 and t.original_invoice_id = $2 and t.return_order_id <> $3 and r.credit_invoice_lines is null) q
		group by line_no`, tenantID, o.OriginalInvoiceID, o.ID)
	if err != nil {
		return nil, err
	}
	defer qtyRows.Close()
	for qtyRows.Next() {
		var no int
		var qty string
		if err := qtyRows.Scan(&no, &qty); err != nil {
			return nil, err
		}
		r := out[no]
		r.qty = decOrZero(&qty)
		out[no] = r
	}
	return out, qtyRows.Err()
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

func (s *Postgres) creditInvoiceLines(ctx context.Context, returnOrderID string) ([]ARInvoiceLine, error) {
	p, _ := tenant.FromContext(ctx)
	var raw *string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select credit_invoice_lines::text from return_order where tenant_id = $1 and id = $2`, p.TenantID, returnOrderID).Scan(&raw)
	})
	if err != nil || raw == nil {
		return nil, err
	}
	var out []ARInvoiceLine
	return out, json.Unmarshal([]byte(*raw), &out)
}

// legacyCreditLines works out the invoice lines of a credit stored before migration 0048, which kept none: the lines
// with tax come from the tax rows the credit stored, and what those leave of the returned quantity of a product is
// placed on its untaxed invoice lines in order. The result is stored, so every repeat sends the same lines.
func (s *Postgres) legacyCreditLines(ctx context.Context, o ReturnOrder) ([]ARInvoiceLine, error) {
	invoice, err := s.readTaxBasis(ctx, o)
	if err != nil || len(invoice.Lines) == 0 {
		return nil, err
	}
	p, _ := tenant.FromContext(ctx)
	byLine := map[int]decimal.Decimal{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select invoice_line_no, qty::text from return_order_tax where tenant_id = $1 and return_order_id = $2`, p.TenantID, o.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var no int
			var qty string
			if err := rows.Scan(&no, &qty); err != nil {
				return err
			}
			byLine[no] = byLine[no].Add(decOrZero(&qty))
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	plan, err := planCredit(o)
	if err != nil {
		return nil, err
	}
	left := map[string]decimal.Decimal{}
	for i, idx := range plan.lines {
		left[o.Lines[idx].ProductID] = left[o.Lines[idx].ProductID].Add(plan.qty[i])
	}
	for _, l := range invoice.Lines {
		left[l.ProductID] = left[l.ProductID].Sub(byLine[l.LineNo])
	}
	for _, l := range invoice.Lines {
		if l.TaxCode != "" || !left[l.ProductID].IsPositive() {
			continue
		}
		take := decimal.Min(left[l.ProductID], l.Qty.Sub(l.CreditedQty))
		if take.IsPositive() {
			byLine[l.LineNo] = byLine[l.LineNo].Add(take)
			left[l.ProductID] = left[l.ProductID].Sub(take)
		}
	}
	var out []ARInvoiceLine
	for _, l := range invoice.Lines {
		if q := byLine[l.LineNo]; q.IsPositive() {
			out = append(out, ARInvoiceLine{LineNo: l.LineNo, Qty: q})
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	raw, _ := json.Marshal(out)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update return_order set credit_invoice_lines = $3::jsonb where tenant_id = $1 and id = $2 and credit_invoice_lines is null`, p.TenantID, o.ID, string(raw))
		return err
	})
	return out, err
}

// repriceMixed values a returned product that is billed at more than one unit price at the invoice lines the return is
// allocated to, line by line, the way billing values the lines it reserves; the order's own unit price is the invoice's
// average, which billing's reservation can fall short of (#557). Products on a single price keep the order's price.
func repriceMixed(o ReturnOrder, invoice Invoice, plan creditPlan, alloc []ARInvoiceLine) (ReturnOrder, bool) {
	if len(alloc) == 0 || len(plan.lines) == 0 {
		return o, false
	}
	exp := money.MinorUnits(o.Currency)
	byNo := map[int]InvoiceLine{}
	for _, l := range invoice.Lines {
		byNo[l.LineNo] = l
	}
	type segment struct{ qty, value decimal.Decimal }
	segments := map[string][]segment{}
	mixed := map[string]bool{}
	unit := map[string]decimal.Decimal{}
	for _, a := range alloc {
		l, ok := byNo[a.LineNo]
		if !ok || !l.Qty.IsPositive() {
			continue
		}
		perUnit := l.Net.Div(l.Qty)
		if first, seen := unit[l.ProductID]; seen && !first.Equal(perUnit) {
			mixed[l.ProductID] = true
		}
		unit[l.ProductID] = perUnit
		segments[l.ProductID] = append(segments[l.ProductID], segment{qty: a.Qty, value: l.Net.Mul(a.Qty).Div(l.Qty)})
	}
	if len(mixed) == 0 {
		return o, false
	}
	out := o
	out.Lines = append([]ReturnLine(nil), o.Lines...)
	for i, idx := range plan.lines {
		product := out.Lines[idx].ProductID
		if !mixed[product] {
			continue
		}
		need, value := plan.qty[i], decimal.Zero
		segs := segments[product]
		for need.IsPositive() && len(segs) > 0 {
			take := decimal.Min(need, segs[0].qty)
			value = value.Add(segs[0].value.Mul(take).Div(segs[0].qty).RoundBank(exp))
			segs[0].qty = segs[0].qty.Sub(take)
			need = need.Sub(take)
			if !segs[0].qty.IsPositive() {
				segs = segs[1:]
			}
		}
		segments[product] = segs
		out.Lines[idx].UnitPrice = value.Div(plan.qty[i])
	}
	return out, true
}
