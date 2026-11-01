package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// Change-management problem codes (README lists the full catalogue).
const (
	CodeNotChangeable   = "SLS-0023"
	CodeInvalidChange   = "SLS-0024"
	CodeNotCancellable  = "SLS-0025"
	changeReasonMaxSize = 500
	maxVersionsListed   = 200
)

// LineChange sets editable fields on one line. Nil fields are left unchanged.
type LineChange struct {
	LineNo            int              `json:"lineNo"`
	Quantity          *decimal.Decimal `json:"quantity,omitempty"`
	UnitPrice         *decimal.Decimal `json:"unitPrice,omitempty"`
	DiscPct           *decimal.Decimal `json:"discPct,omitempty"`
	RequestedShipDate *string          `json:"requestedShipDate,omitempty"`
}

// ChangeRequest is one PATCH: header and line edits plus the reason recorded
// with the version it creates.
type ChangeRequest struct {
	Reason            string       `json:"reason"`
	RequestedShipDate *string      `json:"requestedShipDate,omitempty"`
	Lines             []LineChange `json:"lines"`
	// Approver is set by the API when the caller holds SalesOrderApprove: such
	// a change applies at once even for a customer whose changes need approval.
	Approver bool `json:"-"`
}

// OrderVersion is one entry of an order's change history: the version a change
// produced, who made it, why, and the field-level diff (FR-27.9).
type OrderVersion struct {
	Version   int                  `json:"version"`
	Actor     string               `json:"actor"`
	Reason    string               `json:"reason"`
	Diff      []domain.FieldChange `json:"diff"`
	ChangedAt time.Time            `json:"changedAt"`
}

// ApplyChange applies req to the order in memory and returns the field-level
// diff (FR-27.9) and the line numbers whose promise must be re-run. It
// recomputes each changed line's net and the order total. Only OPEN and
// RESERVED lines can change (BR-27.5). On a CONFIRMED order a changed quantity
// or requested date re-promises that line, and only that line (AC-27.7): its
// confirmed ship date follows the requested date, the same delivery date
// control confirm applies until the ATP projection exists. A supplementary
// line follows its parent and cannot be changed on its own (BR-27.14): its
// quantity and requested date are refused, and a change of the parent
// recomputes, re-dates or cancels it. A request that changes nothing returns
// an empty diff and leaves the order untouched.
func ApplyChange(o *Order, req ChangeRequest) ([]domain.FieldChange, []int, error) {
	if len(req.Reason) > changeReasonMaxSize {
		return nil, nil, errors.New("reason is too long")
	}
	var diff []domain.FieldChange
	if req.RequestedShipDate != nil && *req.RequestedShipDate != o.RequestedShipDate {
		if _, err := time.Parse(dayLayout, *req.RequestedShipDate); err != nil {
			return nil, nil, errors.New("requestedShipDate must be yyyy-mm-dd")
		}
		diff = append(diff, domain.DiffHeader("requestedShipDate", o.RequestedShipDate, *req.RequestedShipDate)...)
		o.RequestedShipDate = *req.RequestedShipDate
	}
	index := make(map[int]int, len(o.Lines))
	for i, l := range o.Lines {
		index[l.LineNo] = i
	}
	seen := map[int]bool{}
	repromised := []int{}
	for _, c := range req.Lines {
		i, ok := index[c.LineNo]
		if !ok {
			return nil, nil, fmt.Errorf("line %d does not exist", c.LineNo)
		}
		if seen[c.LineNo] {
			return nil, nil, fmt.Errorf("line %d is changed twice", c.LineNo)
		}
		seen[c.LineNo] = true
		l := &o.Lines[i]
		if !domain.LineChangeable(l.LineStatus) {
			return nil, nil, fmt.Errorf("line %d is %s and cannot change", c.LineNo, l.LineStatus)
		}
		if l.SupplementaryOf != nil && (c.Quantity != nil && !c.Quantity.Equal(l.Quantity) ||
			c.RequestedShipDate != nil && *c.RequestedShipDate != l.RequestedShipDate) {
			parent := 0
			if l.ParentLineNo != nil {
				parent = *l.ParentLineNo
			}
			return nil, nil, fmt.Errorf("line %d is a supplementary line of line %d; change the parent line's quantity or date instead", c.LineNo, parent)
		}
		before := domain.LineState{Quantity: l.Quantity, UnitPrice: l.UnitPrice, DiscPct: l.DiscPct, RequestedShipDate: l.RequestedShipDate}
		after := before
		if c.Quantity != nil {
			after.Quantity = *c.Quantity
		}
		if c.UnitPrice != nil {
			after.UnitPrice = *c.UnitPrice
		}
		if c.DiscPct != nil {
			after.DiscPct = *c.DiscPct
		}
		if c.RequestedShipDate != nil {
			after.RequestedShipDate = *c.RequestedShipDate
		}
		if err := domain.ValidateQuantity(after.Quantity); err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", c.LineNo, err)
		}
		if after.UnitPrice.IsNegative() || after.DiscPct.IsNegative() || after.DiscPct.GreaterThan(decimal.NewFromInt(100)) {
			return nil, nil, fmt.Errorf("line %d price or discount is out of range", c.LineNo)
		}
		if _, err := time.Parse(dayLayout, after.RequestedShipDate); err != nil {
			return nil, nil, fmt.Errorf("line %d requestedShipDate must be yyyy-mm-dd", c.LineNo)
		}
		lineDiff := domain.DiffLine(c.LineNo, before, after)
		if len(lineDiff) == 0 {
			continue
		}
		net, err := domain.LineNet(after.Quantity, after.UnitPrice, after.DiscPct)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", c.LineNo, err)
		}
		if o.Status == domain.OrderConfirmed && domain.Repromises(before, after) {
			repromised = append(repromised, c.LineNo)
			if l.ConfirmedShipDate == nil || *l.ConfirmedShipDate != after.RequestedShipDate {
				old := ""
				if l.ConfirmedShipDate != nil {
					old = *l.ConfirmedShipDate
				}
				lineDiff = append(lineDiff, domain.FieldChange{LineNo: c.LineNo, Field: "confirmedShipDate", Old: old, New: after.RequestedShipDate})
				confirmed := after.RequestedShipDate
				l.ConfirmedShipDate = &confirmed
			}
		}
		l.Quantity, l.UnitPrice, l.DiscPct, l.RequestedShipDate = after.Quantity, after.UnitPrice, after.DiscPct, after.RequestedShipDate
		l.LineNet = net
		diff = append(diff, lineDiff...)
	}
	followDiff, followed, err := followParents(o)
	if err != nil {
		return nil, nil, err
	}
	diff = append(diff, followDiff...)
	if o.Status == domain.OrderConfirmed {
		repromised = append(repromised, followed...)
	}
	if len(diff) > 0 {
		o.NetTotal = netOf(o.Lines)
	}
	return diff, repromised, nil
}

