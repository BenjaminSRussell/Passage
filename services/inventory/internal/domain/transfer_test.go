package domain

import (
	"testing"
	"time"
)

func baseTransfer(transit bool) TransferOrder {
	return TransferOrder{
		FromWarehouse: "W1", ToWarehouse: "W2", TransitWarehouse: "T1", UseTransit: transit,
		Status: TransferCreated, ShipDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Lines: []TransferLine{{LineNo: 1, ProductID: "p", Unit: "EA", Dims: Dimensions{DimSite: "S1", DimWarehouse: "W1"}, Qty: d("100"),
			UnderPct: d("10"), OverPct: d("0")}},
	}
}

func TestValidateNewTransfer(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*TransferOrder)
		code string
	}{
		{"valid transit", func(*TransferOrder) {}, ""},
		{"same warehouse", func(o *TransferOrder) { o.ToWarehouse = "W1" }, CodeInvalid},
		{"transit missing", func(o *TransferOrder) { o.TransitWarehouse = "" }, CodeTransferTransit},
		{"transit equals to", func(o *TransferOrder) { o.TransitWarehouse = "W2" }, CodeInvalid},
		{"no lines", func(o *TransferOrder) { o.Lines = nil }, CodeInvalid},
		{"zero qty", func(o *TransferOrder) { o.Lines[0].Qty = d("0") }, CodeInvalid},
		{"no unit", func(o *TransferOrder) { o.Lines[0].Unit = "" }, CodeInvalid},
		{"foreign warehouse dims", func(o *TransferOrder) { o.Lines[0].Dims = Dimensions{DimSite: "S1", DimWarehouse: "W9"} }, CodeInvalid},
		{"warehouse taken from header", func(o *TransferOrder) { o.Lines[0].Dims = Dimensions{DimSite: "S1"} }, ""},
		{"site mandatory", func(o *TransferOrder) { o.Lines[0].Dims = Dimensions{DimWarehouse: "W1"} }, CodeDimensionMandatory},
		{"vendor-owned stock cannot be transferred", func(o *TransferOrder) { o.Lines[0].Dims[DimOwner] = "V1" }, CodeOwnershipValue},
		{"license plate reserved", func(o *TransferOrder) { o.Lines[0].Dims[DimLicensePlate] = "LP1" }, CodeInvalid},
		{"serial needs quantity 1", func(o *TransferOrder) { o.Lines[0].Dims[DimSerial] = "SN1" }, CodeSerialQty},
		{"too many decimals", func(o *TransferOrder) { o.Lines[0].Qty = d("1.0000001") }, CodeUOMPrecision},
		{"one-step needs no transit", func(o *TransferOrder) { o.UseTransit, o.TransitWarehouse = false, "" }, ""},
		{"tolerance over 100", func(o *TransferOrder) { o.Lines[0].UnderPct = d("101") }, CodeInvalid},
		{"negative tolerance", func(o *TransferOrder) { o.Lines[0].OverPct = d("-1") }, CodeInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := baseTransfer(true)
			tc.mut(&o)
			if got := codeOf(ValidateNewTransfer(o, 6)); got != tc.code {
				t.Fatalf("code %q, want %q", got, tc.code)
			}
		})
	}
}

func TestWithWarehouseReplacesOnlyWarehouse(t *testing.T) {
	src := Dimensions{DimWarehouse: "W1", DimBatch: "B1"}
	out := WithWarehouse(src, "T1")
	if out[DimWarehouse] != "T1" || out[DimBatch] != "B1" || src[DimWarehouse] != "W1" {
		t.Fatalf("got %v from %v", out, src)
	}
}

func TestDimsAtEachWarehouse(t *testing.T) {
	o := baseTransfer(true)
	l := o.Lines[0]
	l.Dims[DimBatch] = "B1"
	if got := o.TransitDims(l); got[DimWarehouse] != "T1" || got[DimSite] != "S1" || got[DimBatch] != "B1" {
		t.Fatalf("transit dims %v", got)
	}
	o.ToSite = "S2"
	if got := o.DestinationDims(l); got[DimWarehouse] != "W2" || got[DimSite] != "S2" || got[DimBatch] != "B1" {
		t.Fatalf("destination dims %v", got)
	}
	if got := o.SourceDims(l); got[DimWarehouse] != "W1" || got[DimSite] != "S1" {
		t.Fatalf("source dims %v", got)
	}
}

