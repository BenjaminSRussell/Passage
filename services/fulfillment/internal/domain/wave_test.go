package domain

import (
	"errors"
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

func cand(id, order, customer string, lines ...string) WaveCandidate {
	return WaveCandidate{ShipmentID: id, OrderID: order, LegalEntityID: "le-1", Customer: customer,
		Carrier: "UPS", Zone: "A", ProductID: "p-" + id, LineIDs: lines}
}

func TestPlanWaves_Table(t *testing.T) {
	tests := []struct {
		name    string
		cands   []WaveCandidate
		groupBy string
		lim     WaveLimits
		want    []int // line count per wave
		wantErr error
	}{
		{"empty", nil, GroupCustomer, WaveLimits{200, 50}, []int{}, nil},
		{"one group fits", []WaveCandidate{cand("s1", "o1", "C1", "l1", "l2")}, GroupCustomer, WaveLimits{200, 50}, []int{2}, nil},
		{"splits by line limit", []WaveCandidate{
			cand("s1", "o1", "C1", "l1", "l2"), cand("s2", "o2", "C1", "l3", "l4"), cand("s3", "o3", "C1", "l5"),
		}, GroupCustomer, WaveLimits{3, 50}, []int{2, 3}, nil},
		{"splits by order limit", []WaveCandidate{
			cand("s1", "o1", "C1", "l1"), cand("s2", "o2", "C1", "l2"), cand("s3", "o3", "C1", "l3"),
		}, GroupCustomer, WaveLimits{200, 2}, []int{2, 1}, nil},
		{"group key separates waves", []WaveCandidate{
			cand("s1", "o1", "C2", "l1"), cand("s2", "o2", "C1", "l2"),
		}, GroupCustomer, WaveLimits{200, 50}, []int{1, 1}, nil},
		{"shipment too large", []WaveCandidate{cand("s1", "o1", "C1", "l1", "l2", "l3")}, GroupCustomer, WaveLimits{2, 50}, nil, ErrWaveTooLarge},
		{"bad group", nil, "COLOUR", WaveLimits{2, 50}, nil, ErrGroupBy},
		{"bad limits", nil, GroupCustomer, WaveLimits{0, 50}, nil, ErrWaveLimits},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plans, err := PlanWaves(tc.cands, tc.groupBy, tc.lim)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(plans) != len(tc.want) {
				t.Fatalf("waves = %d, want %d", len(plans), len(tc.want))
			}
			for i, w := range plans {
				if len(w.LineIDs) != tc.want[i] {
					t.Fatalf("wave %d lines = %d, want %d", i, len(w.LineIDs), tc.want[i])
				}
			}
		})
	}
}

func TestGroupKey_Unassigned(t *testing.T) {
	k, err := GroupKey(WaveCandidate{}, GroupCarrier)
	if err != nil || k != UnassignedGroup {
		t.Fatalf("got %q, %v; want %q", k, err, UnassignedGroup)
	}
}

// Property: every wave respects both limits, holds one group key, and every
// candidate shipment lands in exactly one wave with all its lines intact.
func TestPlanWaves_Properties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxLines := rapid.IntRange(5, 25).Draw(t, "maxLines")
		maxOrders := rapid.IntRange(1, 6).Draw(t, "maxOrders")
		n := rapid.IntRange(0, 40).Draw(t, "n")
		cands := make([]WaveCandidate, 0, n)
		lineTotal := 0
		for i := 0; i < n; i++ {
			nl := rapid.IntRange(1, 5).Draw(t, "lines")
			lines := make([]string, nl)
			for j := range lines {
				lines[j] = fmt.Sprintf("l-%d-%d", i, j)
			}
			lineTotal += nl
			c := cand(fmt.Sprintf("s-%03d", i), fmt.Sprintf("o-%d", rapid.IntRange(0, 9).Draw(t, "order")),
				fmt.Sprintf("C%d", rapid.IntRange(0, 3).Draw(t, "cust")), lines...)
			cands = append(cands, c)
		}
		plans, err := PlanWaves(cands, GroupCustomer, WaveLimits{MaxLines: maxLines, MaxOrders: maxOrders})
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		seen := map[string]int{}
		total := 0
		for _, w := range plans {
			if len(w.LineIDs) > maxLines || len(w.OrderIDs) > maxOrders || len(w.LineIDs) == 0 {
				t.Fatalf("wave breaks limits: lines=%d orders=%d", len(w.LineIDs), len(w.OrderIDs))
			}
			for _, s := range w.ShipmentIDs {
				seen[s]++
			}
			total += len(w.LineIDs)
		}
		for _, c := range cands {
			if seen[c.ShipmentID] != 1 {
				t.Fatalf("shipment %s in %d waves", c.ShipmentID, seen[c.ShipmentID])
			}
		}
		if total != lineTotal {
			t.Fatalf("lines planned %d, candidates had %d", total, lineTotal)
		}
	})
}

// AC-28.10: 60 released lines for carrier UPS across 12 customers, grouped by
// carrier with WAVE_MAX_LINES 20, give waves of at most 20 lines, no shipment
// split across waves, and every line planned once.
func TestPlanWaves_AC2810(t *testing.T) {
	var cands []WaveCandidate
	for c := 0; c < 12; c++ {
		// Two shipments per customer: 3 lines and 2 lines.
		for s, n := range []int{3, 2} {
			lines := make([]string, n)
			for j := range lines {
				lines[j] = fmt.Sprintf("l-%02d-%d-%d", c, s, j)
			}
			cands = append(cands, cand(fmt.Sprintf("s-%02d-%d", c, s), fmt.Sprintf("o-%02d", c), fmt.Sprintf("C%02d", c), lines...))
		}
	}
	for _, group := range []string{GroupCarrier, GroupCustomer} {
		plans, err := PlanWaves(cands, group, WaveLimits{MaxLines: 20, MaxOrders: 50})
		if err != nil {
			t.Fatal(err)
		}
		total, seen := 0, map[string]bool{}
		for _, w := range plans {
			if len(w.LineIDs) > 20 {
				t.Fatalf("%s: wave has %d lines", group, len(w.LineIDs))
			}
			for _, s := range w.ShipmentIDs {
				if seen[s] {
					t.Fatalf("%s: shipment %s in two waves", group, s)
				}
				seen[s] = true
			}
			total += len(w.LineIDs)
		}
		if total != 60 || len(seen) != 24 {
			t.Fatalf("%s: planned %d lines of %d shipments, want 60 of 24", group, total, len(seen))
		}
		if group == GroupCarrier && len(plans) != 3 {
			t.Fatalf("by carrier: %d waves, want 3 full waves of 20", len(plans))
		}
		if group == GroupCustomer && len(plans) != 12 {
			t.Fatalf("by customer: %d waves, want one per customer", len(plans))
		}
	}
}

func TestPlanWaves_NeverMixesLegalEntities(t *testing.T) {
	a := cand("s1", "o1", "C1", "l1")
	b := cand("s2", "o2", "C1", "l2")
	b.LegalEntityID = "le-2"
	c := cand("s3", "o3", "C1", "l3")
	plans, err := PlanWaves([]WaveCandidate{a, b, c}, GroupCustomer, WaveLimits{MaxLines: 200, MaxOrders: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 || len(plans[0].ShipmentIDs) != 2 || plans[1].LegalEntityID != "le-2" {
		t.Fatalf("plans = %+v, want s1+s3 for le-1 then s2 for le-2", plans)
	}
}