func netOf(lines []Line) decimal.Decimal {
	total := decimal.Zero
	for _, l := range lines {
		if l.LineStatus != domain.LineCancelled {
			total = total.Add(l.LineNet)
		}
	}
	return total
}

// withAllocationMoves makes the version diff record the confirmed ship date a
// line really ends up with when allocation moved it to a later period: the
// re-promise to the requested date that ApplyChange recorded is replaced by
// the moved date, and an entry that no longer changes anything is dropped.
func withAllocationMoves(diff []domain.FieldChange, before map[int]Line, moved map[int]string) []domain.FieldChange {
	if len(moved) == 0 {
		return diff
	}
	out := make([]domain.FieldChange, 0, len(diff)+len(moved))
	seen := map[int]bool{}
	for _, c := range diff {
		if date, ok := moved[c.LineNo]; ok && c.Field == "confirmedShipDate" {
			seen[c.LineNo] = true
			c.New = date
			if c.Old == c.New {
				continue
			}
		}
		out = append(out, c)
	}
	nos := make([]int, 0, len(moved))
	for no := range moved {
		nos = append(nos, no)
	}
	sort.Ints(nos)
	for _, no := range nos {
		old := dayOrEmpty(before[no].ConfirmedShipDate)
		if !seen[no] && old != moved[no] {
			out = append(out, domain.FieldChange{LineNo: no, Field: "confirmedShipDate", Old: old, New: moved[no]})
		}
	}
	return out
}

