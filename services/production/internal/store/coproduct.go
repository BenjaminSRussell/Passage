package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// CodeCoProductUnsupported refuses a split or merge of an order with co-products.
const CodeCoProductUnsupported = "PRODUCTION_COPRODUCT_NOT_SUPPORTED"

// CoProductInput is one co-product line of a report as finished: the quantity of
// the declared co-product received into stock (FR-21.14).
type CoProductInput struct {
	LineNo string
	Qty    decimal.Decimal
}

func insertCoProducts(ctx context.Context, tx pgx.Tx, tenantID, orderID, legalEntityID string, cos []domain.CoProduct) error {
	for _, c := range cos {
		if _, err := tx.Exec(ctx, `insert into production_coproduct (tenant_id, order_id, legal_entity_id, line_number, coproduct_type, item_id, qty_per, qty_planned)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`, tenantID, orderID, legalEntityID, c.LineNo, c.Type, c.ItemID, c.QtyPer.String(), c.QtyPlanned.String()); err != nil {
			return err
		}
	}
	return nil
}

// loadCoProducts returns the order's co-product lines in line-number order.
func loadCoProducts(ctx context.Context, q querier, tenantID, orderID string) ([]domain.CoProduct, error) {
	rows, err := q.Query(ctx, `select line_number, coproduct_type, item_id::text, qty_per::text, qty_planned::text, qty_reported::text
		from production_coproduct where tenant_id = $1 and order_id = $2 order by line_number`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CoProduct{}
	for rows.Next() {
		var c domain.CoProduct
		var per, planned, reported string
		if err := rows.Scan(&c.LineNo, &c.Type, &c.ItemID, &per, &planned, &reported); err != nil {
			return nil, err
		}
		c.QtyPer, c.QtyPlanned, c.QtyReported = dec(per), dec(planned), dec(reported)
		out = append(out, c)
	}
	return out, rows.Err()
}

// replanCoProducts rescales the planned quantities for a new order quantity.
func replanCoProducts(ctx context.Context, tx pgx.Tx, tenantID, orderID string, qty decimal.Decimal) error {
	cos, err := loadCoProducts(ctx, tx, tenantID, orderID)
	if err != nil || len(cos) == 0 {
		return err
	}
	if cos, err = domain.PlanCoProducts(cos, qty); err != nil {
		return domainProblem(err)
	}
	for _, c := range cos {
		if _, err := tx.Exec(ctx, `update production_coproduct set qty_planned = $4 where tenant_id = $1 and order_id = $2 and line_number = $3`,
			tenantID, orderID, c.LineNo, c.QtyPlanned.String()); err != nil {
			return err
		}
	}
	return nil
}

// checkNoCoProducts refuses a split or merge of orders that yield co-products:
// dividing the declared outputs between orders is a later slice.
func checkNoCoProducts(ctx context.Context, tx pgx.Tx, tenantID string, orderIDs ...string) error {
	var n int
	if err := tx.QueryRow(ctx, `select count(*) from production_coproduct where tenant_id = $1 and order_id::text = any($2)`,
		tenantID, orderIDs).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return httpx.NewProblem(http.StatusConflict, CodeCoProductUnsupported, "an order with co-products cannot be split or merged")
	}
	return nil
}

func coProductLine(c domain.CoProduct, qty decimal.Decimal) JournalLine {
	no, item := c.LineNo, c.ItemID
	return JournalLine{Kind: KindCoProduct, CoProductLineNo: &no, ItemID: &item, Qty: qty, QtyGood: qty}
}

// planCoProducts builds the co-product receipt lines of a report as finished:
// the explicit quantities, or, when proportional, what the co-products still
// owe for the product quantity reported with this journal.
func planCoProducts(in NewJournal, h orderHead, cos []domain.CoProduct) ([]JournalLine, error) {
	if len(cos) == 0 && len(in.CoProducts) > 0 {
		return nil, lineProblem("the order has no co-products")
	}
	var out []JournalLine
	if in.CoProductsProportional {
		after := h.qtyRAF.Add(in.QtyGood)
		for _, c := range cos {
			if q := domain.ProportionalCoProduct(c, h.quantity, after); q.IsPositive() {
				out = append(out, coProductLine(c, q))
			}
		}
		return out, nil
	}
	seen := map[string]bool{}
	for _, ci := range in.CoProducts {
		var found *domain.CoProduct
		for i := range cos {
			if cos[i].LineNo == ci.LineNo {
				found = &cos[i]
			}
		}
		if found == nil || seen[ci.LineNo] {
			return nil, lineProblem("line %s is not a co-product of the order, or is listed twice", ci.LineNo)
		}
		seen[ci.LineNo] = true
		out = append(out, coProductLine(*found, ci.Qty))
	}
	return out, nil
}

// bookCoProduct adds a (possibly negative, on reversal) received quantity to the
// co-product line.
func bookCoProduct(ctx context.Context, tx pgx.Tx, tenantID, orderID, lineNo string, qty decimal.Decimal) error {
	_, err := tx.Exec(ctx, `update production_coproduct set qty_reported = qty_reported + $4
		where tenant_id = $1 and order_id = $2 and line_number = $3`, tenantID, orderID, lineNo, qty.String())
	return err
}
