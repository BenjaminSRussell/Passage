package store

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

func catchSpec() *domain.CatchWeight {
	return &domain.CatchWeight{Nominal: decimal.RequireFromString("10"), Min: decimal.RequireFromString("9.8"), Max: decimal.RequireFromString("11")}
}

func catchWeightOrder(t *testing.T, le, customer string, cases int64) NewOrder {
	t.Helper()
	spec := catchSpec()
	net, err := domain.LineNetFor(d(cases), d(0), d(2), d(0), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD",
		RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: ids.New(), OrderDate: time.Date(2027, 2, 20, 0, 0, 0, 0, time.UTC),
		Actor: "sales-rep", Lines: []Line{{
			LineNo: 1, ProductID: ids.New(), Quantity: d(cases), Unit: "CS", SiteID: ids.New(), WarehouseID: ids.New(),
			UnitPrice: d(2), DiscPct: d(0), LineNet: net, RequestedShipDate: "2027-03-04", CatchWeight: spec,
		}}}
}

func weigh(s string) *decimal.Decimal {
	w := decimal.RequireFromString(s)
	return &w
}

func report(t *testing.T, s *Postgres, ctx context.Context, orderID, kind, ref string, cases int64, weight *decimal.Decimal) (OrderProgress, error) {
	t.Helper()
	rep, _, err := s.RecordProgress(ctx, progressIn(orderID, kind, ref, "k-"+ref, "h-"+ref,
		ProgressLineInput{LineNo: 1, Quantity: d(cases), ActualWeight: weight}), time.Now().UTC().Truncate(time.Microsecond))
	return rep, err
}

func wantNet(t *testing.T, s *Postgres, ctx context.Context, orderID, line, total string) Order {
	t.Helper()
	got, err := s.GetOrder(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Lines[0].LineNet.Equal(decimal.RequireFromString(line)) || !got.NetTotal.Equal(decimal.RequireFromString(total)) {
		t.Fatalf("lineNet %s netTotal %s actual %v, want %s / %s", got.Lines[0].LineNet, got.NetTotal, got.Lines[0].ActualWeight, line, total)
	}
	return got
}

// TestPostgres_CatchWeightPartialDeliveries is Wave 24 AC4: 10 cases at nominal
// 10 kg and 2.00 per kg is 200; each report checks its own cases against the band,
// adds to the recorded weight and leaves the undelivered cases at nominal.
func TestPostgres_CatchWeightPartialDeliveries(t *testing.T) {
	s, _, ctx := migrated(t)
	o := confirmed(t, s, ctx, catchWeightOrder(t, ids.New(), ids.New(), 10))
	wantNet(t, s, ctx, o.ID, "200", "200")

	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-45", 5, weigh("45")); problemCode(t, err) != domain.CodeCatchWeight {
		t.Fatalf("45 kg for 5 cases (min 49): %v, want SLS-0061", err)
	}
	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-102", 5, weigh("102")); problemCode(t, err) != domain.CodeCatchWeight {
		t.Fatalf("102 kg for 5 cases (max 55): %v, want SLS-0061", err)
	}
	wantNet(t, s, ctx, o.ID, "200", "200")

	rep, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-1", 5, weigh("51"))
	if err != nil || rep.OrderStatus != domain.OrderPartiallyShipped {
		t.Fatalf("5 of 10 cases at 51 kg: %v %s", err, rep.OrderStatus)
	}
	got := wantNet(t, s, ctx, o.ID, "202", "202")
	if got.Lines[0].ActualWeight == nil || !got.Lines[0].ActualWeight.Equal(d(51)) {
		t.Fatalf("recorded weight after the first partial: %v", got.Lines[0].ActualWeight)
	}

	rep, err = report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-2", 5, weigh("52"))
	if err != nil || rep.OrderStatus != domain.OrderShipped {
		t.Fatalf("second 5 cases at 52 kg: %v %s", err, rep.OrderStatus)
	}
	got = wantNet(t, s, ctx, o.ID, "206", "206")
	if !got.Lines[0].ActualWeight.Equal(d(103)) {
		t.Fatalf("the second report must add to the first: %v, want 103", got.Lines[0].ActualWeight)
	}
}

// TestPostgres_CatchWeightMixedPartials: cases reported without a weight count
// at nominal and are folded into the recorded weight.
func TestPostgres_CatchWeightMixedPartials(t *testing.T) {
	s, _, ctx := migrated(t)
	o := confirmed(t, s, ctx, catchWeightOrder(t, ids.New(), ids.New(), 10))

	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-1", 4, weigh("41")); err != nil {
		t.Fatal(err)
	}
	wantNet(t, s, ctx, o.ID, "202", "202")
	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-2", 3, nil); err != nil {
		t.Fatal(err)
	}
	got := wantNet(t, s, ctx, o.ID, "202", "202")
	if !got.Lines[0].ActualWeight.Equal(d(71)) {
		t.Fatalf("4 cases at 41 kg and 3 unweighed at nominal: %v, want 71", got.Lines[0].ActualWeight)
	}
	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-3", 3, weigh("31")); err != nil {
		t.Fatal(err)
	}
	wantNet(t, s, ctx, o.ID, "204", "204")

	cur, _ := s.GetOrder(ctx, o.ID)
	if _, err := s.CancelLineRemainder(ctx, o.ID, 1, cur.Version, "", "rep", time.Now().UTC()); err == nil {
		t.Fatal("a fully delivered order has no remainder")
	}
}

