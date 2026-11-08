package domain

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones for schedule.timezone, also in images without /usr/share/zoneinfo
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Limits of a distribution schedule (FR-15.8, PRD §2.15.13).
const (
	MaxRecipients      = 50
	MaxScheduleLedgers = 50
	DefaultAttachMB    = 20 // reporting.email_attachment_limit_mb default
	MinAttachMB        = 1
	MaxAttachMB        = 25
)

// ScheduleFormats are the output formats a schedule may request, in display
// order. CSV, HTML and XLSX are rendered; PDF needs the renderer sidecar and a
// firing records it as skipped (NOT-IMPLEMENTED.md).
var ScheduleFormats = []string{"pdf", "xlsx", "csv", "html"}

// Schedule runs one published definition on a cron expression for a set of
// ledgers and distributes the outputs to recipients (FR-15.8). Recipients see
// their own dimension security (BR-15.7), so the schedule stores no filters.
// CurrencyType is the run parameter every standard statement needs (FR-15.5).
type Schedule struct {
	Code              string   `json:"code"`
	Name              string   `json:"name"`
	Definition        string   `json:"definition"`
	Cron              string   `json:"cron"`
	Timezone          string   `json:"timezone"`
	LedgerIDs         []string `json:"ledgerIds"`
	CurrencyType      string   `json:"currencyType"`
	Formats           []string `json:"formats"`
	Recipients        []string `json:"recipients"`
	AttachmentLimitMB int      `json:"attachmentLimitMb"`
	Enabled           bool     `json:"enabled"`
	// FiscalYearStartMonth (1 to 12, default 1) places a firing in the fiscal
	// calendar: a firing runs for the calendar month before its fire time (see
	// FiringPeriod). It stands in for the ledger calendar of wave 14.
	FiscalYearStartMonth int    `json:"fiscalYearStartMonth"`
	UpdatedBy            string `json:"updatedBy,omitempty"`
}

// Normalize applies the documented defaults: attachment limit 20 MB and
// currency type acct (definition.currency_type default), and lower-cases the
// recipient addresses so duplicates are found.
func (s Schedule) Normalize() Schedule {
	if s.AttachmentLimitMB == 0 {
		s.AttachmentLimitMB = DefaultAttachMB
	}
	if s.CurrencyType == "" {
		s.CurrencyType = "acct"
	}
	if s.FiscalYearStartMonth == 0 {
		s.FiscalYearStartMonth = 1
	}
	rec := make([]string, len(s.Recipients))
	for i, r := range s.Recipients {
		rec[i] = strings.ToLower(strings.TrimSpace(r))
	}
	s.Recipients = rec
	return s
}

// NextRun is the next time the schedule fires after from, in its time zone.
func (s Schedule) NextRun(from time.Time) (time.Time, error) {
	spec, err := ParseCron(s.Cron)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := loadZone(s.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	return spec.Next(from.In(loc))
}

// loadZone accepts IANA names only; "Local" and "" would make the schedule
// depend on the server's clock settings.
func loadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("%q is not an IANA time zone", name)
	}
	return time.LoadLocation(name)
}

// Validate checks every field of the schedule with REPORT-0018. An empty result
// means the schedule may be saved.
func (s Schedule) Validate() []Issue {
	var out []Issue
	add := func(field, detail string) {
		out = append(out, Issue{Field: field, Code: CodeScheduleInvalid, Detail: detail})
	}
	if !codePattern.MatchString(s.Code) {
		add("code", "must be 2 to 30 characters: upper-case letters, digits or hyphens, starting with a letter")
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(s.Name)); n < 1 || n > 120 {
		add("name", "must be 1 to 120 characters")
	}
	if !codePattern.MatchString(s.Definition) {
		add("definition", "must name a definition code")
	}
	if _, err := ParseCron(s.Cron); err != nil {
		add("cron", err.Error())
	}
	if _, err := loadZone(s.Timezone); err != nil {
		add("timezone", "is not a known IANA time zone: "+s.Timezone)
	}
	if n := len(s.LedgerIDs); n < 1 || n > MaxScheduleLedgers {
		add("ledgerIds", fmt.Sprintf("must list 1 to %d ledgers", MaxScheduleLedgers))
	}
	if !uniqueAll(s.LedgerIDs, ids.Valid) {
		add("ledgerIds", "each ledger must be a UUID, listed once")
	}
	switch s.CurrencyType {
	case "txn", "acct", "rpt":
	default:
		add("currencyType", "must be txn, acct or rpt")
	}
	if len(s.Formats) == 0 {
		add("formats", "must list at least one format")
	}
	if !uniqueAll(s.Formats, func(f string) bool { return contains(ScheduleFormats, f) }) {
		add("formats", "formats must be pdf, xlsx, csv or html, each listed once")
	}
	if n := len(s.Recipients); n < 1 || n > MaxRecipients {
		add("recipients", fmt.Sprintf("must list 1 to %d recipients", MaxRecipients))
	}
	if !uniqueAll(s.Recipients, plainAddress) {
		add("recipients", "each recipient must be a plain email address, listed once")
	}
	if s.FiscalYearStartMonth < 1 || s.FiscalYearStartMonth > 12 {
		add("fiscalYearStartMonth", "must be a month from 1 to 12")
	}
	if s.AttachmentLimitMB < MinAttachMB || s.AttachmentLimitMB > MaxAttachMB {
		add("attachmentLimitMb", fmt.Sprintf("must be between %d and %d MB", MinAttachMB, MaxAttachMB))
	}
	return out
}

func plainAddress(r string) bool {
	addr, err := mail.ParseAddress(r)
	return err == nil && addr.Address == r && addr.Name == ""
}

// uniqueAll reports whether every item passes ok and none repeats.
func uniqueAll(items []string, ok func(string) bool) bool {
	seen := map[string]bool{}
	for _, it := range items {
		if !ok(it) || seen[it] {
			return false
		}
		seen[it] = true
	}
	return true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
