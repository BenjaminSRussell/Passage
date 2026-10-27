package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// Supplementary rule problem codes (README lists the full catalogue).
const (
	CodeSupplementaryDuplicate = "SLS-0033"
	CodeSupplementaryNotFound  = "SLS-0106"
)

// SupplementaryRuleFilter narrows the rule list. Empty fields match
// everything; an empty LegalEntityID means every legal entity in the caller's
// scope.
type SupplementaryRuleFilter struct {
	LegalEntityID   string
	ParentProductID string
	Limit           int
}

const supplementaryColumns = `id::text, legal_entity_id::text, parent_product_id::text, customer_id::text, supplementary_product_id::text,
	unit, requirement, base_quantity::text, quantity_per_base::text, pricing, unit_price::text`

func scanSupplementaryRule(row pgx.Row) (domain.SupplementaryRule, error) {
	var r domain.SupplementaryRule
	var base, per, price string
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.ParentProductID, &r.CustomerID, &r.SupplementaryProduct, &r.Unit,
		&r.Requirement, &base, &per, &r.Pricing, &price); err != nil {
		return domain.SupplementaryRule{}, err
	}
	r.BaseQuantity = decimal.RequireFromString(base)
	r.QuantityPerBase = decimal.RequireFromString(per)
	r.UnitPrice = decimal.RequireFromString(price)
	return r, nil
}

// CreateSupplementaryRule stores a validated rule. A second rule for the same
// parent, customer (or all customers) and supplementary product is refused
// with 409, so evaluation never adds an item twice.
func (s *Postgres) CreateSupplementaryRule(ctx context.Context, in domain.SupplementaryRule, actor string) (domain.SupplementaryRule, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out domain.SupplementaryRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSupplementaryRule(tx.QueryRow(ctx, `insert into supplementary_item_rule (tenant_id, id, legal_entity_id, parent_product_id,
				customer_id, supplementary_product_id, unit, requirement, base_quantity, quantity_per_base, pricing, unit_price, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10::numeric, $11, $12::numeric, $13)
			returning `+supplementaryColumns,
			p.TenantID, in.ID, in.LegalEntityID, in.ParentProductID, in.CustomerID, in.SupplementaryProduct, in.Unit, in.Requirement,
			in.BaseQuantity.String(), in.QuantityPerBase.String(), in.Pricing, in.UnitPrice.String(), actor))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.SupplementaryRule{}, problem(http.StatusConflict, CodeSupplementaryDuplicate,
			"a rule for this parent product, customer and supplementary product already exists")
	}
	return out, err
}

// ListSupplementaryRules returns rules in the caller's scope, all-customer
// rules and customer rules in creation order.
func (s *Postgres) ListSupplementaryRules(ctx context.Context, f SupplementaryRuleFilter) ([]domain.SupplementaryRule, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.SupplementaryRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+supplementaryColumns+` from supplementary_item_rule
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or parent_product_id::text = $3)
			order by created_at, id limit $4`, p.TenantID, f.LegalEntityID, f.ParentProductID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanSupplementaryRule(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteSupplementaryRule removes a rule. Stored orders are not changed.
func (s *Postgres) DeleteSupplementaryRule(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from supplementary_item_rule where tenant_id = $1 and id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(http.StatusNotFound, CodeSupplementaryNotFound, "supplementary item rule not found")
		}
		return nil
	})
}

// CodeSupplementaryAdded is the SLS-0012 warning returned for each MANDATORY
// supplementary line an order received at commit (PRD §5.27.16).
const CodeSupplementaryAdded = "SLS-0012"

// mandatorySupplementaryTx loads the supplementary item rules of the order's
// legal entity for the ordered products (OPTIONAL ones too, since a
// customer's OPTIONAL rule replaces the all-customers rule for the same item)
// and returns the MANDATORY lines they add (FR-27.14, AC-27.10), numbered after the order's own lines. Each line
// inherits its parent's site, warehouse and requested date (BR-27.14), takes
// the rule's unit and price (zero for FREE) and carries no discount.
// Product listings are checked on the ordered lines only; a supplementary
// item goes with its parent.
func mandatorySupplementaryTx(ctx context.Context, tx pgx.Tx, tenantID string, in NewOrder) ([]Line, error) {
	products := make([]string, 0, len(in.Lines))
	parents := make([]domain.SupplementaryParent, 0, len(in.Lines))
	byNo := make(map[int]Line, len(in.Lines))
	last := 0
	for _, l := range in.Lines {
		products = append(products, l.ProductID)
		parents = append(parents, domain.SupplementaryParent{LineNo: l.LineNo, ProductID: l.ProductID, Quantity: l.Quantity})
		byNo[l.LineNo] = l
		last = max(last, l.LineNo)
	}
	rows, err := tx.Query(ctx, `select `+supplementaryColumns+` from supplementary_item_rule
		where tenant_id = $1 and legal_entity_id = $2 and parent_product_id = any($3::uuid[])
		order by created_at, id`, tenantID, in.LegalEntityID, products)
	if err != nil {
		return nil, err
	}
	rules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.SupplementaryRule, error) {
		return scanSupplementaryRule(row)
	})
	if err != nil || len(rules) == 0 {
		return nil, err
	}
	added, err := domain.MandatorySupplementary(rules, in.CustomerID, parents)
	if err != nil {
		return nil, err
	}
	out := make([]Line, 0, len(added))
	for i, a := range added {
		parent := byNo[a.ParentLineNo]
		net, err := domain.LineNet(a.Quantity, a.UnitPrice, decimal.Zero)
		if err != nil {
			return nil, err
		}
		parentNo, ruleID := a.ParentLineNo, a.RuleID
		out = append(out, Line{LineNo: last + i + 1, ProductID: a.ProductID, Quantity: a.Quantity, Unit: a.Unit,
			SiteID: parent.SiteID, WarehouseID: parent.WarehouseID, UnitPrice: a.UnitPrice, DiscPct: decimal.Zero, LineNet: net,
			RequestedShipDate: parent.RequestedShipDate, ParentLineNo: &parentNo, SupplementaryRuleID: &ruleID,
			suppBase: a.BaseQuantity, suppPerBase: a.QuantityPerBase})
	}
	return out, nil
}

