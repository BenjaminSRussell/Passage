package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// FR-45.14 and AC-45.15: completing an OPEN activity moves it to DONE; DONE is a
// no-op; a CANCELED activity cannot be completed. Canceling mirrors it.
func TestActivityStatusMoves(t *testing.T) {
	tests := []struct {
		name   string
		move   func(string) (string, error)
		status string
		want   string
	}{
		{"complete open", CompleteActivity, ActivityOpen, ActivityDone},
		{"complete done again", CompleteActivity, ActivityDone, ActivityDone},
		{"complete canceled", CompleteActivity, ActivityCanceled, ""},
		{"cancel open", CancelActivity, ActivityOpen, ActivityCanceled},
		{"cancel canceled again", CancelActivity, ActivityCanceled, ActivityCanceled},
		{"cancel done", CancelActivity, ActivityDone, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.move(tc.status)
			if tc.want == "" {
				var de *Error
				if !errors.As(err, &de) || de.Code != CodeActivityNotOpen {
					t.Fatalf("err = %v, want code %s", err, CodeActivityNotOpen)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestCanReschedule_OpenOnly(t *testing.T) {
	if err := CanReschedule(ActivityOpen); err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, s := range []string{ActivityDone, ActivityCanceled} {
		if err := CanReschedule(s); err == nil {
			t.Fatalf("%s must not be reschedulable", s)
		}
	}
}

func TestValidActivityKindAndAssociation(t *testing.T) {
	for _, k := range ActivityKinds {
		if !ValidActivityKind(k) {
			t.Fatalf("%s should be valid", k)
		}
	}
	for _, k := range []string{"", "task", "MEETING"} {
		if ValidActivityKind(k) {
			t.Fatalf("%q should be invalid", k)
		}
	}
	if !ValidAssociation("case") || !ValidAssociation("project") || !ValidAssociation("work_order") {
		t.Fatal("case, project and work_order must be associable")
	}
	if ValidAssociation("payroll_run") || ValidAssociation("Case") {
		t.Fatal("unregistered record types must be refused")
	}
}

// FR-45.14: the calendar export carries one VEVENT per dated activity, escapes
// text, maps status and reminders onto VEVENT values and ends lines with CRLF.
func TestICalendar_EscapesAndMapsStatus(t *testing.T) {
	start := time.Date(2026, 10, 27, 14, 30, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	stamp := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	out := ICalendar([]CalendarEntry{
		{UID: "a1", Summary: "Call Acme; pump, noise\nnow", Start: start, End: &end, Status: ActivityOpen, ReminderMinutes: 15},
		{UID: "a2", Summary: "Dropped visit", Start: start, Status: ActivityCanceled},
	}, stamp)

	for _, want := range []string{
		"BEGIN:VCALENDAR\r\n", "END:VCALENDAR\r\n",
		"UID:a1@passage\r\n", "DTSTART:20261027T143000Z\r\n", "DTEND:20261027T153000Z\r\n", "DTSTAMP:20261026T090000Z\r\n",
		`SUMMARY:Call Acme\; pump\, noise\nnow` + "\r\n",
		"STATUS:CONFIRMED\r\n", "TRIGGER:-PT15M\r\n",
		"STATUS:CANCELLED\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("calendar missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "BEGIN:VEVENT") != 2 || strings.Count(out, "BEGIN:VALARM") != 1 || strings.Count(out, "DTEND") != 1 {
		t.Fatalf("want 2 events, 1 alarm and 1 end:\n%s", out)
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Fatal("lines must end with CRLF only")
	}
}

// RFC 5545 3.1: content lines are at most 75 octets; continuation lines start
// with a space and multi-byte characters are never split.
func TestICalendar_FoldsLongLines(t *testing.T) {
	summary := strings.Repeat("é", 60) + strings.Repeat("x", 100)
	out := ICalendar([]CalendarEntry{{UID: "a", Summary: summary, Start: time.Unix(0, 0), Status: ActivityOpen}}, time.Unix(0, 0))
	var unfolded strings.Builder
	for i, line := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line %d has %d octets: %q", i, len(line), line)
		}
		if strings.HasPrefix(line, " ") {
			unfolded.WriteString(line[1:])
			continue
		}
		unfolded.WriteString("\n" + line)
	}
	if !strings.Contains(unfolded.String(), "SUMMARY:"+summary+"\n") {
		t.Fatalf("unfolding does not restore the summary:\n%s", out)
	}
}
