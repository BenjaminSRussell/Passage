package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func ymd(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func code(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestCheckSatisfy(t *testing.T) {
	contractDate := ymd(2027, time.March, 10)
	satisfied := ymd(2027, time.March, 25)
	tests := []struct {
		name         string
		method       string
		status       string
		recognizedOn *time.Time
		on           time.Time
		wantReplay   bool
		wantCode     string
	}{
		{"open milestone on the BR-29.1 date", MethodMilestone, ScheduleScheduled, nil, satisfied, false, ""},
		{"open milestone on the contract date", MethodMilestone, ScheduleScheduled, nil, contractDate, false, ""},
		{"retry on the same date replays", MethodMilestone, ScheduleRecognized, &satisfied, satisfied, true, ""},
		{"second satisfaction on another date", MethodMilestone, ScheduleRecognized, &satisfied, ymd(2027, time.April, 2), false, "REV-0004"},
		{"reversed schedule", MethodMilestone, ScheduleReversed, nil, satisfied, false, "REV-0004"},
		{"before the contract date", MethodMilestone, ScheduleScheduled, nil, ymd(2027, time.March, 9), false, "REV-0006"},
		{"point in time obligation", MethodPointInTime, ScheduleRecognized, &contractDate, satisfied, false, "REV-0005"},
		{"ratable obligation", MethodRatable, ScheduleScheduled, nil, satisfied, false, "REV-0005"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replay, err := CheckSatisfy(tt.method, tt.status, tt.recognizedOn, tt.on, contractDate)
			if replay != tt.wantReplay || code(err) != tt.wantCode {
				t.Errorf("replay=%v code=%q, want replay=%v code=%q (err %v)", replay, code(err), tt.wantReplay, tt.wantCode, err)
			}
		})
	}
}

func TestCheckSSPSources_AtMostOneResidual(t *testing.T) {
	one := []Obligation{{SSPSource: "EXPLICIT"}, {SSPSource: SSPResidual}, {SSPSource: "LIST"}}
	if err := CheckSSPSources(one); err != nil {
		t.Errorf("one residual obligation refused: %v", err)
	}
	two := []Obligation{{SSPSource: SSPResidual}, {SSPSource: SSPResidual}}
	if got := code(CheckSSPSources(two)); got != "REV-0003" {
		t.Errorf("two residual obligations: code %q, want REV-0003", got)
	}
}

func TestSSPWarnings_BIL0009PerResidualObligation(t *testing.T) {
	obs := []Obligation{{SSPSource: "EXPLICIT"}, {SSPSource: SSPResidual}}
	got := SSPWarnings(obs, []string{"hardware", "installation"})
	if len(got) != 1 || got[0].Code != "BIL-0009" || got[0].Detail != "SSP missing for installation; residual method used" {
		t.Errorf("warnings %+v, want one BIL-0009 naming installation", got)
	}
	if w := SSPWarnings(obs[:1], []string{"hardware"}); len(w) != 0 {
		t.Errorf("explicit SSP warned: %+v", w)
	}
}

// TestScheduleLines_BR29_1Bundle plans the three obligations of the BR-29.1
// bundle: hardware at the transfer date, installation awaiting its milestone,
// support over 13 monthly lines starting with 133.06 and summing to 2,250.00.
func TestScheduleLines_BR29_1Bundle(t *testing.T) {
	shipped := ymd(2027, time.March, 10)
	start, end := ymd(2027, time.March, 10), ymd(2028, time.March, 9)

	hw, err := ScheduleLines(Obligation{Method: MethodPointInTime, Allocated: decimal.RequireFromString("7200.00")}, "USD", nil, nil, &shipped)
	if err != nil || len(hw) != 1 || !hw[0].Date.Equal(shipped) || hw[0].Amount.StringFixed(2) != "7200.00" {
		t.Fatalf("hardware lines %+v err %v", hw, err)
	}
	inst, err := ScheduleLines(Obligation{Method: MethodMilestone, Allocated: decimal.RequireFromString("1350.00")}, "USD", nil, nil, nil)
	if err != nil || len(inst) != 1 || inst[0].Date != nil || inst[0].Amount.StringFixed(2) != "1350.00" {
		t.Fatalf("installation lines %+v err %v", inst, err)
	}
	sup, err := ScheduleLines(Obligation{Method: MethodRatable, Allocated: decimal.RequireFromString("2250.00")}, "USD", &start, &end, nil)
	if err != nil || len(sup) != 13 {
		t.Fatalf("support lines %d err %v, want 13", len(sup), err)
	}
	sum := decimal.Zero
	for i, l := range sup {
		if l.Seq != i+1 || l.Date == nil {
			t.Errorf("line %d: seq %d date %v", i, l.Seq, l.Date)
		}
		sum = sum.Add(l.Amount)
	}
	if sup[0].Amount.StringFixed(2) != "133.06" || !sup[0].Date.Equal(ymd(2027, time.March, 31)) {
		t.Errorf("first support line %s on %v, want 133.06 on 2027-03-31", sup[0].Amount, sup[0].Date)
	}
	if !sup[12].Date.Equal(end) || sum.StringFixed(2) != "2250.00" {
		t.Errorf("last line dated %v, sum %s; want the term end and 2250.00", sup[12].Date, sum)
	}
	usage, err := ScheduleLines(Obligation{Method: MethodUsage, Allocated: decimal.RequireFromString("10")}, "USD", nil, nil, nil)
	if err != nil || len(usage) != 0 {
		t.Errorf("usage lines %+v err %v, want none until usage posts", usage, err)
	}
}

func TestScheduleLines_MissingInputs(t *testing.T) {
	if _, err := ScheduleLines(Obligation{Method: MethodPointInTime}, "USD", nil, nil, nil); code(err) != "REV-0001" {
		t.Errorf("point in time without a transfer date: %v, want REV-0001", err)
	}
	if _, err := ScheduleLines(Obligation{Method: MethodRatable}, "USD", nil, nil, nil); code(err) != "REV-0002" {
		t.Errorf("ratable without a term: %v, want REV-0002", err)
	}
	if _, err := ScheduleLines(Obligation{Method: "BARTER"}, "USD", nil, nil, nil); code(err) != "REV-0001" {
		t.Errorf("unknown method: %v, want REV-0001", err)
	}
}
