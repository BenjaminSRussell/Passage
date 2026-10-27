package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// ac349Voyage is AC-34.9's voyage: two containers carrying three purchase
// order lines from Shanghai, with estimated freight by weight and duty by value.
func ac349Voyage() Voyage {
	v := validVoyage()
	v.ID, v.Status = "voyage-1", VoyageEstimated
	v.CostLines = []VoyageCostLine{
		{CostType: "FREIGHT", Amount: dec("1000.00"), AllocationMethod: MethodWeight},
		{CostType: "DUTY", Amount: dec("112.50"), AllocationMethod: MethodValue},
	}
	return v
}

func TestNewGoodsInTransit_AC349FOBMovesEveryLineWithEstimates(t *testing.T) {
	g, err := NewGoodsInTransit(ac349Voyage(), "fob", "site-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != GITPending || g.Incoterm != "FOB" || g.TransferEvent != TransferOriginShipment || g.InTransitWarehouse != "TRANSIT-site-1" {
		t.Fatalf("order = %+v", g)
	}
	if g.Code != "" || g.ShippedAt != nil {
		t.Fatalf("pending order already moved ownership: %+v", g)
	}
	// Freight 1000 by equal weight: 333.33, 333.33 and 333.34 on the highest-value
	// line 3; duty 112.50 by value 100/100/250: 25.00, 25.00, 62.50.
	want := map[int][2]string{1: {"333.33", "25"}, 2: {"333.33", "25"}, 3: {"333.34", "62.5"}}
	if len(g.Lines) != 3 {
		t.Fatalf("lines = %+v", g.Lines)
	}
	for _, l := range g.Lines {
		w := want[l.Line]
		if len(l.Costs) != 2 || l.Costs[0].CostType != "FREIGHT" || !l.Costs[0].Amount.Equal(dec(w[0])) ||
			l.Costs[1].CostType != "DUTY" || !l.Costs[1].Amount.Equal(dec(w[1])) {
			t.Fatalf("line %d costs = %+v, want freight %s duty %s", l.Line, l.Costs, w[0], w[1])
		}
		if !l.Estimated.Equal(l.Costs[0].Amount.Add(l.Costs[1].Amount)) {
			t.Fatalf("line %d estimated %s is not the sum of its costs", l.Line, l.Estimated)
		}
	}
	if !g.EstimatedTotal.Equal(dec("1112.50")) || !g.GoodsValue.Equal(dec("450")) {
		t.Fatalf("totals = %s estimated, %s value", g.EstimatedTotal, g.GoodsValue)
	}
}

func TestNewGoodsInTransit_SubsetSortedAndMatchesVoyageAllocation(t *testing.T) {
	v := ac349Voyage()
	g, err := NewGoodsInTransit(v, "CIF", "site-1", []int{3, 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Lines) != 2 || g.Lines[0].Line != 1 || g.Lines[1].Line != 3 {
		t.Fatalf("lines = %+v", g.Lines)
	}
	posted, _ := v.Allocate()
	if !g.Lines[1].Estimated.Equal(posted.Allocations[2].Amount) || !g.EstimatedTotal.Equal(posted.Allocations[0].Amount.Add(posted.Allocations[2].Amount)) {
		t.Fatalf("estimates differ from the voyage allocation: %+v vs %+v", g.Lines, posted.Allocations)
	}
}

func TestNewGoodsInTransit_Refusals(t *testing.T) {
	posted := ac349Voyage()
	posted.Status = VoyagePosted
	tests := []struct {
		name     string
		voyage   Voyage
		incoterm string
		lines    []int
		code     string
		field    bool
		unknown  bool
	}{
		{"DAP transfers at destination", ac349Voyage(), "DAP", nil, CodeNoGoodsInTransit, false, false},
		{"DDP transfers at destination", ac349Voyage(), "DDP", nil, CodeNoGoodsInTransit, false, false},
		{"posted voyage", posted, "FOB", nil, CodeGITState, false, false},
		{"line not on the voyage", ac349Voyage(), "FOB", []int{1, 9}, "", true, false},
		{"not an Incoterm", ac349Voyage(), "XYZ", nil, "", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewGoodsInTransit(tc.voyage, tc.incoterm, "site-1", tc.lines)
			var fields FieldErrors
			switch {
			case tc.field:
				if !errors.As(err, &fields) || fields["receiptLines"] == "" {
					t.Fatalf("err = %v, want a receiptLines field error", err)
				}
			case tc.unknown:
				if !errors.Is(err, ErrUnknownIncoterm) {
					t.Fatalf("err = %v, want ErrUnknownIncoterm", err)
				}
			default:
				if errCode(err) != tc.code {
					t.Fatalf("err = %v, want %s", err, tc.code)
				}
			}
		})
	}
}

