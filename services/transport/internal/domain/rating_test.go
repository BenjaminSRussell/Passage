package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func dec(s string) decimal.Decimal {
	return decimal.RequireFromString(s)
}

func lane(weight string, ship time.Time, acc ...string) Shipment {
	return Shipment{OriginZone: "CHI", DestinationZone: "DAL", WeightKg: dec(weight), VolumeM3: dec("0"), Pallets: 1, ShipDate: ship, Accessorials: acc}
}

func testCard() RateCard {
	to := day(2026, 12, 31)
	hi := dec("100")
	return RateCard{
		ID: "rc-1", CarrierID: "car-a", ServiceID: "ltl", Basis: BasisLane, Currency: "USD",
		ValidFrom: day(2026, 1, 1), ValidTo: &to, DimDivisor: 5000, FuelPct: dec("18.5"), MinimumCharge: dec("15"),
		Rows: []RateRow{
			{Origin: "CHI", Destination: "DAL", BreakFrom: dec("0"), BreakTo: &hi, Rate: dec("0.50"), Unit: UnitKG},
			{Origin: "*", Destination: "*", BreakFrom: dec("100"), Rate: dec("0.40"), Unit: UnitKG},
		},
		Accessorials: []Accessorial{{Code: "LIFTGATE", Flat: dec("25")}, {Code: "RESI", Pct: dec("5")}},
	}
}

func TestRate_ExpectedChargesAndTrace(t *testing.T) {
	tests := []struct {
		name   string
		ship   Shipment
		status string
		total  string
		codes  []string
	}{
		{"band rate plus flat accessorial plus fuel", lane("80", day(2026, 6, 1), "LIFTGATE"), QuoteOK, "72.40", []string{"BASE", "ACC:LIFTGATE", "FUEL"}},
		{"minimum charge lifts small shipment and fuel applies to it", lane("10", day(2026, 6, 1)), QuoteOK, "17.78", []string{"BASE", "FUEL"}},
		{"dimensional weight moves shipment to next band", Shipment{OriginZone: "CHI", DestinationZone: "DAL", WeightKg: dec("20"), VolumeM3: dec("0.5"), Pallets: 1, ShipDate: day(2026, 6, 1)}, QuoteOK, "47.40", []string{"BASE", "FUEL"}},
		{"percent accessorial on base", lane("80", day(2026, 6, 1), "RESI"), QuoteOK, "49.40", []string{"BASE", "ACC:RESI", "FUEL"}},
		{"unknown accessorial is not charged", lane("80", day(2026, 6, 1), "FOO"), QuoteOK, "47.40", []string{"BASE", "FUEL"}},
		{"ship date before card is NO_VALID_RATE_CARD", lane("80", day(2025, 12, 31)), QuoteNoValidRateCard, "0", nil},
		{"ship date after card is NO_VALID_RATE_CARD", lane("80", day(2027, 1, 1)), QuoteNoValidRateCard, "0", nil},
		{"unknown origin is NO_LANE_RATE", Shipment{OriginZone: "XXX", DestinationZone: "DAL", WeightKg: dec("80"), ShipDate: day(2026, 6, 1)}, QuoteNoLaneRate, "0", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := Rate(testCard(), tc.ship)
			if q.Status != tc.status {
				t.Fatalf("status = %s, want %s", q.Status, tc.status)
			}
			if !q.Total.Equal(dec(tc.total)) {
				t.Fatalf("total = %s, want %s", q.Total, tc.total)
			}
			if len(q.Charges) != len(tc.codes) {
				t.Fatalf("charges = %+v, want codes %v", q.Charges, tc.codes)
			}
			for i, code := range tc.codes {
				if q.Charges[i].Code != code {
					t.Fatalf("charge %d = %s, want %s", i, q.Charges[i].Code, code)
				}
			}
			if len(q.Trace) == 0 {
				t.Fatal("explain trace is empty")
			}
		})
	}
}

func TestRate_TraceNamesEachStep(t *testing.T) {
	q := Rate(testCard(), lane("10", day(2026, 6, 1), "LIFTGATE"))
	var names []string
	for _, s := range q.Trace {
		names = append(names, s.Name)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"chargeable_weight", "base", "minimum", "accessorial"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("trace %q lacks step %q", joined, want)
		}
	}
}

