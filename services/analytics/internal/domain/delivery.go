package domain

import (
	"slices"
	"time"
)

// Delivery outcomes for one attempt of a scheduled report (FR-46.10, §9.46.16).
const (
	DeliveryDelivered = "DELIVERED"
	DeliveryRetry     = "RETRY"
	DeliveryFailed    = "FAILED"

	// CodeDeliveryFailed is the message raised when a schedule fails after its retries.
	CodeDeliveryFailed = "ANA-0108"
)

// IsDeliveryStatus reports whether s is a delivery outcome.
func IsDeliveryStatus(s string) bool {
	return slices.Contains([]string{DeliveryDelivered, DeliveryRetry, DeliveryFailed}, s)
}

// PlanAttempt decides what one delivery attempt means. attempt is 1-based and
// counts the try being judged: a schedule with no failures in the current run
// is on attempt 1. A failed attempt is retried after RetryDelay (1, 4, 16
// minutes) until maxRetries is spent; the next failure is FAILED and the
// schedule waits for its next cron slot.
func PlanAttempt(attempt, maxRetries int, ok bool) (string, time.Duration) {
	if ok {
		return DeliveryDelivered, 0
	}
	if wait, more := RetryDelay(attempt, maxRetries); more {
		return DeliveryRetry, wait
	}
	return DeliveryFailed, 0
}

// Delivery is one attempt of a scheduled run (report_delivery, the schedule
// deliveries inquiry of §9.46.14). Every attempt of one run shares RunID.
type Delivery struct {
	ID              string     `json:"deliveryId"`
	ScheduleID      string     `json:"scheduleId"`
	RunID           string     `json:"runId"`
	Attempt         int        `json:"attempt"`
	Status          string     `json:"status"`
	Format          string     `json:"format"`
	DestinationType string     `json:"destinationType"`
	RowCount        int64      `json:"rowCount"`
	Bytes           int64      `json:"bytes"`
	Error           string     `json:"error,omitempty"`
	RunAt           time.Time  `json:"runAt"`
	RetryAt         *time.Time `json:"retryAt,omitempty"`
}
