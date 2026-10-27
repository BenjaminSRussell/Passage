package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	invoiceNumberConstraint = "receiving_vendor_invoice_number_uq"
	invoiceAggregate        = "vendorinvoice"
)

type ItemKey struct {
	ReceiptID string
	LineNo    int
}

func (l InvoiceLine) ItemKey() ItemKey { return ItemKey{l.ReceiptID, l.ReceiptLineNo} }

func (s *Postgres) PostInvoice(ctx context.Context, in Invoice, fn InvoiceFunc) (Invoice, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Invoice{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, tenantID+"|"+in.VendorID+"|"+strings.ToLower(in.InvoiceNumber)); err != nil {
			return err
		}
		var existing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM receiving_vendor_invoice WHERE tenant_id = $1 AND vendor_id = $2 AND lower(invoice_number) = lower($3)`,
			tenantID, in.VendorID, in.InvoiceNumber).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return duplicateInvoice(in.InvoiceNumber)
		}
		byKey := map[ItemKey]*domain.GrirItem{}
		items := make([]*domain.GrirItem, len(in.Lines))
		var distinct []ItemKey
		for _, l := range in.Lines {
			if _, seen := byKey[l.ItemKey()]; !seen {
				byKey[l.ItemKey()] = nil
				distinct = append(distinct, l.ItemKey())
			}
		}
		slices.SortFunc(distinct, func(a, b ItemKey) int {
			return cmp.Or(strings.Compare(a.ReceiptID, b.ReceiptID), cmp.Compare(a.LineNo, b.LineNo))
		})
		for _, key := range distinct {
			item, err := lockGrirItem(ctx, tx, tenantID, in, key)
			if err != nil {
				return err
			}
			byKey[key] = item
		}
		for i, l := range in.Lines {
			items[i] = byKey[l.ItemKey()]
		}
		outcome, err := fn(items)
		if err != nil {
			return err
		}
		in.LedgerEntryID, in.Total = outcome.LedgerEntryID, outcome.Total
		if err := insertInvoice(ctx, tx, tenantID, &in); err != nil {
			return err
		}
		var grirRows [][]any
		for _, key := range distinct {
			item := byKey[key]
			grirRows = append(grirRows, []any{tenantID, key.ReceiptID, key.LineNo, item.InvoicedQty, item.ClearedAmount.Value, item.ReceivedQty})
		}
		if err := execBatch(ctx, tx, `UPDATE receiving_grir_open_item SET invoiced_qty = $4::numeric, cleared_amount = $5,
			status = CASE WHEN $4::numeric >= $6::numeric THEN 'CLEARED' ELSE 'PARTIAL' END,
			cleared_at = CASE WHEN $4::numeric >= $6::numeric THEN now() END
			WHERE tenant_id = $1 AND receipt_id = $2 AND line_no = $3`, grirRows); err != nil {
			return err
		}
		lineRows := make([][]any, len(grirRows))
		for i, row := range grirRows {
			lineRows[i] = row[:4]
		}
		if err := execBatch(ctx, tx, `UPDATE receiving_receipt_line SET invoiced_qty = $4 WHERE tenant_id = $1 AND receipt_id = $2 AND line_no = $3`, lineRows); err != nil {
			return err
		}
		if err := markPostingsDone(ctx, tx, tenantID, in.ID, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, invoiceAggregate, in.ID, outcome.Event)
	})
	if isUnique(err, invoiceNumberConstraint) {
		return Invoice{}, duplicateInvoice(in.InvoiceNumber)
	}
	return in, err
}

func duplicateInvoice(number string) *domain.Error {
	return &domain.Error{Code: domain.CodeDuplicateInvoice, Detail: fmt.Sprintf("invoice %q is already posted for the vendor", number)}
}

func lockGrirItem(ctx context.Context, tx pgx.Tx, tenantID string, in Invoice, key ItemKey) (*domain.GrirItem, error) {
	var legalEntityID, vendorID, currency string
	item := &domain.GrirItem{ReceiptID: key.ReceiptID, LineNo: key.LineNo}
	var receivedAmount, clearedAmount decimal.Decimal
	err := tx.QueryRow(ctx, `SELECT legal_entity_id::text, COALESCE(vendor_id::text, ''), received_qty, invoiced_qty, received_amount, cleared_amount, currency
		FROM receiving_grir_open_item WHERE tenant_id = $1 AND receipt_id = $2 AND line_no = $3 FOR UPDATE`, tenantID, key.ReceiptID, key.LineNo).
		Scan(&legalEntityID, &vendorID, &item.ReceivedQty, &item.InvoicedQty, &receivedAmount, &clearedAmount, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGrirNotFound
	}
	if err != nil {
		return nil, err
	}
	if legalEntityID != in.LegalEntityID || vendorID != in.VendorID {
		return nil, &domain.Error{Code: domain.CodeInvalidReceipt, Detail: fmt.Sprintf("receipt %s line %d belongs to another vendor or legal entity", key.ReceiptID, key.LineNo)}
	}
	item.ReceivedAmount = money.Amount{Value: receivedAmount, Currency: currency}
	item.ClearedAmount = money.Amount{Value: clearedAmount, Currency: currency}
	return item, nil
}

func insertInvoice(ctx context.Context, tx pgx.Tx, tenantID string, in *Invoice) error {
	if err := tx.QueryRow(ctx, `INSERT INTO receiving_vendor_invoice (tenant_id, id, legal_entity_id, vendor_id, invoice_number, invoice_date, posting_date,
		currency, ledger_id, fiscal_period_id, ledger_entry_id, total) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING created_at`,
		tenantID, in.ID, in.LegalEntityID, in.VendorID, in.InvoiceNumber, in.InvoiceDate, in.PostingDate, in.Currency, in.LedgerID, in.FiscalPeriodID,
		in.LedgerEntryID, in.Total).Scan(&in.CreatedAt); err != nil {
		return err
	}
	rows := make([][]any, len(in.Lines))
	for i, l := range in.Lines {
		rows[i] = []any{tenantID, in.ID, i + 1, in.LegalEntityID, l.ReceiptID, l.ReceiptLineNo, l.Qty, l.UnitPrice, l.Amount, l.StandardCost}
	}
	return execBatch(ctx, tx, `INSERT INTO receiving_vendor_invoice_line (tenant_id, invoice_id, line_no, legal_entity_id, receipt_id, receipt_line_no, qty, unit_price, amount, standard_cost)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, rows)
}
