package domain

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

const (
	siteA = "site-a"
	siteB = "site-b"
)

// epochDay returns the days since 1970-01-01 of a calendar date.
func epochDay(y int, m time.Month, d int) int {
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

func cand(id, typ, site, vendor string, day, qty int) FirmCandidate {
	return FirmCandidate{ID: id, OrderType: typ, SiteID: site, VendorID: vendor, DeliveryDay: day, Qty: decimal.NewFromInt(int64(qty))}
}

// summary renders groups as "ids=qty@day" for compact comparison.
func summary(groups []FirmGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, fmt.Sprintf("%v=%s@%d", g.IDs, g.Qty, g.DeliveryDay))
	}
	return out
}

// TestGroupForFirming_AC228 is AC-22.8: three planned purchase orders for one
// vendor on one date firm into one purchase order with three lines.
func TestGroupForFirming_AC228(t *testing.T) {
	day := epochDay(2026, 11, 10)
	groups, err := GroupForFirming([]FirmCandidate{
		cand("p3", OrderPurchase, siteA, "V1", day, 30),
		cand("p1", OrderPurchase, siteA, "V1", day, 100),
		cand("p2", OrderPurchase, siteA, "V1", day, 50),
	}, DefaultGrouping)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || !slices.Equal(groups[0].IDs, []string{"p1", "p2", "p3"}) || groups[0].Qty.String() != "180" {
		t.Fatalf("groups = %v, want one group of p1, p2, p3 for 180", summary(groups))
	}
}

