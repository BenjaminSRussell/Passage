package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

func dp(s string) *decimal.Decimal {
	v := decimal.RequireFromString(s)
	return &v
}

func sp(s string) *string { return &s }

// changeFixture is the AC-27.7 order: line 10 is 5 x 10.00, line 20 is 1 x 7.00.
func changeFixture() *Order {
	return &Order{
		Status:            domain.OrderConfirmed,
		Version:           1,
		RequestedShipDate: "2027-03-04",
		NetTotal:          d(57),
		Lines: []Line{
			{LineNo: 10, Quantity: d(5), UnitPrice: d(10), DiscPct: d(0), LineNet: d(50), RequestedShipDate: "2027-03-04", ConfirmedShipDate: sp("2027-03-06"), LineStatus: "OPEN"},
			{LineNo: 20, Quantity: d(1), UnitPrice: d(7), DiscPct: d(0), LineNet: d(7), RequestedShipDate: "2027-03-04", ConfirmedShipDate: sp("2027-03-06"), LineStatus: "OPEN"},
		},
	}
}

// TestApplyChange_RepricesOnlyTheChangedLine covers AC-27.7: line 10 moves from
// 5 to 8, its net and the order total move with it, and only that line appears
// in the diff.
func TestApplyChange_RepricesOnlyTheChangedLine(t *testing.T) {
	o := changeFixture()
	diff, repromised, err := ApplyChange(o, ChangeRequest{Reason: "customer call", Lines: []LineChange{{LineNo: 10, Quantity: dp("8")}}})
	if err != nil {
		t.Fatal(err)
	}
	// The order is CONFIRMED, so the quantity change re-promises line 10: its
	// confirmed date (03-06, set at confirm) follows the requested date (03-04).
	want := []domain.FieldChange{
		{LineNo: 10, Field: "quantity", Old: "5", New: "8"},
		{LineNo: 10, Field: "confirmedShipDate", Old: "2027-03-06", New: "2027-03-04"},
	}
	if len(diff) != len(want) || diff[0] != want[0] || diff[1] != want[1] {
		t.Fatalf("diff = %+v, want %+v", diff, want)
	}
	if len(repromised) != 1 || repromised[0] != 10 {
		t.Fatalf("repromised = %v, want only line 10", repromised)
	}
	if *o.Lines[1].ConfirmedShipDate != "2027-03-06" {
		t.Fatalf("line 20 must keep its promise, got %s", *o.Lines[1].ConfirmedShipDate)
	}
	if !o.Lines[0].LineNet.Equal(d(80)) || o.Lines[1].LineNet.Cmp(d(7)) != 0 {
		t.Fatalf("line nets = %s, %s", o.Lines[0].LineNet, o.Lines[1].LineNet)
	}
	if !o.NetTotal.Equal(d(87)) {
		t.Fatalf("net total = %s, want 87", o.NetTotal)
	}
}

func TestApplyChange_NoOpLeavesOrderUntouched(t *testing.T) {
	o := changeFixture()
	diff, _, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 10, Quantity: dp("5")}}})
	if err != nil || len(diff) != 0 {
		t.Fatalf("no-op: diff=%+v err=%v", diff, err)
	}
	if !o.NetTotal.Equal(d(57)) {
		t.Fatalf("net total moved on a no-op: %s", o.NetTotal)
	}
}

func TestApplyChange_HeaderDate(t *testing.T) {
	o := changeFixture()
	diff, _, err := ApplyChange(o, ChangeRequest{RequestedShipDate: sp("2027-03-09")})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 1 || diff[0].LineNo != 0 || diff[0].Field != "requestedShipDate" || diff[0].New != "2027-03-09" {
		t.Fatalf("header diff = %+v", diff)
	}
}

