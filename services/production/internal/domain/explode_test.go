package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// AC-21.1: a 3-level BOM with phantoms explodes to stocked lines only, phantom
// children numbered under the parent (30.1, 30.2, 30.2.1), quantities per BR-21.2.
func TestExplode_ThreeLevelWithPhantoms(t *testing.T) {
	bom := []BOMComponent{
		{LineNo: "10", ItemID: "frame", QtyPer: d("1"), SeriesSize: d("1")},
		{LineNo: "20", ItemID: "bolt", QtyPer: d("8"), ScrapPct: d("2.5")},
		{LineNo: "30", ItemID: "wheel-kit", QtyPer: d("2"), Phantom: true, Children: []BOMComponent{
			{ItemID: "rim", QtyPer: d("1")},
			{ItemID: "spoke-set", QtyPer: d("0.5"), Phantom: true, Children: []BOMComponent{
				{ItemID: "spoke", QtyPer: d("36"), ScrapPct: d("1")},
			}},
		}},
		{LineNo: "40", ItemID: "paint", QtyPer: d("0.35"), SeriesSize: d("10"), RoundUp: true},
	}
	lines, err := Explode(bom, d("100"))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ no, item, qtyPer, required string }{
		{"10", "frame", "1", "100"},
		{"20", "bolt", "8", "820"},
		{"30.1", "rim", "2", "200"},
		{"30.2.1", "spoke", "36", "3636"},
		{"40", "paint", "0.35", "4"},
	}
	if len(lines) != len(want) {
		t.Fatalf("want %d lines, got %d: %+v", len(want), len(lines), lines)
	}
	for i, w := range want {
		l := lines[i]
		if l.LineNo != w.no || l.ItemID != w.item || !l.QtyPer.Equal(d(w.qtyPer)) || !l.QtyRequired.Equal(d(w.required)) {
			t.Errorf("line %d: want %s %s per %s required %s, got %s %s per %s required %s",
				i, w.no, w.item, w.qtyPer, w.required, l.LineNo, l.ItemID, l.QtyPer, l.QtyRequired)
		}
	}
	if !lines[0].SeriesSize.Equal(d("1")) || !lines[1].SeriesSize.Equal(d("1")) {
		t.Errorf("a missing series size defaults to 1, got %s and %s", lines[0].SeriesSize, lines[1].SeriesSize)
	}
}

func TestExplode_RejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		bom  []BOMComponent
		qty  string
	}{
		{"zero order quantity", []BOMComponent{{LineNo: "10", ItemID: "a", QtyPer: d("1")}}, "0"},
		{"negative quantity per", []BOMComponent{{LineNo: "10", ItemID: "a", QtyPer: d("-1")}}, "1"},
		{"scrap of 100%", []BOMComponent{{LineNo: "10", ItemID: "a", QtyPer: d("1"), ScrapPct: d("100")}}, "1"},
		{"negative series", []BOMComponent{{LineNo: "10", ItemID: "a", QtyPer: d("1"), SeriesSize: d("-2")}}, "1"},
		{"bad line under a phantom", []BOMComponent{{LineNo: "10", Phantom: true, QtyPer: d("1"),
			Children: []BOMComponent{{ItemID: "a", QtyPer: d("1"), ScrapPct: d("-1")}}}}, "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Explode(tt.bom, d(tt.qty))
			if de, ok := err.(*Error); !ok || de.Code != CodeInvalidInput {
				t.Fatalf("want %s, got %v", CodeInvalidInput, err)
			}
		})
	}
}

// Flattening a phantom is the same as multiplying its quantity into the child.
func TestExplode_PhantomEqualsMultipliedChild(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		outer := decimal.New(rapid.Int64Range(1, 5000).Draw(t, "outer"), -2)
		inner := decimal.New(rapid.Int64Range(1, 5000).Draw(t, "inner"), -3)
		scrap := decimal.New(rapid.Int64Range(0, 2000).Draw(t, "scrap"), -2)
		qty := decimal.New(rapid.Int64Range(1, 100000).Draw(t, "qty"), -1)
		viaPhantom, err := Explode([]BOMComponent{{LineNo: "10", Phantom: true, QtyPer: outer,
			Children: []BOMComponent{{ItemID: "x", QtyPer: inner, ScrapPct: scrap}}}}, qty)
		if err != nil {
			t.Fatal(err)
		}
		direct, err := Explode([]BOMComponent{{LineNo: "10", ItemID: "x", QtyPer: outer.Mul(inner), ScrapPct: scrap}}, qty)
		if err != nil {
			t.Fatal(err)
		}
		if !viaPhantom[0].QtyRequired.Equal(direct[0].QtyRequired) || viaPhantom[0].LineNo != "10.1" {
			t.Fatalf("phantom %s (%s) vs direct %s", viaPhantom[0].QtyRequired, viaPhantom[0].LineNo, direct[0].QtyRequired)
		}
	})
}

func TestRequantify_MatchesFreshExplosion(t *testing.T) {
	bom := []BOMComponent{
		{LineNo: "10", ItemID: "a", QtyPer: d("3"), ScrapPct: d("5")},
		{LineNo: "20", ItemID: "b", QtyPer: d("1"), SeriesSize: d("3")},
		{LineNo: "30", ItemID: "c", QtyPer: d("0.7"), RoundUp: true},
	}
	at100, err := Explode(bom, d("100"))
	if err != nil {
		t.Fatal(err)
	}
	moved, err := Requantify(at100, d("40"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Explode(bom, d("40"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range fresh {
		if !moved[i].QtyRequired.Equal(fresh[i].QtyRequired) {
			t.Errorf("line %s: requantified %s, fresh %s", fresh[i].LineNo, moved[i].QtyRequired, fresh[i].QtyRequired)
		}
	}
	if !at100[0].QtyRequired.Equal(d("315")) {
		t.Errorf("Requantify changed its input: %s", at100[0].QtyRequired)
	}
	if _, err := Requantify(at100, d("0")); err == nil {
		t.Error("zero quantity accepted")
	}
}

func TestPlanOperations(t *testing.T) {
	ops, err := PlanOperations([]Operation{
		{OperNumber: 10, ScrapPct: d("5")},
		{OperNumber: 20, ScrapPct: d("10")},
	}, d("100"))
	if err != nil {
		t.Fatal(err)
	}
	if !ops[0].ProcessQty.Equal(d("116.959066")) || !ops[1].ProcessQty.Equal(d("111.111112")) {
		t.Fatalf("process quantities %s, %s", ops[0].ProcessQty, ops[1].ProcessQty)
	}
	if _, err := PlanOperations([]Operation{{OperNumber: 20}, {OperNumber: 20}}, d("1")); err == nil {
		t.Fatal("duplicate operation number accepted")
	}
	if _, err := PlanOperations([]Operation{{OperNumber: 20}, {OperNumber: 10}}, d("1")); err == nil {
		t.Fatal("decreasing operation numbers accepted")
	}
}
