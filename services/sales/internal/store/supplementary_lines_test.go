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

// supplementaryFixture is a confirmed AC-27.10 order: line 1 is 25 FG-100,
// line 2 the 2 free MANUAL-EN its "1 per 10" rule added, line 3 a priced
// CABLE line from a "2 per 5" rule at 3.25.
func supplementaryFixture() *Order {
	parent := "parent-line"
	one := 1
	return &Order{
		Status: domain.OrderConfirmed, Version: 2, RequestedShipDate: "2027-03-04", NetTotal: decimal.RequireFromString("282.5"),
		Lines: []Line{
			{ID: parent, LineNo: 1, Quantity: d(25), UnitPrice: d(10), DiscPct: d(0), LineNet: d(250), RequestedShipDate: "2027-03-04",
				ConfirmedShipDate: sp("2027-03-04"), LineStatus: "OPEN"},
			{ID: "manual", LineNo: 2, Quantity: d(2), UnitPrice: d(0), DiscPct: d(0), LineNet: d(0), RequestedShipDate: "2027-03-04",
				ConfirmedShipDate: sp("2027-03-04"), LineStatus: "OPEN", SupplementaryOf: &parent, ParentLineNo: &one, suppBase: d(10), suppPerBase: d(1)},
			{ID: "cable", LineNo: 3, Quantity: d(10), UnitPrice: decimal.RequireFromString("3.25"), DiscPct: d(0), LineNet: decimal.RequireFromString("32.5"),
				RequestedShipDate: "2027-03-04", ConfirmedShipDate: sp("2027-03-04"), LineStatus: "OPEN", SupplementaryOf: &parent, ParentLineNo: &one,
				suppBase: d(5), suppPerBase: d(2)},
		},
	}
}

func hasChange(diff []domain.FieldChange, lineNo int, field, oldV, newV string) bool {
	for _, c := range diff {
		if c.LineNo == lineNo && c.Field == field && c.Old == oldV && c.New == newV {
			return true
		}
	}
	return false
}

// TestApplyChange_SupplementaryFollowsParentQuantity covers BR-27.14: the
// parent going from 25 to 12 recomputes the free line to 1 and the priced
// line to 4 (net 13), re-promises both with the parent, and the total moves.
func TestApplyChange_SupplementaryFollowsParentQuantity(t *testing.T) {
	o := supplementaryFixture()
	diff, repromised, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("12")}}})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Lines[1].Quantity.Equal(d(1)) || !o.Lines[2].Quantity.Equal(d(4)) || !o.Lines[2].LineNet.Equal(d(13)) {
		t.Fatalf("supplementary lines = %+v / %+v", o.Lines[1], o.Lines[2])
	}
	if !hasChange(diff, 2, "quantity", "2", "1") || !hasChange(diff, 3, "quantity", "10", "4") {
		t.Fatalf("diff must record the follow-on quantities: %+v", diff)
	}
	if len(repromised) != 3 || repromised[0] != 1 || repromised[1] != 2 || repromised[2] != 3 {
		t.Fatalf("parent and both supplementary lines are re-promised together, got %v", repromised)
	}
	if !o.NetTotal.Equal(d(133)) {
		t.Fatalf("net total %s, want 120 + 0 + 13 = 133", o.NetTotal)
	}
}

// TestApplyChange_SupplementaryRemovedBelowBase covers BR-27.14 removal: at 9
// units the parent is below the free rule's base of 10, so that line is
// cancelled and leaves the total; the "2 per 5" line stays at 2.
func TestApplyChange_SupplementaryRemovedBelowBase(t *testing.T) {
	o := supplementaryFixture()
	diff, _, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("9")}}})
	if err != nil {
		t.Fatal(err)
	}
	if o.Lines[1].LineStatus != domain.LineCancelled || !hasChange(diff, 2, "lineStatus", "OPEN", "CANCELLED") {
		t.Fatalf("free line must be cancelled: %+v diff %+v", o.Lines[1], diff)
	}
	if o.Lines[2].LineStatus != "OPEN" || !o.Lines[2].Quantity.Equal(d(2)) {
		t.Fatalf("cable line = %+v", o.Lines[2])
	}
	if !o.NetTotal.Equal(decimal.RequireFromString("96.5")) {
		t.Fatalf("net total %s, want 90 + 6.5", o.NetTotal)
	}
}

