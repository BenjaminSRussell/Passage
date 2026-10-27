package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func shipmentID(n int) string { return fmt.Sprintf("0190f0a0-0000-7000-8000-%012d", n) }

func shipment(n int, weight string) LoadShipment {
	return LoadShipment{ID: shipmentID(n), ShipFromSiteID: shipmentID(900), ShipToRegion: "US-SE", Mode: ModeTL,
		PickupDate: "2026-06-01", WeightKg: decimal.RequireFromString(weight), VolumeM3: decimal.NewFromInt(1), Pallets: 1}
}

var tlRule = Capacity{MaxWeightKg: decimal.NewFromInt(20000), MaxVolumeM3: decimal.NewFromInt(90), MaxPallets: 30}

// TestBuildLoads_AC348: 30 shipments totalling 48,000 kg under a 20,000 kg TL
// rule build 3 loads, none over capacity.
func TestBuildLoads_AC348(t *testing.T) {
	var in []LoadShipment
	for i := 1; i <= 30; i++ {
		in = append(in, shipment(i, "1600"))
	}
	loads, err := BuildLoads(in, tlRule)
	if err != nil {
		t.Fatal(err)
	}
	if len(loads) != 3 {
		t.Fatalf("loads = %d, want 3", len(loads))
	}
	for _, l := range loads {
		if l.TotalWeightKg.GreaterThan(tlRule.MaxWeightKg) || l.Status != LoadPlanned || l.Capacity != tlRule {
			t.Fatalf("load = %+v", l)
		}
	}
}

// TestBuildLoads_SplitsByPalletsAndGroupsByLane: the pallet dimension splits a
// load even under the weight limit, and shipments on different lanes, modes or
// pickup dates never share a load.
func TestBuildLoads_SplitsByPalletsAndGroupsByLane(t *testing.T) {
	cp := Capacity{MaxWeightKg: decimal.NewFromInt(20000), MaxVolumeM3: decimal.NewFromInt(90), MaxPallets: 2}
	a, b, c := shipment(1, "100"), shipment(2, "100"), shipment(3, "100")
	other := shipment(4, "100")
	other.ShipToRegion = "US-NE"
	ltl := shipment(5, "100")
	ltl.Mode = ModeLTL
	later := shipment(6, "100")
	later.PickupDate = "2026-06-02"
	loads, err := BuildLoads([]LoadShipment{a, b, c, other, ltl, later}, cp)
	if err != nil {
		t.Fatal(err)
	}
	if len(loads) != 5 {
		t.Fatalf("loads = %d, want 5 (3 pallets over 2 positions, plus three other groups)", len(loads))
	}
	for _, l := range loads {
		if l.Pallets > 2 {
			t.Fatalf("load over pallet capacity: %+v", l)
		}
	}
}

func TestBuildLoads_RefusesShipmentLargerThanALoad(t *testing.T) {
	_, err := BuildLoads([]LoadShipment{shipment(1, "100"), shipment(2, "20000.000001")}, tlRule)
	var tooLarge *TooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.ShipmentID != shipmentID(2) {
		t.Fatalf("err = %v, want TooLargeError for shipment 2", err)
	}
}

// TestBuildLoads_Properties checks BR-34.8 on random input: every shipment lands
// on exactly one load, no load exceeds any capacity dimension, weight is
// conserved, loads share their group key, and the input order does not matter.
func TestBuildLoads_Properties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 60).Draw(t, "n")
		cp := Capacity{MaxWeightKg: decimal.NewFromInt(10000), MaxVolumeM3: decimal.NewFromInt(60), MaxPallets: 20}
		in := make([]LoadShipment, 0, n)
		sum := decimal.Zero
		for i := 0; i < n; i++ {
			s := LoadShipment{
				ID:             shipmentID(i + 1),
				ShipFromSiteID: shipmentID(900 + rapid.IntRange(1, 2).Draw(t, "site")),
				ShipToRegion:   rapid.SampledFrom([]string{"US-SE", "US-MW"}).Draw(t, "region"),
				Mode:           rapid.SampledFrom(LoadModes).Draw(t, "mode"),
				PickupDate:     rapid.SampledFrom([]string{"2026-06-01", "2026-06-02"}).Draw(t, "date"),
				WeightKg:       decimal.New(int64(rapid.IntRange(0, 1000000).Draw(t, "w")), -2),
				VolumeM3:       decimal.NewFromInt(int64(rapid.IntRange(0, 60).Draw(t, "v"))),
				Pallets:        rapid.IntRange(0, 20).Draw(t, "p"),
			}
			sum = sum.Add(s.WeightKg)
			in = append(in, s)
		}
		loads, err := BuildLoads(in, cp)
		if err != nil {
			t.Fatalf("BuildLoads: %v", err)
		}
		byID := map[string]LoadShipment{}
		for _, s := range in {
			byID[s.ID] = s
		}
		seen := map[string]int{}
		total := decimal.Zero
		for _, l := range loads {
			if l.TotalWeightKg.GreaterThan(cp.MaxWeightKg) || l.TotalVolumeM3.GreaterThan(cp.MaxVolumeM3) || l.Pallets > cp.MaxPallets {
				t.Fatalf("load over capacity: %+v", l)
			}
			for _, id := range l.ShipmentIDs {
				seen[id]++
				if s := byID[id]; s.ShipFromSiteID != l.ShipFromSiteID || s.Mode != l.Mode || s.ShipToRegion != l.ShipToRegion || s.PickupDate != l.PickupDate {
					t.Fatalf("shipment %s does not share the load's group: %+v", id, l)
				}
			}
			total = total.Add(l.TotalWeightKg)
		}
		for _, s := range in {
			if seen[s.ID] != 1 {
				t.Fatalf("shipment %s placed %d times", s.ID, seen[s.ID])
			}
		}
		if !total.Equal(sum) {
			t.Fatalf("weight not conserved: %s != %s", total, sum)
		}
		reversed := make([]LoadShipment, len(in))
		for i, s := range in {
			reversed[len(in)-1-i] = s
		}
		again, _ := BuildLoads(reversed, cp)
		if fmt.Sprint(again) != fmt.Sprint(loads) {
			t.Fatalf("result depends on the input order")
		}
	})
}

func TestValidateLoadBuild(t *testing.T) {
	ok := shipment(1, "10")
	if f := ValidateLoadBuild([]LoadShipment{ok}, tlRule); len(f) != 0 {
		t.Fatalf("valid build rejected: %v", f)
	}
	bad := LoadShipment{ID: "S-1", ShipFromSiteID: "", ShipToRegion: "", Mode: "AIR", PickupDate: "01/06/2026",
		WeightKg: decimal.NewFromInt(-1), VolumeM3: decimal.RequireFromString("0.0000001"), Pallets: -1}
	f := ValidateLoadBuild([]LoadShipment{bad, ok, ok}, Capacity{})
	for _, k := range []string{"capacity.maxWeightKg", "capacity.maxVolumeM3", "capacity.maxPallets", "shipments[0].id", "shipments[0].shipFromSiteId",
		"shipments[0].shipToRegion", "shipments[0].mode", "shipments[0].pickupDate", "shipments[0].weightKg", "shipments[0].volumeM3",
		"shipments[0].pallets", "shipments[2].id"} {
		if f[k] == "" {
			t.Errorf("missing field error %s in %v", k, f)
		}
	}
	if f := ValidateLoadBuild(nil, tlRule); f["shipments"] == "" {
		t.Error("an empty build must be rejected")
	}
}
