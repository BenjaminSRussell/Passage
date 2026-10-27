package domain

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

// zoneRunDay is the run date every zone test classifies against.
func zoneRunDay() Day { return Day{Time: time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)} }

func zoneAt(offset int) Day { return Day{Time: zoneRunDay().Time.AddDate(0, 0, offset)} }

func TestClassifyZone(t *testing.T) {
	z := ZoneProfile{FirmDays: 14, TradeoffDays: 28}
	cases := []struct {
		offset int
		want   string
	}{
		{-3, ZoneFirm}, // past dates are firm
		{0, ZoneFirm},
		{14, ZoneFirm}, // firm horizon is inclusive
		{15, ZoneTradeoff},
		{28, ZoneTradeoff},
		{29, ZoneForecast},
		{365, ZoneForecast},
	}
	for _, tc := range cases {
		if got := z.ClassifyZone(zoneRunDay(), zoneAt(tc.offset)); got != tc.want {
			t.Fatalf("offset %d: zone %s, want %s", tc.offset, got, tc.want)
		}
	}
}

func TestZoneProfileValidate(t *testing.T) {
	cases := []struct {
		name    string
		z       ZoneProfile
		wantErr bool
	}{
		{"firm within trade-off", ZoneProfile{FirmDays: 14, TradeoffDays: 28}, false},
		{"firm equal to trade-off", ZoneProfile{FirmDays: 14, TradeoffDays: 14}, false},
		{"trade-off before firm", ZoneProfile{FirmDays: 28, TradeoffDays: 14}, true},
		{"negative firm", ZoneProfile{FirmDays: -1, TradeoffDays: 5}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.z.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestCheckScheduleReduction(t *testing.T) {
	cases := []struct {
		name        string
		zone        string
		oldQty      string
		newQty      string
		reason      string
		wantWarn    bool
		wantBlocked bool
	}{
		{"firm reduction without reason is blocked", ZoneFirm, "100", "50", "", false, true},
		{"firm reduction with reason passes", ZoneFirm, "100", "50", "R1", false, false},
		{"trade-off reduction warns", ZoneTradeoff, "100", "50", "", true, false},
		{"forecast reduction is free", ZoneForecast, "100", "50", "", false, false},
		{"firm increase needs nothing", ZoneFirm, "100", "120", "", false, false},
		{"unchanged quantity needs nothing", ZoneTradeoff, "100", "100", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warn, err := CheckScheduleReduction(tc.zone, rpDec(tc.oldQty), rpDec(tc.newQty), tc.reason)
			if (err != nil) != tc.wantBlocked {
				t.Fatalf("error = %v, want blocked %v", err, tc.wantBlocked)
			}
			if warn != tc.wantWarn {
				t.Fatalf("warning = %v, want %v", warn, tc.wantWarn)
			}
		})
	}
}

func TestCheckCumulativeRelease(t *testing.T) {
	if err := CheckCumulativeRelease(rpDec("1200"), rpDec("1300"), false); err != nil {
		t.Fatalf("increase rejected: %v", err)
	}
	if err := CheckCumulativeRelease(rpDec("1200"), rpDec("1000"), false); err == nil {
		t.Fatal("decrease without a reset was accepted")
	}
	if err := CheckCumulativeRelease(rpDec("1200"), rpDec("0"), true); err != nil {
		t.Fatalf("recorded reset rejected: %v", err)
	}
}

// Property: a later schedule date never lies in an earlier zone.
func TestClassifyZoneMonotone(t *testing.T) {
	rank := map[string]int{ZoneFirm: 0, ZoneTradeoff: 1, ZoneForecast: 2}
	rapid.Check(t, func(t *rapid.T) {
		firm := rapid.IntRange(0, 60).Draw(t, "firm")
		trade := rapid.IntRange(firm, 120).Draw(t, "trade")
		z := ZoneProfile{FirmDays: firm, TradeoffDays: trade}
		a := rapid.IntRange(-30, 200).Draw(t, "a")
		b := rapid.IntRange(a, 260).Draw(t, "b")
		if rank[z.ClassifyZone(zoneRunDay(), zoneAt(a))] > rank[z.ClassifyZone(zoneRunDay(), zoneAt(b))] {
			t.Fatalf("zone of %d is later than zone of %d", a, b)
		}
	})
}
