package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// NewLine is a line a PATCH adds to an order (FR-27.9): the same fields an
// order line is entered with. The line number is the next one after the
// order's highest, so a number is never reused.
type NewLine struct {
	ProductID           string          `json:"productId"`
	Quantity            decimal.Decimal `json:"quantity"`
	Unit                string          `json:"unit"`
	SiteID              string          `json:"siteId"`
	WarehouseID         string          `json:"warehouseId"`
	UnitPrice           decimal.Decimal `json:"unitPrice"`
	DiscPct             decimal.Decimal `json:"discPct"`
	RequestedShipDate   string          `json:"requestedShipDate"`
	DeliveryDateControl *string         `json:"deliveryDateControl,omitempty"`
}

// buildAdded turns the added lines of a change into order lines numbered after
// the order's highest line number. Each gets an id so MANDATORY supplementary
// lines can name it as their parent.
func buildAdded(o *Order, adds []NewLine) ([]Line, error) {
	last := 0
	for _, l := range o.Lines {
		last = max(last, l.LineNo)
	}
	out := make([]Line, 0, len(adds))
	for i, a := range adds {
		no := last + i + 1
		if !ids.Valid(a.ProductID) || !ids.Valid(a.SiteID) || !ids.Valid(a.WarehouseID) {
			return nil, fmt.Errorf("added line %d: productId, siteId and warehouseId must be UUIDs", i+1)
		}
		if n := len(a.Unit); n == 0 || n > 10 {
			return nil, fmt.Errorf("added line %d: unit must be 1 to 10 characters", i+1)
		}
		if _, err := time.Parse(dayLayout, a.RequestedShipDate); err != nil {
			return nil, fmt.Errorf("added line %d: requestedShipDate must be yyyy-mm-dd", i+1)
		}
		if a.DeliveryDateControl != nil {
			if msg := domain.ValidateDeliveryControl(*a.DeliveryDateControl); msg != "" {
				return nil, fmt.Errorf("added line %d: deliveryDateControl %s", i+1, msg)
			}
		}
		net, err := domain.LineNet(a.Quantity, a.UnitPrice, a.DiscPct)
		if err != nil {
			return nil, fmt.Errorf("added line %d: %w", i+1, err)
		}
		out = append(out, Line{ID: ids.New(), LineNo: no, ProductID: a.ProductID, Quantity: a.Quantity, Unit: a.Unit, SiteID: a.SiteID,
			WarehouseID: a.WarehouseID, UnitPrice: a.UnitPrice, DiscPct: a.DiscPct, LineNet: net, RequestedShipDate: a.RequestedShipDate,
			LineStatus: domain.LineOpen, DeliveryDateControl: a.DeliveryDateControl, DeliveredQty: decimal.Zero, InvoicedQty: decimal.Zero, added: true})
	}
	return out, nil
}

// prepareChange does the database half of adding lines before ApplyChange
// runs: it builds the lines, checks the product listings of the added lines
// (BR-27.13; a caller with sales.order.approve and a listingOverrideReason may
// accept a denied one) and adds the MANDATORY supplementary lines their
// products call for (BR-27.14), numbered after the added lines. The result
// carries them in unexported fields, so the stored request stays what the
// caller sent. A request that adds no line is returned as it is.
func prepareChange(ctx context.Context, tx pgx.Tx, tenantID string, o Order, req ChangeRequest, actor string, now time.Time) (ChangeRequest, error) {
	if len(req.AddLines) == 0 {
		return req, nil
	}
	added, err := buildAdded(&o, req.AddLines)
	if err != nil {
		return req, problem(http.StatusUnprocessableEntity, CodeInvalidChange, err.Error())
	}
	in := NewOrder{LegalEntityID: o.LegalEntityID, CustomerID: o.CustomerID, CustomerGroup: o.CustomerGroup, Lines: added,
		OrderDate: now, Actor: actor, CanOverrideListing: req.Approver, ListingOverrideReason: req.ListingOverrideReason}
	overrides, err := checkListings(ctx, tx, tenantID, in)
	if err != nil {
		return req, err
	}
	supp, err := mandatorySupplementaryTx(ctx, tx, tenantID, in)
	if err != nil {
		return req, err
	}
	parents := make(map[int]string, len(added))
	for _, l := range added {
		parents[l.LineNo] = l.ID
	}
	for i := range supp {
		parent := parents[*supp[i].ParentLineNo]
		supp[i].ID = ids.New()
		supp[i].SupplementaryOf = &parent
		supp[i].LineStatus = domain.LineOpen
		supp[i].added = true
	}
	req.added, req.addedSupp, req.overrides = added, supp, overrides
	return req, nil
}

