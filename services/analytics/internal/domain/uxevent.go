package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// UX events (#510). The web app reports how people move through a flow so the
// team can see where they stop. An event carries a random per-tab session id and
// the release, never a user id, an email or a record id: nothing here can be
// tied back to a person. A search query is kept only when it has no "@" and fewer
// than seven digits (see ScrubQuery), and is cut to MaxQueryLen.
const (
	MaxUXBatch  = 50
	MaxQueryLen = 80
	// A query with this many digits in all looks like an account, phone or document number.
	MaxQueryDigits = 7

	// Event types.
	UXFunnelStep      = "funnel_step"
	UXValidationError = "validation_error"
	UXSearchNoResults = "search_no_results"
	UXFormAbandoned   = "form_abandoned"

	// How far from now a client timestamp may be before the server uses its own clock.
	uxMaxPast   = 24 * time.Hour
	uxMaxFuture = 5 * time.Minute
)

// Funnels is the ordered steps of each measured flow. The web app sends a
// funnel_step event when a person reaches a step; the query counts the sessions
// that reached each one.
var Funnels = map[string][]string{
	"procure_to_pay": {
		"requisition_created", "requisition_submitted", "purchase_order_created",
		"receipt_posted", "vendor_invoice_posted", "payment_proposal_created",
	},
	"onboarding": {
		"legal_entity_created", "fiscal_calendar_set", "site_created", "party_added", "product_created", "first_transaction",
	},
}

// FunnelNames lists the measured flows in a stable order.
func FunnelNames() []string { return []string{"onboarding", "procure_to_pay"} }

var (
	sessionPattern = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)
	releasePattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,32}$`)
	fieldPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._\[\]-]{0,63}$`)
	routePattern   = regexp.MustCompile(`^/[A-Za-z0-9/_:.{}*-]{0,199}$`)
)

// UXEvent is one thing a person did. Type decides which other fields are needed.
type UXEvent struct {
	SessionID  string     `json:"sessionId"`
	Release    string     `json:"release"`
	Type       string     `json:"type"`
	Flow       string     `json:"flow,omitempty"`
	Step       string     `json:"step,omitempty"`
	Route      string     `json:"route,omitempty"`
	Field      string     `json:"field,omitempty"`
	Query      string     `json:"query,omitempty"`
	DurationMS *int       `json:"durationMs,omitempty"`
	OccurredAt *time.Time `json:"occurredAt,omitempty"`
}