// TestPostgres_CatchWeightRemainderCancelKeepsActualWeight: closing the rest of
// a half-delivered catch-weight line prices it at the weight delivered.
func TestPostgres_CatchWeightRemainderCancelKeepsActualWeight(t *testing.T) {
	s, _, ctx := migrated(t)
	o := confirmed(t, s, ctx, catchWeightOrder(t, ids.New(), ids.New(), 10))
	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-1", 5, weigh("51")); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.GetOrder(ctx, o.ID)
	if _, err := s.CancelLineRemainder(ctx, o.ID, 1, cur.Version, "", "rep", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	wantNet(t, s, ctx, o.ID, "102", "102")
}

func TestPostgres_CatchWeightReportRefusals(t *testing.T) {
	s, _, ctx := migrated(t)
	o := confirmed(t, s, ctx, catchWeightOrder(t, ids.New(), ids.New(), 10))

	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-HALF", "k-half", "h-half",
		ProgressLineInput{LineNo: 1, Quantity: decimal.RequireFromString("2.5"), ActualWeight: weigh("25")}), time.Now().UTC()); problemCode(t, err) != domain.CodeCatchWeightInput {
		t.Fatalf("2.5 cases with a weight: %v, want SLS-0063", err)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-HALF-NOW", "k-half-now", "h-half-now",
		ProgressLineInput{LineNo: 1, Quantity: decimal.RequireFromString("2.5")}), time.Now().UTC()); problemCode(t, err) != domain.CodeCatchWeightInput {
		t.Fatalf("2.5 cases without a weight: %v, want SLS-0063", err)
	}
	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-1", 10, weigh("100")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressInvoiced, "INV-HALF", "k-inv-half", "h-inv-half",
		ProgressLineInput{LineNo: 1, Quantity: decimal.RequireFromString("0.5")}), time.Now().UTC()); problemCode(t, err) != domain.CodeCatchWeightInput {
		t.Fatalf("0.5 cases invoiced: %v, want SLS-0063", err)
	}
	if _, err := report(t, s, ctx, o.ID, domain.ProgressInvoiced, "INV-1", 10, weigh("100")); problemCode(t, err) != domain.CodeCatchWeightInput {
		t.Fatalf("actualWeight on an INVOICED report: %v, want SLS-0063", err)
	}

	plain := confirmed(t, s, ctx, orderOf(ids.New(), ids.New(), ids.New(), "2027-03-04", 10))
	if _, err := report(t, s, ctx, plain.ID, domain.ProgressDelivered, "PS-P", 5, weigh("51")); problemCode(t, err) != domain.CodeCatchWeightInput {
		t.Fatalf("actualWeight on a line that is not catch-weight: %v, want SLS-0063", err)
	}
}

// TestPostgres_CatchWeightCreditExposure: open and delivered-not-invoiced
// exposure follow the weight, not cases times the per-weight price.
func TestPostgres_CatchWeightCreditExposure(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(100000), Currency: "USD", LimitType: domain.LimitBalanceAll, OldestItemDaysMax: 30, MaxDunningLevel: 3}}, 0); err != nil {
		t.Fatal(err)
	}
	check := func(dni, open string) {
		t.Helper()
		p, err := s.GetCreditProfile(ctx, le, customer)
		if err != nil || !p.Exposure.DeliveredNotInvoiced.Equal(decimal.RequireFromString(dni)) || !p.Exposure.OpenOrders.Equal(decimal.RequireFromString(open)) {
			t.Fatalf("want dni %s open %s: %+v %v", dni, open, p.Exposure, err)
		}
	}
	o := confirmed(t, s, ctx, catchWeightOrder(t, le, customer, 10))
	check("0", "200")
	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-1", 4, weigh("41")); err != nil {
		t.Fatal(err)
	}
	check("82", "120")
	if _, err := report(t, s, ctx, o.ID, domain.ProgressInvoiced, "INV-1", 1, nil); err != nil {
		t.Fatal(err)
	}
	check("61.5", "120")
	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-2", 6, nil); err != nil {
		t.Fatal(err)
	}
	check("181.8", "0")
}

func TestApplyChange_CatchWeightQuantityStaysWholeCases(t *testing.T) {
	line := Line{ID: "cw", LineNo: 1, Quantity: d(10), UnitPrice: d(2), DiscPct: d(0), LineNet: d(200), RequestedShipDate: "2027-03-04",
		LineStatus: "OPEN", CatchWeight: catchSpec()}
	o := &Order{Status: domain.OrderConfirmed, Version: 2, RequestedShipDate: "2027-03-04", NetTotal: d(200), Lines: []Line{line}}
	if _, _, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("2.5")}}}); err == nil {
		t.Fatal("a fractional case count on a catch-weight line must be refused")
	}
	if _, _, err := ApplyChange(o, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("12")}}}); err != nil {
		t.Fatal(err)
	}
	if !o.Lines[0].LineNet.Equal(d(240)) {
		t.Fatalf("12 cases at nominal 10 and 2.00: %s, want 240", o.Lines[0].LineNet)
	}
}