func TestApplyChange_Refusals(t *testing.T) {
	cases := []struct {
		name string
		req  ChangeRequest
		want string
	}{
		{"unknown line", ChangeRequest{Lines: []LineChange{{LineNo: 99, Quantity: dp("1")}}}, "does not exist"},
		{"line twice", ChangeRequest{Lines: []LineChange{{LineNo: 10, Quantity: dp("1")}, {LineNo: 10, Quantity: dp("2")}}}, "changed twice"},
		{"zero quantity", ChangeRequest{Lines: []LineChange{{LineNo: 10, Quantity: dp("0")}}}, "greater than 0"},
		{"seven decimal places", ChangeRequest{Lines: []LineChange{{LineNo: 10, Quantity: dp("1.0000001")}}}, "6 decimal places"},
		{"negative price", ChangeRequest{Lines: []LineChange{{LineNo: 10, UnitPrice: dp("-1")}}}, "out of range"},
		{"discount above 100", ChangeRequest{Lines: []LineChange{{LineNo: 10, DiscPct: dp("101")}}}, "out of range"},
		{"bad line date", ChangeRequest{Lines: []LineChange{{LineNo: 10, RequestedShipDate: sp("03/04/2027")}}}, "yyyy-mm-dd"},
		{"bad header date", ChangeRequest{RequestedShipDate: sp("tomorrow")}, "yyyy-mm-dd"},
		{"reason too long", ChangeRequest{Reason: strings.Repeat("x", changeReasonMaxSize+1)}, "too long"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := changeFixture()
			_, _, err := ApplyChange(o, c.req)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want containing %q", err, c.want)
			}
		})
	}
}

func TestApplyChange_OnlyOpenOrReservedLinesChange(t *testing.T) {
	for status, ok := range map[string]bool{"OPEN": true, "RESERVED": true, "PICKED": false, "DELIVERED": false, "INVOICED": false, "CANCELLED": false} {
		o := changeFixture()
		o.Lines[0].LineStatus = status
		_, _, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 10, Quantity: dp("2")}}})
		if (err == nil) != ok {
			t.Errorf("%s line: err = %v, want change allowed=%v", status, err, ok)
		}
	}
}

func TestApplyChange_PriceChangeDoesNotRepromise(t *testing.T) {
	o := changeFixture()
	diff, repromised, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 10, UnitPrice: dp("12")}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 1 || diff[0].Field != "unitPrice" || len(repromised) != 0 || *o.Lines[0].ConfirmedShipDate != "2027-03-06" {
		t.Fatalf("price change: diff=%+v repromised=%v confirmed=%s", diff, repromised, *o.Lines[0].ConfirmedShipDate)
	}
}

func TestApplyChange_DraftOrderIsNotRepromised(t *testing.T) {
	o := changeFixture()
	o.Status = domain.OrderDraft
	o.Lines[0].ConfirmedShipDate = nil
	diff, repromised, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 10, Quantity: dp("8")}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 1 || len(repromised) != 0 || o.Lines[0].ConfirmedShipDate != nil {
		t.Fatalf("draft change: diff=%+v repromised=%v", diff, repromised)
	}
}

func changeOrderFixture(t *testing.T, s *Postgres, ctx context.Context, le, customer string, prices ...int64) Order {
	t.Helper()
	site, wh := ids.New(), ids.New()
	const day = "2027-03-04"
	var lines []Line
	for i, price := range prices {
		lines = append(lines, Line{LineNo: i + 1, ProductID: ids.New(), Quantity: d(5), Unit: "EA", SiteID: site, WarehouseID: wh,
			UnitPrice: d(price), DiscPct: d(0), LineNet: d(5 * price), RequestedShipDate: day})
	}
	order, _, err := s.CreateOrder(ctx, NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER",
		Currency: "USD", RequestedShipDate: day, Channel: "UI", RequestHash: ids.New(), Lines: lines})
	if err != nil {
		t.Fatal(err)
	}
	return order
}

