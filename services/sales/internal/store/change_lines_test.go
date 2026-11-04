package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func newLine(qty, price int64) NewLine {
	return NewLine{ProductID: ids.New(), Quantity: d(qty), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: d(price),
		DiscPct: d(0), RequestedShipDate: "2027-03-09"}
}

// TestApplyChange_AddsALineAfterTheHighestNumber: the added line gets number 21
// after lines 10 and 20, is promised at once on a confirmed order, joins the
// total and is named in the diff.
func TestApplyChange_AddsALineAfterTheHighestNumber(t *testing.T) {
	o := changeFixture()
	diff, repromised, err := ApplyChange(o, ChangeRequest{AddLines: []NewLine{newLine(3, 4)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Lines) != 3 || o.Lines[2].LineNo != 21 || !o.Lines[2].LineNet.Equal(d(12)) || o.Lines[2].LineStatus != domain.LineOpen {
		t.Fatalf("lines = %+v", o.Lines)
	}
	if o.Lines[2].ConfirmedShipDate == nil || *o.Lines[2].ConfirmedShipDate != "2027-03-09" {
		t.Fatalf("a line added to a confirmed order is promised: %+v", o.Lines[2])
	}
	if !o.NetTotal.Equal(d(69)) {
		t.Fatalf("net total = %s, want 57 + 12", o.NetTotal)
	}
	if !hasChange(diff, 21, "lineAdded", "", o.Lines[2].ProductID) || !hasChange(diff, 21, "quantity", "", "3") ||
		!hasChange(diff, 21, "confirmedShipDate", "", "2027-03-09") {
		t.Fatalf("diff = %+v", diff)
	}
	if len(repromised) != 1 || repromised[0] != 21 {
		t.Fatalf("repromised = %v", repromised)
	}
}

func TestApplyChange_DraftAddIsNotPromised(t *testing.T) {
	o := changeFixture()
	o.Status = domain.OrderDraft
	o.Lines[0].ConfirmedShipDate, o.Lines[1].ConfirmedShipDate = nil, nil
	_, repromised, err := ApplyChange(o, ChangeRequest{AddLines: []NewLine{newLine(1, 1)}})
	if err != nil || len(repromised) != 0 || o.Lines[2].ConfirmedShipDate != nil {
		t.Fatalf("draft add: repromised=%v line=%+v err=%v", repromised, o.Lines[2], err)
	}
}

// TestApplyChange_RemovesALine: the line is cancelled, not deleted, so its
// number stays; the total drops by its net; its supplementary lines go with it.
func TestApplyChange_RemovesALine(t *testing.T) {
	o := changeFixture()
	diff, _, err := ApplyChange(o, ChangeRequest{RemoveLines: []int{20}})
	if err != nil {
		t.Fatal(err)
	}
	if o.Lines[1].LineStatus != domain.LineCancelled || !hasChange(diff, 20, "lineStatus", "OPEN", "CANCELLED") || !o.NetTotal.Equal(d(50)) {
		t.Fatalf("line 20 = %+v diff %+v total %s", o.Lines[1], diff, o.NetTotal)
	}

	s := supplementaryFixture()
	diff, _, err = ApplyChange(s, ChangeRequest{RemoveLines: []int{1}})
	if err == nil {
		t.Fatalf("removing the only ordered line leaves the supplementary lines cancelled with it and the order empty: diff %+v", diff)
	}
	if !strings.Contains(err.Error(), "without a line") {
		t.Fatalf("err = %v", err)
	}
	s = supplementaryFixture()
	s.Lines = append(s.Lines, Line{ID: "other", LineNo: 4, Quantity: d(1), UnitPrice: d(5), DiscPct: d(0), LineNet: d(5), RequestedShipDate: "2027-03-04", LineStatus: "OPEN"})
	s.NetTotal = s.NetTotal.Add(d(5))
	if _, _, err := ApplyChange(s, ChangeRequest{RemoveLines: []int{1}}); err != nil {
		t.Fatal(err)
	}
	if s.Lines[1].LineStatus != domain.LineCancelled || s.Lines[2].LineStatus != domain.LineCancelled || !s.NetTotal.Equal(d(5)) {
		t.Fatalf("supplementary lines must be cancelled with their parent: %+v total %s", s.Lines, s.NetTotal)
	}
}

func TestApplyChange_RemovalRefusals(t *testing.T) {
	plan := "po-1"
	cases := map[string]struct {
		mutate func(*Order)
		req    ChangeRequest
		want   string
	}{
		"unknown line":   {nil, ChangeRequest{RemoveLines: []int{99}}, "does not exist"},
		"twice":          {nil, ChangeRequest{RemoveLines: []int{10, 10}}, "twice"},
		"changed too":    {nil, ChangeRequest{RemoveLines: []int{10}, Lines: []LineChange{{LineNo: 10, Quantity: dp("2")}}}, "twice"},
		"delivered line": {func(o *Order) { o.Lines[0].LineStatus = "PICKED" }, ChangeRequest{RemoveLines: []int{10}}, "cannot be removed"},
		"planned":        {func(o *Order) { o.Lines[0].ProductionOrderID = &plan }, ChangeRequest{RemoveLines: []int{10}}, "production order"},
		"every line":     {nil, ChangeRequest{RemoveLines: []int{10, 20}}, "without a line"},
		"supplementary": {func(o *Order) {
			parent := o.Lines[0].ID
			o.Lines[1].SupplementaryOf = &parent
		}, ChangeRequest{RemoveLines: []int{20}}, "supplementary"},
		"bad added line": {nil, ChangeRequest{AddLines: []NewLine{{ProductID: "x", Quantity: d(1)}}}, "UUIDs"},
		"zero quantity":  {nil, ChangeRequest{AddLines: []NewLine{newLine(0, 1)}}, "greater than 0"},
	}
	for name, c := range cases {
		o := changeFixture()
		if c.mutate != nil {
			c.mutate(o)
		}
		if _, _, err := ApplyChange(o, c.req); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

// TestPostgres_ChangeAddsAndRemovesLines is FR-27.9 end to end on a confirmed
// order: a line is added (promised, listed for the customer, with its MANDATORY
// supplementary item) and another removed, in one version; the total, the
// history, order.changed and the schedule follow; a denied listing, a removal
// that empties the order and a stale version are refused.
func TestPostgres_ChangeAddsAndRemovesLines(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	order := changeOrderFixture(t, s, ctx, le, customer, 10, 7) // 5 x 10, 5 x 7
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	confirmed, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirm: %v %v", confirmed.Confirmed, err)
	}

	added := newLine(10, 20)
	added.SiteID, added.WarehouseID = order.Lines[0].SiteID, order.Lines[0].WarehouseID
	manual := ids.New()
	if _, err := s.CreateSupplementaryRule(ctx, domain.SupplementaryRule{LegalEntityID: le, ParentProductID: added.ProductID, SupplementaryProduct: manual,
		Unit: "EA", Requirement: domain.SuppMandatory, BaseQuantity: d(5), QuantityPerBase: d(1), Pricing: domain.SuppFree, UnitPrice: d(0)}, "product-admin"); err != nil {
		t.Fatal(err)
	}
	req := ChangeRequest{Reason: "customer added a product", AddLines: []NewLine{added}, RemoveLines: []int{2}}
	changed, err := s.ChangeOrder(ctx, order.ID, confirmed.Order.Version, req, "sales-rep", now)
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if changed.Version != confirmed.Order.Version+1 || len(changed.Lines) != 4 || !changed.NetTotal.Equal(d(250)) {
		t.Fatalf("after change: version=%d lines=%d total=%s", changed.Version, len(changed.Lines), changed.NetTotal)
	}
	byNo := map[int]Line{}
	for _, l := range changed.Lines {
		byNo[l.LineNo] = l
	}
	if byNo[2].LineStatus != domain.LineCancelled || byNo[3].ProductID != added.ProductID || byNo[3].ConfirmedShipDate == nil ||
		*byNo[3].ConfirmedShipDate != "2027-03-09" {
		t.Fatalf("lines = %+v", changed.Lines)
	}
	if sup := byNo[4]; sup.ProductID != manual || sup.SupplementaryOf == nil || *sup.SupplementaryOf != byNo[3].ID || !sup.Quantity.Equal(d(2)) ||
		sup.ConfirmedShipDate == nil || *sup.ConfirmedShipDate != "2027-03-09" {
		t.Fatalf("the MANDATORY supplementary line for the added parent = %+v", byNo[4])
	}
	if len(changed.Warnings) == 0 || changed.Warnings[len(changed.Warnings)-1].Code != CodeSupplementaryAdded {
		t.Fatalf("warnings = %+v", changed.Warnings)
	}
	if n := countWhere(t, pool, `select count(*) from order_schedule_line where order_id = $1 and status = 'CONFIRMED'`, order.ID); n != 3 {
		t.Fatalf("schedule lines for lines 1, 3 and 4 expected, got %d", n)
	}
	var data salesevents.OrderChangedData
	if err := json.Unmarshal(outboxData(t, pool, order.ID, salesevents.OrderChanged), &data); err != nil {
		t.Fatal(err)
	}
	if data.Version != changed.Version || len(data.RepromisedLines) != 2 {
		t.Fatalf("order.changed = %+v", data)
	}
	// Fulfillment follows order.remaindercancelled, not order.changed: the
	// removed line goes out with nothing kept, so a released shipment is cut.
	var cut salesevents.OrderRemainderCancelledData
	if err := json.Unmarshal(outboxData(t, pool, order.ID, salesevents.OrderRemainderCancelled), &cut); err != nil {
		t.Fatal(err)
	}
	if cut.Version != changed.Version || cut.Status != domain.OrderConfirmed || cut.LegalEntityID != le || len(cut.Lines) != 1 ||
		cut.Lines[0].LineID != byNo[2].ID || cut.Lines[0].KeptQty != "0" || cut.Lines[0].CancelledQty != "5" || cut.Lines[0].LineStatus != domain.LineCancelled {
		t.Fatalf("order.remaindercancelled = %+v", cut)
	}
	history, err := s.ListOrderVersions(ctx, order.ID)
	if err != nil || len(history) != 1 || !hasChange(history[0].Diff, 2, "lineStatus", "OPEN", "CANCELLED") ||
		!hasChange(history[0].Diff, 3, "lineAdded", "", added.ProductID) {
		t.Fatalf("history = %+v err=%v", history, err)
	}

	// The removed line's number is not reused by the next addition.
	next, err := s.ChangeOrder(ctx, order.ID, changed.Version, ChangeRequest{AddLines: []NewLine{newLine(1, 1)}}, "sales-rep", now)
	if err != nil || next.Lines[len(next.Lines)-1].LineNo != 5 {
		t.Fatalf("next addition: %+v err=%v", next.Lines, err)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, confirmed.Order.Version, req, "sales-rep", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, next.Version, ChangeRequest{RemoveLines: []int{1, 3, 5}}, "sales-rep", now); problemCode(t, err) != CodeInvalidChange {
		t.Fatalf("removing every ordered line: %v", err)
	}
	left, err := s.ChangeOrder(ctx, order.ID, next.Version, ChangeRequest{RemoveLines: []int{1, 3}}, "sales-rep", now)
	if err != nil || left.Lines[3].LineStatus != domain.LineCancelled || !left.NetTotal.Equal(d(1)) {
		t.Fatalf("removing the parent cancels its supplementary line: %+v total=%s err=%v", left.Lines, left.NetTotal, err)
	}
	// The second removal names lines 1 and 3 and the supplementary line 4
	// cancelled with its parent; an addition alone queues no such event.
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2`, order.ID, salesevents.OrderRemainderCancelled); n != 2 {
		t.Fatalf("order.remaindercancelled events = %d, want 2", n)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2 and (payload->'data'->>'version')::int = $3
		and jsonb_array_length(payload->'data'->'lines') = 3`, order.ID, salesevents.OrderRemainderCancelled, left.Version); n != 1 {
		t.Fatalf("the second removal must name lines 1, 3 and 4")
	}
}

// TestPostgres_ChangeAddedLineListingAndLastLine: an added line is checked
// against the product listing (BR-27.13); the approve duty with a reason
// accepts it and logs the override on the order; removing every line is refused.
func TestPostgres_ChangeAddedLineListingAndLastLine(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	order := changeOrderFixture(t, s, ctx, le, customer, 10)
	banned := newLine(1, 5)
	if _, err := s.CreateListing(ctx, ProductListing{LegalEntityID: le, ScopeType: domain.ScopeCustomer, ScopeKey: customer, ProductID: banned.ProductID,
		ListingType: domain.ListingDeny, CreatedBy: "listing-admin"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{AddLines: []NewLine{banned}}, "sales-rep", now); problemCode(t, err) != CodeProductNotListed {
		t.Fatalf("denied listing: %v", err)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{AddLines: []NewLine{banned}, ListingOverrideReason: "key account"}, "sales-rep", now); problemCode(t, err) != CodeProductNotListed {
		t.Fatalf("a reason without the approve duty: %v", err)
	}
	changed, err := s.ChangeOrder(ctx, order.ID, order.Version,
		ChangeRequest{AddLines: []NewLine{banned}, ListingOverrideReason: "key account", Approver: true}, "approver", now)
	if err != nil || len(changed.Lines) != 2 || len(changed.ListingOverrides) != 1 || changed.ListingOverrides[0].LineNo != 2 ||
		changed.ListingOverrides[0].Reason != "key account" {
		t.Fatalf("override: %+v err=%v", changed.ListingOverrides, err)
	}
	if changed.Status != domain.OrderDraft || changed.Lines[1].ConfirmedShipDate != nil {
		t.Fatalf("a draft order is not promised: %s %+v", changed.Status, changed.Lines[1])
	}
	if _, err := s.ChangeOrder(ctx, order.ID, changed.Version, ChangeRequest{RemoveLines: []int{1, 2}}, "sales-rep", now); problemCode(t, err) != CodeInvalidChange {
		t.Fatalf("removing every line: %v", err)
	}
	cut, err := s.ChangeOrder(ctx, order.ID, changed.Version, ChangeRequest{RemoveLines: []int{1}}, "sales-rep", now)
	if err != nil || !cut.NetTotal.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("remove line 1: total=%s err=%v", cut.NetTotal, err)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2`, order.ID, salesevents.OrderRemainderCancelled); n != 0 {
		t.Fatalf("a draft order has nothing released: %d order.remaindercancelled events", n)
	}
}

// TestPostgres_RemovedReleaseLineReturnsAgreementQuantity: line 1 of an order
// released from a sales agreement is the release line; once another line is
// added it can be removed, and its quantity goes back to the agreement as a
// whole-order cancel would give it, once. Removing the added line later
// returns nothing more.
func TestPostgres_RemovedReleaseLineReturnsAgreementQuantity(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := agreementFixture(t, s, ctx, le, 1000, nil, nil)
	res, _, err := s.ReleaseAgreement(ctx, a.ID, release(600, "r1"), now)
	if err != nil || !res.Agreement.Remaining.Equal(d(400)) {
		t.Fatalf("release: %+v %v", res.Agreement, err)
	}
	order := res.Order
	extra := newLine(2, 3)
	added, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{AddLines: []NewLine{extra}}, "key-account", now)
	if err != nil || len(added.Lines) != 2 {
		t.Fatalf("add: %+v %v", added.Lines, err)
	}
	if left, err := s.GetAgreement(ctx, a.ID); err != nil || !left.Remaining.Equal(d(400)) {
		t.Fatalf("an addition draws nothing from the agreement: %+v %v", left, err)
	}
	removed, err := s.ChangeOrder(ctx, order.ID, added.Version, ChangeRequest{AddLines: []NewLine{newLine(1, 1)}, RemoveLines: []int{1}}, "key-account", now)
	if err != nil || removed.Lines[0].LineStatus != domain.LineCancelled {
		t.Fatalf("remove the release line: %+v %v", removed.Lines, err)
	}
	if back, err := s.GetAgreement(ctx, a.ID); err != nil || !back.Remaining.Equal(d(1000)) {
		t.Fatalf("the release quantity goes back: %+v %v", back, err)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2`, a.ID, salesevents.AgreementReturned); n != 1 {
		t.Fatalf("agreement.returned events = %d", n)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, removed.Version, ChangeRequest{RemoveLines: []int{2}}, "key-account", now); err != nil {
		t.Fatal(err)
	}
	if again, err := s.GetAgreement(ctx, a.ID); err != nil || !again.Remaining.Equal(d(1000)) {
		t.Fatalf("an added line gives nothing back: %+v %v", again, err)
	}
}

// TestPostgres_ThresholdCountsAddedAndRemovedLines: under a 25 USD threshold
// (slice 27) an added line counts its whole net toward the order's cumulative
// unapproved value, and so does a removed line: 2 x 5 = 10 applies at once,
// then removing line 2 (35) brings the total moved to 45 and waits.
func TestPostgres_ThresholdCountsAddedAndRemovedLines(t *testing.T) {
	s, pool, ctx := migrated(t)
	_, order, now := thresholdFixture(t, s, ctx, nil)
	small, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{AddLines: []NewLine{newLine(2, 5)}}, "sales-rep", now)
	if err != nil || small.PendingChange != nil || !small.NetTotal.Equal(d(95)) || !unapprovedValue(t, pool, order.ID).Equal(d(10)) {
		t.Fatalf("a 10 USD addition applies and counts 10: %+v total=%s err=%v", small.PendingChange, small.NetTotal, err)
	}
	waits, err := s.ChangeOrder(ctx, order.ID, small.Version, ChangeRequest{RemoveLines: []int{2}}, "sales-rep", now)
	if err != nil || waits.PendingChange == nil || waits.Version != small.Version || !unapprovedValue(t, pool, order.ID).Equal(d(10)) {
		t.Fatalf("removing 35 on top of 10 waits: %+v err=%v", waits.PendingChange, err)
	}
}

// TestPostgres_ApprovedChangeAcceptsListingOverride: a user with
// sales.order.approve outside the customer's named group adds a product the
// listing denies with a reason; the change is stored for approval, and the
// named approver's approval applies it with the override logged (the replay
// must not refuse what the request was accepted with).
func TestPostgres_ApprovedChangeAcceptsListingOverride(t *testing.T) {
	s, _, ctx := migrated(t)
	_, order, now := thresholdFixture(t, s, ctx, []string{approverA})
	banned := newLine(10, 5)
	banned.SiteID, banned.WarehouseID = order.Lines[0].SiteID, order.Lines[0].WarehouseID
	if _, err := s.CreateListing(ctx, ProductListing{LegalEntityID: order.LegalEntityID, ScopeType: domain.ScopeCustomer, ScopeKey: order.CustomerID,
		ProductID: banned.ProductID, ListingType: domain.ListingDeny, CreatedBy: "listing-admin"}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{AddLines: []NewLine{banned}, ListingOverrideReason: "key account", Approver: true}, outsider, now)
	if err != nil || pending.PendingChange == nil {
		t.Fatalf("stored for approval: %+v %v", pending.PendingChange, err)
	}
	res, err := s.ApproveChangeRequest(ctx, pending.PendingChange.ID, pending.PendingChange.Version, "ok", approverA, now)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if len(res.Order.Lines) != 3 || len(res.Order.ListingOverrides) != 1 || res.Order.ListingOverrides[0].LineNo != 3 || res.Order.ListingOverrides[0].Reason != "key account" {
		t.Fatalf("after approval: lines=%d overrides=%+v", len(res.Order.Lines), res.Order.ListingOverrides)
	}
}

// TestPostgres_ChangeAddLineRaisesCreditExposure: adding a line to a confirmed
// order raises the total, so the credit check runs again and a failure places
// a CREDIT hold (FR-27.9, BR-27.4).
func TestPostgres_ChangeAddLineRaisesCreditExposure(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(5), ToleranceAmount: d(1000), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits, Exposure: domain.Exposure{OpenAR: d(8500)}}, 0); err != nil {
		t.Fatal(err)
	}
	order := changeOrderFixture(t, s, ctx, le, customer, 200)
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	confirmed, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirm: %v %v", confirmed.Confirmed, err)
	}
	raised, err := s.ChangeOrder(ctx, order.ID, confirmed.Order.Version, ChangeRequest{AddLines: []NewLine{newLine(10, 200)}}, "sales-rep", now)
	if err != nil || len(raised.Holds) != 1 || raised.Holds[0].HoldType != domain.HoldCredit || !raised.Holds[0].OrderAmount.Equal(d(3000)) {
		t.Fatalf("holds = %+v err=%v", raised.Holds, err)
	}
}

