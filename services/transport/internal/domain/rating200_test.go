package domain

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// rating200 is testdata/rating-200.json, written by testdata/gen_rating_200.py: an
// independent exact-fraction implementation of BR-34.2 (AC-34.2, issue #34).
type rating200 struct {
	Stats map[string]int `json:"stats"`
	Cards []struct {
		RateCard
		ValidFrom string  `json:"validFrom"`
		ValidTo   *string `json:"validTo"`
	} `json:"cards"`
	Cases []struct {
		ID       string `json:"id"`
		Shipment struct {
			OriginZone      string          `json:"originZone"`
			DestinationZone string          `json:"destinationZone"`
			WeightKg        decimal.Decimal `json:"weightKg"`
			VolumeM3        decimal.Decimal `json:"volumeM3"`
			Pallets         int             `json:"pallets"`
			DistanceKm      decimal.Decimal `json:"distanceKm"`
			ShipDate        string          `json:"shipDate"`
			Accessorials    []string        `json:"accessorials"`
		} `json:"shipment"`
		Expected []struct {
			CarrierID       string   `json:"carrierId"`
			ServiceID       string   `json:"serviceId"`
			Status          string   `json:"status"`
			Total           string   `json:"total"`
			Charges         []Charge `json:"charges"`
			WeightBreakStep bool     `json:"weightBreakStep"`
			Deficit         bool     `json:"deficit"`
			MinimumApplied  bool     `json:"minimumApplied"`
		} `json:"expected"`
	} `json:"cases"`
}

func loadRating200(t *testing.T) ([]RateCard, rating200) {
	t.Helper()
	raw, err := os.ReadFile("testdata/rating-200.json")
	if err != nil {
		t.Fatal(err)
	}
	var f rating200
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	cards := make([]RateCard, len(f.Cards))
	for i, c := range f.Cards {
		card := c.RateCard
		card.ValidFrom = mustDay(t, c.ValidFrom)
		if c.ValidTo != nil {
			to := mustDay(t, *c.ValidTo)
			card.ValidTo = &to
		}
		if errs := card.Validate(); len(errs) > 0 {
			t.Fatalf("fixture card %s is not a valid rate card: %v", card.ID, errs)
		}
		cards[i] = card
	}
	return cards, f
}

func mustDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestRating200_RatesToExpectedCharges is AC-34.2: 200 shipments rate-shopped over
// 12 cards (all seven bases) give, per quote and in rank order, the reference's
// status, every charge and the total to the cent, and an explain trace that lists
// the weight-break, minimum, accessorial and fuel steps behind them.
func TestRating200_RatesToExpectedCharges(t *testing.T) {
	cards, f := loadRating200(t)
	if len(f.Cases) != 200 {
		t.Fatalf("fixture has %d shipments, want 200", len(f.Cases))
	}
	quotes := 0
	for _, c := range f.Cases {
		s := Shipment{
			OriginZone: c.Shipment.OriginZone, DestinationZone: c.Shipment.DestinationZone,
			WeightKg: c.Shipment.WeightKg, VolumeM3: c.Shipment.VolumeM3, Pallets: c.Shipment.Pallets,
			DistanceKm: c.Shipment.DistanceKm, ShipDate: mustDay(t, c.Shipment.ShipDate), Accessorials: c.Shipment.Accessorials,
		}
		got := ShopRates(cards, s)
		if len(got) != len(c.Expected) {
			t.Errorf("%s: %d quotes, want %d", c.ID, len(got), len(c.Expected))
			continue
		}
		for i, want := range c.Expected {
			q := got[i]
			quotes++
			where := c.ID + " #" + want.CarrierID + "/" + want.ServiceID
			if q.CarrierID != want.CarrierID || q.ServiceID != want.ServiceID || q.Status != want.Status {
				t.Errorf("%s: rank %d is %s/%s %s, want %s", where, i+1, q.CarrierID, q.ServiceID, q.Status, want.Status)
				continue
			}
			if !q.Total.Equal(dec(want.Total)) {
				t.Errorf("%s: total %s, want %s", where, q.Total, want.Total)
			}
			if len(q.Charges) != len(want.Charges) {
				t.Errorf("%s: charges %+v, want %+v", where, q.Charges, want.Charges)
				continue
			}
			for k, ch := range want.Charges {
				if q.Charges[k].Code != ch.Code || !q.Charges[k].Amount.Equal(ch.Amount) {
					t.Errorf("%s: charge %d = %s %s, want %s %s", where, k, q.Charges[k].Code, q.Charges[k].Amount, ch.Code, ch.Amount)
				}
			}
			if want.Status == QuoteOK {
				checkTrace(t, where, q, want.Charges, want.WeightBreakStep, want.Deficit, want.MinimumApplied, len(c.Shipment.Accessorials))
			} else if !traceStates(q.Trace, want.Status) {
				t.Errorf("%s: trace %+v does not state %s", where, q.Trace, want.Status)
			}
		}
	}
	t.Logf("%d quotes over 200 shipments; reference stats %v", quotes, f.Stats)
}

