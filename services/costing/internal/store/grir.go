package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

// InvoicePPV is an invoice PPV row to record against the receipt it clears.
// Row carries the reference fields; its Amount and GrirAmount are set here.
type InvoicePPV struct {
	Row         CostTransaction
	ReceiptKey  string
	Qty         decimal.Decimal
	InvoiceUnit decimal.Decimal
	POUnit      decimal.Decimal
	Scale       int32
}

// RecordInvoicePPV records an invoice PPV row whose GR/IR is allocated from its
// receipt's stored GR/IR by remainder (domain.InvoiceGRIR), so the invoices that
// split a receipt clear it exactly; the PPV is the payable round(qty × invoice
// price) less that GR/IR. Invoices against one receipt are serialised by an
// advisory lock. A retry returns the stored row unchanged (inserted false);
// the same key with another quantity, payable or receipt is CST-CTX-001. A
// receipt that is not recorded, carries no GR/IR, or is for another entity,
// product, site or currency is refused (CST-GRIR-001): nothing is posted.
func (s *Postgres) RecordInvoicePPV(ctx context.Context, in InvoicePPV) (CostTransaction, bool, error) {
	p, _ := tenant.FromContext(ctx)
	out := in.Row
	inserted := false
	payable := in.Qty.Mul(in.InvoiceUnit).RoundBank(in.Scale)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "costing.grir:"+p.TenantID+":"+in.ReceiptKey); err != nil {
			return err
		}
		stored, receiptKey, qty, err := readInvoiceRow(ctx, tx, p.TenantID, in.Row.SourceKey)
		if err == nil {
			if !sameInvoice(stored, in, receiptKey, qty, payable) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCostTxnConflict, "the invoice line was already recorded with other content")
			}
			out = stored
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		receipt, err := scanCostTxn(tx.QueryRow(ctx, `select `+costTxnColumns+` from cost_transaction where tenant_id = $1 and source_key = $2`, p.TenantID, in.ReceiptKey))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeGRIRAllocation, Detail: "the receipt line the invoice names has no recorded PPV row"}
		}
		if err != nil {
			return err
		}
		r := in.Row
		if receipt.Kind != domain.KindPPV || receipt.GrirAmount == "" || receipt.LegalEntityID != r.LegalEntityID ||
			receipt.ProductID != r.ProductID || receipt.SiteID != r.SiteID || receipt.Currency != r.Currency {
			return &domain.Error{Code: domain.CodeGRIRAllocation, Detail: "the receipt line is for another legal entity, product, site or currency, or carries no GR/IR amount"}
		}
		var priorQty, priorGRIR string
		if err := tx.QueryRow(ctx, `select coalesce(sum(grir_qty), 0)::text, coalesce(sum(gr_ir_amount), 0)::text from cost_transaction
			where tenant_id = $1 and grir_receipt_key = $2`, p.TenantID, in.ReceiptKey).Scan(&priorQty, &priorGRIR); err != nil {
			return err
		}
		grir, err := domain.InvoiceGRIR(decimal.RequireFromString(receipt.Qty), decimal.RequireFromString(receipt.GrirAmount),
			decimal.RequireFromString(priorQty), decimal.RequireFromString(priorGRIR), in.Qty, in.POUnit, in.Scale)
		if err != nil {
			return err
		}
		r.Amount, r.GrirAmount = payable.Sub(grir).StringFixed(in.Scale), grir.StringFixed(in.Scale)
		r.GrirReceiptKey, r.GrirQty = in.ReceiptKey, in.Qty.String()
		rows := []CostTransaction{r}
		if err := resolveLedgers(ctx, tx, p.TenantID, rows); err != nil {
			return err
		}
		if err := checkPeriodsOpen(ctx, tx, p.TenantID, rows); err != nil {
			return err
		}
		n, err := insertCostTransactions(ctx, tx, p.TenantID, rows)
		inserted, out = n == 1, rows[0]
		return err
	})
	return out, inserted, err
}

func readInvoiceRow(ctx context.Context, tx pgx.Tx, tenantID, sourceKey string) (CostTransaction, string, string, error) {
	var c CostTransaction
	var receiptKey, qty string
	err := tx.QueryRow(ctx, `select `+costTxnColumns+`, coalesce(grir_receipt_key, ''), coalesce(grir_qty::text, '')
		from cost_transaction where tenant_id = $1 and source_key = $2`, tenantID, sourceKey).Scan(&c.ID, &c.SourceKey, &c.LegalEntityID,
		&c.InventoryTransactionID, &c.LedgerEntryID, &c.ProductID, &c.SiteID, &c.WarehouseID, &c.ValuationType, &c.ValuationLedgerID,
		&c.Kind, &c.Qty, &c.Amount, &c.Currency, &c.FinancialDate, &c.CreatedAt, &c.GrirAmount, &receiptKey, &qty)
	return c, receiptKey, qty, err
}

// sameInvoice: a retry names the same receipt, quantity and payable; the stored
// split of the payable into GR/IR and PPV is kept, since later invoices depend on it.
func sameInvoice(stored CostTransaction, in InvoicePPV, receiptKey, qty string, payable decimal.Decimal) bool {
	q, errQ := decimal.NewFromString(qty)
	a, errA := decimal.NewFromString(stored.Amount)
	g, errG := decimal.NewFromString(stored.GrirAmount)
	if errQ != nil || errA != nil || errG != nil {
		return false
	}
	r := in.Row
	return receiptKey == in.ReceiptKey && q.Equal(in.Qty) && a.Add(g).Equal(payable) && stored.Kind == r.Kind &&
		stored.LegalEntityID == r.LegalEntityID && stored.ProductID == r.ProductID && stored.SiteID == r.SiteID &&
		stored.Currency == r.Currency && stored.FinancialDate == r.FinancialDate
}
