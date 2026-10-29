package domain

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestCronNext_AC46_10_MondayBerlin(t *testing.T) {
	c, err := ParseCron("0 6 * * 1", "Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-01-05 is a Monday. 06:00 CET is 05:00 UTC.
	got, err := c.Next(time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
	// Strictly after: the run that just happened is skipped.
	next, _ := c.Next(want)
	if !next.Equal(want.AddDate(0, 0, 7)) {
		t.Fatalf("next after run = %s", next)
	}
	// Summer time starts on 2026-03-29: the next Monday 06:00 is CEST, 04:00 UTC.
	summer, _ := c.Next(time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 3, 30, 4, 0, 0, 0, time.UTC); !summer.Equal(want) {
		t.Fatalf("next across DST = %s, want %s", summer, want)
	}
}

func TestCronNext_LastDayOfMonth(t *testing.T) {
	c, err := ParseCron("0 20 L * *", "Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 31, 19, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("L next = %s, want %s", got, want)
	}
	// February 2026 has 28 days.
	feb, _ := c.Next(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 2, 28, 19, 0, 0, 0, time.UTC); !feb.Equal(want) {
		t.Fatalf("Feb L next = %s, want %s", feb, want)
	}
}

func TestParseCron_Rejects(t *testing.T) {
	cases := []struct {
		expr, tz string
	}{
		{"0 6 * *", "UTC"},
		{"60 6 * * *", "UTC"},
		{"0 25 * * *", "UTC"},
		{"*/0 * * * *", "UTC"},
		{"0 6 * * 1", "Mars/Olympus"},
		{"0 6 32 * *", "UTC"},
	}
	for _, c := range cases {
		if _, err := ParseCron(c.expr, c.tz); err == nil {
			t.Errorf("ParseCron(%q,%q) accepted", c.expr, c.tz)
		}
	}
}

func TestParseCron_StepsRangesLists(t *testing.T) {
	c, err := ParseCron("*/15 9-10 * * 1-5,0", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := c.Next(time.Date(2026, 1, 5, 9, 14, 0, 0, time.UTC))
	if !got.Equal(time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)) {
		t.Fatalf("step next = %s", got)
	}
	// Sunday is day 0; 7 is also accepted as Sunday.
	sun, err := ParseCron("0 0 * * 7", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := sun.Next(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC))
	if n.Weekday() != time.Sunday {
		t.Fatalf("day 7 must be Sunday, got %s", n.Weekday())
	}
}

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		attempt, max int
		want         time.Duration
		ok           bool
	}{
		{1, 3, time.Minute, true},
		{2, 3, 4 * time.Minute, true},
		{3, 3, 16 * time.Minute, true},
		{4, 3, 0, false},
		{0, 3, 0, false},
		{1, 0, 0, false},
		{1, 6, 0, false},
	}
	for _, c := range cases {
		got, ok := RetryDelay(c.attempt, c.max)
		if ok != c.ok || got != c.want {
			t.Errorf("RetryDelay(%d,%d) = %v,%v want %v,%v", c.attempt, c.max, got, ok, c.want, c.ok)
		}
	}
}

func TestChooseExport(t *testing.T) {
	cases := []struct {
		rows, max int64
		mode      string
		code      string
	}{
		{50, 0, ExportInline, ""},
		{100_000, 0, ExportInline, ""},
		{100_001, 0, ExportJob, ""},
		{250_000, 0, ExportJob, ""},
		{1_000_001, 0, "", CodeExportTooLarge},
		{2_000, 1_000, "", CodeExportTooLarge},
		{-1, 0, "", CodeValidation},
	}
	for _, c := range cases {
		mode, err := ChooseExport(c.rows, c.max)
		if c.code == "" {
			if err != nil || mode != c.mode {
				t.Errorf("ChooseExport(%d) = %s,%v want %s", c.rows, mode, err, c.mode)
			}
			continue
		}
		if err == nil || err.Code != c.code {
			t.Errorf("ChooseExport(%d) err=%v want %s", c.rows, err, c.code)
		}
	}
}

func TestExportReuseAndLifetime_BR46_7(t *testing.T) {
	created := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	if !CanReuseExport(created, created.Add(59*time.Minute)) {
		t.Fatal("identical request within an hour must reuse")
	}
	if CanReuseExport(created, created.Add(time.Hour)) {
		t.Fatal("request at one hour must not reuse")
	}
	if !ExportDeleteAt(created).Equal(created.Add(24 * time.Hour)) {
		t.Fatal("export must be deleted after 24 hours")
	}
	if !LinkExpiresAt(created, DefaultLinkTTLHours).Equal(created.Add(24 * time.Hour)) {
		t.Fatal("default link ttl is 24 hours")
	}
	if err := ValidateLinkTTL(0); err == nil {
		t.Fatal("link ttl 0 must be rejected")
	}
	if err := ValidateLinkTTL(168); err != nil {
		t.Fatal(err)
	}
}