func TestOverlaps(t *testing.T) {
	june := day(2026, 6, 30)
	july := day(2026, 7, 1)
	open := func(from time.Time) RateCard {
		return RateCard{CarrierID: "c", ServiceID: "s", Basis: BasisLane, ValidFrom: from}
	}
	closed := func(from, to time.Time) RateCard {
		c := open(from)
		c.ValidTo = &to
		return c
	}
	first := closed(day(2026, 1, 1), june)
	tests := []struct {
		name string
		b    RateCard
		want bool
	}{
		{"shared end day overlaps", closed(june, day(2026, 12, 31)), true},
		{"next day does not overlap", closed(july, day(2026, 12, 31)), false},
		{"open ended later card overlaps", open(day(2026, 3, 1)), true},
		{"other basis never overlaps", otherBasis(closed(june, day(2026, 12, 31))), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Overlaps(first, tc.b); got != tc.want {
				t.Fatalf("Overlaps = %v, want %v", got, tc.want)
			}
		})
	}
}

func otherBasis(c RateCard) RateCard {
	c.Basis = BasisZone
	return c
}

func TestShopRates_CheapestOKFirstInvalidLast(t *testing.T) {
	cheap := testCard()
	cheap.ID, cheap.CarrierID = "cheap", "car-b"
	dear := testCard()
	dear.CarrierID = "car-c"
	dear.FuelPct = dec("50")
	expired := testCard()
	expired.CarrierID = "car-d"
	to := day(2025, 12, 31)
	expired.ValidTo = &to
	quotes := ShopRates([]RateCard{dear, expired, cheap}, lane("80", day(2026, 6, 1)))
	order := []string{quotes[0].CarrierID, quotes[1].CarrierID, quotes[2].CarrierID}
	if order[0] != "car-b" || order[1] != "car-c" || order[2] != "car-d" {
		t.Fatalf("order = %v, want car-b, car-c, car-d", order)
	}
	if quotes[2].Status != QuoteNoValidRateCard {
		t.Fatalf("last quote status = %s", quotes[2].Status)
	}
}

func TestCard_Validate(t *testing.T) {
	if f := testCard().Validate(); len(f) != 0 {
		t.Fatalf("valid card reported %v", f)
	}
	bad := testCard()
	bad.Basis = "BOGUS"
	bad.Rows[0].Unit = "STONE"
	tenTo := dec("10")
	bad.Rows = append(bad.Rows, RateRow{Origin: "A", Destination: "B", Rate: dec("1"), Unit: UnitKG, BreakFrom: dec("50"), BreakTo: &tenTo})
	bad.DimDivisor = 0
	f := bad.Validate()
	for _, key := range []string{"basis", "dimDivisor", "rows[0]", "rows[2]"} {
		if _, ok := f[key]; !ok {
			t.Fatalf("Validate missed %s: %v", key, f)
		}
	}
}