func sameDay(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// hasActiveHold reports whether o has an ACTIVE hold of holdType.
func hasActiveHold(o Order, holdType string) bool {
	for _, h := range o.Holds {
		if h.Status == domain.HoldActive && h.HoldType == holdType {
			return true
		}
	}
	return false
}

// ChangeOrder applies a PATCH to a changeable order (BR-27.5). It bumps the
// version, records the diff in sales_order_version and writes order.changed.v1
// to the outbox in the same transaction. When the net total of a CONFIRMED
// order rises, the credit check runs again (FR-27.9); a failure that no valid
// CREDIT release covers places a CREDIT hold (BR-27.4: a release covers up to
// its amount plus 5%). A no-op request returns the order as it is, with no new
// version.
func (s *Postgres) ChangeOrder(ctx context.Context, id string, version int, req ChangeRequest, actor string, now time.Time) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return versionConflict()
		}
		if !domain.CanChange(o.Status) {
			return problem(http.StatusUnprocessableEntity, CodeNotChangeable, fmt.Sprintf("order is %s and cannot be changed", o.Status))
		}
		if o.Status == domain.OrderConfirmed && !req.Approver {
			required, err := changeApprovalRequired(ctx, tx, p.TenantID, o.LegalEntityID, o.CustomerID)
			if err != nil {
				return err
			}
			if required {
				pending, err := submitChangeRequestTx(ctx, tx, p.TenantID, o, req, actor, now)
				if err != nil {
					return err
				}
				out = o
				out.PendingChange = pending
				return nil
			}
		}
		out, err = applyChangeTx(ctx, tx, p.TenantID, o, id, req, actor, now)
		return err
	})
	return out, err
}

// applyChangeTx applies req to the locked order o in tx: line rows, allocation,
// schedule, version, order.changed and the credit re-check. It returns the
// order at its new version, or o unchanged for a request that changes nothing.
func applyChangeTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, id string, req ChangeRequest, actor string, now time.Time) (Order, error) {
	p := tenant.Principal{TenantID: tenantID}
	var out Order
	err := func() error {
		before := make(map[int]Line, len(o.Lines))
		for _, l := range o.Lines {
			before[l.LineNo] = l
		}
		oldTotal := o.NetTotal
		diff, repromised, err := ApplyChange(&o, req)
		if err != nil {
			return problem(http.StatusUnprocessableEntity, CodeInvalidChange, err.Error())
		}
		if len(diff) == 0 {
			out = o
			return nil
		}
		reallocate := map[int]bool{}
		for _, l := range o.Lines {
			b := before[l.LineNo]
			if !l.Quantity.Equal(b.Quantity) || l.RequestedShipDate != b.RequestedShipDate || l.LineStatus != b.LineStatus {
				reallocate[l.LineNo] = true
			}
			if l.Quantity.Equal(b.Quantity) && l.UnitPrice.Equal(b.UnitPrice) && l.DiscPct.Equal(b.DiscPct) &&
				l.RequestedShipDate == b.RequestedShipDate && sameDay(l.ConfirmedShipDate, b.ConfirmedShipDate) && l.LineStatus == b.LineStatus {
				continue
			}
			if _, err := tx.Exec(ctx, `update sales_order_line set quantity = $4::numeric, unit_price = $5::numeric, disc_pct = $6::numeric,
					line_net = $7::numeric, requested_ship_date = $8::date, confirmed_ship_date = $9::date, line_status = $10
				where tenant_id = $1 and order_id = $2 and line_no = $3`,
				p.TenantID, id, l.LineNo, l.Quantity.String(), l.UnitPrice.String(), l.DiscPct.String(), l.LineNet.String(),
				l.RequestedShipDate, l.ConfirmedShipDate, l.LineStatus); err != nil {
				return err
			}
		}
		var notices []Notice
		if o.Status == domain.OrderConfirmed && len(reallocate) > 0 {
			// A confirmed line that changed quantity or date, or was cancelled or
			// reopened, gives back what it drew and draws again (BR-27.15).
			if err := returnAllocationTx(ctx, tx, p.TenantID, o, reallocate, now); err != nil {
				return err
			}
			alloc, err := allocateOrderTx(ctx, tx, p.TenantID, o, reallocate)
			if err != nil {
				return err
			}
			notices = alloc.Notices
			diff = withAllocationMoves(diff, before, alloc.Moved)
		}
		if o.Status == domain.OrderConfirmed {
			if err := syncScheduleTx(ctx, tx, p.TenantID, id); err != nil {
				return err
			}
		}
		next := o.Version + 1
		if _, err := tx.Exec(ctx, `update sales_order set requested_ship_date = $3::date, net_total = $4::numeric, version = $5, updated_at = $6
			where tenant_id = $1 and id = $2`, p.TenantID, id, o.RequestedShipDate, o.NetTotal.String(), next, now); err != nil {
			return err
		}
		if err := insertVersion(ctx, tx, p.TenantID, o.LegalEntityID, id, next, actor, req.Reason, diff, now); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "order", id, salesevents.OrderChanged, p.TenantID, salesevents.OrderChangedData{
			OrderID: id, Version: next, Reason: req.Reason, Actor: actor, Diff: salesevents.DiffOf(diff), RepromisedLines: repromised,
		}); err != nil {
			return err
		}
		if o.Status == domain.OrderConfirmed && o.NetTotal.GreaterThan(oldTotal) {
			eval, err := evaluateCreditTx(ctx, tx, p.TenantID, o, now)
			if err != nil {
				return err
			}
			if eval.Outcome == domain.RuleFail && !coveredByRelease(o, now) && !hasActiveHold(o, domain.HoldCredit) {
				if err := applyCreditHoldTx(ctx, tx, p.TenantID, o, eval, actor, next); err != nil {
					return err
				}
			}
		}
		loaded, err := loadOrder(ctx, tx, p.TenantID, id, false)
		loaded.Warnings = notices
		out = loaded
		return err
	}()
	return out, err
}