// TestPostgres_ChangeConfirmedOrder covers AC-27.7 end to end: on a confirmed
// order line 1 goes from 5 to 8 with a new requested date; the version
// increments, the diff is stored, only line 1 is re-promised, and
// order.changed carries the diff and the re-promised line. A stale If-Match
// is refused, and the history lists newest first.
func TestPostgres_ChangeConfirmedOrder(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(appOf(t, pool))
	order := changeOrderFixture(t, s, ctx, ids.New(), ids.New(), 10, 7)
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	confirmed, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirm: confirmed=%v err=%v", confirmed.Confirmed, err)
	}

	req := ChangeRequest{Reason: "customer call", Lines: []LineChange{{LineNo: 1, Quantity: dp("8"), RequestedShipDate: sp("2027-03-09")}}}
	changed, err := s.ChangeOrder(ctx, order.ID, confirmed.Order.Version, req, "sales-rep", now)
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if changed.Version != confirmed.Order.Version+1 || changed.Status != domain.OrderConfirmed || !changed.NetTotal.Equal(d(115)) {
		t.Fatalf("after change: version=%d status=%s total=%s", changed.Version, changed.Status, changed.NetTotal)
	}
	if got := *changed.Lines[0].ConfirmedShipDate; got != "2027-03-09" {
		t.Fatalf("line 1 confirmed %s, want re-promised to 2027-03-09", got)
	}
	if got := *changed.Lines[1].ConfirmedShipDate; got != "2027-03-04" {
		t.Fatalf("line 2 confirmed %s, want untouched 2027-03-04", got)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, confirmed.Order.Version, req, "sales-rep", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale If-Match: %v", err)
	}
	noop, err := s.ChangeOrder(ctx, order.ID, changed.Version, req, "sales-rep", now)
	if err != nil || noop.Version != changed.Version {
		t.Fatalf("a no-op change must not bump the version: version=%d err=%v", noop.Version, err)
	}

	var data salesevents.OrderChangedData
	if err := json.Unmarshal(outboxData(t, pool, order.ID, salesevents.OrderChanged), &data); err != nil {
		t.Fatal(err)
	}
	if data.Version != changed.Version || data.Actor != "sales-rep" || data.Reason != "customer call" ||
		len(data.RepromisedLines) != 1 || data.RepromisedLines[0] != 1 {
		t.Fatalf("order.changed = %+v", data)
	}
	fields := map[string]salesevents.FieldChange{}
	for _, f := range data.Diff {
		if f.LineNo != 1 {
			t.Fatalf("only line 1 may appear in the diff: %+v", data.Diff)
		}
		fields[f.Field] = f
	}
	if fields["quantity"].Old != "5" || fields["quantity"].New != "8" || fields["confirmedShipDate"].New != "2027-03-09" || len(fields) != 3 {
		t.Fatalf("diff = %+v", data.Diff)
	}

	history, err := s.ListOrderVersions(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Version != changed.Version || history[0].Reason != "customer call" || len(history[0].Diff) != 3 {
		t.Fatalf("history = %+v", history)
	}
	if _, err := s.ListOrderVersions(ctx, ids.New()); problemCode(t, err) != CodeOrderNotFound {
		t.Fatalf("history of an unknown order: %v", err)
	}
}