// ScrubQuery prepares a search query for storage: trimmed, lower-cased and cut
// to MaxQueryLen. A query that looks like it holds an email address or a long
// number (an account, phone or document number) is dropped, and ok is false.
func ScrubQuery(q string) (string, bool) {
	q = strings.ToLower(strings.TrimSpace(q))
	digits := 0
	for _, r := range q {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	if q == "" || strings.Contains(q, "@") || digits >= MaxQueryDigits {
		return "", false
	}
	if utf8.RuneCountInString(q) > MaxQueryLen {
		q = string([]rune(q)[:MaxQueryLen])
	}
	return q, true
}

// PrepareUXEvents validates a batch and returns the events ready to store, with
// each timestamp settled (the client's when it is recent, otherwise now). A
// search event whose query fails ScrubQuery is not an error and has nothing
// left to report, so it is left out and counted in dropped.
func PrepareUXEvents(in []UXEvent, now time.Time) (out []UXEvent, dropped int, err *Error) {
	if len(in) == 0 || len(in) > MaxUXBatch {
		return nil, 0, validation(map[string]string{"events": "between 1 and " + strconv.Itoa(MaxUXBatch) + " events"})
	}
	fields := map[string]string{}
	for i, e := range in {
		key := "events[" + strconv.Itoa(i) + "]"
		if msg := checkUXEvent(&e); msg != "" {
			fields[key] = msg
			continue
		}
		if e.Type == UXSearchNoResults {
			q, ok := ScrubQuery(e.Query)
			if !ok {
				dropped++
				continue
			}
			e.Query = q
		}
		at := now
		if e.OccurredAt != nil && e.OccurredAt.After(now.Add(-uxMaxPast)) && e.OccurredAt.Before(now.Add(uxMaxFuture)) {
			at = *e.OccurredAt
		}
		at = at.UTC()
		e.OccurredAt = &at
		out = append(out, e)
	}
	if len(fields) > 0 {
		return nil, 0, validation(fields)
	}
	return out, dropped, nil
}

func checkUXEvent(e *UXEvent) string {
	switch {
	case !sessionPattern.MatchString(e.SessionID):
		return "sessionId must be 8 to 64 letters, digits or dashes"
	case !releasePattern.MatchString(e.Release):
		return "release must be 1 to 32 characters such as 2026.11.1"
	case e.DurationMS != nil && (*e.DurationMS < 0 || *e.DurationMS > 24*60*60*1000):
		return "durationMs must be between 0 and 86400000"
	case e.Route != "" && !routePattern.MatchString(e.Route):
		return "route must be a path template without a query string"
	}
	switch e.Type {
	case UXFunnelStep:
		steps, ok := Funnels[e.Flow]
		if !ok {
			return "flow must be one of " + strings.Join(FunnelNames(), ", ")
		}
		for _, s := range steps {
			if s == e.Step {
				return ""
			}
		}
		return "step is not a step of the " + e.Flow + " flow"
	case UXValidationError:
		if e.Route == "" || !fieldPattern.MatchString(e.Field) {
			return "route and field are required; field is a field name, not its value"
		}
	case UXSearchNoResults:
		if strings.TrimSpace(e.Query) == "" {
			return "query is required"
		}
	case UXFormAbandoned:
		if e.Route == "" {
			return "route is required"
		}
	default:
		return "type must be funnel_step, validation_error, search_no_results or form_abandoned"
	}
	return ""
}

// FunnelStep is how many sessions reached a step.
type FunnelStep struct {
	Step string `json:"step"`
	// Sessions that sent this step.
	Sessions int `json:"sessions"`
	// Share of the previous step's sessions that also reached this one, 0 to 1; 1 for the first step.
	ConversionFromPrevious float64 `json:"conversionFromPrevious"`
}

// Funnel is a flow's sessions by step for a period. Steps are counted by
// session, so a session that skips a step is still counted at the later one.
type Funnel struct {
	Flow    string       `json:"flow"`
	From    string       `json:"from"`
	To      string       `json:"to"`
	Release string       `json:"release,omitempty"`
	Steps   []FunnelStep `json:"steps"`
	// SecondsToLastStep is the median time in seconds from a session's first
	// event in the flow to its last step, over the sessions that reached the last
	// step. Nil when none did.
	SecondsToLastStep *float64 `json:"secondsToLastStep,omitempty"`
}

// BuildFunnel turns per-step session counts into a funnel with conversion.
func BuildFunnel(flow, from, to, release string, counts map[string]int, secondsToLast *float64) Funnel {
	f := Funnel{Flow: flow, From: from, To: to, Release: release, Steps: []FunnelStep{}, SecondsToLastStep: secondsToLast}
	prev := 0
	for i, step := range Funnels[flow] {
		n := counts[step]
		conv := 1.0
		if i > 0 {
			conv = 0
			if prev > 0 {
				conv = float64(n) / float64(prev)
				if conv > 1 {
					conv = 1
				}
			}
		}
		f.Steps = append(f.Steps, FunnelStep{Step: step, Sessions: n, ConversionFromPrevious: conv})
		prev = n
	}
	return f
}

// UXSignals are the three friction lists for a period: where validation fails,
// what people search for and do not find, and which forms they leave unsaved.
type UXSignals struct {
	From             string             `json:"from"`
	To               string             `json:"to"`
	Release          string             `json:"release,omitempty"`
	ValidationErrors []ValidationSignal `json:"validationErrors"`
	NoResultSearches []SearchSignal     `json:"noResultSearches"`
	AbandonedForms   []AbandonedSignal  `json:"abandonedForms"`
}

// ValidationSignal counts validation errors on one field of one page.
type ValidationSignal struct {
	Route string `json:"route"`
	Field string `json:"field"`
	Count int    `json:"count"`
}

// SearchSignal counts searches that found nothing.
type SearchSignal struct {
	Query string `json:"query"`
	Count int    `json:"count"`
}

// AbandonedSignal counts forms left unsaved on one page.
type AbandonedSignal struct {
	Route string `json:"route"`
	Count int    `json:"count"`
}

// ValidateUXPeriod checks a funnel or signals request: dates, an optional release.
func ValidateUXPeriod(from, to, release string) (time.Time, time.Time, *Error) {
	fields := map[string]string{}
	f, ferr := time.Parse(time.DateOnly, from)
	t, terr := time.Parse(time.DateOnly, to)
	if ferr != nil {
		fields["from"] = "YYYY-MM-DD"
	}
	if terr != nil {
		fields["to"] = "YYYY-MM-DD"
	}
	if ferr == nil && terr == nil && (t.Before(f) || t.Sub(f) > 366*24*time.Hour) {
		fields["to"] = "on or after from, within one year"
	}
	if release != "" && !releasePattern.MatchString(release) {
		fields["release"] = "1 to 32 characters such as 2026.11.1"
	}
	if len(fields) > 0 {
		return time.Time{}, time.Time{}, validation(fields)
	}
	return f, t, nil
}