// CancelOrder cancels an order that has not shipped (BR-27.5). Every OPEN or
// RESERVED line is cancelled; an order with a picked, delivered or invoiced
// line is refused. ACTIVE holds are CLEARED so the order leaves the release
// queue. The order gets a new version, and order.cancelled.v1 is queued.
func (s *Postgres) CancelOrder(ctx context.Context, id string, version int, reason, actor string, now time.Time) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	if len(reason) > changeReasonMaxSize {
		return Order{}, problem(http.StatusUnprocessableEntity, CodeInvalidChange, "reason is too long")
	}
	var out Order
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return versionConflict()
		}
		if !domain.CanCancel(o.Status) {
			return problem(http.StatusUnprocessableEntity, CodeNotCancellable, fmt.Sprintf("order is %s and cannot be cancelled", o.Status))
		}
		lineIDs := []string{}
		for _, l := range o.Lines {
			switch {
			case l.LineStatus == domain.LineCancelled:
			case domain.LineChangeable(l.LineStatus):
				lineIDs = append(lineIDs, l.ID)
			default:
				return problem(http.StatusUnprocessableEntity, CodeNotCancellable, fmt.Sprintf("line %d is %s; cancel it through a return", l.LineNo, l.LineStatus))
			}
		}
		if _, err := tx.Exec(ctx, `update sales_order_line set line_status = 'CANCELLED'
			where tenant_id = $1 and order_id = $2 and line_status in ('OPEN', 'RESERVED')`, p.TenantID, id); err != nil {
			return err
		}
		if err := returnAllocationTx(ctx, tx, p.TenantID, o, nil, now); err != nil {
			return err
		}
		if err := syncScheduleTx(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update order_hold set status = 'CLEARED' where tenant_id = $1 and order_id = $2 and status = 'ACTIVE'`, p.TenantID, id); err != nil {
			return err
		}
		if err := returnAgreementTx(ctx, tx, p.TenantID, o, actor, now); err != nil {
			return err
		}
		next := o.Version + 1
		if _, err := tx.Exec(ctx, `update sales_order set status = 'CANCELLED', version = $3, updated_at = $4 where tenant_id = $1 and id = $2`,
			p.TenantID, id, next, now); err != nil {
			return err
		}
		diff := domain.DiffHeader("status", o.Status, "CANCELLED")
		if err := insertVersion(ctx, tx, p.TenantID, o.LegalEntityID, id, next, actor, reason, diff, now); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "order", id, salesevents.OrderCancelled, p.TenantID, salesevents.OrderCancelledData{
			OrderID: id, Version: next, LineIDs: lineIDs, Reason: reason, Actor: actor,
		}); err != nil {
			return err
		}
		out, err = loadOrder(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

func insertVersion(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, orderID string, version int, actor, reason string, diff []domain.FieldChange, now time.Time) error {
	if diff == nil {
		diff = []domain.FieldChange{}
	}
	raw, err := json.Marshal(diff)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into sales_order_version (tenant_id, legal_entity_id, order_id, version, actor, reason, diff, changed_at)
		values ($1, $2, $3, $4, $5, $6, $7::jsonb, $8)`, tenantID, legalEntityID, orderID, version, actor, reason, string(raw), now)
	return err
}

// ListOrderVersions returns an order's change history, newest first. An order
// that does not exist (or is outside the caller's scope) is 404.
func (s *Postgres) ListOrderVersions(ctx context.Context, id string) ([]OrderVersion, error) {
	p, _ := tenant.FromContext(ctx)
	out := []OrderVersion{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from sales_order where tenant_id = $1 and id = $2)`, p.TenantID, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return orderNotFound()
		}
		rows, err := tx.Query(ctx, `select version, actor, reason, diff::text, changed_at from sales_order_version
			where tenant_id = $1 and order_id = $2 order by version desc limit $3`, p.TenantID, id, maxVersionsListed)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v OrderVersion
			var raw string
			if err := rows.Scan(&v.Version, &v.Actor, &v.Reason, &raw, &v.ChangedAt); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(raw), &v.Diff); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}