// linkSupplementaryParents sets ParentLineNo on each supplementary line from
// its parent line's id.
func linkSupplementaryParents(lines []Line) {
	numbers := make(map[string]int, len(lines))
	for _, l := range lines {
		numbers[l.ID] = l.LineNo
	}
	for i := range lines {
		if lines[i].SupplementaryOf == nil {
			continue
		}
		if no, ok := numbers[*lines[i].SupplementaryOf]; ok {
			lines[i].ParentLineNo = &no
		}
	}
}

// supplementaryWarnings returns the SLS-0012 warning for each open
// supplementary line of a newly created order.
func supplementaryWarnings(o Order) []Notice {
	var out []Notice
	for _, l := range o.Lines {
		if l.SupplementaryOf == nil || l.LineStatus == domain.LineCancelled {
			continue
		}
		out = append(out, Notice{Code: CodeSupplementaryAdded, LineNo: l.LineNo,
			Message: fmt.Sprintf("Mandatory supplementary item %s added (%s %s)", l.ProductID, l.Quantity.String(), l.Unit)})
	}
	return out
}

// followParents applies BR-27.14 after a change: each open supplementary line
// takes its parent's requested date and, on a confirmed order, its confirmed
// date; its quantity is recomputed from the parent's quantity, and it is
// cancelled when the parent drops below one base quantity or is cancelled. A
// supplementary line that was cancelled for being below the base quantity is
// reopened when its open parent reaches the base quantity again, as the
// MANDATORY rule would add it at commit (FR-27.14); a cancelled parent never
// reopens, so lines cancelled with it stay cancelled.
// It returns the diff of the lines it moved and the numbers of those whose
// quantity, status or dates changed (they are re-promised with their parent).
func followParents(o *Order) ([]domain.FieldChange, []int, error) {
	byID := make(map[string]int, len(o.Lines))
	for i, l := range o.Lines {
		byID[l.ID] = i
	}
	var diff []domain.FieldChange
	var moved []int
	for i := range o.Lines {
		l := &o.Lines[i]
		reopen := l.LineStatus == domain.LineCancelled
		if l.SupplementaryOf == nil || !reopen && !domain.LineChangeable(l.LineStatus) {
			continue
		}
		pi, ok := byID[*l.SupplementaryOf]
		if !ok {
			continue
		}
		parent := o.Lines[pi]
		qty := domain.SupplementaryQuantity(parent.Quantity, l.suppBase, l.suppPerBase)
		if reopen && (!domain.LineChangeable(parent.LineStatus) || !qty.IsPositive()) {
			continue
		}
		if parent.LineStatus == domain.LineCancelled || !qty.IsPositive() {
			diff = append(diff, domain.FieldChange{LineNo: l.LineNo, Field: "lineStatus", Old: l.LineStatus, New: domain.LineCancelled})
			l.LineStatus = domain.LineCancelled
			continue
		}
		before := domain.LineState{Quantity: l.Quantity, UnitPrice: l.UnitPrice, DiscPct: l.DiscPct, RequestedShipDate: l.RequestedShipDate}
		after := before
		after.Quantity, after.RequestedShipDate = qty, parent.RequestedShipDate
		lineDiff := domain.DiffLine(l.LineNo, before, after)
		if reopen {
			lineDiff = append([]domain.FieldChange{{LineNo: l.LineNo, Field: "lineStatus", Old: domain.LineCancelled, New: domain.LineOpen}}, lineDiff...)
			l.LineStatus = domain.LineOpen
		}
		if o.Status == domain.OrderConfirmed && !sameDay(l.ConfirmedShipDate, parent.ConfirmedShipDate) {
			lineDiff = append(lineDiff, domain.FieldChange{LineNo: l.LineNo, Field: "confirmedShipDate", Old: dayOrEmpty(l.ConfirmedShipDate),
				New: dayOrEmpty(parent.ConfirmedShipDate)})
			l.ConfirmedShipDate = parent.ConfirmedShipDate
		}
		if len(lineDiff) == 0 {
			continue
		}
		net, err := domain.LineNet(qty, l.UnitPrice, l.DiscPct)
		if err != nil {
			return nil, nil, fmt.Errorf("supplementary line %d: %w", l.LineNo, err)
		}
		l.LineNet = net
		l.Quantity, l.RequestedShipDate = qty, parent.RequestedShipDate
		diff = append(diff, lineDiff...)
		moved = append(moved, l.LineNo)
	}
	return diff, moved, nil
}

func dayOrEmpty(d *string) string {
	if d == nil {
		return ""
	}
	return *d
}