// TestPostgres_ChangeRaisingTotalRechecksCredit covers FR-27.9 with BR-27.3
// and BR-27.4: an order of 1,000 against limit 10,000 and open AR 8,500 confirms;
// raising it to 3,000 re-runs the credit check, which fails and places a CREDIT
// hold, while the order stays CONFIRMED. Lowering it does not re-check.
func TestPostgres_ChangeRaisingTotalRechecksCredit(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(appOf(t, pool))
	le, customer := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(5), ToleranceAmount: d(1000), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits, Exposure: domain.Exposure{OpenAR: d(8500)}}, 0); err != nil {
		t.Fatal(err)
	}
	order := changeOrderFixture(t, s, ctx, le, customer, 200) // 5 x 200 = 1,000
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	confirmed, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirm: confirmed=%v err=%v", confirmed.Confirmed, err)
	}

	lowered, err := s.ChangeOrder(ctx, order.ID, confirmed.Order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, UnitPrice: dp("150")}}}, "sales-rep", now)
	if err != nil || len(lowered.Holds) != 0 {
		t.Fatalf("lowering the total: holds=%d err=%v", len(lowered.Holds), err)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2`, order.ID, salesevents.CreditCheckEvaluated); n != 1 {
		t.Fatalf("a lower total must not re-run the credit check: %d evaluations", n)
	}

	raised, err := s.ChangeOrder(ctx, order.ID, lowered.Version, ChangeRequest{Reason: "bigger order", Lines: []LineChange{{LineNo: 1, UnitPrice: dp("600")}}}, "sales-rep", now)
	if err != nil {
		t.Fatalf("raise: %v", err)
	}
	if raised.Status != domain.OrderConfirmed || len(raised.Holds) != 1 || raised.Holds[0].HoldType != domain.HoldCredit ||
		raised.Holds[0].Status != domain.HoldActive || !raised.Holds[0].OrderAmount.Equal(d(3000)) {
		t.Fatalf("after raising to 3,000: status=%s holds=%+v", raised.Status, raised.Holds)
	}
	types := outboxTypes(t, pool, order.ID)
	if last := types[len(types)-3:]; last[0] != salesevents.OrderChanged || last[1] != salesevents.CreditCheckEvaluated || last[2] != salesevents.OrderHoldApplied {
		t.Fatalf("outbox tail %v", last)
	}
	page, err := s.ListOrders(ctx, OrderFilter{HoldType: domain.HoldCredit, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != order.ID {
		t.Fatalf("the held order must be in the release queue: %+v err=%v", page.Items, err)
	}
}

// TestPostgres_CancelOrder covers BR-27.5: cancel moves every open line to
// CANCELLED, clears the active CREDIT hold so the order leaves the release
// queue, writes order.cancelled with the line ids, and is refused afterwards.
// An order with a picked line cannot be cancelled.
func TestPostgres_CancelOrder(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(appOf(t, pool))
	le, customer := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(10), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	order := changeOrderFixture(t, s, ctx, le, customer, 10, 7)
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	held, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || held.Confirmed || len(held.Order.Holds) != 1 {
		t.Fatalf("confirm over the limit must hold: %+v err=%v", held.Order.Holds, err)
	}

	cancelled, err := s.CancelOrder(ctx, order.ID, held.Order.Version, "duplicate", "sales-rep", now)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != "CANCELLED" || cancelled.Version != held.Order.Version+1 || cancelled.Holds[0].Status != "CLEARED" {
		t.Fatalf("after cancel: status=%s version=%d holds=%+v", cancelled.Status, cancelled.Version, cancelled.Holds)
	}
	for _, l := range cancelled.Lines {
		if l.LineStatus != "CANCELLED" {
			t.Fatalf("line %d status %s", l.LineNo, l.LineStatus)
		}
	}
	page, err := s.ListOrders(ctx, OrderFilter{HoldType: domain.HoldCredit, Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("a cancelled order must leave the release queue: %+v err=%v", page.Items, err)
	}
	var data salesevents.OrderCancelledData
	if err := json.Unmarshal(outboxData(t, pool, order.ID, salesevents.OrderCancelled), &data); err != nil {
		t.Fatal(err)
	}
	if data.Version != cancelled.Version || data.Reason != "duplicate" || len(data.LineIDs) != 2 {
		t.Fatalf("order.cancelled = %+v", data)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, cancelled.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("1")}}}, "sales-rep", now); problemCode(t, err) != CodeNotChangeable {
		t.Fatalf("change after cancel: %v", err)
	}
	if _, err := s.CancelOrder(ctx, order.ID, cancelled.Version, "again", "sales-rep", now); problemCode(t, err) != CodeNotCancellable {
		t.Fatalf("second cancel: %v", err)
	}
	history, err := s.ListOrderVersions(ctx, order.ID)
	if err != nil || len(history) != 1 || history[0].Diff[0].Field != "status" || history[0].Diff[0].New != "CANCELLED" {
		t.Fatalf("history = %+v err=%v", history, err)
	}

	picked := changeOrderFixture(t, s, ctx, le, ids.New(), 1)
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update sales_order_line set line_status = 'PICKED' where tenant_id = $1 and order_id = $2`, testkit.TenantID(1), picked.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelOrder(ctx, picked.ID, picked.Version, "", "sales-rep", now); problemCode(t, err) != CodeNotCancellable {
		t.Fatalf("cancel with a picked line: %v", err)
	}
}
