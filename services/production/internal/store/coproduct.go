package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

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

// splitCoProducts divides the declared outputs of a split order between the two
// orders (FR-21.14 with FR-21.15): the original is re-planned for its smaller
// quantity and the new order gets the same lines, planned for the split quantity
// with nothing reported. A received co-product stays on the original, which a
// split never re-plans below what it received: every receipt rides on a report as
// finished, which leaves the order ReportedAsFinished (not splittable) until a
// reset reverses it, and this check holds the rule on its own (PRODUCTION_SPLIT_REFUSED).
func splitCoProducts(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, orderID, newID string, originalQty, splitQty decimal.Decimal) error {
	cos, err := loadCoProducts(ctx, tx, tenantID, orderID)
	if err != nil || len(cos) == 0 {
		return err
	}
	for _, c := range cos {
		if !c.QtyReported.IsZero() {
			return splitRefused("co-product line %s has %s received; reset the report as finished first", c.LineNo, c.QtyReported)
		}
	}
	if err := replanCoProducts(ctx, tx, tenantID, orderID, originalQty); err != nil {
		return err
	}
	planned, err := domain.PlanCoProducts(cos, splitQty)
	if err != nil {
		return domainProblem(err)
	}
	return insertCoProducts(ctx, tx, tenantID, newID, legalEntityID, planned)
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

// journaledCoProducts names the co-product lines any journal of the order references,
// posted, reversed or draft: they keep their item and type.
func journaledCoProducts(ctx context.Context, tx pgx.Tx, tenantID, orderID string) (map[string]bool, error) {
	used := map[string]bool{}
	rows, err := tx.Query(ctx, `select distinct l.coproduct_line_number from production_journal_line l
		join production_journal j on j.tenant_id = l.tenant_id and j.id = l.journal_id
		where l.tenant_id = $1 and j.order_id = $2 and l.coproduct_line_number is not null`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var no string
		if err := rows.Scan(&no); err != nil {
			return nil, err
		}
		used[no] = true
	}
	return used, rows.Err()
}

// writeEditedCoProducts deletes the removed co-product lines, updates the kept ones
// and inserts the added ones (reported quantities are untouched).
func writeEditedCoProducts(ctx context.Context, tx pgx.Tx, tenantID, orderID, legalEntityID string, before, after []domain.CoProduct) error {
	kept := map[string]bool{}
	for _, c := range after {
		kept[c.LineNo] = true
	}
	existed := map[string]bool{}
	for _, c := range before {
		existed[c.LineNo] = true
		if !kept[c.LineNo] {
			if _, err := tx.Exec(ctx, `delete from production_coproduct where tenant_id = $1 and order_id = $2 and line_number = $3`,
				tenantID, orderID, c.LineNo); err != nil {
				return err
			}
		}
	}
	var added []domain.CoProduct
	for _, c := range after {
		if !existed[c.LineNo] {
			added = append(added, c)
			continue
		}
		if _, err := tx.Exec(ctx, `update production_coproduct set coproduct_type = $4, item_id = $5, qty_per = $6, qty_planned = $7
			where tenant_id = $1 and order_id = $2 and line_number = $3`, tenantID, orderID, c.LineNo, c.Type, c.ItemID,
			c.QtyPer.String(), c.QtyPlanned.String()); err != nil {
			return err
		}
	}
	return insertCoProducts(ctx, tx, tenantID, orderID, legalEntityID, added)
}