func TestChargeable(t *testing.T) {
	tests := []struct {
		name   string
		weight string
		vol    string
		div    int
		want   string
	}{
		{"actual weight wins", "80", "0.1", 5000, "80"},
		{"dimensional weight wins", "20", "0.5", 5000, "100"},
		{"zero divisor uses default", "20", "0.5", 0, "100"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Chargeable(Shipment{WeightKg: dec(tc.weight), VolumeM3: dec(tc.vol)}, tc.div)
			if !got.Equal(dec(tc.want)) {
				t.Fatalf("Chargeable = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestRate_PropertyDeterministicAndSums checks that rating is deterministic and
// that the total is exactly the sum of the rounded charges (FR-34.3).
func TestRate_PropertyDeterministicAndSums(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cents := rapid.Int64Range(0, 500000).Draw(t, "weightCents")
		weight := decimal.New(cents, -2)
		card := testCard()
		card.FuelPct = decimal.New(rapid.Int64Range(0, 3000).Draw(t, "fuelTenths"), -1)
		s := Shipment{OriginZone: "CHI", DestinationZone: "DAL", WeightKg: weight, VolumeM3: dec("0"), Pallets: 1, ShipDate: day(2026, 6, 1), Accessorials: []string{"LIFTGATE", "RESI"}}
		a, b := Rate(card, s), Rate(card, s)
		if !a.Total.Equal(b.Total) || len(a.Charges) != len(b.Charges) {
			t.Fatalf("non-deterministic: %v vs %v", a.Total, b.Total)
		}
		sum := decimal.Zero
		for _, c := range a.Charges {
			if !c.Amount.Equal(c.Amount.Round(2)) {
				t.Fatalf("charge %s = %s is not in cents", c.Code, c.Amount)
			}
			sum = sum.Add(c.Amount)
		}
		if !sum.Equal(a.Total) {
			t.Fatalf("sum of charges %s != total %s", sum, a.Total)
		}
	})
}

// weightBreakCard prices CHI→DAL at 1.00/kg below 100 kg and 0.70/kg from 100 kg.
func weightBreakCard() RateCard {
	hundredKg := dec("100")
	return RateCard{
		ID: "wb", CarrierID: "car-w", ServiceID: "ltl", Basis: BasisWeightBreak, Currency: "USD",
		ValidFrom: day(2026, 1, 1), DimDivisor: 5000, MinimumCharge: dec("20"),
		Rows: []RateRow{
			{Origin: "CHI", Destination: "DAL", BreakFrom: dec("0"), BreakTo: &hundredKg, Rate: dec("1.00"), Unit: UnitKG},
			{Origin: "CHI", Destination: "DAL", BreakFrom: dec("100"), Rate: dec("0.70"), Unit: UnitKG},
		},
	}
}

// TestRate_WeightBreakDeficit is BR-34.2: a 90 kg shipment costs 90.00 in its own
// band but 70.00 as 100 kg at the next break, so the lower deficit charge applies.
func TestRate_WeightBreakDeficit(t *testing.T) {
	tests := []struct {
		weight string
		total  string
		input  string
	}{
		{"90", "70.00", "is lower"},
		{"60", "60.00", "is not lower"},
		{"150", "105.00", ""},
	}
	for _, tc := range tests {
		t.Run(tc.weight+"kg", func(t *testing.T) {
			q := Rate(weightBreakCard(), lane(tc.weight, day(2026, 6, 1)))
			if q.Status != QuoteOK || !q.Total.Equal(dec(tc.total)) {
				t.Fatalf("quote = %s %s, want OK %s", q.Status, q.Total, tc.total)
			}
			var step *Step
			for i := range q.Trace {
				if q.Trace[i].Name == "weight_break" {
					step = &q.Trace[i]
				}
			}
			if tc.input == "" {
				if step != nil {
					t.Fatalf("the top band has no next break, got step %+v", step)
				}
				return
			}
			if step == nil || !strings.Contains(step.Input, tc.input) {
				t.Fatalf("weight_break step = %+v, want input containing %q", step, tc.input)
			}
		})
	}
}

func TestRate_DistanceBasisPerMileAndKm(t *testing.T) {
	card := testCard()
	card.Basis = BasisDistance
	card.FuelPct = dec("0")
	card.MinimumCharge = dec("0")
	card.Rows = []RateRow{{Origin: "*", Destination: "*", BreakFrom: dec("0"), Rate: dec("2.00"), Unit: UnitMile}}
	s := lane("500", day(2026, 6, 1))
	s.DistanceKm = dec("160.9344")
	if q := Rate(card, s); !q.Total.Equal(dec("200.00")) {
		t.Fatalf("100 miles at 2.00 = %s, want 200.00", q.Total)
	}
	card.Rows[0].Unit = UnitKM
	if q := Rate(card, s); !q.Total.Equal(dec("321.87")) {
		t.Fatalf("160.9344 km at 2.00 = %s, want 321.87", q.Total)
	}
}

// TestShopRates_OneQuotePerCarrierService checks BR-34.2 card selection: a
// superseded card does not add a NO_VALID_RATE_CARD option next to the valid
// one, and a service whose cards have all expired yields exactly one.
func TestShopRates_OneQuotePerCarrierService(t *testing.T) {
	current := testCard()
	old := testCard()
	old.ID = "rc-old"
	oldTo := day(2025, 12, 31)
	old.ValidFrom, old.ValidTo = day(2025, 1, 1), &oldTo
	gone1, gone2 := old, old
	gone1.CarrierID, gone2.CarrierID = "car-z", "car-z"
	gone2.ID = "rc-older"
	quotes := ShopRates([]RateCard{old, current, gone1, gone2}, lane("80", day(2026, 6, 1)))
	if len(quotes) != 2 {
		t.Fatalf("quotes = %d (%+v), want one per carrier service", len(quotes), quotes)
	}
	if quotes[0].CarrierID != "car-a" || quotes[0].Status != QuoteOK {
		t.Fatalf("first = %+v, want car-a OK", quotes[0])
	}
	if quotes[1].CarrierID != "car-z" || quotes[1].Status != QuoteNoValidRateCard {
		t.Fatalf("second = %+v, want car-z NO_VALID_RATE_CARD", quotes[1])
	}
	if !strings.HasPrefix(quotes[1].Trace[0].Input, QuoteNoValidRateCard) {
		t.Fatalf("explain trace must state NO_VALID_RATE_CARD, got %q", quotes[1].Trace[0].Input)
	}
}

func TestRateCard_MarshalsCalendarDates(t *testing.T) {
	c := testCard()
	c.Accessorials = nil
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{`"validFrom":"2026-01-01"`, `"validTo":"2026-12-31"`, `"accessorials":[]`, `"fuelPct":"18.5"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("json %s lacks %s", got, want)
		}
	}
}
