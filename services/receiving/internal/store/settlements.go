package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	adjustmentNumberConstraint = "receiving_invoice_adjustment_number_uq"
	adjustmentAggregate        = "invoiceadjustment"
	settlementAggregate        = "settlement"
)

func PlanSettled(poID string, lineNo int) *domain.Error {
	return &domain.Error{Code: domain.CodePlanSettled, Detail: fmt.Sprintf("line %d of purchase order %s is settled by its invoicing plan and cannot also be invoiced against a receipt", lineNo, poID)}
}

func AuditBlocked(invoiceID string) *domain.Error {
	return &domain.Error{Code: domain.CodeAuditBlocked, Detail: fmt.Sprintf("invoice %s is held or rejected by the audit block and cannot be adjusted", invoiceID)}
}

func DuplicateAdjustment(number string) *domain.Error {
	return &domain.Error{Code: domain.CodeDuplicateInvoice, Detail: fmt.Sprintf("adjustment %q is already posted for the invoice", number)}
}

func (s *Postgres) PostInvoiceAdjustment(ctx context.Context, in InvoiceAdjustment, fn InvoiceAdjustmentFunc) (InvoiceAdjustment, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return InvoiceAdjustment{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, tenantID+"|adjust|"+in.OriginalInvoiceID); err != nil {
			return err
		}
		var existing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM receiving_invoice_adjustment WHERE tenant_id = $1 AND original_invoice_id = $2 AND lower(document_number) = lower($3)`,
			tenantID, in.OriginalInvoiceID, in.DocumentNumber).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return DuplicateAdjustment(in.DocumentNumber)
		}
		base, err := loadInvoiceBase(ctx, tx, tenantID, in.OriginalInvoiceID)
		if err != nil {
			return err
		}
		if base.Invoice.LegalEntityID != in.LegalEntityID {
			return &domain.Error{Code: domain.CodeInvalidReceipt, Detail: "legalEntityId differs from the legal entity of the original invoice"}
		}
		if base.Invoice.Status != domain.InvoiceStatusPosted {
			return AuditBlocked(base.Invoice.ID)
		}
		outcome, err := fn(base)
		if err != nil {
			return err
		}
		in.VendorID, in.Currency = base.Invoice.VendorID, base.Invoice.Currency
		in.LedgerEntryID, in.Total = outcome.LedgerEntryID, outcome.Total
		if err := tx.QueryRow(ctx, `INSERT INTO receiving_invoice_adjustment (tenant_id, id, legal_entity_id, original_invoice_id, vendor_id, kind, document_number, document_date,
			posting_date, currency, ledger_id, fiscal_period_id, ledger_entry_id, total) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING created_at`,
			tenantID, in.ID, in.LegalEntityID, in.OriginalInvoiceID, in.VendorID, in.Kind, in.DocumentNumber, in.DocumentDate, in.PostingDate, in.Currency,
			in.LedgerID, in.FiscalPeriodID, in.LedgerEntryID, in.Total).Scan(&in.CreatedAt); err != nil {
			return err
		}
		rows := make([][]any, len(in.Lines))
		for i, l := range in.Lines {
			rows[i] = []any{tenantID, in.ID, i + 1, in.LegalEntityID, in.OriginalInvoiceID, l.InvoiceLineNo, l.Amount}
		}
		if err := execBatch(ctx, tx, `INSERT INTO receiving_invoice_adjustment_line (tenant_id, adjustment_id, line_no, legal_entity_id, invoice_id, invoice_line_no, amount)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, rows); err != nil {
			return err
		}
		if err := markPostingsDone(ctx, tx, tenantID, in.ID, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, adjustmentAggregate, in.ID, outcome.Event)
	})
	if isUnique(err, adjustmentNumberConstraint) {
		return InvoiceAdjustment{}, DuplicateAdjustment(in.DocumentNumber)
	}
	return in, err
}

