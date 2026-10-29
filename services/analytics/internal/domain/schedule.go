package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Schedule and export limits (FR-46.9, FR-46.10, BR-46.7, §9.46.13).
const (
	// DefaultScheduleRetries is the delivery retry count (3 retries, 1/4/16 minutes).
	DefaultScheduleRetries = 3

	MaxScheduleRetries = 5

	// AsyncExportRows is the row count above which an export runs as a Temporal job.
	AsyncExportRows = 100_000

	// DefaultExportMaxRows is the export cap (analytics.export.max_rows).
	DefaultExportMaxRows = 1_000_000

	// MaxExportRows is the ceiling for analytics.export.max_rows.
	MaxExportRows = 5_000_000

	DefaultLinkTTLHours = 24
	MinLinkTTLHours     = 1
	MaxLinkTTLHours     = 168

	// ExportReuseWindow: an identical export requested within this window returns the existing file.
	ExportReuseWindow = time.Hour

	// ExportFileLifetime: export files are deleted this long after creation.
	ExportFileLifetime = 24 * time.Hour

	CodeExportTooLarge = "ANA-0101"
)

// ExportMode is where an export runs.
const (
	ExportInline = "INLINE"
	ExportJob    = "TEMPORAL_JOB"
)

// Export formats the explorer writes. Power BI and Tableau connect through Trino
// and are not file exports.
var exportFormats = []string{"XLSX", "CSV"}

// ValidateExportFormat accepts XLSX and CSV.
func ValidateExportFormat(format string) error {
	if !slices.Contains(exportFormats, format) {
		return &Error{Code: CodeValidation, Status: 400, Detail: "format must be XLSX or CSV", Fields: map[string]string{"format": "XLSX or CSV"}}
	}
	return nil
}

// ChooseExport validates the row count against the cap and picks the mode.
func ChooseExport(rows, maxRows int64) (string, *Error) {
	if maxRows <= 0 || maxRows > MaxExportRows {
		maxRows = DefaultExportMaxRows
	}
	if rows < 0 {
		return "", &Error{Code: CodeValidation, Status: 400, Detail: "row count must not be negative"}
	}
	if rows > maxRows {
		return "", &Error{Code: CodeExportTooLarge, Status: 422, Detail: fmt.Sprintf("export exceeds the %d row limit", maxRows)}
	}
	if rows > AsyncExportRows {
		return ExportJob, nil
	}
	return ExportInline, nil
}

// ValidateLinkTTL checks the signed link lifetime in hours.
func ValidateLinkTTL(hours int) error {
	if hours < MinLinkTTLHours || hours > MaxLinkTTLHours {
		return &Error{Code: CodeValidation, Status: 400, Detail: "link ttl must be 1-168 hours", Fields: map[string]string{"linkTtlHours": "1 to 168 hours"}}
	}
	return nil
}

// QueryHash fingerprints an export request so identical requests can be matched.
// canonical is the request rendered in a stable form (sorted measures, filters).
func QueryHash(canonical string) string {
	s := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(s[:])
}

// CanReuseExport reports whether an identical export created at created can be
// returned instead of starting a new one (BR-46.7).
func CanReuseExport(created, now time.Time) bool {
	return !now.Before(created) && now.Sub(created) < ExportReuseWindow
}

// ExportDeleteAt is when the export file is removed (BR-46.7).
func ExportDeleteAt(created time.Time) time.Time { return created.Add(ExportFileLifetime) }

// LinkExpiresAt is when the signed download link stops working.
func LinkExpiresAt(created time.Time, ttlHours int) time.Time {
	return created.Add(time.Duration(ttlHours) * time.Hour)
}

// RetryDelay is the wait before retry attempt n (1-based): 1, 4, 16, 64, 256
// minutes. ok is false when n is beyond the configured retry count, meaning the
// delivery is FAILED.
func RetryDelay(attempt, maxRetries int) (time.Duration, bool) {
	if attempt < 1 || attempt > maxRetries || maxRetries > MaxScheduleRetries {
		return 0, false
	}
	minutes := time.Duration(1)
	for i := 1; i < attempt; i++ {
		minutes *= 4
	}
	return minutes * time.Minute, true
}

// Cron is a parsed five-field schedule: minute hour day-of-month month day-of-week.
// Supported syntax per field: *, n, a-b, a,b,..., */step, a-b/step, and L (last
// day of month) in the day-of-month field. Day-of-week 0 and 7 are Sunday.
type Cron struct {
	minutes []int
	hours   []int
	months  []int
	dows    []int
	dom     []int
	lastDay bool
	domAny  bool
	dowAny  bool
	loc     *time.Location
}

// ParseCron parses expr and binds it to a time zone name.
func ParseCron(expr, timezone string) (Cron, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil || timezone == "" || timezone == "Local" {
		return Cron{}, &Error{Code: CodeValidation, Status: 400, Detail: "unknown time zone", Fields: map[string]string{"timezone": "an IANA time zone such as Europe/Berlin"}}
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Cron{}, &Error{Code: CodeValidation, Status: 400, Detail: "cron must have five fields", Fields: map[string]string{"cron": "five fields: minute hour day-of-month month day-of-week"}}
	}
	c := Cron{loc: loc, domAny: f[2] == "*", dowAny: f[4] == "*"}
	var perr error
	c.minutes, perr = parseField(f[0], 0, 59)
	if perr == nil {
		c.hours, perr = parseField(f[1], 0, 23)
	}
	if perr == nil && f[2] == "L" {
		c.lastDay = true
	} else if perr == nil {
		c.dom, perr = parseField(f[2], 1, 31)
	}
	if perr == nil {
		c.months, perr = parseField(f[3], 1, 12)
	}
	if perr == nil {
		c.dows, perr = parseField(f[4], 0, 7)
	}
	if perr != nil {
		return Cron{}, &Error{Code: CodeValidation, Status: 400, Detail: perr.Error(), Fields: map[string]string{"cron": perr.Error()}}
	}
	for i, d := range c.dows {
		if d == 7 {
			c.dows[i] = 0
		}
	}
	return c, nil
}

