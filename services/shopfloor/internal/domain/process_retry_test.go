package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var sweepAt = time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

func TestDueForRetry_TableDriven(t *testing.T) {
	past, future := sweepAt.Add(-time.Second), sweepAt.Add(time.Second)
	cases := []struct {
		name string
		d    Delivery
		want bool
	}{
		{"failed and due", Delivery{Status: DeliveryPending, Attempts: 1, NextAttemptAt: &past}, true},
		{"failed and exactly due", Delivery{Status: DeliveryPending, Attempts: 4, NextAttemptAt: &sweepAt}, true},
		{"failed but still waiting", Delivery{Status: DeliveryPending, Attempts: 1, NextAttemptAt: &future}, false},
		{"never reported waits for its consumer", Delivery{Status: DeliveryPending, Attempts: 0, NextAttemptAt: &past}, false},
		{"no schedule", Delivery{Status: DeliveryPending, Attempts: 1}, false},
		{"delivered", Delivery{Status: DeliveryDelivered, Attempts: 1, NextAttemptAt: &past}, false},
		{"error waits for a supervisor", Delivery{Status: DeliveryError, Attempts: 5, NextAttemptAt: &past}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.DueForRetry(sweepAt); got != c.want {
				t.Fatalf("DueForRetry = %v, want %v", got, c.want)
			}
		})
	}
}

// The lease after a sweep publication follows the schedule, so an unanswered
// destination is published again after 1, 5, 15 or 60 minutes, not every minute.
func TestClaim_LeaseFollowsSchedule(t *testing.T) {
	for attempts, want := range map[int]time.Duration{1: time.Minute, 2: 5 * time.Minute, 3: 15 * time.Minute, 4: time.Hour} {
		d := Delivery{Status: DeliveryPending, Attempts: attempts, NextAttemptAt: &sweepAt}.Claim(sweepAt)
		if d.NextAttemptAt == nil || !d.NextAttemptAt.Equal(sweepAt.Add(want)) {
			t.Fatalf("attempts %d: lease until %v, want +%v", attempts, d.NextAttemptAt, want)
		}
		if d.DueForRetry(sweepAt.Add(want - time.Second)) {
			t.Fatalf("attempts %d: due again during the lease", attempts)
		}
		if !d.DueForRetry(sweepAt.Add(want)) {
			t.Fatalf("attempts %d: lease did not end", attempts)
		}
	}
}

// After a sweep publication the consumer's answer wins over the lease: a failure
// moves on to the next wait of the schedule and the fifth failure ends in error.
func TestClaim_ThenReportFollowsSchedule(t *testing.T) {
	d := Delivery{Status: DeliveryPending}
	now := sweepAt
	waits := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}
	for i := 0; i < MaxDeliveryAttempts; i++ {
		var err error
		if d, err = d.Report(OutcomeFailed, nil, now); err != nil {
			t.Fatal(err)
		}
		if i == MaxDeliveryAttempts-1 {
			break
		}
		if !d.NextAttemptAt.Equal(now.Add(waits[i])) {
			t.Fatalf("after failure %d next attempt %v, want +%v", i+1, d.NextAttemptAt, waits[i])
		}
		now = *d.NextAttemptAt
		if !d.DueForRetry(now) {
			t.Fatalf("failure %d not due at %v", i+1, now)
		}
		d = d.Claim(now)
	}
	if d.Status != DeliveryError || d.DueForRetry(now.Add(24*time.Hour)) {
		t.Fatalf("after five failures = %+v", d)
	}
}

func TestCheckDeletable_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		ds   []Delivery
		ok   bool
	}{
		{"one destination in error", []Delivery{{Status: DeliveryDelivered}, {Status: DeliveryError}}, true},
		{"all in error", []Delivery{{Status: DeliveryError}}, true},
		{"still retrying", []Delivery{{Status: DeliveryDelivered}, {Status: DeliveryPending, Attempts: 3}}, false},
		{"all delivered", []Delivery{{Status: DeliveryDelivered}}, false},
		{"no destinations", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckDeletable(c.ds)
			if c.ok != (err == nil) {
				t.Fatalf("err = %v, want ok %v", err, c.ok)
			}
			var de *Error
			if err != nil && (!errors.As(err, &de) || de.Code != CodeNotDeletable) {
				t.Fatalf("err = %v, want %s", err, CodeNotDeletable)
			}
		})
	}
}

func TestUndelivered(t *testing.T) {
	ds := []Delivery{{Destination: DestEBR, Status: DeliveryDelivered}, {Destination: DestQuality, Status: DeliveryError}, {Destination: DestWebhook, Status: DeliveryPending}}
	if got := Undelivered(ds); !reflect.DeepEqual(got, []string{DestQuality, DestWebhook}) {
		t.Fatalf("Undelivered = %v", got)
	}
	if got := Undelivered(nil); got == nil || len(got) != 0 {
		t.Fatalf("Undelivered(nil) = %#v, want empty slice", got)
	}
}

// Property: a destination the sweep claimed is never due again before its lease
// ends, whatever its failed attempts, and always due once the lease is over.
func TestPropertyClaimIsNotDueDuringLease(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attempts := rapid.IntRange(1, MaxDeliveryAttempts-1).Draw(t, "attempts")
		offset := time.Duration(rapid.Int64Range(0, int64(2*time.Hour)).Draw(t, "offset"))
		d := Delivery{Status: DeliveryPending, Attempts: attempts, NextAttemptAt: &sweepAt}.Claim(sweepAt)
		lease := RetryLease(attempts)
		if got := d.DueForRetry(sweepAt.Add(offset)); got != (offset >= lease) {
			t.Fatalf("attempts %d, %v after the claim: due = %v (lease %v)", attempts, offset, got, lease)
		}
	})
}