func TestGroupForFirming_Modes(t *testing.T) {
	mon, tue, sun, nextMon := epochDay(2026, 11, 9), epochDay(2026, 11, 10), epochDay(2026, 11, 15), epochDay(2026, 11, 16)
	purchases := []FirmCandidate{
		cand("a", OrderPurchase, siteA, "V1", tue, 10),
		cand("b", OrderPurchase, siteA, "V1", mon, 20),
		cand("c", OrderPurchase, siteA, "V1", sun, 30),
		cand("d", OrderPurchase, siteA, "V1", nextMon, 40),
		cand("e", OrderPurchase, siteA, "V2", tue, 50),
	}
	tests := []struct {
		name string
		in   []FirmCandidate
		mode string
		want []string
	}{
		{"none keeps every order apart", purchases, GroupNone, []string{
			fmt.Sprintf("[b]=20@%d", mon), fmt.Sprintf("[a]=10@%d", tue), fmt.Sprintf("[c]=30@%d", sun), fmt.Sprintf("[d]=40@%d", nextMon), fmt.Sprintf("[e]=50@%d", tue)}},
		{"vendor merges all of a vendor at the earliest delivery", purchases, GroupVendor, []string{
			fmt.Sprintf("[a b c d]=100@%d", mon), fmt.Sprintf("[e]=50@%d", tue)}},
		{"vendor-date merges one delivery day only", purchases, GroupVendorDate, []string{
			fmt.Sprintf("[b]=20@%d", mon), fmt.Sprintf("[a]=10@%d", tue), fmt.Sprintf("[c]=30@%d", sun), fmt.Sprintf("[d]=40@%d", nextMon), fmt.Sprintf("[e]=50@%d", tue)}},
		{"vendor-week merges Monday to Sunday and splits at the next Monday", purchases, GroupVendorWeek, []string{
			fmt.Sprintf("[a b c]=60@%d", mon), fmt.Sprintf("[d]=40@%d", nextMon), fmt.Sprintf("[e]=50@%d", tue)}},
		{"production and transfer orders never merge", []FirmCandidate{
			cand("m1", OrderProduction, siteA, "", tue, 5), cand("m2", OrderProduction, siteA, "", tue, 7), cand("t1", OrderTransfer, siteA, "", tue, 9),
		}, GroupVendor, []string{fmt.Sprintf("[m1]=5@%d", tue), fmt.Sprintf("[m2]=7@%d", tue), fmt.Sprintf("[t1]=9@%d", tue)}},
		{"purchases of two sites do not merge", []FirmCandidate{
			cand("x", OrderPurchase, siteA, "V1", tue, 1), cand("y", OrderPurchase, siteB, "V1", tue, 2),
		}, GroupVendorDate, []string{fmt.Sprintf("[x]=1@%d", tue), fmt.Sprintf("[y]=2@%d", tue)}},
		{"weeks before 1970 still start on Monday", []FirmCandidate{
			cand("p", OrderPurchase, siteA, "V1", epochDay(1969, 12, 29), 1), cand("q", OrderPurchase, siteA, "V1", epochDay(1970, 1, 4), 1),
			cand("r", OrderPurchase, siteA, "V1", epochDay(1970, 1, 5), 1),
		}, GroupVendorWeek, []string{fmt.Sprintf("[p q]=2@%d", epochDay(1969, 12, 29)), fmt.Sprintf("[r]=1@%d", epochDay(1970, 1, 5))}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			groups, err := GroupForFirming(tc.in, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if got := summary(groups); !slices.Equal(got, tc.want) {
				t.Fatalf("groups = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGroupForFirming_InvalidMode(t *testing.T) {
	if _, err := GroupForFirming(nil, "weekly"); !errors.Is(err, ErrInvalidGrouping) {
		t.Fatalf("err = %v, want ErrInvalidGrouping", err)
	}
}

// TestGroupForFirming_Property: every candidate lands in exactly one group,
// group quantities add up to the input, a group never mixes order types,
// sites or vendors, and the grouping does not depend on the input order.
func TestGroupForFirming_Property(t *testing.T) {
	modes := []string{GroupNone, GroupVendor, GroupVendorDate, GroupVendorWeek}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 40).Draw(t, "n")
		cands := make([]FirmCandidate, n)
		total := decimal.Zero
		byID := map[string]FirmCandidate{}
		for i := range cands {
			cands[i] = FirmCandidate{
				ID:          fmt.Sprintf("po-%03d", i),
				OrderType:   rapid.SampledFrom([]string{OrderPurchase, OrderProduction, OrderTransfer}).Draw(t, "type"),
				SiteID:      rapid.SampledFrom([]string{siteA, siteB}).Draw(t, "site"),
				VendorID:    rapid.SampledFrom([]string{"", "V1", "V2"}).Draw(t, "vendor"),
				DeliveryDay: rapid.IntRange(-20, 60).Draw(t, "day"),
				Qty:         decimal.NewFromInt(int64(rapid.IntRange(1, 500).Draw(t, "qty"))),
			}
			total = total.Add(cands[i].Qty)
			byID[cands[i].ID] = cands[i]
		}
		mode := rapid.SampledFrom(modes).Draw(t, "mode")
		groups, err := GroupForFirming(cands, mode)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		sum := decimal.Zero
		for _, g := range groups {
			sum = sum.Add(g.Qty)
			for _, id := range g.IDs {
				if seen[id] {
					t.Fatalf("%s grouped twice", id)
				}
				seen[id] = true
				c := byID[id]
				if c.OrderType != g.OrderType || c.SiteID != g.SiteID || c.VendorID != g.VendorID || c.DeliveryDay < g.DeliveryDay {
					t.Fatalf("%s (%+v) does not belong in group %+v", id, c, g)
				}
			}
			if g.OrderType != OrderPurchase && len(g.IDs) != 1 {
				t.Fatalf("%s orders merged: %v", g.OrderType, g.IDs)
			}
		}
		if len(seen) != n || !sum.Equal(total) {
			t.Fatalf("grouped %d orders for %s, want %d for %s", len(seen), sum, n, total)
		}
		shuffled := slices.Clone(cands)
		for i := len(shuffled) - 1; i > 0; i-- {
			j := rapid.IntRange(0, i).Draw(t, "swap")
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		}
		again, _ := GroupForFirming(shuffled, mode)
		if !slices.Equal(summary(again), summary(groups)) {
			t.Fatalf("grouping depends on input order: %v vs %v", summary(again), summary(groups))
		}
	})
}