// traceStates: a NO_VALID_RATE_CARD trace says so (BR-34.10); a NO_LANE_RATE trace
// ends at the failed lane lookup.
func traceStates(trace []Step, status string) bool {
	if len(trace) == 0 {
		return false
	}
	last := trace[len(trace)-1]
	if status == QuoteNoLaneRate {
		return last.Name == "lane_lookup"
	}
	return strings.Contains(last.Input, status)
}

// checkTrace: the explain trace names the steps that produced the charges, and
// the base, minimum, weight-break and fuel steps carry the charged amounts.
func checkTrace(t *testing.T, where string, q Quote, charges []Charge, wbStep, deficit, minApplied bool, requested int) {
	t.Helper()
	base := charges[0].Amount
	var accSteps int
	var sawMin, sawWB, sawFuel bool
	for _, st := range q.Trace {
		switch {
		case st.Name == "minimum":
			sawMin = true
			if !st.Amount.Equal(base) || strings.Contains(st.Input, " applied") != minApplied {
				t.Errorf("%s: minimum step %+v, want amount %s applied=%v", where, st, base, minApplied)
			}
		case st.Name == "weight_break":
			sawWB = true
			if deficit && !st.Amount.Equal(base) {
				t.Errorf("%s: weight-break step %+v, want the deficit base %s", where, st, base)
			}
		case strings.HasPrefix(st.Name, "accessorial"):
			accSteps++
		case strings.HasPrefix(st.Name, "fuel"):
			sawFuel = true
			if fuel := charges[len(charges)-1]; fuel.Code != "FUEL" || !st.Amount.Equal(fuel.Amount) {
				t.Errorf("%s: fuel step %+v, want the FUEL charge", where, st)
			}
		}
	}
	hasFuel := charges[len(charges)-1].Code == "FUEL"
	if !sawMin || sawWB != wbStep || sawFuel != hasFuel || accSteps != requested {
		t.Errorf("%s: trace %+v: minimum %v, weight-break %v (want %v), fuel %v (want %v), accessorial steps %d (want %d)",
			where, q.Trace, sawMin, sawWB, wbStep, sawFuel, hasFuel, accSteps, requested)
	}
}

// TestRating200_FixtureCoversEveryRule: the fixture exercises each rule the AC names
// rather than a lucky subset. The counts are what the reference reports, not targets.
func TestRating200_FixtureCoversEveryRule(t *testing.T) {
	_, f := loadRating200(t)
	for _, k := range []string{"OK", "NO_VALID_RATE_CARD", "NO_LANE_RATE", "deficit", "weightBreakStep", "minimumApplied", "dimGoverns", "halfCentRoundings"} {
		if f.Stats[k] == 0 {
			t.Errorf("fixture exercises no %s case", k)
		}
	}
	bases := map[Basis]bool{}
	cards, _ := loadRating200(t)
	for _, c := range cards {
		bases[c.Basis] = true
	}
	if len(bases) != 7 {
		t.Errorf("fixture cards cover %d of 7 bases", len(bases))
	}
}

// TestRating200_FixtureMatchesReference: the committed fixture is exactly what the
// independent reference writes after its hand-worked self-checks pass, so expected
// charges are never edited by hand or taken from the engine.
func TestRating200_FixtureMatchesReference(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	out := filepath.Join(t.TempDir(), "rating-200.json")
	if msg, err := exec.Command(py, "-I", "testdata/gen_rating_200.py", out).CombinedOutput(); err != nil {
		t.Fatalf("reference generator failed: %v\n%s", err, msg)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/rating-200.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("testdata/rating-200.json differs from the reference output; regenerate it with python3 -I testdata/gen_rating_200.py")
	}
}
