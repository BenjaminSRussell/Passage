package domain

import "time"

// CodeNotDeletable is raised when a message without a destination in error is deleted.
const CodeNotDeletable = "SF-0119"

// DueForRetry reports whether the retry sweep (FR-25.17, every minute) should publish
// the message again for this destination: it failed at least once, is still pending
// and its wait has passed. A destination that never reported is waiting for its
// consumer to answer the first publication, not for the sweep.
func (d Delivery) DueForRetry(now time.Time) bool {
	return d.Status == DeliveryPending && d.Attempts > 0 && d.NextAttemptAt != nil && !d.NextAttemptAt.After(now)
}

// RetryLease is how long a destination the sweep just published again waits before
// the sweep may publish it again without an answer: the same wait as the failure
// that made it due (1, 5, 15 or 60 minutes). The consumer's next report replaces the
// lease with the schedule, so a consumer that is down is not flooded every minute.
func RetryLease(failedAttempts int) time.Duration {
	if d, ok := RetryDelay(failedAttempts); ok {
		return d
	}
	return time.Minute
}

// Claim leases a due destination after the sweep publishes it again.
func (d Delivery) Claim(now time.Time) Delivery {
	next := now.Add(RetryLease(d.Attempts))
	d.NextAttemptAt = &next
	return d
}

// CheckDeletable allows a supervisor to delete a message (with a reason) only when at
// least one destination is in error, i.e. its retries are used up (FR-25.17: failed
// destinations "stay in error for manual reprocess or deletion"). Every category also
// routes to Kafka, which is delivered at commit, so the message status itself is never
// error; the destination state decides.
func CheckDeletable(ds []Delivery) error {
	for _, d := range ds {
		if d.Status == DeliveryError {
			return nil
		}
	}
	return &Error{Code: CodeNotDeletable, Detail: "only a message with a destination in error can be deleted"}
}

// Undelivered lists the destinations of ds that are not delivered.
func Undelivered(ds []Delivery) []string {
	out := []string{}
	for _, d := range ds {
		if d.Status != DeliveryDelivered {
			out = append(out, d.Destination)
		}
	}
	return out
}