func loadInvoiceBase(ctx context.Context, tx pgx.Tx, tenantID, invoiceID string) (InvoiceBase, error) {
	inv := Invoice{ID: invoiceID}
	err := tx.QueryRow(ctx, `SELECT legal_entity_id::text, vendor_id::text, invoice_number, invoice_date, posting_date, currency, ledger_id::text, fiscal_period_id::text, ledger_entry_id, total, created_at, status
		FROM receiving_vendor_invoice WHERE tenant_id = $1 AND id = $2`, tenantID, invoiceID).
		Scan(&inv.LegalEntityID, &inv.VendorID, &inv.InvoiceNumber, &inv.InvoiceDate, &inv.PostingDate, &inv.Currency, &inv.LedgerID, &inv.FiscalPeriodID, &inv.LedgerEntryID, &inv.Total, &inv.CreatedAt, &inv.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvoiceBase{}, ErrInvoiceNotFound
	}
	if err != nil {
		return InvoiceBase{}, err
	}
	lines, err := tx.Query(ctx, `SELECT line_no, amount, standard_cost, service FROM receiving_vendor_invoice_line WHERE tenant_id = $1 AND invoice_id = $2 ORDER BY line_no`, tenantID, invoiceID)
	if err != nil {
		return InvoiceBase{}, err
	}
	defer lines.Close()
	base := InvoiceBase{Invoice: inv}
	byNo := map[int]int{}
	for lines.Next() {
		var l domain.InvoiceLineState
		if err := lines.Scan(&l.LineNo, &l.Amount, &l.StandardCost, &l.Service); err != nil {
			return InvoiceBase{}, err
		}
		byNo[l.LineNo] = len(base.Lines)
		base.Lines = append(base.Lines, l)
	}
	if err := lines.Err(); err != nil {
		return InvoiceBase{}, err
	}
	lines.Close()
	sums, err := tx.Query(ctx, `SELECT l.invoice_line_no, a.kind, sum(l.amount) FROM receiving_invoice_adjustment_line l
		JOIN receiving_invoice_adjustment a ON a.tenant_id = l.tenant_id AND a.id = l.adjustment_id
		WHERE l.tenant_id = $1 AND l.invoice_id = $2 GROUP BY l.invoice_line_no, a.kind`, tenantID, invoiceID)
	if err != nil {
		return InvoiceBase{}, err
	}
	defer sums.Close()
	for sums.Next() {
		var lineNo int
		var kind string
		var total decimal.Decimal
		if err := sums.Scan(&lineNo, &kind, &total); err != nil {
			return InvoiceBase{}, err
		}
		i, ok := byNo[lineNo]
		if !ok {
			continue
		}
		if kind == domain.AdjustmentCredit {
			base.Lines[i].Credited = total
		} else {
			base.Lines[i].Debited = total
		}
	}
	return base, sums.Err()
}