// TestApplyChange_SupplementaryReopensAboveBase: a free line cancelled because
// the parent fell below the base quantity reopens, recomputed and re-dated,
// when the parent reaches the base again (FR-27.14); while the parent stays
// below the base it stays cancelled.
func TestApplyChange_SupplementaryReopensAboveBase(t *testing.T) {
	o := supplementaryFixture()
	if _, _, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("9")}}}); err != nil {
		t.Fatal(err)
	}
	diff, _, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("8")}}})
	if err != nil || o.Lines[1].LineStatus != domain.LineCancelled || hasChange(diff, 2, "lineStatus", "CANCELLED", "OPEN") {
		t.Fatalf("below the base the line stays cancelled: %v %+v diff %+v", err, o.Lines[1], diff)
	}
	diff, repromised, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("30"), RequestedShipDate: sp("2027-03-12")}}})
	if err != nil {
		t.Fatal(err)
	}
	free := o.Lines[1]
	if free.LineStatus != domain.LineOpen || !free.Quantity.Equal(d(3)) || free.RequestedShipDate != "2027-03-12" ||
		free.ConfirmedShipDate == nil || *free.ConfirmedShipDate != "2027-03-12" {
		t.Fatalf("free line must reopen at 3 on the parent's dates: %+v", free)
	}
	if !hasChange(diff, 2, "lineStatus", "CANCELLED", "OPEN") || !hasChange(diff, 2, "quantity", "2", "3") {
		t.Fatalf("diff must record the reopening: %+v", diff)
	}
	if !o.Lines[2].Quantity.Equal(d(12)) || len(repromised) != 3 {
		t.Fatalf("cable line %+v, repromised %v", o.Lines[2], repromised)
	}
	if !o.NetTotal.Equal(decimal.RequireFromString("339")) {
		t.Fatalf("net total %s, want 300 + 0 + 39", o.NetTotal)
	}
}

// TestApplyChange_SupplementaryOfCancelledParentStaysCancelled: lines
// cancelled with their parent never reopen.
func TestApplyChange_SupplementaryOfCancelledParentStaysCancelled(t *testing.T) {
	o := supplementaryFixture()
	o.Lines[0].LineStatus = domain.LineCancelled
	o.Lines[1].LineStatus = domain.LineCancelled
	o.Lines[0].Quantity = d(30)
	diff, _, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 3, UnitPrice: dp("4")}}})
	if err != nil {
		t.Fatal(err)
	}
	if o.Lines[1].LineStatus != domain.LineCancelled || hasChange(diff, 2, "lineStatus", "CANCELLED", "OPEN") {
		t.Fatalf("a line of a cancelled parent must stay cancelled: %+v diff %+v", o.Lines[1], diff)
	}
}

// TestApplyChange_SupplementaryFollowsParentDates covers BR-27.14
// inheritance: a new requested date on the parent of a confirmed order moves
// the supplementary lines' requested and confirmed dates with it.
func TestApplyChange_SupplementaryFollowsParentDates(t *testing.T) {
	o := supplementaryFixture()
	diff, repromised, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 1, RequestedShipDate: sp("2027-03-10")}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range o.Lines[1:] {
		if l.RequestedShipDate != "2027-03-10" || l.ConfirmedShipDate == nil || *l.ConfirmedShipDate != "2027-03-10" {
			t.Fatalf("line %d dates %s / %v", l.LineNo, l.RequestedShipDate, l.ConfirmedShipDate)
		}
	}
	if !hasChange(diff, 2, "confirmedShipDate", "2027-03-04", "2027-03-10") || len(repromised) != 3 {
		t.Fatalf("diff %+v repromised %v", diff, repromised)
	}
}