func TestValidateReceiptLineList(t *testing.T) {
	if f := ValidateReceiptLineList(nil); len(f) != 0 {
		t.Fatalf("empty list means every line: %v", f)
	}
	if f := ValidateReceiptLineList([]int{1, 2}); len(f) != 0 {
		t.Fatalf("valid list refused: %v", f)
	}
	if f := ValidateReceiptLineList([]int{0}); f["receiptLines"] == "" {
		t.Fatal("line 0 accepted")
	}
	if f := ValidateReceiptLineList([]int{2, 2}); f["receiptLines"] == "" {
		t.Fatal("repeated line accepted")
	}
}

func TestShip_MovesOwnershipOnce(t *testing.T) {
	g, _ := NewGoodsInTransit(ac349Voyage(), "FOB", "site-1", nil)
	at := time.Date(2026, 4, 1, 6, 30, 0, 0, time.FixedZone("CST", 8*3600))
	shipped, err := g.Ship(at)
	if err != nil {
		t.Fatal(err)
	}
	if shipped.Status != GITInTransit || shipped.Code != CodeOwnershipMoved || !shipped.ShippedAt.Equal(at) || shipped.ShippedAt.Location() != time.UTC {
		t.Fatalf("shipped = %+v", shipped)
	}
	if _, err := shipped.Ship(at); errCode(err) != CodeGITState {
		t.Fatalf("second ship err = %v, want TMS-0119", err)
	}
}

// Property: orders over disjoint line sets that cover the voyage carry exactly
// the voyage's cost total between them, to the cent (AC-34.9 "Σ allocated").
func TestGoodsInTransit_PropertyPartitionSumsToCostTotal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "lines")
		v := Voyage{ID: "v", Reference: "R", Currency: "USD", Status: VoyageEstimated,
			CostLines: []VoyageCostLine{
				{CostType: "FREIGHT", Amount: decimal.New(int64(rapid.IntRange(0, 10_000_00).Draw(t, "freight")), -2), AllocationMethod: MethodWeight},
				{CostType: "DUTY", Amount: decimal.New(int64(rapid.IntRange(0, 10_000_00).Draw(t, "duty")), -2), AllocationMethod: MethodValue},
			}}
		var first, second []int
		for i := 1; i <= n; i++ {
			v.ReceiptLines = append(v.ReceiptLines, VoyageReceiptLine{Line: i,
				Value:    decimal.NewFromInt(int64(rapid.IntRange(1, 5000).Draw(t, "value"))),
				Weight:   decimal.NewFromInt(int64(rapid.IntRange(1, 900).Draw(t, "weight"))),
				Quantity: decimal.NewFromInt(1)})
			if rapid.Bool().Draw(t, "first") {
				first = append(first, i)
			} else {
				second = append(second, i)
			}
		}
		total := decimal.Zero
		for _, part := range [][]int{first, second} {
			if len(part) == 0 {
				continue
			}
			g, err := NewGoodsInTransit(v, "FOB", "s", part)
			if err != nil {
				t.Fatal(err)
			}
			total = total.Add(g.EstimatedTotal)
		}
		if !total.Equal(v.CostTotal()) {
			t.Fatalf("orders carry %s, voyage costs %s", total, v.CostTotal())
		}
	})
}
