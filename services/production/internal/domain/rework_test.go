package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCheckReworkSource(t *testing.T) {
	tests := []struct {
		name            string
		product, origin string
		qty, raf        string
		ok              bool
	}{
		{"same product within the reported output", "FG", "FG", "5", "8", true},
		{"all the reported output", "FG", "FG", "8", "8", true},
		{"more than the original reported", "FG", "FG", "8.000001", "8", false},
		{"another product", "FG", "RM", "1", "8", false},
		{"an original with nothing reported", "FG", "FG", "1", "0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckReworkSource(tt.product, d(tt.qty), tt.origin, d(tt.raf))
			if (err == nil) != tt.ok {
				t.Fatalf("CheckReworkSource = %v, want ok=%v", err, tt.ok)
			}
			if err != nil && err.(*Error).Code != CodeReworkReference {
				t.Fatalf("code %s", err.(*Error).Code)
			}
		})
	}
}

func TestValidTrackingID(t *testing.T) {
	for s, ok := range map[string]bool{"S-1": true, "B-2026-07": true, "": false, " S-1": false, "S-1\t": false,
		strings.Repeat("x", MaxTrackingID): true, strings.Repeat("x", MaxTrackingID+1): false} {
		if ValidTrackingID(s) != ok {
			t.Errorf("ValidTrackingID(%q) = %v, want %v", s, !ok, ok)
		}
	}
}

// The reworked units come back at the planned cost of the order's own
// product line; components, labor and overhead stay in WIP.
func TestReworkReceiptBase(t *testing.T) {
	planned := []CostRow{
		{Group: GroupMaterial, Key: "FG", Amount: d("210")},
		{Group: GroupMaterial, Key: "SEAL", Amount: d("12")},
		{Group: GroupLabor, Key: "FG", Amount: d("60")},
	}
	if got := ReworkReceiptBase(planned, "FG"); !got.Equal(d("210")) {
		t.Fatalf("base = %s, want 210", got)
	}
	if got := ReworkReceiptBase(planned, "OTHER"); !got.IsZero() {
		t.Fatalf("without a product line the base is %s, want 0", got)
	}
}

func TestWIPGroup(t *testing.T) {
	for typ, want := range map[string]CostGroup{PostWIPMaterial: GroupMaterial, PostWIPReceipt: GroupMaterial,
		PostWIPLabor: GroupLabor, PostWIPOverhead: GroupOverhead} {
		if g, ok := WIPGroup(typ); !ok || g != want {
			t.Errorf("WIPGroup(%s) = %s %v, want %s", typ, g, ok, want)
		}
	}
	for _, typ := range []string{PostInventoryIssue, PostFinishedGoods, PostWIPSettlement, PostLaborAbsorb} {
		if _, ok := WIPGroup(typ); ok {
			t.Errorf("WIPGroup(%s) maps a non-cost row", typ)
		}
	}
}

// AC-21.15 in the domain: 2.00 h at 30.00/h is a 60.00 rework variance, and
// the variances always sum to the WIP balance they clear (property).
func TestReworkVariances(t *testing.T) {
	got := ReworkVariances(map[CostGroup]decimal.Decimal{GroupMaterial: decimal.Zero, GroupLabor: d("60")})
	if len(got) != 1 || got[0].Type != VarRework || got[0].Group != GroupLabor || !got[0].Amount.Equal(d("60")) {
		t.Fatalf("variances = %+v", got)
	}
	if got := ReworkVariances(nil); len(got) != 0 {
		t.Fatalf("no balance gave %+v", got)
	}
	rapid.Check(t, func(t *rapid.T) {
		byGroup := map[CostGroup]decimal.Decimal{}
		total := decimal.Zero
		for _, g := range []CostGroup{GroupMaterial, GroupLabor, GroupOverhead, GroupSubcontract} {
			v := decimal.New(rapid.Int64Range(-1_000_000, 1_000_000).Draw(t, string(g)), -2)
			byGroup[g] = v
			total = total.Add(v)
		}
		vars := ReworkVariances(byGroup)
		sum := decimal.Zero
		for _, v := range vars {
			if v.Type != VarRework || v.Amount.IsZero() {
				t.Fatalf("variance %+v", v)
			}
			sum = sum.Add(v.Amount)
		}
		if !sum.Equal(total) {
			t.Fatalf("variances sum to %s, balance %s", sum, total)
		}
		p := Profile{ItemGroup: AllKey, SiteID: AllKey, WIP: "1450", VarRework: "5650"}
		ps, err := SettlementPostings(p, total, vars)
		if err != nil || !Balanced(ps) {
			t.Fatalf("settlement postings: %v %+v", err, ps)
		}
	})
}

// Ending a rework order with a rework cost needs the rework variance account.
func TestSettlementPostings_ReworkAccountRequired(t *testing.T) {
	vars := []Variance{{Type: VarRework, Group: GroupLabor, Amount: d("60")}}
	_, err := SettlementPostings(Profile{ItemGroup: AllKey, SiteID: AllKey, WIP: "1450"}, d("60"), vars)
	if err == nil || err.(*Error).Code != CodeNoPostingProfile {
		t.Fatalf("missing rework account: %v", err)
	}
	ps, err := SettlementPostings(Profile{ItemGroup: AllKey, SiteID: AllKey, WIP: "1450", VarRework: "5650"}, d("60"), vars)
	if err != nil || len(ps) != 2 || ps[1].Account != "5650" || !ps[1].Dr.Equal(d("60")) || !ps[0].Cr.Equal(d("60")) {
		t.Fatalf("postings: %+v %v", ps, err)
	}
}