// TestApplyChange_SupplementaryCannotChangeAlone covers BR-27.14: a
// supplementary line's own quantity or date is refused, naming the parent;
// its price may still be changed, and repeating its current quantity is fine.
func TestApplyChange_SupplementaryCannotChangeAlone(t *testing.T) {
	for name, c := range map[string]LineChange{
		"quantity": {LineNo: 2, Quantity: dp("5")},
		"date":     {LineNo: 3, RequestedShipDate: sp("2027-04-01")},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ApplyChange(supplementaryFixture(), ChangeRequest{Lines: []LineChange{c}})
			if err == nil || !strings.Contains(err.Error(), "supplementary line of line 1") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	o := supplementaryFixture()
	diff, _, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 3, Quantity: dp("10"), UnitPrice: dp("3")}}})
	if err != nil || !o.Lines[2].LineNet.Equal(d(30)) || !hasChange(diff, 3, "unitPrice", "3.25", "3") {
		t.Fatalf("price change on a supplementary line: %v %+v %+v", err, o.Lines[2], diff)
	}
}

// TestPostgres_MandatorySupplementaryLines covers AC-27.10 end to end: a
// MANDATORY "1 x MANUAL-EN free per 10 x FG-100" rule and a 25-unit FG-100
// line give a zero-priced line of 2 MANUAL-EN that names its parent, inherits
// its site, warehouse and date, and is reported as SLS-0012; an OPTIONAL rule
// and a rule for another customer add nothing. Lowering the parent below 10
// cancels the line in the same version, raising it to 20 reopens it, and
// cancelling the order cancels the supplementary lines with their parent.
func TestPostgres_MandatorySupplementaryLines(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer, fg100, manual, poster, bag, cable := ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	other := ids.New()
	rules := []domain.SupplementaryRule{
		{LegalEntityID: le, ParentProductID: fg100, SupplementaryProduct: manual, Unit: "EA", Requirement: domain.SuppMandatory,
			BaseQuantity: d(10), QuantityPerBase: d(1), Pricing: domain.SuppFree, UnitPrice: d(0)},
		{LegalEntityID: le, ParentProductID: fg100, SupplementaryProduct: poster, Unit: "EA", Requirement: domain.SuppOptional,
			BaseQuantity: d(1), QuantityPerBase: d(1), Pricing: domain.SuppPriced, UnitPrice: d(2)},
		{LegalEntityID: le, ParentProductID: fg100, CustomerID: &other, SupplementaryProduct: bag, Unit: "EA", Requirement: domain.SuppMandatory,
			BaseQuantity: d(1), QuantityPerBase: d(1), Pricing: domain.SuppFree, UnitPrice: d(0)},
		{LegalEntityID: le, ParentProductID: fg100, SupplementaryProduct: cable, Unit: "M", Requirement: domain.SuppMandatory,
			BaseQuantity: d(5), QuantityPerBase: d(2), Pricing: domain.SuppPriced, UnitPrice: decimal.RequireFromString("3.25")},
	}
	for _, r := range rules {
		if _, err := s.CreateSupplementaryRule(ctx, r, "product-admin"); err != nil {
			t.Fatal(err)
		}
	}
	in := oneLineOrder(le, customer, fg100, time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC))
	in.Lines[0].Quantity, in.Lines[0].LineNet = d(25), d(250)
	key := "supp-1"
	in.IdempotencyKey = &key
	order, created, err := s.CreateOrder(ctx, in)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if len(order.Lines) != 3 {
		t.Fatalf("want the parent, MANUAL-EN and CABLE lines, got %+v", order.Lines)
	}
	parent, free, priced := order.Lines[0], order.Lines[1], order.Lines[2]
	if free.ProductID != manual || free.LineNo != 2 || !free.Quantity.Equal(d(2)) || !free.UnitPrice.IsZero() || !free.LineNet.IsZero() || free.Unit != "EA" {
		t.Fatalf("free line = %+v", free)
	}
	if free.SupplementaryOf == nil || *free.SupplementaryOf != parent.ID || free.ParentLineNo == nil || *free.ParentLineNo != 1 || free.SupplementaryRuleID == nil {
		t.Fatalf("free line must reference line 1 (%s): %+v", parent.ID, free)
	}
	if free.SiteID != parent.SiteID || free.WarehouseID != parent.WarehouseID || free.RequestedShipDate != parent.RequestedShipDate {
		t.Fatalf("free line must inherit site, warehouse and date: %+v vs %+v", free, parent)
	}
	if priced.ProductID != cable || !priced.Quantity.Equal(d(10)) || priced.Unit != "M" || !priced.LineNet.Equal(decimal.RequireFromString("32.5")) {
		t.Fatalf("priced line = %+v", priced)
	}
	if !order.NetTotal.Equal(decimal.RequireFromString("282.5")) {
		t.Fatalf("net total %s, want 250 + 0 + 32.5", order.NetTotal)
	}
	if len(order.Warnings) != 2 || order.Warnings[0].Code != CodeSupplementaryAdded || order.Warnings[0].LineNo != 2 ||
		!strings.Contains(order.Warnings[0].Message, manual) || !strings.Contains(order.Warnings[0].Message, "(2 EA)") {
		t.Fatalf("warnings = %+v", order.Warnings)
	}
	replay, created, err := s.CreateOrder(ctx, in)
	if err != nil || created || len(replay.Lines) != 3 || len(replay.Warnings) != 2 {
		t.Fatalf("replay returns the stored order and its warnings: created=%v err=%v %+v", created, err, replay.Warnings)
	}
	read, err := s.GetOrder(ctx, order.ID)
	if err != nil || read.Warnings != nil || read.Lines[1].ParentLineNo == nil || *read.Lines[1].ParentLineNo != 1 {
		t.Fatalf("read: %v warnings=%+v line=%+v", err, read.Warnings, read.Lines[1])
	}

	if _, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 2, Quantity: dp("5")}}}, "sales-rep", time.Now()); problemCode(t, err) != CodeInvalidChange {
		t.Fatalf("a supplementary line's own quantity must be refused: %v", err)
	}
	changed, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{Reason: "customer cut", Lines: []LineChange{{LineNo: 1, Quantity: dp("9")}}}, "sales-rep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if changed.Lines[1].LineStatus != domain.LineCancelled || !changed.Lines[2].Quantity.Equal(d(2)) || !changed.NetTotal.Equal(decimal.RequireFromString("96.5")) {
		t.Fatalf("after the cut: %+v total %s", changed.Lines, changed.NetTotal)
	}
	var data salesevents.OrderChangedData
	if err := json.Unmarshal(outboxData(t, pool, order.ID, salesevents.OrderChanged), &data); err != nil {
		t.Fatal(err)
	}
	if !hasChange(diffOf(data), 2, "lineStatus", "OPEN", "CANCELLED") || !hasChange(diffOf(data), 3, "quantity", "10", "2") {
		t.Fatalf("order.changed must carry the follow-on changes: %+v", data.Diff)
	}

	reopened, err := s.ChangeOrder(ctx, order.ID, changed.Version, ChangeRequest{Reason: "customer back", Lines: []LineChange{{LineNo: 1, Quantity: dp("20")}}}, "sales-rep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Lines[1].LineStatus != domain.LineOpen || !reopened.Lines[1].Quantity.Equal(d(2)) || !reopened.NetTotal.Equal(decimal.RequireFromString("226")) {
		t.Fatalf("raising the parent to 20 reopens the free line at 2: %+v total %s", reopened.Lines, reopened.NetTotal)
	}
	history, err := s.ListOrderVersions(ctx, order.ID)
	if err != nil || len(history) == 0 || !hasChange(history[0].Diff, 2, "lineStatus", "CANCELLED", "OPEN") {
		t.Fatalf("the reopening is in the version diff: %v %+v", err, history)
	}

	cancelled, err := s.CancelOrder(ctx, order.ID, reopened.Version, "customer withdrew", "sales-rep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range cancelled.Lines {
		if l.LineStatus != domain.LineCancelled {
			t.Fatalf("AC-27.10: line %d must be cancelled with its parent: %+v", l.LineNo, l)
		}
	}
}