// applyRemovals cancels the lines a change removes. Only an OPEN or RESERVED
// line can go (BR-27.5); a supplementary line goes with its parent, and a line
// with a production order planned for it is cancelled there first. Its
// supplementary lines are cancelled by followParents.
func applyRemovals(o *Order, req ChangeRequest, index map[int]int, seen map[int]bool) ([]domain.FieldChange, error) {
	var diff []domain.FieldChange
	for _, no := range req.RemoveLines {
		i, ok := index[no]
		if !ok {
			return nil, fmt.Errorf("line %d does not exist", no)
		}
		if seen[no] {
			return nil, fmt.Errorf("line %d is changed or removed twice", no)
		}
		seen[no] = true
		l := &o.Lines[i]
		if !domain.LineChangeable(l.LineStatus) {
			return nil, fmt.Errorf("line %d is %s and cannot be removed", no, l.LineStatus)
		}
		if l.SupplementaryOf != nil {
			return nil, fmt.Errorf("line %d is a supplementary line; remove its parent line instead", no)
		}
		if l.ProductionOrderID != nil {
			return nil, fmt.Errorf("line %d has a production order planned; cancel that first", no)
		}
		diff = append(diff, domain.FieldChange{LineNo: no, Field: "lineStatus", Old: l.LineStatus, New: domain.LineCancelled})
		l.LineStatus = domain.LineCancelled
	}
	return diff, nil
}

// applyAdditions appends the lines a change adds (and their supplementary
// lines) to the order in memory and returns their diff and line numbers. On a
// CONFIRMED order each line is promised at once through the same delivery date
// control confirm applies (BR-27.2), and a supplementary line takes its
// parent's date (BR-27.14).
func applyAdditions(o *Order, req ChangeRequest) ([]domain.FieldChange, []int, error) {
	added := req.added
	if added == nil && len(req.AddLines) > 0 {
		var err error
		if added, err = buildAdded(o, req.AddLines); err != nil {
			return nil, nil, err
		}
	}
	var diff []domain.FieldChange
	var promised []int
	confirmed := o.Status == domain.OrderConfirmed
	for _, l := range added {
		if confirmed {
			date := l.RequestedShipDate
			if o.dates != nil {
				dec, err := o.dates.ship(l, o.Lines, l.RequestedShipDate)
				if err != nil {
					return nil, nil, fmt.Errorf("line %d requestedShipDate must be yyyy-mm-dd", l.LineNo)
				}
				date = dec.Date.Format(dayLayout)
			}
			l.ConfirmedShipDate = &date
			promised = append(promised, l.LineNo)
		}
		o.Lines = append(o.Lines, l)
		diff = append(diff, addedLineDiff(l)...)
	}
	for _, l := range req.addedSupp {
		if confirmed {
			for _, p := range o.Lines {
				if l.SupplementaryOf != nil && p.ID == *l.SupplementaryOf {
					l.ConfirmedShipDate = p.ConfirmedShipDate
				}
			}
			promised = append(promised, l.LineNo)
		}
		o.Lines = append(o.Lines, l)
		diff = append(diff, addedLineDiff(l)...)
	}
	return diff, promised, nil
}

// addedLineDiff is the version diff of an added line: every field is new, so
// Old is empty, and the product the line was added for is named first, then
// the line id, unit, site and warehouse a consumer needs to plan it.
func addedLineDiff(l Line) []domain.FieldChange {
	out := []domain.FieldChange{
		{LineNo: l.LineNo, Field: "lineAdded", New: l.ProductID},
		{LineNo: l.LineNo, Field: "lineId", New: l.ID},
		{LineNo: l.LineNo, Field: "unit", New: l.Unit},
		{LineNo: l.LineNo, Field: "siteId", New: l.SiteID},
		{LineNo: l.LineNo, Field: "warehouseId", New: l.WarehouseID},
		{LineNo: l.LineNo, Field: "quantity", New: l.Quantity.String()},
		{LineNo: l.LineNo, Field: "unitPrice", New: l.UnitPrice.String()},
		{LineNo: l.LineNo, Field: "discPct", New: l.DiscPct.String()},
		{LineNo: l.LineNo, Field: "requestedShipDate", New: l.RequestedShipDate},
	}
	if l.ConfirmedShipDate != nil {
		out = append(out, domain.FieldChange{LineNo: l.LineNo, Field: "confirmedShipDate", New: *l.ConfirmedShipDate})
	}
	return out
}

