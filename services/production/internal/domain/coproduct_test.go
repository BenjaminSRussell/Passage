package domain

import "testing"

func TestPlanCoProducts(t *testing.T) {
	got, err := PlanCoProducts([]CoProduct{{LineNo: "C1", ItemID: "i", QtyPer: d("0.25")}}, d("10"))
	if err != nil || !got[0].QtyPlanned.Equal(d("2.5")) || got[0].Type != CoProductTypeCo {
		t.Fatalf("plan: %+v %v", got, err)
	}
	for name, in := range map[string][]CoProduct{
		"bad type":  {{LineNo: "C1", Type: "scrap", QtyPer: d("1")}},
		"zero per":  {{LineNo: "C1", QtyPer: d("0")}},
		"duplicate": {{LineNo: "C1", QtyPer: d("1")}, {LineNo: "C1", QtyPer: d("1")}},
		"no line":   {{QtyPer: d("1")}},
	} {
		if _, err := PlanCoProducts(in, d("10")); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := PlanCoProducts(nil, d("0")); err == nil {
		t.Error("zero order quantity must fail")
	}
}

func TestSettleBasisAndCostBase(t *testing.T) {
	cos := []CoProduct{{QtyPlanned: d("20"), QtyReported: d("5")}, {QtyPlanned: d("10"), QtyReported: d("0")}}
	if got := CostBase(d("100"), cos); !got.Equal(d("130")) {
		t.Fatalf("cost base %s", got)
	}
	o, r := SettleBasis(d("100"), d("50"), cos)
	if !o.Equal(d("130")) || !r.Equal(d("55")) {
		t.Fatalf("basis %s %s", o, r)
	}
	// A by-product carries no cost (BR-9.12): it is in neither the base nor the reported output.
	withBy := append(cos, CoProduct{Type: CoProductTypeBy, QtyPlanned: d("40"), QtyReported: d("40")})
	if o, r := SettleBasis(d("100"), d("50"), withBy); !o.Equal(d("130")) || !r.Equal(d("55")) {
		t.Fatalf("basis with by-product %s %s", o, r)
	}
}

func TestCheckCoProductReport(t *testing.T) {
	c := CoProduct{LineNo: "C1", QtyPlanned: d("20"), QtyReported: d("15")}
	if err := CheckCoProductReport(c, d("5"), d("0")); err != nil {
		t.Fatalf("exactly planned: %v", err)
	}
	err := CheckCoProductReport(c, d("5.01"), d("0"))
	if e, ok := err.(*Error); !ok || e.Code != CodeCoProductExceeds {
		t.Fatalf("over: %v", err)
	}
	if err := CheckCoProductReport(c, d("6"), d("5")); err != nil {
		t.Fatalf("tolerance: %v", err)
	}
	if err := CheckCoProductReport(c, d("0"), d("0")); err == nil {
		t.Fatal("zero quantity must fail")
	}
}

func TestProportionalCoProduct(t *testing.T) {
	c := CoProduct{QtyPlanned: d("20"), QtyReported: d("4")}
	if got := ProportionalCoProduct(c, d("100"), d("50")); !got.Equal(d("6")) {
		t.Fatalf("half: %s", got)
	}
	if got := ProportionalCoProduct(c, d("100"), d("10")); !got.IsZero() {
		t.Fatalf("already ahead: %s", got)
	}
}

// Quantity allocation: 100 kg product + 20 kg co-product share a planned cost of
// 1200, so each kg carries 10; the receipts sum to the planned cost.
func TestCostBaseAllocation(t *testing.T) {
	cos := []CoProduct{{QtyPlanned: d("20")}}
	base := CostBase(d("100"), cos)
	main := ReceiptValue(d("1200"), base, d("0"), d("100"), "USD")
	co := ReceiptValue(d("1200"), base, d("0"), d("20"), "USD")
	if !main.Equal(d("1000")) || !co.Equal(d("200")) {
		t.Fatalf("main %s co %s", main, co)
	}
}
