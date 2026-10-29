package domain

import (
	"testing"
	"time"
)

// FR-46.10: retry 3 times with backoff, then FAILED.
func TestPlanAttempt_RetriesThenFails(t *testing.T) {
	cases := []struct {
		name       string
		attempt    int
		maxRetries int
		ok         bool
		outcome    string
		wait       time.Duration
	}{
		{"delivered first try", 1, DefaultScheduleRetries, true, DeliveryDelivered, 0},
		{"first failure retries after 1 min", 1, DefaultScheduleRetries, false, DeliveryRetry, time.Minute},
		{"second failure retries after 4 min", 2, DefaultScheduleRetries, false, DeliveryRetry, 4 * time.Minute},
		{"third failure retries after 16 min", 3, DefaultScheduleRetries, false, DeliveryRetry, 16 * time.Minute},
		{"fourth failure is FAILED", 4, DefaultScheduleRetries, false, DeliveryFailed, 0},
		{"no retries configured fails at once", 1, 0, false, DeliveryFailed, 0},
		{"late success still delivers", 4, DefaultScheduleRetries, true, DeliveryDelivered, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, wait := PlanAttempt(tc.attempt, tc.maxRetries, tc.ok)
			if outcome != tc.outcome || wait != tc.wait {
				t.Fatalf("PlanAttempt(%d,%d,%v) = %s,%v; want %s,%v",
					tc.attempt, tc.maxRetries, tc.ok, outcome, wait, tc.outcome, tc.wait)
			}
		})
	}
}