// insertAddedLinesTx stores the lines a change added; parents come before
// their supplementary lines because o.Lines lists them in that order.
func insertAddedLinesTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order) error {
	for _, l := range o.Lines {
		if !l.added {
			continue
		}
		var base, per *string
		if l.SupplementaryOf != nil {
			b, p := l.suppBase.String(), l.suppPerBase.String()
			base, per = &b, &p
		}
		if _, err := tx.Exec(ctx, `insert into sales_order_line (tenant_id, id, order_id, line_no, product_id, quantity, unit, site_id, warehouse_id,
				unit_price, disc_pct, line_net, requested_ship_date, confirmed_ship_date, line_status, delivery_date_control,
				supplementary_of, supplementary_rule_id, supplementary_base_qty, supplementary_qty_per_base)
			values ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9, $10::numeric, $11::numeric, $12::numeric, $13::date, $14::date, 'OPEN', $15,
				$16, $17, $18::numeric, $19::numeric)`,
			tenantID, l.ID, o.ID, l.LineNo, l.ProductID, l.Quantity.String(), l.Unit, l.SiteID, l.WarehouseID,
			l.UnitPrice.String(), l.DiscPct.String(), l.LineNet.String(), l.RequestedShipDate, l.ConfirmedShipDate, l.DeliveryDateControl,
			l.SupplementaryOf, l.SupplementaryRuleID, base, per); err != nil {
			return err
		}
	}
	return nil
}

// appendListingOverridesTx logs the listing overrides accepted for added lines
// on the order, after those already there (FR-27.15).
func appendListingOverridesTx(ctx context.Context, tx pgx.Tx, tenantID, orderID string, overrides []ListingOverride) error {
	if len(overrides) == 0 {
		return nil
	}
	raw, err := json.Marshal(overrides)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `update sales_order set listing_overrides = listing_overrides || $3::jsonb where tenant_id = $1 and id = $2`,
		tenantID, orderID, string(raw))
	return err
}

// errNoActiveLine is returned when a change would leave an order with no line
// that is not cancelled; cancelling the order is the way to end it.
var errNoActiveLine = errors.New("a change cannot leave the order without a line; cancel the order instead")

// addedSupplementaryNotices is the SLS-0012 warning for each MANDATORY
// supplementary line a change added with its parent.
func addedSupplementaryNotices(o Order) []Notice {
	var out []Notice
	for _, l := range o.Lines {
		if l.added && l.SupplementaryOf != nil {
			out = append(out, Notice{Code: CodeSupplementaryAdded, LineNo: l.LineNo,
				Message: fmt.Sprintf("Mandatory supplementary item %s added (%s %s)", l.ProductID, l.Quantity.String(), l.Unit)})
		}
	}
	return out
}

// followRemovalsTx does what removing lines owes outside the order's own rows,
// in the PATCH transaction, after order.changed (version is the new version).
//
// A removed line 1 of an order released from a sales agreement is the release
// line (ReleaseAgreement creates the order with that one line), so its
// quantity goes back to the agreement as a whole-order cancel would give it
// (at most the release quantity, once per release); on any other order there
// is no release and nothing happens.
//
// On a CONFIRMED order, order.remaindercancelled.v1 is queued for the removed
// lines and the supplementary lines cancelled with them, each with kept_qty 0.
// Fulfillment follows that event and not order.changed: it cuts what it
// released or picked for the lines, closes their drop-ships, and refuses a
// later packing slip for them (FUL-0066). Such lines are cancelled for good (a
// removed line cannot change again and its supplementary lines follow it), so
// the kept quantity never has to rise again.
func followRemovalsTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, before map[int]Line, req ChangeRequest, actor string, version int, now time.Time) error {
	if len(req.RemoveLines) == 0 {
		return nil
	}
	removed := make(map[string]bool, len(req.RemoveLines))
	for _, no := range req.RemoveLines {
		removed[before[no].ID] = true
	}
	if first, ok := before[1]; ok && removed[first.ID] && first.SupplementaryOf == nil {
		qty := first.Quantity
		if err := returnAgreementTx(ctx, tx, tenantID, o, actor, now, &qty); err != nil {
			return err
		}
	}
	if o.Status != domain.OrderConfirmed {
		return nil
	}
	var lines []salesevents.RemainderLine
	for _, l := range o.Lines {
		b, existed := before[l.LineNo]
		if !existed || b.LineStatus == domain.LineCancelled || l.LineStatus != domain.LineCancelled {
			continue
		}
		if !removed[l.ID] && (l.SupplementaryOf == nil || !removed[*l.SupplementaryOf]) {
			continue
		}
		lines = append(lines, salesevents.RemainderLine{LineID: l.ID, LineNo: l.LineNo, ProductID: l.ProductID,
			CancelledQty: b.Quantity.String(), KeptQty: "0", LineStatus: domain.LineCancelled})
	}
	if len(lines) == 0 {
		return nil
	}
	return enqueue(ctx, tx, "order", o.ID, salesevents.OrderRemainderCancelled, tenantID, salesevents.OrderRemainderCancelledData{
		OrderID: o.ID, LegalEntityID: o.LegalEntityID, Version: version, Status: o.Status, Lines: lines, Reason: req.Reason, Actor: actor,
	})
}
