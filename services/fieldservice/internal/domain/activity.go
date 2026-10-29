package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Activity kinds and statuses (FR-45.14).
const (
	ActivityTask        = "TASK"
	ActivityAppointment = "APPOINTMENT"
	ActivityCallNote    = "CALL_NOTE"
	ActivityEmailNote   = "EMAIL_NOTE"

	ActivityOpen     = "OPEN"
	ActivityDone     = "DONE"
	ActivityCanceled = "CANCELED"
)

// Codes returned by the activity and knowledge rules.
const (
	CodeActivityNotOpen   = "SVC-ACT-002"
	CodeArticleNotInState = "KB-ART-002"
	CodeReviewerSoD       = "KB-PUB-001"
)

// ActivityKinds lists the four activity kinds.
var ActivityKinds = []string{ActivityTask, ActivityAppointment, ActivityCallNote, ActivityEmailNote}

// ActivityStatuses lists the activity statuses.
var ActivityStatuses = []string{ActivityOpen, ActivityDone, ActivityCanceled}

// AssociableTypes are the record types an activity may hang off (FR-45.14):
// this service's own records plus the wave-3 registry names the PRD lists for
// case associations (FR-45.12).
var AssociableTypes = []string{"case", "work_order", "agreement", "service_object", "knowledge_article", "project",
	"customer", "vendor", "worker", "invoice", "item", "asset", "audit_finding"}

// ValidActivityKind reports whether kind is one of the four activity kinds.
func ValidActivityKind(kind string) bool { return slices.Contains(ActivityKinds, kind) }

// ValidAssociation reports whether a record type may be associated with an activity.
func ValidAssociation(entityType string) bool { return slices.Contains(AssociableTypes, entityType) }

// CompleteActivity returns the status after completing an activity (AC-45.15).
// Completing a DONE activity is a no-op; a CANCELED activity cannot be completed.
func CompleteActivity(status string) (string, error) {
	switch status {
	case ActivityOpen, ActivityDone:
		return ActivityDone, nil
	default:
		return "", &Error{Code: CodeActivityNotOpen, Detail: fmt.Sprintf("an activity in status %s cannot be completed", status)}
	}
}

// CancelActivity returns the status after canceling an activity. Canceling a
// CANCELED activity is a no-op; a DONE activity stays done.
func CancelActivity(status string) (string, error) {
	switch status {
	case ActivityOpen, ActivityCanceled:
		return ActivityCanceled, nil
	default:
		return "", &Error{Code: CodeActivityNotOpen, Detail: fmt.Sprintf("an activity in status %s cannot be canceled", status)}
	}
}

// CanReschedule reports whether an activity's dates may change: only an OPEN one.
func CanReschedule(status string) error {
	if status != ActivityOpen {
		return &Error{Code: CodeActivityNotOpen, Detail: fmt.Sprintf("an activity in status %s cannot be rescheduled", status)}
	}
	return nil
}

// CalendarEntry is one VEVENT of the iCalendar export.
type CalendarEntry struct {
	UID             string
	Summary         string
	Start           time.Time
	End             *time.Time
	Status          string
	ReminderMinutes int
}

// ICalendar renders entries as an RFC 5545 calendar: text values are escaped,
// lines longer than 75 octets are folded and every line ends with CRLF.
func ICalendar(entries []CalendarEntry, stamp time.Time) string {
	var b strings.Builder
	write := func(line string) { b.WriteString(fold(line)); b.WriteString("\r\n") }
	const layout = "20060102T150405Z"
	write("BEGIN:VCALENDAR")
	write("VERSION:2.0")
	write("PRODID:-//Passage//Field Service//EN")
	for _, e := range entries {
		write("BEGIN:VEVENT")
		write("UID:" + icalEscape(e.UID) + "@passage")
		write("DTSTAMP:" + stamp.UTC().Format(layout))
		write("DTSTART:" + e.Start.UTC().Format(layout))
		if e.End != nil && !e.End.Before(e.Start) {
			write("DTEND:" + e.End.UTC().Format(layout))
		}
		write("SUMMARY:" + icalEscape(e.Summary))
		write("STATUS:" + icalStatus(e.Status))
		if e.ReminderMinutes > 0 {
			write("BEGIN:VALARM")
			write("ACTION:DISPLAY")
			write("DESCRIPTION:Reminder")
			write(fmt.Sprintf("TRIGGER:-PT%dM", e.ReminderMinutes))
			write("END:VALARM")
		}
		write("END:VEVENT")
	}
	write("END:VCALENDAR")
	return b.String()
}

// icalStatus maps an activity status to a VEVENT status (RFC 5545 3.8.1.11):
// a canceled activity is CANCELLED, every other one CONFIRMED.
func icalStatus(status string) string {
	if status == ActivityCanceled {
		return "CANCELLED"
	}
	return "CONFIRMED"
}

var icalReplacer = strings.NewReplacer("\\", "\\\\", ";", "\\;", ",", "\\,", "\r\n", "\\n", "\n", "\\n", "\r", "\\n")

func icalEscape(s string) string { return icalReplacer.Replace(s) }

// fold splits a content line into pieces of at most 75 octets joined by CRLF
// and a space (RFC 5545 3.1), never inside a UTF-8 sequence. The leading space
// of a continuation line counts toward its 75 octets.
func fold(line string) string {
	const limit = 75
	if len(line) <= limit {
		return line
	}
	var b strings.Builder
	width, room := 0, limit
	for _, r := range line {
		n := len(string(r))
		if width+n > room {
			b.WriteString("\r\n ")
			width, room = 0, limit-1
		}
		b.WriteRune(r)
		width += n
	}
	return b.String()
}