func (s *Postgres) PostSettlement(ctx context.Context, in Settlement, fn SettlementFunc) (Settlement, bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Settlement{}, false, err
	}
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replayed = false
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, strings.Join([]string{tenantID, "settlement", in.Kind, in.SourceRef}, "|")); err != nil {
			return err
		}
		var prior Settlement
		err := tx.QueryRow(ctx, `SELECT id::text, legal_entity_id::text, vendor_id, currency, amount, posting_date, ledger_id::text, fiscal_period_id::text, ledger_entry_id, created_at
			FROM receiving_settlement WHERE tenant_id = $1 AND kind = $2 AND source_ref = $3`, tenantID, in.Kind, in.SourceRef).
			Scan(&prior.ID, &prior.LegalEntityID, &prior.VendorID, &prior.Currency, &prior.Amount, &prior.PostingDate, &prior.LedgerID, &prior.FiscalPeriodID, &prior.LedgerEntryID, &prior.CreatedAt)
		if err == nil {
			prior.Kind, prior.SourceRef = in.Kind, in.SourceRef
			in, replayed = prior, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.Kind == domain.SettlementPlanRow && in.ProductID == "" {
			return &domain.Error{Code: domain.CodeInvalidReceipt, Detail: "a plan row settlement needs the product of its purchase order line"}
		}
		if in.Kind == domain.SettlementPlanRow {
			if err := lockPurchaseOrderLine(ctx, tx, tenantID, in.PurchaseOrderID, in.ProductID); err != nil {
				return err
			}
			var invoiced bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM receiving_grir_open_item g
				JOIN receiving_receipt r ON r.tenant_id = g.tenant_id AND r.id = g.receipt_id
				JOIN receiving_receipt_line l ON l.tenant_id = g.tenant_id AND l.receipt_id = g.receipt_id AND l.line_no = g.line_no
				WHERE g.tenant_id = $1 AND r.purchase_order_id = $2 AND l.product_id = $3 AND g.invoiced_qty > 0)`,
				tenantID, in.PurchaseOrderID, in.ProductID).Scan(&invoiced); err != nil {
				return err
			}
			if invoiced {
				return ErrLineInvoiced
			}
		}
		outcome, err := fn()
		if err != nil {
			return err
		}
		in.LedgerEntryID = outcome.LedgerEntryID
		if err := tx.QueryRow(ctx, `INSERT INTO receiving_settlement (tenant_id, id, legal_entity_id, kind, source_ref, vendor_id, currency, amount, posting_date, ledger_id, fiscal_period_id, ledger_entry_id,
			purchase_order_id, po_line_no, product_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13::text, '')::uuid, NULLIF($14::int, 0), NULLIF($15::text, '')::uuid) RETURNING created_at`,
			tenantID, in.ID, in.LegalEntityID, in.Kind, in.SourceRef, in.VendorID, in.Currency, in.Amount, in.PostingDate, in.LedgerID, in.FiscalPeriodID, in.LedgerEntryID,
			in.PurchaseOrderID, in.POLineNo, in.ProductID).Scan(&in.CreatedAt); err != nil {
			return err
		}
		if err := markPostingsDone(ctx, tx, tenantID, in.ID, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, settlementAggregate, in.ID, outcome.Event)
	})
	return in, replayed, err
}

func (s *Postgres) SettledSources(ctx context.Context, kind string, sources []string) (map[string]struct{}, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	settled := map[string]struct{}{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT source_ref FROM receiving_settlement WHERE tenant_id = $1 AND kind = $2 AND source_ref = ANY($3)`, tenantID, kind, sources)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref string
			if err := rows.Scan(&ref); err != nil {
				return err
			}
			settled[ref] = struct{}{}
		}
		return rows.Err()
	})
	return settled, err
}

func lockPurchaseOrderLine(ctx context.Context, tx pgx.Tx, tenantID, purchaseOrderID, productID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, strings.ToLower(strings.Join([]string{tenantID, "poline", purchaseOrderID, productID}, "|")))
	return err
}

func refuseSettledPlanLines(ctx context.Context, tx pgx.Tx, tenantID string, keys []ItemKey) error {
	receiptIDs, lineNos := make([]string, len(keys)), make([]int, len(keys))
	for i, k := range keys {
		receiptIDs[i], lineNos[i] = k.ReceiptID, k.LineNo
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT r.purchase_order_id::text, l.product_id::text FROM receiving_receipt_line l
		JOIN receiving_receipt r ON r.tenant_id = l.tenant_id AND r.id = l.receipt_id
		JOIN unnest($2::uuid[], $3::int[]) AS k(receipt_id, line_no) ON k.receipt_id = l.receipt_id AND k.line_no = l.line_no
		WHERE l.tenant_id = $1 ORDER BY 1, 2`, tenantID, receiptIDs, lineNos)
	if err != nil {
		return err
	}
	defer rows.Close()
	var lines [][2]string
	for rows.Next() {
		var line [2]string
		if err := rows.Scan(&line[0], &line[1]); err != nil {
			return err
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, line := range lines {
		if err := lockPurchaseOrderLine(ctx, tx, tenantID, line[0], line[1]); err != nil {
			return err
		}
		var lineNo int
		err := tx.QueryRow(ctx, `SELECT po_line_no FROM receiving_settlement WHERE tenant_id = $1 AND kind = 'PLAN_ROW' AND purchase_order_id = $2 AND product_id = $3 LIMIT 1`,
			tenantID, line[0], line[1]).Scan(&lineNo)
		if err == nil {
			return PlanSettled(line[0], lineNo)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
}
