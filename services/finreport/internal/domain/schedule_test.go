package domain

import (
	"fmt"
	"testing"
	"time"
)

func TestParseCron_Table(t *testing.T) {
	tests := []struct {
		name, expr string
		ok         bool
	}{
		{"nightly pack", "0 23 * * *", true},
		{"lists and steps", "*/15 8-17 1,15 * 1-5", true},
		{"sunday as 7", "0 0 * * 7", true},
		{"29 February fires in leap years", "0 0 29 2 *", true},
		{"30 February never fires", "0 0 30 2 *", false},
		{"31st in 30-day months only", "0 0 31 4,6,9,11 *", false},
		{"30 February or any Monday fires", "0 0 30 2 1", true},
		{"too few fields", "0 23 * *", false},
		{"minute out of range", "60 0 * * *", false},
		{"bad step", "*/0 0 * * *", false},
		{"not numeric", "x 0 * * *", false},
		{"reversed range", "0 0 * 5-3 *", false},
		{"empty list item", "0,,5 0 * * *", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCron(tc.expr)
			if (err == nil) != tc.ok {
				t.Fatalf("ParseCron(%q) error = %v, want ok=%v", tc.expr, err, tc.ok)
			}
		})
	}
}

func TestCronNext_Table(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 3, 31, 22, 30, 0, 0, time.UTC)
	tests := []struct {
		name, expr string
		from       time.Time
		want       time.Time
	}{
		{"nightly 23:00", "0 23 * * *", from, time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC)},
		{"strictly after an exact match", "30 22 * * *", from, time.Date(2026, 4, 1, 22, 30, 0, 0, time.UTC)},
		{"next day", "0 1 * * *", from, time.Date(2026, 4, 1, 1, 0, 0, 0, time.UTC)},
		{"first of month", "0 0 1 * *", from, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{"every 15 minutes", "*/15 * * * *", from, time.Date(2026, 3, 31, 22, 45, 0, 0, time.UTC)},
		{"day of month or weekday", "0 9 15 * 1", from, time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)},
		{"weekday step counts as unrestricted", "0 9 13 * */1", from, time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC)},
		{"next leap day", "0 0 29 2 *", from, time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
		// 29 March 2026 02:30 does not exist in Berlin (clocks go to 03:00).
		{"skipped by daylight saving", "30 2 * * *", time.Date(2026, 3, 28, 12, 0, 0, 0, berlin), time.Date(2026, 3, 30, 2, 30, 0, 0, berlin)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseCron(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			got, err := spec.Next(tc.from)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("Next = %s, want %s", got, tc.want)
			}
		})
	}
}

const testLedger = "0196a0c0-7b1e-7000-8000-000000000001"

func validSchedule() Schedule {
	return Schedule{
		Code: "NIGHTLY-PACK", Name: "Nightly statement pack", Definition: "BS-STD",
		Cron: "0 23 * * *", Timezone: "UTC", LedgerIDs: []string{testLedger}, CurrencyType: "acct",
		Formats: []string{"pdf", "csv"}, Recipients: []string{"controller@example.com"},
		AttachmentLimitMB: DefaultAttachMB, Enabled: true,
	}
}

func TestScheduleValidate_Table(t *testing.T) {
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("user%d@example.com", i)
		}
		return out
	}
	tests := []struct {
		name   string
		mutate func(*Schedule)
		field  string
	}{
		{"valid", func(*Schedule) {}, ""},
		{"50 recipients", func(s *Schedule) { s.Recipients = many(50) }, ""},
		{"bad code", func(s *Schedule) { s.Code = "nightly" }, "code"},
		{"blank name", func(s *Schedule) { s.Name = "  " }, "name"},
		{"bad definition", func(s *Schedule) { s.Definition = "" }, "definition"},
		{"bad cron", func(s *Schedule) { s.Cron = "0 23 *" }, "cron"},
		{"never fires", func(s *Schedule) { s.Cron = "0 0 30 2 *" }, "cron"},
		{"unknown zone", func(s *Schedule) { s.Timezone = "Mars/Olympus" }, "timezone"},
		{"server-local zone", func(s *Schedule) { s.Timezone = "Local" }, "timezone"},
		{"no ledgers", func(s *Schedule) { s.LedgerIDs = nil }, "ledgerIds"},
		{"bad ledger", func(s *Schedule) { s.LedgerIDs = []string{"x"} }, "ledgerIds"},
		{"duplicate ledger", func(s *Schedule) { s.LedgerIDs = []string{testLedger, testLedger} }, "ledgerIds"},
		{"unknown currency type", func(s *Schedule) { s.CurrencyType = "usd" }, "currencyType"},
		{"no formats", func(s *Schedule) { s.Formats = nil }, "formats"},
		{"unknown format", func(s *Schedule) { s.Formats = []string{"docx"} }, "formats"},
		{"duplicate format", func(s *Schedule) { s.Formats = []string{"pdf", "pdf"} }, "formats"},
		{"display name address", func(s *Schedule) { s.Recipients = []string{"Ann <ann@example.com>"} }, "recipients"},
		{"duplicate recipient", func(s *Schedule) { s.Recipients = []string{"a@example.com", "a@example.com"} }, "recipients"},
		{"51 recipients", func(s *Schedule) { s.Recipients = many(51) }, "recipients"},
		{"attachment over limit", func(s *Schedule) { s.AttachmentLimitMB = 26 }, "attachmentLimitMb"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := validSchedule()
			tc.mutate(&s)
			issues := s.Validate()
			if tc.field == "" {
				if len(issues) != 0 {
					t.Fatalf("unexpected issues: %+v", issues)
				}
				return
			}
			if len(issues) == 0 || issues[0].Field != tc.field || issues[0].Code != CodeScheduleInvalid {
				t.Fatalf("issues = %+v, want first field %q with %s", issues, tc.field, CodeScheduleInvalid)
			}
		})
	}
}

func TestScheduleNormalize(t *testing.T) {
	s := validSchedule()
	s.AttachmentLimitMB, s.CurrencyType = 0, ""
	s.Recipients = []string{" Ann@Example.com", "ann@example.com"}
	n := s.Normalize()
	if n.AttachmentLimitMB != DefaultAttachMB || n.CurrencyType != "acct" {
		t.Fatalf("defaults = %d MB, %q; want 20 MB, acct", n.AttachmentLimitMB, n.CurrencyType)
	}
	if issues := n.Validate(); len(issues) == 0 || issues[0].Field != "recipients" {
		t.Fatalf("addresses differing only in case must count as duplicates, issues %+v", issues)
	}
	if s.Recipients[0] != " Ann@Example.com" {
		t.Fatal("Normalize must not modify the receiver's recipients")
	}
}

func TestScheduleNextRun_UsesTheScheduleZone(t *testing.T) {
	s := validSchedule()
	s.Timezone = "America/New_York"
	got, err := s.NextRun(time.Date(2026, 7, 1, 2, 0, 0, 0, time.UTC)) // 22:00 on 30 June in New York
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 7, 1, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next run = %s, want %s (23:00 EDT)", got.UTC(), want)
	}
}