func parseField(s string, lo, hi int) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		step, stepped := 1, false
		if base, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid step in %q", s)
			}
			step, part, stepped = n, base, true
		}
		from, to := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			x, err1 := strconv.Atoi(a)
			y, err2 := strconv.Atoi(b)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid range in %q", s)
			}
			from, to = x, y
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid value in %q", s)
			}
			from, to = n, n
			if stepped {
				to = hi // "5/15" means 5-hi/15, as in standard cron.
			}
		}
		if from < lo || to > hi || from > to {
			return nil, fmt.Errorf("value out of range in %q", s)
		}
		for v := from; v <= to; v += step {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func (c Cron) dayMatches(t time.Time) bool {
	if !slices.Contains(c.months, int(t.Month())) {
		return false
	}
	domOK := false
	if c.lastDay {
		domOK = t.AddDate(0, 0, 1).Day() == 1
	} else {
		domOK = slices.Contains(c.dom, t.Day())
	}
	dowOK := slices.Contains(c.dows, int(t.Weekday()))
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dowOK
	case c.dowAny:
		return domOK
	}
	// Both restricted: standard cron matches when either matches.
	return domOK || dowOK
}

// Next returns the first run strictly after after, in the schedule's time zone.
// It searches up to five years ahead and returns an error if nothing matches.
func (c Cron) Next(after time.Time) (time.Time, error) {
	local := after.In(c.loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.loc)
	for range 366 * 5 {
		if c.dayMatches(day) {
			for _, h := range c.hours {
				for _, m := range c.minutes {
					cand := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, c.loc)
					if cand.After(after) {
						return cand, nil
					}
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}, errors.New("analytics: cron has no run within five years")
}

// Schedule targets, formats and destinations (FR-46.10, report_schedule).
const (
	TargetDashboard = "DASHBOARD"
	TargetQuery     = "QUERY"

	DestinationEmail = "EMAIL"
	DestinationSFTP  = "SFTP"
	DestinationInbox = "INBOX"

	// MaxScheduleRecipients bounds one destination's recipient list.
	MaxScheduleRecipients = 50
)

var (
	scheduleFormats = []string{"PDF", "XLSX"}
	targetIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	emailPattern    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	sftpPattern     = regexp.MustCompile(`^sftp://[A-Za-z0-9.-]+(:[0-9]{1,5})?/\S*$`)
)

// Destination is where a scheduled report is delivered (destination_json).
// Targets are e-mail addresses (EMAIL), one sftp:// URL (SFTP) or user ids (INBOX).
type Destination struct {
	Type    string   `json:"type"`
	Targets []string `json:"targets"`
}

// Schedule is one scheduled delivery of a dashboard or explorer query.
type Schedule struct {
	ID           string      `json:"scheduleId"`
	Name         string      `json:"name"`
	TargetType   string      `json:"targetType"`
	TargetID     string      `json:"targetId"`
	Cron         string      `json:"cron"`
	Timezone     string      `json:"timezone"`
	Format       string      `json:"format"`
	Destination  Destination `json:"destination"`
	LastRunAt    *time.Time  `json:"lastRunAt,omitempty"`
	NextRunAt    *time.Time  `json:"nextRunAt,omitempty"`
	FailureCount int         `json:"failureCount"`
	Version      int         `json:"version"`
}

// PrepareSchedule validates a new schedule and computes its first run strictly
// after now. Every invalid field is reported at once.
func PrepareSchedule(s Schedule, now time.Time) (Schedule, *Error) {
	fields := map[string]string{}
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" || len(s.Name) > 120 {
		fields["name"] = "required, at most 120 characters"
	}
	if s.TargetType != TargetDashboard && s.TargetType != TargetQuery {
		fields["targetType"] = "DASHBOARD or QUERY"
	}
	if !targetIDPattern.MatchString(s.TargetID) {
		fields["targetId"] = "the dashboard or saved query id"
	}
	if !slices.Contains(scheduleFormats, s.Format) {
		fields["format"] = "PDF or XLSX"
	}
	if msg := destinationProblem(s.Destination); msg != "" {
		fields["destination"] = msg
	}
	cron, cerr := ParseCron(s.Cron, s.Timezone)
	var cronErr *Error
	if errors.As(cerr, &cronErr) {
		for k, v := range cronErr.Fields {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		return Schedule{}, validation(fields)
	}
	next, err := cron.Next(now)
	if err != nil {
		return Schedule{}, validation(map[string]string{"cron": "never fires within five years"})
	}
	next = next.UTC()
	s.NextRunAt = &next
	s.LastRunAt, s.FailureCount = nil, 0
	return s, nil
}

func destinationProblem(d Destination) string {
	if len(d.Targets) == 0 || len(d.Targets) > MaxScheduleRecipients {
		return "between 1 and 50 targets"
	}
	for _, t := range d.Targets {
		switch d.Type {
		case DestinationEmail:
			if !emailPattern.MatchString(t) {
				return "EMAIL targets must be e-mail addresses"
			}
		case DestinationSFTP:
			if len(d.Targets) != 1 || !sftpPattern.MatchString(t) {
				return "SFTP takes one sftp://host/path target"
			}
		case DestinationInbox:
			if !ids.Valid(t) {
				return "INBOX targets must be user ids"
			}
		default:
			return "type must be EMAIL, SFTP or INBOX"
		}
	}
	return ""
}
