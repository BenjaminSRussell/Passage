package domain

import "time"

// Operator-tunable limits (§6.32.13, §6.32.19). Defaults apply when the matching
// environment variable is unset.
const (
	DefaultRetentionDays = 30  // job log retention (§6.32.19, FR-32.13 "job log kept 30 days")
	MaxRetentionDays     = 365 // upper bound for PRINT_JOB_RETENTION_DAYS
	MaxRetriesPerDevice  = 10  // upper bound for PRINT_RETRY (print_retry_count 0–10)
)

// Printer states derived from the most recent job routed to a printer (§6.32.14
// printer status). There is no device probe, so the state is what the service last saw.
const (
	StateIdle  = "IDLE"  // no job has been sent to the printer
	StateBusy  = "BUSY"  // the last job is still QUEUED
	StateReady = "READY" // the last job printed
	StateError = "ERROR" // the last job failed
)

// PrinterState maps the status of a printer's most recent job to a printer state.
func PrinterState(lastStatus string) string {
	switch lastStatus {
	case JobPrinted:
		return StateReady
	case JobFailed:
		return StateError
	case JobQueued:
		return StateBusy
	}
	return StateIdle
}

// PrinterStatus is the operational view of one printer: the depth of its queue and the
// outcome of the last job it handled (§6.32.14 printer status).
type PrinterStatus struct {
	PrinterID  string     `json:"printerId"`
	Code       string     `json:"code"`
	QueueCode  string     `json:"queueCode"`
	State      string     `json:"state"`
	QueueDepth int        `json:"queueDepth"`
	LastError  string     `json:"lastError,omitempty"`
	LastJobAt  *time.Time `json:"lastJobAt,omitempty"`
}

// RetentionCutoff returns the instant before which finished job log rows are purged.
func RetentionCutoff(now time.Time, days int) time.Time {
	return now.UTC().AddDate(0, 0, -days)
}