func TestApplyReceiveTolerances(t *testing.T) {
	cases := []struct {
		name        string
		over        string
		qty         string
		closeShort  bool
		received    string
		shipped     string
		fromTransit string
		fromSource  string
		closed      bool
		code        string
	}{
		{"partial stays open", "0", "40", false, "40", "100", "40", "0", false, ""},
		{"exact closes", "0", "100", false, "100", "100", "100", "0", true, ""},
		{"short within tolerance closes (AC-16.11)", "0", "95", true, "95", "100", "95", "0", true, ""},
		{"short beyond tolerance refused", "0", "80", true, "", "", "", "", false, CodeTransferShortClose},
		{"short without close stays open", "0", "80", false, "80", "100", "80", "0", false, ""},
		{"over-receipt refused", "0", "101", false, "", "", "", "", false, CodeTransferOverReceipt},
		{"over tolerance: excess comes from the source and closes", "5", "104", false, "104", "104", "100", "4", true, ""},
		{"over tolerance still bounded", "5", "106", false, "", "", "", "", false, CodeTransferOverReceipt},
		{"zero refused", "0", "0", false, "", "", "", "", false, CodeInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := TransferLine{LineNo: 1, Qty: d("100"), ShippedQty: d("100"), UnderPct: d("10"), OverPct: d(tc.over)}
			out, err := ApplyReceive(l, d(tc.qty), tc.closeShort)
			if got := codeOf(err); got != tc.code {
				t.Fatalf("code %q, want %q (%v)", got, tc.code, err)
			}
			if tc.code != "" {
				if !out.Line.ReceivedQty.Equal(l.ReceivedQty) || out.Line.Closed {
					t.Fatalf("a refused receipt changed the line: %+v", out.Line)
				}
				return
			}
			if !out.Line.ReceivedQty.Equal(d(tc.received)) || !out.Line.ShippedQty.Equal(d(tc.shipped)) || out.Line.Closed != tc.closed ||
				!out.FromTransit.Equal(d(tc.fromTransit)) || !out.FromSource.Equal(d(tc.fromSource)) {
				t.Fatalf("got line %+v transit %s source %s", out.Line, out.FromTransit, out.FromSource)
			}
			if !out.FromTransit.Add(out.FromSource).Equal(d(tc.qty)) {
				t.Fatalf("receipt split %s + %s does not add up to %s", out.FromTransit, out.FromSource, tc.qty)
			}
		})
	}
}