// TestPostgres_AddedAndRemovedLinesGoThroughApproval: for a customer whose
// changes need approval, a PATCH that adds and removes lines is stored as a
// request with the lines in it; the order stays as it is until a manager
// approves, which applies both (FR-27.9).
func TestPostgres_AddedAndRemovedLinesGoThroughApproval(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	if _, err := s.PutChangePolicy(ctx, ChangePolicy{LegalEntityID: le, CustomerID: customer, ApprovalRequired: true}, 0, "manager"); err != nil {
		t.Fatal(err)
	}
	order := changeOrderFixture(t, s, ctx, le, customer, 10, 7)
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	conf, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !conf.Confirmed {
		t.Fatalf("confirm: %v %v", conf.Confirmed, err)
	}
	added := newLine(2, 50)
	pending, err := s.ChangeOrder(ctx, order.ID, conf.Order.Version, ChangeRequest{Reason: "add and drop", AddLines: []NewLine{added}, RemoveLines: []int{2}}, "sales-rep", now)
	if err != nil || pending.PendingChange == nil || len(pending.Lines) != 2 {
		t.Fatalf("stored, not applied: %+v err=%v", pending.PendingChange, err)
	}
	req := pending.PendingChange
	if len(req.Change.AddLines) != 1 || req.Change.AddLines[0].ProductID != added.ProductID || len(req.Change.RemoveLines) != 1 || !hasChange(req.Diff, 3, "lineAdded", "", added.ProductID) {
		t.Fatalf("stored change = %+v diff %+v", req.Change, req.Diff)
	}
	res, err := s.ApproveChangeRequest(ctx, req.ID, req.Version, "ok", "manager", now)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if len(res.Order.Lines) != 3 || res.Order.Lines[1].LineStatus != domain.LineCancelled || res.Order.Lines[2].ProductID != added.ProductID ||
		!res.Order.NetTotal.Equal(d(150)) {
		t.Fatalf("after approval: %+v total %s", res.Order.Lines, res.Order.NetTotal)
	}
}
