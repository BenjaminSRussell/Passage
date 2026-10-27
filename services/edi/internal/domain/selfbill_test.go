package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var shipDay = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// tenShipments: DN-0 .. DN-9, each 2 x 10.00 at 21 % VAT (tax 4.20).
func tenShipments() []SBShipment {
	var out []SBShipment
	for i := 0; i < 10; i++ {
		out = append(out, SBShipment{
			ID: fmt.Sprintf("SHP-%d", i), DeliveryNote: fmt.Sprintf("DN-%d", i), POLine: "1", Item: "ITEM-A",
			Quantity: d("2"), UnitPrice: d("10.00"), TaxRatePct: d("21"), ShippedOn: shipDay,
		})
	}
	return out
}

func lineFor(s SBShipment) SBLine {
	return SBLine{Line: "L-" + s.ID, DeliveryNote: s.DeliveryNote, POLine: s.POLine, Item: s.Item,
		Quantity: s.Quantity, UnitPrice: s.UnitPrice, Tax: d("4.20")}
}

func TestMatchSelfBilling_AC35_13(t *testing.T) {
	ships := tenShipments()
	var lines []SBLine
	for _, s := range ships[:9] {
		lines = append(lines, lineFor(s))
	}
	lines[8].UnitPrice = d("10.05") // one price difference of 0.05 per unit
	lines[8].Tax = d("4.22")        // the customer's VAT on its own amount, 2 x 10.05 x 21 %
	asOf := shipDay.AddDate(0, 0, 31)

	res, err := MatchSelfBilling(lines, ships, DefaultSBTolerance(), asOf, 30)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, l := range res.Lines {
		counts[l.Outcome]++
	}
	if counts[SelfBillMatched] != 8 || counts[SelfBillDifference] != 1 || counts[SelfBillUnmatched] != 0 {
		t.Fatalf("counts = %v", counts)
	}
	if len(res.Lines[8].Differences) != 1 || res.Lines[8].Differences[0].Field != "unitPrice" {
		t.Fatalf("difference = %+v", res.Lines[8].Differences)
	}
	if len(res.Unbilled) != 1 || res.Unbilled[0] != "SHP-9" {
		t.Fatalf("unbilled = %v", res.Unbilled)
	}
	if !hasCode(res.Issues, CodeSelfBillDifference) || !hasCode(res.Issues, CodeSelfBillUnbilled) {
		t.Fatalf("issues = %+v", res.Issues)
	}
}

func hasCode(issues []Issue, code string) bool {
	for _, i := range issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

func TestMatchSelfBilling_Outcomes(t *testing.T) {
	ship := tenShipments()[0]
	tests := []struct {
		name     string
		edit     func(*SBLine)
		shipQty  string
		want     string
		wantDiff string
	}{
		{"identical", func(*SBLine) {}, "2", SelfBillMatched, ""},
		{"quantity off by one", func(l *SBLine) { l.Quantity, l.Tax = d("3"), d("6.30") }, "2", SelfBillDifference, "quantity"},
		{"price within tolerance", func(l *SBLine) { l.UnitPrice = d("10.01") }, "2", SelfBillMatched, ""},
		{"price beyond tolerance", func(l *SBLine) { l.UnitPrice = d("10.02") }, "2", SelfBillDifference, "unitPrice"},
		{"tax differs by a cent over tolerance", func(l *SBLine) { l.Tax = d("4.22") }, "2", SelfBillDifference, "tax"},
		{"tax exactly one cent off passes", func(l *SBLine) { l.Tax = d("4.21") }, "2", SelfBillMatched, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := ship
			s.Quantity = d(tc.shipQty)
			l := lineFor(s)
			tc.edit(&l)
			res, err := MatchSelfBilling([]SBLine{l}, []SBShipment{s}, DefaultSBTolerance(), shipDay, 30)
			if err != nil {
				t.Fatal(err)
			}
			got := res.Lines[0]
			if got.Outcome != tc.want {
				t.Fatalf("outcome %s want %s (%+v)", got.Outcome, tc.want, got.Differences)
			}
			if tc.wantDiff != "" && (len(got.Differences) != 1 || got.Differences[0].Field != tc.wantDiff) {
				t.Fatalf("differences = %+v, want %s", got.Differences, tc.wantDiff)
			}
		})
	}
}