func TestShortClosedLineKeepsShortfallInTransit(t *testing.T) {
	l := TransferLine{LineNo: 1, Qty: d("100"), ShippedQty: d("100"), UnderPct: d("10")}
	r, err := ApplyReceive(l, d("95"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Line.Closed || !r.Line.InTransit().Equal(d("5")) {
		t.Fatalf("closed %v in transit %s, want closed with 5 in transit", r.Line.Closed, r.Line.InTransit())
	}
}

func TestApplyReceiveRefusesClosedLine(t *testing.T) {
	l := TransferLine{LineNo: 2, Qty: d("5"), ShippedQty: d("5"), ReceivedQty: d("5"), Closed: true}
	if _, err := ApplyReceive(l, d("1"), false); codeOf(err) != CodeTransferState {
		t.Fatalf("code %q, want %s", codeOf(err), CodeTransferState)
	}
}

func TestShipOneStepReceivesAtShipment(t *testing.T) {
	o, _, err := ApplyShip(baseTransfer(false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != TransferReceived || !o.Lines[0].Closed || !o.Lines[0].ReceivedQty.Equal(d("100")) {
		t.Fatalf("one-step ship: status %s line %+v", o.Status, o.Lines[0])
	}
	transit, _, err := ApplyShip(baseTransfer(true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if transit.Status != TransferShipped || transit.Lines[0].InTransit().Cmp(d("100")) != 0 {
		t.Fatalf("two-step ship: status %s in transit %s", transit.Status, transit.Lines[0].InTransit())
	}
}

func TestStatusAndCancelRules(t *testing.T) {
	o := baseTransfer(true)
	if err := CanShip(o); err != nil {
		t.Fatalf("created order should ship: %v", err)
	}
	if err := CanReceive(o); codeOf(err) != CodeTransferState {
		t.Fatalf("receive before ship: %v", err)
	}
	if err := CanCancel(o); err != nil {
		t.Fatalf("created order should cancel: %v", err)
	}
	shipped, _, err := ApplyShip(o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := CanCancel(shipped); codeOf(err) != CodeTransferCancel {
		t.Fatalf("shipped order cancel: %v", err)
	}
	for _, status := range []string{TransferCancelled, TransferReceived} {
		done := shipped
		done.Status = status
		if err := CanCancel(done); codeOf(err) != CodeTransferState {
			t.Fatalf("cancel from %s: %v, want %s", status, err, CodeTransferState)
		}
	}
	if _, _, err := ApplyShip(shipped, nil); codeOf(err) != CodeTransferShipQty {
		t.Fatalf("ship twice: %v, want %s", err, CodeTransferShipQty)
	}
	for _, status := range []string{TransferCancelled, TransferReceived} {
		done := o
		done.Status = status
		if err := CanShip(done); codeOf(err) != CodeTransferState {
			t.Fatalf("ship from %s: %v, want %s", status, err, CodeTransferState)
		}
	}
	partial := shipped
	partial.Lines = []TransferLine{{Qty: d("100"), ShippedQty: d("100"), ReceivedQty: d("40")}}
	if got := SettleTransferStatus(partial); got != TransferPartial {
		t.Fatalf("status %s, want PARTIALLY_RECEIVED", got)
	}
}

func TestInTransitDays(t *testing.T) {
	ship := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if got := InTransitDays(ship, ship.Add(49*time.Hour)); got != 2 {
		t.Fatalf("days %d, want 2", got)
	}
	if got := InTransitDays(ship, ship.Add(-time.Hour)); got != 0 {
		t.Fatalf("future ship date days %d, want 0", got)
	}
}

func TestTransferLineLateness(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, 9, n, 0, 0, 0, 0, time.UTC) }
	open := TransferLine{Qty: d("10"), ShippedQty: d("10"), ReceivedQty: d("4")}
	closed := open
	closed.Closed = true
	received := TransferLine{Qty: d("10"), ShippedQty: d("10"), ReceivedQty: d("10")}
	cases := []struct {
		name, status string
		line         TransferLine
		ship, asOf   time.Time
		want         string
		days         int
	}{
		{"created past planned date", TransferCreated, TransferLine{Qty: d("10")}, day(1), day(4), LateShipment, 3},
		{"created on planned date", TransferCreated, TransferLine{Qty: d("10")}, day(2), day(2), "", 0},
		{"created with a future planned date", TransferCreated, TransferLine{Qty: d("10")}, day(9), day(2), "", 0},
		{"shipped exactly at the allowance is due today", TransferShipped, open, day(1), day(8), "", 0},
		{"shipped one day beyond the allowance", TransferShipped, open, day(1), day(9), LateReceipt, 1},
		{"partially received and old", TransferPartial, open, day(1), day(20), LateReceipt, 12},
		{"closed short is for investigation, not late", TransferPartial, closed, day(1), day(20), "", 0},
		{"nothing left in transit", TransferShipped, received, day(1), day(20), "", 0},
		{"received order", TransferReceived, open, day(1), day(20), "", 0},
		{"cancelled order", TransferCancelled, TransferLine{Qty: d("10")}, day(1), day(20), "", 0},
	}
	for _, c := range cases {
		if got, days := TransferLineLateness(c.status, c.line, c.ship, time.Time{}, c.asOf, DefaultLateReceiptDays); got != c.want || days != c.days {
			t.Errorf("%s: got %q %d days, want %q %d", c.name, got, days, c.want, c.days)
		}
	}
	if got, days := TransferLineLateness(TransferShipped, open, day(1), time.Time{}, day(2), 0); got != LateReceipt || days != 1 {
		t.Errorf("zero allowance: got %q %d, want a late receipt one day overdue", got, days)
	}
}

func TestTransferLineRemaining(t *testing.T) {
	cases := []struct {
		name, status string
		line         TransferLine
		want         string
	}{
		{"not shipped", TransferCreated, TransferLine{Qty: d("10")}, "10"},
		{"partly received", TransferPartial, TransferLine{Qty: d("10"), ShippedQty: d("10"), ReceivedQty: d("4")}, "6"},
		{"closed short", TransferReceived, TransferLine{Qty: d("10"), ShippedQty: d("10"), ReceivedQty: d("9"), Closed: true}, "0"},
		{"over-received", TransferReceived, TransferLine{Qty: d("10"), ShippedQty: d("10"), ReceivedQty: d("11")}, "0"},
		{"cancelled", TransferCancelled, TransferLine{Qty: d("10")}, "0"},
	}
	for _, c := range cases {
		if got := TransferLineRemaining(c.status, c.line); !got.Equal(d(c.want)) {
			t.Errorf("%s: remaining %s, want %s", c.name, got, c.want)
		}
	}
}

func TestPlannedReceiptDateReplacesTheAllowance(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, 9, n, 0, 0, 0, 0, time.UTC) }
	open := TransferLine{Qty: d("10"), ShippedQty: d("10"), ReceivedQty: d("4")}
	// Shipped on the 1st, planned for the 20th: not late on the 15th although the 7-day allowance passed.
	if got, days := TransferLineLateness(TransferShipped, open, day(1), day(20), day(15), DefaultLateReceiptDays); got != "" || days != 0 {
		t.Errorf("before planned receipt: got %q %d", got, days)
	}
	if got, days := TransferLineLateness(TransferShipped, open, day(1), day(20), day(23), DefaultLateReceiptDays); got != LateReceipt || days != 3 {
		t.Errorf("after planned receipt: got %q %d, want LATE_RECEIPT 3", got, days)
	}
}

func partialOrder(transit bool) TransferOrder {
	o := baseTransfer(transit)
	o.Number = "TO-1"
	o.Lines = []TransferLine{
		{ID: "a", LineNo: 1, ProductID: "p", Unit: "EA", Qty: d("100"), UnderPct: d("10"), OverPct: d("0")},
		{ID: "b", LineNo: 2, ProductID: "q", Unit: "EA", Qty: d("50"), UnderPct: d("0"), OverPct: d("0")},
	}
	return o
}

func TestPartialShipmentsAccumulateUntilTheLineIsShipped(t *testing.T) {
	o := partialOrder(true)
	o, moved, err := ApplyShip(o, []ShipQty{{LineID: "a", Qty: d("60")}})
	if err != nil || o.Status != TransferShipped || !moved["a"].Equal(d("60")) || len(moved) != 1 {
		t.Fatalf("first shipment: status %s moved %v err %v", o.Status, moved, err)
	}
	if !o.Lines[0].UnshippedQty().Equal(d("40")) || !o.Lines[1].UnshippedQty().Equal(d("50")) {
		t.Fatalf("unshipped %s / %s", o.Lines[0].UnshippedQty(), o.Lines[1].UnshippedQty())
	}
	if err := CanShip(o); err != nil {
		t.Fatalf("a shipped order with quantity left should ship again: %v", err)
	}
	if _, _, err := ApplyShip(o, []ShipQty{{LineID: "a", Qty: d("41")}}); codeOf(err) != CodeTransferShipQty {
		t.Fatalf("over-ship: %v", err)
	}
	if _, _, err := ApplyShip(o, []ShipQty{{LineID: "a", Qty: d("1")}, {LineID: "a", Qty: d("1")}}); codeOf(err) != CodeInvalid {
		t.Fatalf("duplicate line: %v", err)
	}
	if _, _, err := ApplyShip(o, []ShipQty{{LineID: "zz", Qty: d("1")}}); codeOf(err) != CodeTransferNotFound {
		t.Fatalf("unknown line: %v", err)
	}
	if _, _, err := ApplyShip(o, []ShipQty{{LineID: "a", Qty: d("0")}}); codeOf(err) != CodeInvalid {
		t.Fatalf("zero quantity: %v", err)
	}
	// Shipping with no quantities ships what is left on every line.
	o, moved, err = ApplyShip(o, nil)
	if err != nil || !moved["a"].Equal(d("40")) || !moved["b"].Equal(d("50")) {
		t.Fatalf("ship the rest: moved %v err %v", moved, err)
	}
	if !o.Lines[0].ShippedQty.Equal(d("100")) || !o.Lines[1].ShippedQty.Equal(d("50")) {
		t.Fatalf("shipped %s / %s", o.Lines[0].ShippedQty, o.Lines[1].ShippedQty)
	}
}

func TestReceivingAllShippedKeepsAPartlyShippedLineOpen(t *testing.T) {
	o, _, _ := ApplyShip(partialOrder(true), []ShipQty{{LineID: "a", Qty: d("60")}})
	r, err := ApplyReceive(o.Lines[0], d("60"), false)
	if err != nil || r.Line.Closed {
		t.Fatalf("received the shipped 60 of 100: closed %v err %v", r.Line.Closed, err)
	}
	o.Lines[0] = r.Line
	if got := SettleTransferStatus(o); got != TransferPartial {
		t.Fatalf("status %s, want PARTIALLY_RECEIVED", got)
	}
	o, _, _ = ApplyShip(o, []ShipQty{{LineID: "a", Qty: d("40")}})
	r, err = ApplyReceive(o.Lines[0], d("40"), false)
	if err != nil || !r.Line.Closed {
		t.Fatalf("received the rest: closed %v err %v", r.Line.Closed, err)
	}
}

func TestOneStepPartialShipmentClosesWhenFullyShipped(t *testing.T) {
	o, _, err := ApplyShip(partialOrder(false), []ShipQty{{LineID: "a", Qty: d("100")}})
	if err != nil || o.Status != TransferPartial || !o.Lines[0].Closed || o.Lines[1].Closed {
		t.Fatalf("first shipment: status %s err %v lines %+v", o.Status, err, o.Lines)
	}
	if o.Lines[1].UnshippedQty().Equal(d("50")) == false {
		t.Fatal("line b should have all 50 left")
	}
	o, _, err = ApplyShip(o, nil)
	if err != nil || o.Status != TransferReceived {
		t.Fatalf("second shipment: status %s err %v", o.Status, err)
	}
}

func TestCloseShortOnAPartlyShippedLineCountsTheUnshippedRemainder(t *testing.T) {
	o, _, _ := ApplyShip(partialOrder(true), []ShipQty{{LineID: "a", Qty: d("60")}}) // 100 ordered, 10% under
	// Receiving the 60 in transit and closing short would give up 40 of 100: beyond 10%.
	if _, err := ApplyReceive(o.Lines[0], d("60"), true); codeOf(err) != CodeTransferShortClose {
		t.Fatalf("close short giving up 40 of 100: %v", err)
	}
	// 58 of 60 shipped with close short: 42 of 100 short, refused even though 2 of 60 is within 10%.
	if _, err := ApplyReceive(o.Lines[0], d("58"), true); codeOf(err) != CodeTransferShortClose {
		t.Fatalf("close short of a partial shipment: %v", err)
	}
	o, _, _ = ApplyShip(o, []ShipQty{{LineID: "a", Qty: d("32")}}) // 92 shipped, 8 left
	r, err := ApplyReceive(o.Lines[0], d("92"), true)
	if err != nil || !r.Line.Closed || !r.Line.InTransit().IsZero() || !r.Line.UnshippedQty().IsZero() {
		t.Fatalf("closing short with 8 of 100 unshipped: closed %v in transit %s err %v", r.Line.Closed, r.Line.InTransit(), err)
	}
	// Without close short the same receipt keeps the line open for the 8 left.
	if r, err = ApplyReceive(o.Lines[0], d("92"), false); err != nil || r.Line.Closed {
		t.Fatalf("receipt without close short: closed %v err %v", r.Line.Closed, err)
	}
}

func TestShipQuantityScaleFollowsTheUnitPrecision(t *testing.T) {
	if err := ValidateShipScale([]ShipQty{{LineID: "a", Qty: d("1.25")}}, 2); err != nil {
		t.Fatalf("two decimals at scale 2: %v", err)
	}
	if err := ValidateShipScale([]ShipQty{{LineID: "a", Qty: d("1")}, {LineID: "b", Qty: d("1.255")}}, 2); codeOf(err) != CodeUOMPrecision {
		t.Fatalf("three decimals at scale 2: %v, want %s", err, CodeUOMPrecision)
	}
}
