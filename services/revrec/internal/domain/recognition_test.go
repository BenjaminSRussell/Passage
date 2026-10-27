package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestInitialStatus_PointInTimeDatedIsRecognized(t *testing.T) {
	day := time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		method string
		date   *time.Time
		want   string
	}{
		{"point in time dated", MethodPointInTime, &day, ScheduleRecognized},
		{"point in time undated", MethodPointInTime, nil, ScheduleScheduled},
		{"ratable dated", MethodRatable, &day, ScheduleScheduled},
		{"milestone undated", MethodMilestone, nil, ScheduleScheduled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InitialStatus(tt.method, tt.date); got != tt.want {
				t.Errorf("InitialStatus = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestNewRollForward_AC29_4 is AC-29.4: additions 3,600.00 and recognized
// 1,483.06 close at 2,116.94.
func TestNewRollForward_AC29_4(t *testing.T) {
	rf := NewRollForward(decimal.Zero, decimal.RequireFromString("3600.00"), decimal.RequireFromString("1483.06"))
	if rf.Closing.StringFixed(2) != "2116.94" {
		t.Errorf("closing %s, want 2116.94", rf.Closing.StringFixed(2))
	}
	if !rf.Adjustments.IsZero() {
		t.Errorf("adjustments %s, want 0", rf.Adjustments)
	}
}

func TestNewRollForward_OpeningCarriesForward(t *testing.T) {
	tests := []struct {
		name                     string
		opening, additions, recg string
		closing                  string
	}{
		{"no movement", "500.00", "0", "0", "500.00"},
		{"recognize all", "500.00", "0", "500.00", "0.00"},
		{"add and recognize", "100.00", "250.50", "300.25", "50.25"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rf := NewRollForward(decimal.RequireFromString(tt.opening), decimal.RequireFromString(tt.additions), decimal.RequireFromString(tt.recg))
			if got := rf.Closing.StringFixed(2); got != tt.closing {
				t.Errorf("closing %s, want %s", got, tt.closing)
			}
		})
	}
}

func TestFullyRecognized(t *testing.T) {
	tests := []struct {
		name     string
		methods  []string
		statuses []string
		want     bool
	}{
		{"hardware only, recognized at transfer", []string{MethodPointInTime}, []string{ScheduleRecognized}, true},
		{"BR-29.1 bundle after creation", []string{MethodPointInTime, MethodMilestone, MethodRatable}, []string{ScheduleRecognized, ScheduleScheduled, ScheduleScheduled}, false},
		{"every line recognized", []string{MethodMilestone, MethodRatable}, []string{ScheduleRecognized, ScheduleRecognized}, true},
		{"usage obligation stays open", []string{MethodUsage}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FullyRecognized(tt.methods, tt.statuses); got != tt.want {
				t.Errorf("FullyRecognized = %v, want %v", got, tt.want)
			}
		})
	}
}