// The VAT check runs on the customer's own amount: a self-consistent price
// difference is one DIFFERENCE, while VAT left at our price is a second one.
func TestMatchSelfBilling_TaxRecomputedOnBilledAmount(t *testing.T) {
	s := tenShipments()[0]
	consistent := lineFor(s)
	consistent.UnitPrice, consistent.Tax = d("10.50"), d("4.41") // 2 x 10.50 x 21 %
	stale := lineFor(s)
	stale.UnitPrice = d("10.50") // tax still 4.20, computed on our price
	for _, tc := range []struct {
		name   string
		line   SBLine
		fields []string
	}{
		{"consistent VAT", consistent, []string{"unitPrice"}},
		{"VAT on our price", stale, []string{"unitPrice", "tax"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := MatchSelfBilling([]SBLine{tc.line}, []SBShipment{s}, DefaultSBTolerance(), shipDay, 30)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range res.Lines[0].Differences {
				got = append(got, d.Field)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.fields) {
				t.Fatalf("differences = %v, want %v", got, tc.fields)
			}
		})
	}
}

func TestMatchSelfBilling_UnmatchedAndDuplicateClaim(t *testing.T) {
	ships := tenShipments()[:1]
	first := lineFor(ships[0])
	second := lineFor(ships[0])
	second.Line = "dup"
	orphan := SBLine{Line: "orphan", DeliveryNote: "DN-X", POLine: "1", Item: "ITEM-A", Quantity: d("1"), UnitPrice: d("1")}
	res, err := MatchSelfBilling([]SBLine{first, second, orphan}, ships, DefaultSBTolerance(), shipDay, 30)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lines[0].Outcome != SelfBillMatched || res.Lines[1].Outcome != SelfBillUnmatched || res.Lines[2].Outcome != SelfBillUnmatched {
		t.Fatalf("lines = %+v", res.Lines)
	}
	if !hasCode(res.Issues, CodeSelfBillUnmatched) {
		t.Fatal("UNMATCHED must raise EDI-0137")
	}
}

func TestMatchSelfBilling_WaitPeriod(t *testing.T) {
	ships := tenShipments()[:1]
	tests := []struct {
		name     string
		asOf     time.Time
		waitDays int
		wantTask bool
	}{
		{"one day before the wait ends", shipDay.AddDate(0, 0, 29), 30, false},
		{"exactly at the wait", shipDay.AddDate(0, 0, 30), 30, true},
		{"short custom wait", shipDay.AddDate(0, 0, 3), 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := MatchSelfBilling(nil, ships, DefaultSBTolerance(), tc.asOf, tc.waitDays)
			if err != nil {
				t.Fatal(err)
			}
			if (len(res.Unbilled) == 1) != tc.wantTask {
				t.Fatalf("unbilled = %v", res.Unbilled)
			}
		})
	}
}

func TestMatchSelfBilling_RejectsBadParameters(t *testing.T) {
	if _, err := MatchSelfBilling(nil, nil, DefaultSBTolerance(), shipDay, 0); err == nil {
		t.Error("wait 0 must be rejected")
	}
	if _, err := MatchSelfBilling(nil, nil, DefaultSBTolerance(), shipDay, 121); err == nil {
		t.Error("wait 121 must be rejected")
	}
	neg := SBTolerance{Quantity: d("-1"), Price: d("0.01")}
	if _, err := MatchSelfBilling(nil, nil, neg, shipDay, 30); err == nil {
		t.Error("negative tolerance must be rejected")
	}
}

// Property: a document that repeats every shipment exactly matches every line and
// leaves nothing unbilled, whatever the quantities and prices.
func TestMatchSelfBilling_PropertyExactDocumentMatches(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "n")
		var ships []SBShipment
		var lines []SBLine
		for i := 0; i < n; i++ {
			q := decimal.NewFromInt(rapid.Int64Range(1, 500).Draw(t, "q"))
			p := decimal.New(rapid.Int64Range(1, 100000).Draw(t, "p"), -2)
			rate := decimal.NewFromInt(rapid.Int64Range(0, 25).Draw(t, "rate"))
			s := SBShipment{ID: fmt.Sprintf("S%d", i), DeliveryNote: fmt.Sprintf("DN%d", i), POLine: "1", Item: "I",
				Quantity: q, UnitPrice: p, TaxRatePct: rate, ShippedOn: shipDay}
			ships = append(ships, s)
			tax := q.Mul(p).Mul(rate).Div(hundred).Round(2)
			lines = append(lines, SBLine{Line: s.ID, DeliveryNote: s.DeliveryNote, POLine: s.POLine, Item: s.Item,
				Quantity: q, UnitPrice: p, Tax: tax})
		}
		res, err := MatchSelfBilling(lines, ships, DefaultSBTolerance(), shipDay, 30)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range res.Lines {
			if l.Outcome != SelfBillMatched {
				t.Fatalf("line %s = %s (%+v)", l.Line, l.Outcome, l.Differences)
			}
		}
		if len(res.Unbilled) != 0 || len(res.Issues) != 0 {
			t.Fatalf("unexpected unbilled %v issues %+v", res.Unbilled, res.Issues)
		}
	})
}