func TestValidateExportFormat(t *testing.T) {
	if ValidateExportFormat("XLSX") != nil || ValidateExportFormat("CSV") != nil {
		t.Fatal("XLSX and CSV must pass")
	}
	if ValidateExportFormat("PDF") == nil {
		t.Fatal("PDF is not an explorer export format")
	}
}

func TestQueryHashDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "canonical")
		if QueryHash(s) != QueryHash(s) || len(QueryHash(s)) != 64 {
			t.Fatal("query hash must be a stable 64-character digest")
		}
	})
}

func TestParseCron_SingleValueStepRunsToFieldEnd(t *testing.T) {
	c, err := ParseCron("5/20 * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 5, 9, 6, 0, 0, time.UTC)
	got, _ := c.Next(from)
	if !got.Equal(time.Date(2026, 1, 5, 9, 25, 0, 0, time.UTC)) {
		t.Fatalf("5/20 after 09:06 = %s, want 09:25", got)
	}
}

func TestParseCron_RejectsEmptyAndLocalTimeZone(t *testing.T) {
	for _, tz := range []string{"", "Local"} {
		if _, err := ParseCron("0 6 * * 1", tz); err == nil {
			t.Errorf("time zone %q must be rejected: schedules are always bound to a named zone", tz)
		}
	}
}

func TestPrepareSchedule(t *testing.T) {
	now := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)
	valid := Schedule{Name: "Weekly revenue", TargetType: TargetDashboard, TargetID: "finance-overview", Cron: "0 6 * * 1",
		Timezone: "Europe/Berlin", Format: "PDF", Destination: Destination{Type: DestinationEmail, Targets: []string{"cfo@example.com"}}}
	got, err := PrepareSchedule(valid, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextRunAt == nil || !got.NextRunAt.Equal(time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC)) || got.NextRunAt.Location() != time.UTC {
		t.Fatalf("next run = %v, want Monday 06:00 Berlin stored as UTC", got.NextRunAt)
	}
	cases := []struct {
		name   string
		mutate func(*Schedule)
		field  string
	}{
		{"blank name", func(s *Schedule) { s.Name = "  " }, "name"},
		{"unknown target type", func(s *Schedule) { s.TargetType = "TILE" }, "targetType"},
		{"target id with spaces", func(s *Schedule) { s.TargetID = "my dashboard" }, "targetId"},
		{"csv is an explorer export, not a schedule format", func(s *Schedule) { s.Format = "CSV" }, "format"},
		{"bad e-mail", func(s *Schedule) { s.Destination.Targets = []string{"cfo"} }, "destination"},
		{"no targets", func(s *Schedule) { s.Destination.Targets = nil }, "destination"},
		{"two sftp targets", func(s *Schedule) {
			s.Destination = Destination{Type: DestinationSFTP, Targets: []string{"sftp://a.example/x", "sftp://b.example/y"}}
		}, "destination"},
		{"inbox needs user ids", func(s *Schedule) { s.Destination = Destination{Type: DestinationInbox, Targets: []string{"alice"}} }, "destination"},
		{"unknown destination", func(s *Schedule) { s.Destination.Type = "FAX" }, "destination"},
		{"bad cron", func(s *Schedule) { s.Cron = "0 6 * *" }, "cron"},
		{"bad zone", func(s *Schedule) { s.Timezone = "Mars/Olympus" }, "timezone"},
		{"never fires", func(s *Schedule) { s.Cron = "0 6 31 2 *" }, "cron"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := valid
			s.Destination.Targets = append([]string(nil), valid.Destination.Targets...)
			c.mutate(&s)
			_, err := PrepareSchedule(s, now)
			if err == nil || err.Code != CodeValidation || err.Fields[c.field] == "" {
				t.Fatalf("PrepareSchedule = %+v, want a %s field error", err, c.field)
			}
		})
	}
	sftp := valid
	sftp.Destination = Destination{Type: DestinationSFTP, Targets: []string{"sftp://files.example.com:2222/reports/"}}
	if _, err := PrepareSchedule(sftp, now); err != nil {
		t.Fatalf("one sftp URL must be accepted: %v", err)
	}
}
