package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func TestEvaluateVariable(t *testing.T) {
	lo, hi := decimal.RequireFromString("9.95"), decimal.RequireFromString("10.05")
	line := TestLine{Kind: KindVariable, Decimals: 1, Min: &lo, Max: &hi}
	cases := []struct {
		value  string
		rounds string
		pass   bool
	}{
		{"10.04", "10.0", true},
		{"10.06", "10.1", false},
		{"9.95", "10.0", true},   // bounds are inclusive
		{"10.049", "10.0", true}, // rounds to 10.0 before the comparison
		{"10.05", "10.1", false}, // 10.05 rounds half-up to 10.1 and is above max
	}
	for _, c := range cases {
		rounded, pass := EvaluateVariable(line, decimal.RequireFromString(c.value))
		if !rounded.Equal(decimal.RequireFromString(c.rounds)) || pass != c.pass {
			t.Errorf("value %s: got %s %v, want %s %v", c.value, rounded, pass, c.rounds, c.pass)
		}
	}
	// Half-up is symmetric around zero: -1.25 rounds to -1.3, as 1.25 rounds to 1.3.
	neg := decimal.RequireFromString("-1.3")
	cold := TestLine{Kind: KindVariable, Decimals: 1, Max: &neg}
	if rounded, pass := EvaluateVariable(cold, decimal.RequireFromString("-1.25")); !rounded.Equal(neg) || !pass {
		t.Errorf("-1.25 at one decimal: got %s %v, want -1.3 true", rounded, pass)
	}
	open := TestLine{Kind: KindVariable, Decimals: 0}
	if _, pass := EvaluateVariable(open, decimal.NewFromInt(-1000)); !pass {
		t.Error("an open range accepts any value")
	}
}

func TestEvaluateAttribute(t *testing.T) {
	pass, fail := ids.New(), ids.New()
	line := TestLine{Kind: KindAttribute, PassingOutcomes: []string{pass}}
	if !EvaluateAttribute(line, pass) || EvaluateAttribute(line, fail) {
		t.Error("only passing outcomes pass")
	}
}

func TestValidateTestGroup(t *testing.T) {
	a := ids.New()
	if f := ValidateTestGroup("GRP-1", []TestLine{{TestID: a, Kind: KindAttribute, PassingOutcomes: []string{ids.New()}}}); len(f) != 0 {
		t.Errorf("valid group: %v", f)
	}
	dup := []TestLine{{TestID: a, Kind: KindAttribute, PassingOutcomes: []string{ids.New()}}, {TestID: a, Kind: KindAttribute, PassingOutcomes: []string{ids.New()}}}
	if f := ValidateTestGroup("g", dup); f["code"] == "" || f["tests[1].testId"] == "" {
		t.Errorf("duplicate test and bad code: %v", f)
	}
	hi, lo := decimal.NewFromInt(5), decimal.NewFromInt(1)
	if f := ValidateTestGroup("GRP", []TestLine{{TestID: a, Kind: KindVariable, Min: &hi, Max: &lo, Decimals: 7}}); f["tests[0].min"] == "" || f["tests[0].decimals"] == "" {
		t.Errorf("min above max and decimals: %v", f)
	}
}

func TestCheckInstrument(t *testing.T) {
	calibrated := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	inst, err := NewInstrument("PH-01", "pH meter", 30, calibrated)
	if err != nil {
		t.Fatal(err)
	}
	if !inst.DueAt.Equal(calibrated.AddDate(0, 0, 30)) {
		t.Fatalf("due_at = %s", inst.DueAt)
	}
	if err := CheckInstrument(inst, inst.DueAt); err != nil {
		t.Errorf("due today is still in calibration: %v", err)
	}
	var de *Error
	err = CheckInstrument(inst, inst.DueAt.Add(time.Hour))
	if !errors.As(err, &de) || de.Code != CodeInstrumentOverdue || de.Reason != "INSTRUMENT_OVERDUE" {
		t.Errorf("overdue: got %v", err)
	}
	now := calibrated
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{now, CalibrationOK},
		{inst.DueAt.AddDate(0, 0, -WarnDays), CalibrationDueSoon},
		{inst.DueAt, CalibrationDueSoon},
		{inst.DueAt.Add(time.Second), CalibrationOverdue},
	} {
		if got := CalibrationStatus(inst, c.at); got != c.want {
			t.Errorf("status at %s: %s, want %s", c.at, got, c.want)
		}
	}
	if _, err := NewInstrument("x", "", 0, calibrated); err == nil {
		t.Error("invalid instrument must fail")
	}
}

func TestRecalibrate(t *testing.T) {
	last := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inst, _ := NewInstrument("PH-01", "pH meter", 90, last)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	next, err := inst.Recalibrate(now.Add(-time.Hour), now)
	if err != nil || !next.DueAt.Equal(now.Add(-time.Hour).AddDate(0, 0, 90)) || CalibrationStatus(next, now) != CalibrationOK {
		t.Fatalf("recalibrated: %+v %v", next, err)
	}
	if _, err := inst.Recalibrate(now.Add(time.Minute), now); err == nil {
		t.Error("a calibration in the future must be refused")
	}
	if _, err := inst.Recalibrate(last.Add(-time.Hour), now); err == nil {
		t.Error("a calibration before the recorded one must be refused")
	}
}
