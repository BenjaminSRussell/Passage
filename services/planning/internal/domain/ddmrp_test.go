package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// BR-22.11 worked example: ADU 10, DLT 10, LTF 0.5, VF 0.5, MOQ 0, order cycle 7.
var br2211Profile = BufferProfile{
	LeadTimeFactor: dec("0.5"), VariabilityFactor: dec("0.5"), OrderCycleDays: 7,
	MinOrderQty: decimal.Zero, SpikeThresholdPct: dec("50"),
}

func TestComputeZones_WorkedExample(t *testing.T) {
	z, err := ComputeZones(br2211Profile, dec("10"), dec("10"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]decimal.Decimal{
		"redBase": dec("50"), "redSafety": dec("25"), "yellow": dec("100"), "green": dec("70"),
		"topOfRed": dec("75"), "topOfYellow": dec("175"), "topOfGreen": dec("245"),
	}
	got := map[string]decimal.Decimal{
		"redBase": z.RedBase, "redSafety": z.RedSafety, "yellow": z.Yellow, "green": z.Green,
		"topOfRed": z.TopOfRed, "topOfYellow": z.TopOfYellow, "topOfGreen": z.TopOfGreen,
	}
	for k, v := range want {
		if !got[k].Equal(v) {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
}

func TestComputeZones_GreenTakesMaxOfThree(t *testing.T) {
	tests := []struct {
		name string
		p    BufferProfile
		adu  string
		dlt  string
		want string
	}{
		{"red base wins", BufferProfile{LeadTimeFactor: dec("1"), OrderCycleDays: 1, MinOrderQty: decimal.Zero}, "10", "10", "100"},
		{"MOQ wins", BufferProfile{LeadTimeFactor: dec("0.5"), OrderCycleDays: 0, MinOrderQty: dec("500")}, "10", "10", "500"},
		{"order cycle wins", BufferProfile{LeadTimeFactor: dec("0.5"), OrderCycleDays: 30, MinOrderQty: decimal.Zero}, "10", "10", "300"},
		{"zero usage", br2211Profile, "0", "10", "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			z, err := ComputeZones(tt.p, dec(tt.adu), dec(tt.dlt))
			if err != nil {
				t.Fatal(err)
			}
			if !z.Green.Equal(dec(tt.want)) {
				t.Fatalf("green = %s, want %s", z.Green, tt.want)
			}
		})
	}
}

func TestComputeZones_RejectsNegativeInputs(t *testing.T) {
	bad := []struct {
		name string
		p    BufferProfile
		adu  string
		dlt  string
	}{
		{"negative ADU", br2211Profile, "-1", "10"},
		{"negative DLT", br2211Profile, "10", "-1"},
		{"negative LTF", BufferProfile{LeadTimeFactor: dec("-0.1")}, "10", "10"},
		{"negative order cycle", BufferProfile{OrderCycleDays: -1}, "10", "10"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ComputeZones(tt.p, dec(tt.adu), dec(tt.dlt)); !errors.Is(err, ErrInvalidBuffer) {
				t.Fatalf("err = %v, want ErrInvalidBuffer", err)
			}
		})
	}
}

func TestEvaluateBuffer_NetFlowExample(t *testing.T) {
	st, err := EvaluateBuffer(BufferInput{
		Profile: br2211Profile, ADU: dec("10"), DLT: dec("10"), DAF: dec("1"),
		OnHand: dec("90"), OpenSupply: dec("60"),
		Demand:           []DayDemand{{Day: 0, Qty: dec("20")}},
		SpikeHorizonDays: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st.QualifiedDemand.Equal(dec("20")) || !st.NetFlow.Equal(dec("130")) {
		t.Fatalf("qualified %s nfp %s, want 20 and 130", st.QualifiedDemand, st.NetFlow)
	}
	if !st.PlannedQty.Equal(dec("115")) {
		t.Fatalf("planned = %s, want 115", st.PlannedQty)
	}
	if !st.Priority.Equal(dec("0.5306")) {
		t.Fatalf("priority = %s, want 0.5306", st.Priority)
	}
	if st.Zone != ZoneYellow {
		t.Fatalf("zone = %s, want yellow", st.Zone)
	}
}

func TestEvaluateBuffer_ZonesAndOrders(t *testing.T) {
	base := BufferInput{Profile: br2211Profile, ADU: dec("10"), DLT: dec("10"), DAF: dec("1"), SpikeHorizonDays: 10}
	tests := []struct {
		name    string
		onHand  string
		open    string
		zone    string
		planned string
	}{
		{name: "red zone orders to top of green", onHand: "40", open: "0", zone: ZoneRed, planned: "205"},
		{name: "exactly at TOR is red", onHand: "75", open: "0", zone: ZoneRed, planned: "170"},
		{name: "exactly at TOY is yellow and orders", onHand: "175", open: "0", zone: ZoneYellow, planned: "70"},
		{name: "above TOY is green, no order", onHand: "200", open: "0", zone: ZoneGreen, planned: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			in.OnHand, in.OpenSupply = dec(tt.onHand), dec(tt.open)
			st, err := EvaluateBuffer(in)
			if err != nil {
				t.Fatal(err)
			}
			if st.Zone != tt.zone {
				t.Errorf("zone = %s, want %s", st.Zone, tt.zone)
			}
			if !st.PlannedQty.Equal(dec(tt.planned)) {
				t.Errorf("planned = %s, want %s", st.PlannedQty, tt.planned)
			}
		})
	}
}

func TestEvaluateBuffer_MOQAndMultiple(t *testing.T) {
	tests := []struct {
		name          string
		moq, multiple string
		want          string
	}{
		// MOQ is a minimum, not a multiple: 115 stays 115 (it was rounded to 150 before).
		{"MOQ below the need leaves it", "50", "0", "115"},
		// MOQ 200 widens green to 200, so TOG is 375 and the order 245 (>= MOQ by construction).
		{"MOQ widens the green zone", "200", "0", "245"},
		{"multiple rounds up", "0", "50", "150"},
		// Green 120, TOG 295, need 165, rounded up to a multiple of 25.
		{"MOQ then multiple", "120", "25", "175"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := br2211Profile
			p.MinOrderQty = dec(tt.moq)
			st, err := EvaluateBuffer(BufferInput{Profile: p, ADU: dec("10"), DLT: dec("10"), DAF: dec("1"),
				OnHand: dec("90"), OpenSupply: dec("60"), Demand: []DayDemand{{Day: 0, Qty: dec("20")}}, SpikeHorizonDays: 10,
				OrderMultiple: dec(tt.multiple)})
			if err != nil {
				t.Fatal(err)
			}
			if !st.PlannedQty.Equal(dec(tt.want)) {
				t.Fatalf("planned = %s, want %s", st.PlannedQty, tt.want)
			}
		})
	}
}

func TestEvaluateBuffer_DAFScalesUsage(t *testing.T) {
	st, err := EvaluateBuffer(BufferInput{Profile: br2211Profile, ADU: dec("10"), DLT: dec("10"), DAF: dec("1.5"),
		SpikeHorizonDays: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !st.AdjustedADU.Equal(dec("15")) || !st.Zones.TopOfGreen.Equal(dec("367.5")) {
		t.Fatalf("adjusted ADU %s, TOG %s; want 15 and 367.5", st.AdjustedADU, st.Zones.TopOfGreen)
	}
}

func TestEvaluateBuffer_RejectsBadInput(t *testing.T) {
	if _, err := EvaluateBuffer(BufferInput{Profile: br2211Profile, ADU: dec("10"), DLT: dec("10"), DAF: dec("-1")}); !errors.Is(err, ErrInvalidBuffer) {
		t.Fatalf("negative DAF: err = %v", err)
	}
	if _, err := EvaluateBuffer(BufferInput{Profile: br2211Profile, ADU: dec("10"), DLT: dec("10"), DAF: dec("1"), SpikeHorizonDays: -2}); !errors.Is(err, ErrInvalidBuffer) {
		t.Fatalf("negative horizon: err = %v", err)
	}
}

func TestQualifiedDemand_SpikesAndPastDue(t *testing.T) {
	// Red zone 75, threshold 50% => spikes are days with demand above 37.5.
	demand := []DayDemand{
		{Day: -3, Qty: dec("10")}, // past due: always qualified
		{Day: 0, Qty: dec("5")},   // due today: always qualified
		{Day: 2, Qty: dec("40")},  // spike inside horizon 5: qualified
		{Day: 3, Qty: dec("30")},  // below threshold: not qualified
		{Day: 9, Qty: dec("90")},  // spike outside horizon 5: not qualified
	}
	got := QualifiedDemand(demand, dec("75"), dec("50"), 5)
	if !got.Equal(dec("55")) {
		t.Fatalf("qualified = %s, want 55", got)
	}
}

// A spike is judged on the day's total demand, not on each order line: two
// lines of 20 on one day (40 > 37.5) are a spike; one line of 20 is not.
func TestQualifiedDemand_SpikeIsADaysTotal(t *testing.T) {
	demand := []DayDemand{
		{Day: 2, Qty: dec("20")},
		{Day: 2, Qty: dec("20")},
		{Day: 4, Qty: dec("20")},
	}
	if got := QualifiedDemand(demand, dec("75"), dec("50"), 5); !got.Equal(dec("40")) {
		t.Fatalf("qualified = %s, want 40", got)
	}
}

func TestAverageDailyUsage(t *testing.T) {
	hist := []DayDemand{{Day: -90, Qty: dec("900")}, {Day: -1, Qty: dec("90")}, {Day: -91, Qty: dec("999")}, {Day: 0, Qty: dec("10")}}
	adu, err := AverageDailyUsage(hist, 90, 1)
	if err != nil {
		t.Fatal(err)
	}
	// window -90..-1 plus day 0: (900 + 90 + 10) / 91
	if !adu.Equal(dec("1000").Div(dec("91"))) {
		t.Fatalf("adu = %s", adu)
	}
	if _, err := AverageDailyUsage(hist, 0, 0); !errors.Is(err, ErrInvalidBuffer) {
		t.Fatalf("empty window: err = %v", err)
	}
}

func TestPriorityOrder_MostUrgentFirst(t *testing.T) {
	got := PriorityOrder([]decimal.Decimal{dec("0.9"), dec("0.2"), dec("0.2"), dec("0.5")})
	want := []int{1, 2, 3, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestDLTDays_RoundsUp(t *testing.T) {
	if DLTDays(dec("10")) != 10 || DLTDays(dec("10.01")) != 11 || DLTDays(dec("0")) != 0 {
		t.Fatal("DLTDays must round up to whole days")
	}
}

// Property: the tops of zones are ordered TOR <= TOY <= TOG and a planned
// order lifts NFP to at least TOG whenever it is placed.
func TestProperty_ZonesOrderedAndOrderLiftsToTOG(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		adu := rapid.IntRange(0, 500).Draw(t, "adu")
		dlt := rapid.IntRange(0, 60).Draw(t, "dlt")
		ltf := rapid.IntRange(0, 100).Draw(t, "ltf")
		vf := rapid.IntRange(0, 100).Draw(t, "vf")
		moq := rapid.IntRange(0, 300).Draw(t, "moq")
		cycle := rapid.IntRange(0, 60).Draw(t, "cycle")
		onHand := rapid.IntRange(-200, 2000).Draw(t, "onHand")
		open := rapid.IntRange(0, 500).Draw(t, "open")
		p := BufferProfile{
			LeadTimeFactor: decimal.NewFromInt(int64(ltf)).Div(hundred), VariabilityFactor: decimal.NewFromInt(int64(vf)).Div(hundred),
			OrderCycleDays: cycle, MinOrderQty: decimal.NewFromInt(int64(moq)), SpikeThresholdPct: dec("50"),
		}
		in := BufferInput{Profile: p, ADU: decimal.NewFromInt(int64(adu)), DLT: decimal.NewFromInt(int64(dlt)), DAF: decimal.NewFromInt(1),
			OnHand: decimal.NewFromInt(int64(onHand)), OpenSupply: decimal.NewFromInt(int64(open)), SpikeHorizonDays: dlt}
		st, err := EvaluateBuffer(in)
		if err != nil {
			t.Fatal(err)
		}
		z := st.Zones
		if z.TopOfRed.GreaterThan(z.TopOfYellow) || z.TopOfYellow.GreaterThan(z.TopOfGreen) {
			t.Fatalf("zones not ordered: %+v", z)
		}
		if st.PlannedQty.IsPositive() {
			lifted := st.NetFlow.Add(st.PlannedQty)
			if lifted.LessThan(z.TopOfGreen) {
				t.Fatalf("planned order %s does not reach TOG %s from NFP %s", st.PlannedQty, z.TopOfGreen, st.NetFlow)
			}
		}
	})
}
