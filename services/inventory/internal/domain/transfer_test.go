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
		{"reserved dimension", func(o *TransferOrder) { o.Lines[0].Dims[DimOwner] = "V1" }, CodeInvalid},
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
	o := ApplyShip(baseTransfer(false))
	if o.Status != TransferReceived || !o.Lines[0].Closed || !o.Lines[0].ReceivedQty.Equal(d("100")) {
		t.Fatalf("one-step ship: status %s line %+v", o.Status, o.Lines[0])
	}
	transit := ApplyShip(baseTransfer(true))
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
	shipped := ApplyShip(o)
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
	if err := CanShip(shipped); codeOf(err) != CodeTransferState {
		t.Fatalf("ship twice: %v", err)
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
