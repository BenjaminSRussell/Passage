package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

type GrirFilter struct {
	VendorID        string
	PurchaseOrderID string
	Status          string
	AgeGt           *int
	CursorCreatedAt time.Time
	CursorID        string
	Limit           int
}

type GrirItem struct {
	ID             string
	ReceiptID      string
	ReceiptNumber  string
	LineNo         int
	VendorID       string
	POLineID       string
	ReceivedQty    decimal.Decimal
	InvoicedQty    decimal.Decimal
	ReceivedAmount decimal.Decimal
	ClearedAmount  decimal.Decimal
	OpenAmount     decimal.Decimal
	Currency       string
	Status         string
	AgeDays        int
	CreatedAt      time.Time
}

type CurrencyAmount struct {
	Amount   decimal.Decimal
	Currency string
}

type GrirTotals struct {
	OpenAmount []CurrencyAmount
	Count      int
}

type GrirPage struct {
	Items  []GrirItem
	Totals GrirTotals
}

const grirFilter = `g.tenant_id = $1
	AND ($2 = '' OR g.vendor_id = $2::uuid)
	AND ($3 = '' OR r.purchase_order_id = $3::uuid)
	AND ($4 = '' OR g.status = $4)
	AND ($5::int IS NULL OR now() - g.created_at > make_interval(days => $5::int))`

func (s *Postgres) ListGrir(ctx context.Context, f GrirFilter) (GrirPage, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return GrirPage{}, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)
	var cursorID any
	if !f.CursorCreatedAt.IsZero() {
		cursorID = f.CursorID
	}
	var page GrirPage
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT g.id::text, g.receipt_id::text, COALESCE(r.receipt_number, ''), g.line_no, COALESCE(g.vendor_id::text, ''),
			COALESCE(g.po_line_id::text, ''), g.received_qty, g.invoiced_qty, g.received_amount, g.cleared_amount,
			g.received_amount - g.cleared_amount, g.currency, g.status, EXTRACT(DAY FROM now() - g.created_at)::int, g.created_at
			FROM receiving_grir_open_item g
			JOIN receiving_receipt r ON r.tenant_id = g.tenant_id AND r.id = g.receipt_id
			WHERE `+grirFilter+`
			AND ($6::timestamptz IS NULL OR (g.created_at, g.id) < ($6::timestamptz, $7::uuid))
			ORDER BY g.created_at DESC, g.id DESC LIMIT $8`,
			tenantID, f.VendorID, f.PurchaseOrderID, f.Status, f.AgeGt, nullTime(f.CursorCreatedAt), cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g GrirItem
			if err := rows.Scan(&g.ID, &g.ReceiptID, &g.ReceiptNumber, &g.LineNo, &g.VendorID, &g.POLineID, &g.ReceivedQty, &g.InvoicedQty,
				&g.ReceivedAmount, &g.ClearedAmount, &g.OpenAmount, &g.Currency, &g.Status, &g.AgeDays, &g.CreatedAt); err != nil {
				return err
			}
			page.Items = append(page.Items, g)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		totals, err := tx.Query(ctx, `SELECT g.currency, SUM(g.received_amount - g.cleared_amount), count(*)
			FROM receiving_grir_open_item g
			JOIN receiving_receipt r ON r.tenant_id = g.tenant_id AND r.id = g.receipt_id
			WHERE `+grirFilter+` GROUP BY g.currency ORDER BY g.currency`,
			tenantID, f.VendorID, f.PurchaseOrderID, f.Status, f.AgeGt)
		if err != nil {
			return err
		}
		defer totals.Close()
		for totals.Next() {
			var c CurrencyAmount
			var n int
			if err := totals.Scan(&c.Currency, &c.Amount, &n); err != nil {
				return err
			}
			page.Totals.OpenAmount = append(page.Totals.OpenAmount, c)
			page.Totals.Count += n
		}
		return totals.Err()
	})
	return page, err
}