// TestPostgres_SupplementaryOnConversionAndRelease checks that orders created
// by an agreement release and a quotation conversion get MANDATORY
// supplementary lines too, since both commit order lines (FR-27.14).
func TestPostgres_SupplementaryOnConversionAndRelease(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	a := agreementFixture(t, s, ctx, le, 100, nil, nil)
	if _, err := s.CreateSupplementaryRule(ctx, domain.SupplementaryRule{LegalEntityID: le, ParentProductID: a.ProductID, SupplementaryProduct: ids.New(),
		Unit: "EA", Requirement: domain.SuppMandatory, BaseQuantity: d(10), QuantityPerBase: d(1), Pricing: domain.SuppFree, UnitPrice: d(0)}, "product-admin"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 3, 2, 9, 0, 0, 0, time.UTC)
	res, _, err := s.ReleaseAgreement(ctx, a.ID, release(30, "rel-supp"), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Order.Lines) != 2 || !res.Order.Lines[1].Quantity.Equal(d(3)) || res.Order.Lines[1].ParentLineNo == nil || len(res.Order.Warnings) != 1 {
		t.Fatalf("release order lines %+v warnings %+v", res.Order.Lines, res.Order.Warnings)
	}
	replay, created, err := s.ReleaseAgreement(ctx, a.ID, release(30, "rel-supp"), now)
	if err != nil || created || replay.Order.ID != res.Order.ID || len(replay.Order.Warnings) != 1 {
		t.Fatalf("a replayed release returns the same order and its warnings: created=%v err=%v %+v", created, err, replay.Order.Warnings)
	}

	qin := threeLineQuotation(le, ids.New(), "quote-supp")
	qin.Lines[2].Quantity, qin.Lines[2].LineNet = d(20), d(200)
	if _, err := s.CreateSupplementaryRule(ctx, domain.SupplementaryRule{LegalEntityID: le, ParentProductID: qin.Lines[2].ProductID, SupplementaryProduct: ids.New(),
		Unit: "EA", Requirement: domain.SuppMandatory, BaseQuantity: d(10), QuantityPerBase: d(1), Pricing: domain.SuppFree, UnitPrice: d(0)}, "product-admin"); err != nil {
		t.Fatal(err)
	}
	q, _, err := s.CreateQuotation(ctx, qin)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := s.SendQuotation(ctx, q.ID, q.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: sent.Version, LineNos: []int{1, 3}, Actor: "sales-rep"}, now)
	if err != nil {
		t.Fatal(err)
	}
	lines := conv.Order.Lines
	if len(lines) != 3 || !lines[2].Quantity.Equal(d(2)) || lines[2].ParentLineNo == nil || *lines[2].ParentLineNo != lines[1].LineNo ||
		lines[2].RequestedShipDate != lines[1].RequestedShipDate {
		t.Fatalf("converted order lines %+v", lines)
	}
}

func diffOf(data salesevents.OrderChangedData) []domain.FieldChange {
	out := make([]domain.FieldChange, 0, len(data.Diff))
	for _, c := range data.Diff {
		out = append(out, domain.FieldChange{LineNo: c.LineNo, Field: c.Field, Old: c.Old, New: c.New})
	}
	return out
}
