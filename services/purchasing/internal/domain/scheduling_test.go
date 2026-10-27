package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func schedDay(y int, m time.Month, d int) Day {
	return Day{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

func schedPtr(d Day) *Day { return &d }

func schedErrCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// weeklySA is the AC-17.11 agreement: qty EA weekly for weeks from 2027-01-04
// under the default profile (firm 14 days, trade-off 28, JIT 14, FRC 90).
func weeklySA(weeks int, qty int64) SchedulingAgreement {
	start := schedDay(2027, time.January, 4)
	sa := SchedulingAgreement{
		ID: "sa-1", Number: "SA-00000001", LegalEntityID: "le", VendorID: "v", ProductID: "p", SiteID: "s",
		Currency: "USD", Unit: "EA", UnitPrice: decimal.RequireFromString("2.50"),
		TargetQty: decimal.NewFromInt(qty * int64(weeks)), ValidFrom: start,
		Profile: DefaultReleaseProfile(), Version: 1,
	}
	for i := 0; i < weeks; i++ {
		sa.Lines = append(sa.Lines, ScheduleLine{LineNo: i + 1, DeliveryDate: Day{start.AddDate(0, 0, 7*i)}, Qty: decimal.NewFromInt(qty)})
	}
	return sa
}

func TestValidateSchedulingAgreement(t *testing.T) {
	end := schedDay(2027, time.January, 10)
	cases := []struct {
		name   string
		mutate func(sa *SchedulingAgreement)
		ok     bool
	}{
		{"valid", func(*SchedulingAgreement) {}, true},
		{"bad currency", func(sa *SchedulingAgreement) { sa.Currency = "US" }, false},
		{"no unit", func(sa *SchedulingAgreement) { sa.Unit = "" }, false},
		{"negative price", func(sa *SchedulingAgreement) { sa.UnitPrice = decimal.NewFromInt(-1) }, false},
		{"negative target", func(sa *SchedulingAgreement) { sa.TargetQty = decimal.NewFromInt(-1) }, false},
		{"valid_to before valid_from", func(sa *SchedulingAgreement) { sa.ValidTo = schedPtr(Day{sa.ValidFrom.AddDate(0, 0, -1)}) }, false},
		{"firm beyond trade-off", func(sa *SchedulingAgreement) { sa.Profile.FirmDays, sa.Profile.TradeoffDays = 40, 10 }, false},
		{"negative horizon", func(sa *SchedulingAgreement) { sa.Profile.JITHorizonDays = -1 }, false},
		{"unknown frequency", func(sa *SchedulingAgreement) { sa.Profile.ReleaseFrequency = "HOURLY" }, false},
		{"no lines", func(sa *SchedulingAgreement) { sa.Lines = nil }, false},
		{"zero qty", func(sa *SchedulingAgreement) { sa.Lines[0].Qty = decimal.Zero }, false},
		{"date before validity", func(sa *SchedulingAgreement) { sa.Lines[0].DeliveryDate = Day{sa.ValidFrom.AddDate(0, 0, -1)} }, false},
		{"date after validity", func(sa *SchedulingAgreement) { sa.ValidTo = &end }, false},
		{"too many lines", func(sa *SchedulingAgreement) {
			sa.Lines = nil
			for i := 0; i <= MaxScheduleLines; i++ {
				sa.Lines = append(sa.Lines, ScheduleLine{LineNo: i + 1, DeliveryDate: sa.ValidFrom, Qty: decimal.NewFromInt(1)})
			}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sa := weeklySA(3, 100)
			tc.mutate(&sa)
			err := sa.Validate()
			if (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && schedErrCode(err) != CodeValidation {
				t.Fatalf("code = %s, want %s", schedErrCode(err), CodeValidation)
			}
		})
	}
}

// TestAC17_11_ReleasesAndZones walks AC-17.11: 100 EA weekly for 12 weeks, firm
// zone 14 days. A JIT release on day 0 shows cumulative released 1,200 and
// received 0; after 100 EA are received against week 1, release 2 on day 8
// shows cumulative received 100; reducing week 2 to 50 needs a reason code.
func TestAC17_11_ReleasesAndZones(t *testing.T) {
	sa := weeklySA(12, 100)
	day0 := schedDay(2027, time.January, 4)

	first, err := BuildRelease(sa, ReleaseJIT, day0, 1, decimal.Zero, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Number != "REL-SA-00000001-1" || first.AgreementVersion != 1 || first.Kind != ReleaseJIT {
		t.Fatalf("release 1 header = %+v", first)
	}
	if !first.CumulativeReleased.Equal(decimal.NewFromInt(1200)) || !first.CumulativeReceived.IsZero() {
		t.Fatalf("release 1 cumulative = %s / %s, want 1200 / 0", first.CumulativeReleased, first.CumulativeReceived)
	}
	// Day 0, 7 and 14 are FIRM, 21 and 28 TRADEOFF, later weeks FORECAST.
	wantZones := []string{ZoneFirm, ZoneFirm, ZoneFirm, ZoneTradeoff, ZoneTradeoff, ZoneForecast}
	for i, z := range wantZones {
		if first.Lines[i].Zone != z {
			t.Fatalf("line %d zone = %s, want %s", i+1, first.Lines[i].Zone, z)
		}
	}
	if !first.Lines[11].CumulativeReleased.Equal(decimal.NewFromInt(1200)) || !first.Lines[0].CumulativeReleased.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("running cumulative: first %s last %s", first.Lines[0].CumulativeReleased, first.Lines[11].CumulativeReleased)
	}

	sa.Lines[0].ReceivedQty = decimal.NewFromInt(100)
	day8 := schedDay(2027, time.January, 12)
	second, err := BuildRelease(sa, ReleaseJIT, day8, 2, first.CumulativeReleased, false)
	if err != nil {
		t.Fatal(err)
	}
	if !second.CumulativeReceived.Equal(decimal.NewFromInt(100)) || !second.CumulativeReleased.Equal(decimal.NewFromInt(1200)) {
		t.Fatalf("release 2 cumulative = %s / %s, want 1200 / 100", second.CumulativeReleased, second.CumulativeReceived)
	}
	if !second.Lines[0].CumulativeReceived.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("line 1 cumulative received = %s, want 100", second.Lines[0].CumulativeReceived)
	}

	week2 := sa.Lines[1]
	cut := func(reason string) []ScheduleChange {
		changes := []ScheduleChange{}
		for _, l := range sa.Lines {
			c := ScheduleChange{LineNo: l.LineNo, DeliveryDate: l.DeliveryDate, Qty: l.Qty}
			if l.LineNo == week2.LineNo {
				c.Qty, c.Reason = decimal.NewFromInt(50), reason
			}
			changes = append(changes, c)
		}
		return changes
	}
	if _, _, err := ApplySchedule(sa, cut(""), day8); schedErrCode(err) != CodeFirmZone {
		t.Fatalf("week 2 to 50 without a reason = %v, want %s", err, CodeFirmZone)
	}
	lines, _, err := ApplySchedule(sa, cut("VENDOR_CAPACITY"), day8)
	if err != nil {
		t.Fatal(err)
	}
	if !lines[1].Qty.Equal(decimal.NewFromInt(50)) || lines[1].ReductionReason != "VENDOR_CAPACITY" {
		t.Fatalf("week 2 = %+v", lines[1])
	}
}

func TestBuildReleaseHorizonValidityAndCumulativeGuard(t *testing.T) {
	sa := weeklySA(4, 10)
	run := schedDay(2027, time.January, 4)
	rel, err := BuildRelease(sa, ReleaseJIT, run, 1, decimal.Zero, false)
	if err != nil {
		t.Fatal(err)
	}
	// JIT horizon 14 days: weeks at day 0, 7 and 14 are inside; day 21 is not.
	for i, want := range []bool{true, true, true, false} {
		if rel.Lines[i].InHorizon != want {
			t.Fatalf("line %d InHorizon = %v, want %v", i+1, rel.Lines[i].InHorizon, want)
		}
	}
	frc, err := BuildRelease(sa, ReleaseFRC, run, 1, decimal.Zero, false)
	if err != nil || !frc.Lines[3].InHorizon {
		t.Fatalf("FRC horizon 90 days should cover day 21: %v %+v", err, frc.Lines)
	}

	// A reduced schedule cannot release below the previous cumulative without a reset.
	sa.Lines[3].Qty = decimal.NewFromInt(1)
	if _, err := BuildRelease(sa, ReleaseJIT, run, 2, decimal.NewFromInt(40), false); schedErrCode(err) != CodeCumulativeDecrease {
		t.Fatalf("cumulative decrease = %v, want %s", err, CodeCumulativeDecrease)
	}
	reset, err := BuildRelease(sa, ReleaseJIT, run, 2, decimal.NewFromInt(40), true)
	if err != nil || !reset.CumulativeReset || !reset.CumulativeReleased.Equal(decimal.NewFromInt(31)) {
		t.Fatalf("reset release = %+v, %v", reset, err)
	}
	if _, err := BuildRelease(sa, "WEEKLY", run, 2, decimal.Zero, false); schedErrCode(err) != CodeValidation {
		t.Fatalf("unknown kind = %v", err)
	}
	if _, err := BuildRelease(sa, ReleaseJIT, schedDay(2027, time.January, 3), 2, decimal.Zero, false); schedErrCode(err) != CodeValidation {
		t.Fatalf("run before validity = %v", err)
	}
	sa.ValidTo = schedPtr(schedDay(2027, time.February, 1))
	if _, err := BuildRelease(sa, ReleaseJIT, schedDay(2027, time.February, 2), 2, decimal.Zero, false); schedErrCode(err) != CodeValidation {
		t.Fatalf("run after validity = %v", err)
	}
}

func TestNextReleaseDue(t *testing.T) {
	from := schedDay(2027, time.January, 4)
	last := schedDay(2027, time.January, 31)
	end := schedDay(2027, time.February, 28)
	cases := []struct {
		freq    string
		lastRun *Day
		validTo *Day
		want    *Day
	}{
		{FrequencyWeekly, nil, nil, schedPtr(from)},
		{FrequencyDaily, &last, nil, schedPtr(schedDay(2027, time.February, 1))},
		{FrequencyWeekly, &last, nil, schedPtr(schedDay(2027, time.February, 7))},
		{FrequencyMonthly, &last, nil, schedPtr(schedDay(2027, time.March, 3))},
		{FrequencyMonthly, &last, &end, nil},
	}
	for _, tc := range cases {
		p := DefaultReleaseProfile()
		p.ReleaseFrequency = tc.freq
		got := p.NextReleaseDue(from, tc.validTo, tc.lastRun)
		if (got == nil) != (tc.want == nil) || (got != nil && !got.Equal(tc.want.Time)) {
			t.Errorf("%s after %v = %v, want %v", tc.freq, tc.lastRun, got, tc.want)
		}
	}
}

// The cumulative released figure of a release is the running sum of the
// schedule in date order: it never decreases along the lines and ends at Σ qty.
func TestReleaseCumulativeIsRunningSumProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 40).Draw(t, "lines")
		sa := weeklySA(n, 1)
		total, received := decimal.Zero, decimal.Zero
		for i := range sa.Lines {
			q := rapid.Int64Range(1, 1000).Draw(t, "qty")
			r := rapid.Int64Range(0, q).Draw(t, "received")
			sa.Lines[i].Qty, sa.Lines[i].ReceivedQty = decimal.NewFromInt(q), decimal.NewFromInt(r)
			total, received = total.Add(sa.Lines[i].Qty), received.Add(sa.Lines[i].ReceivedQty)
		}
		// Shuffle line order: the release sorts by date, not by input order.
		perm := rapid.Permutation(sa.Lines).Draw(t, "order")
		sa.Lines = perm
		rel, err := BuildRelease(sa, ReleaseFRC, schedDay(2027, time.January, 4), 1, decimal.Zero, false)
		if err != nil {
			t.Fatal(err)
		}
		if !rel.CumulativeReleased.Equal(total) || !rel.CumulativeReceived.Equal(received) {
			t.Fatalf("cumulative %s / %s, want %s / %s", rel.CumulativeReleased, rel.CumulativeReceived, total, received)
		}
		prev := decimal.Zero
		for i, l := range rel.Lines {
			if l.CumulativeReleased.LessThan(prev) || l.CumulativeReceived.GreaterThan(l.CumulativeReleased) {
				t.Fatalf("line %d: cumulative %s after %s, received %s", l.LineNo, l.CumulativeReleased, prev, l.CumulativeReceived)
			}
			if i > 0 && l.DeliveryDate.Before(rel.Lines[i-1].DeliveryDate.Time) {
				t.Fatalf("lines out of date order at %d", l.LineNo)
			}
			prev = l.CumulativeReleased
		}
	})
}
