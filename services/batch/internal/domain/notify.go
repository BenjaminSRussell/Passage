package domain

import (
	"regexp"
	"strings"
	"time"
)

const (
	ChannelEmail   = "email"
	ChannelInApp   = "in_app"
	ChannelWebhook = "webhook"

	MsgQueued   = "queued"
	MsgSending  = "sending"
	MsgSent     = "sent"
	MsgFailed   = "failed"
	MsgBounced  = "bounced"
	MsgRead     = "read"
	MsgCanceled = "canceled"

	LayerSys      = "sys"
	LayerCustomer = "customer"

	DigestNone   = "none"
	DigestDaily  = "daily"
	DigestWeekly = "weekly"

	PriorityImmediate = "immediate"
	PriorityBatch     = "batch"
)

var placeholderRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.]+)(?:\s*\|[^}]+)?\s*\}\}`)

// Template is a versioned message body.
type Template struct {
	Code     string
	Channel  string
	Language string
	Layer    string
	Version  int
	Subject  string
	Body     string
	Status   string
}

// Message is one outbound row.
type Message struct {
	ID              string
	Channel         string
	RecipientUserID string
	RecipientAddr   string
	TemplateCode    string
	Subject         string
	Body            string
	Status          string
	Attempts        int
	Priority        string
}

// Preference is per-user, per-event-class delivery settings.
type Preference struct {
	UserID     string
	EventClass string
	Channels   []string
	Digest     string
	QuietHours *[2]int
	Language   string
}

// ValidatePlaceholders rejects unknown {{tokens}} at publish time (AC-4.13).
func ValidatePlaceholders(subject, body string, allowed map[string]bool) error {
	for _, src := range []string{subject, body} {
		for _, match := range placeholderRe.FindAllStringSubmatch(src, -1) {
			name := match[1]
			if strings.HasPrefix(name, "text:") {
				continue
			}
			if allowed != nil && !allowed[name] {
				return errf(CodeUnknownPlaceholder, "unknown placeholder {{"+name+"}}")
			}
		}
	}
	return nil
}

// Render replaces {{path}} and {{path | filter}} tokens. Missing keys fail closed.
func Render(src string, data map[string]string) (string, error) {
	var failed string
	out := placeholderRe.ReplaceAllStringFunc(src, func(tok string) string {
		match := placeholderRe.FindStringSubmatch(tok)
		if len(match) < 2 {
			failed = tok
			return tok
		}
		name := match[1]
		if v, ok := data[name]; ok {
			return v
		}
		failed = name
		return tok
	})
	if failed != "" {
		return "", errf(CodeRenderFailed, "missing placeholder "+failed)
	}
	return out, nil
}

// NextAttempt returns when to retry and whether retries remain (BR-4.11).
func NextAttempt(attempts int, now time.Time) (time.Time, bool) {
	delays := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, time.Hour}
	if attempts < 1 || attempts > len(delays) {
		return time.Time{}, false
	}
	return now.Add(delays[attempts-1]), attempts < len(delays)
}

// HardBounce marks bouncing after three hard failures in the window (BR-4.13).
func HardBounce(priorHard int) (next int, bouncing bool) {
	next = priorHard + 1
	return next, next >= 3
}

// ShouldEmail reports whether immediate email is allowed for this preference.
func ShouldEmail(pref Preference, eventClass string) bool {
	if eventClass == "workflow_escalated" {
		return true
	}
	if pref.Digest == DigestDaily || pref.Digest == DigestWeekly {
		return false
	}
	for _, ch := range pref.Channels {
		if ch == ChannelEmail {
			return true
		}
	}
	return false
}

// DigestEligible keeps unread items for a digest email (AC-4.12).
func DigestEligible(unread bool, alreadyRead bool) bool {
	return unread && !alreadyRead
}

// Allowlisted reports whether a non-prod recipient may be mailed.
func Allowlisted(address string, domains []string) bool {
	if len(domains) == 0 {
		return true
	}
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return false
	}
	host := strings.ToLower(address[at+1:])
	for _, d := range domains {
		if strings.EqualFold(host, d) {
			return true
		}
	}
	return false
}

// ShippedTemplates are the sys templates required by FR-4.14.
func ShippedTemplates() []string {
	return []string{
		"workflow.workitem.assigned",
		"workflow.workitem.escalated",
		"workflow.workitem.delegated",
		"workflow.instance.completed",
		"workflow.instance.change_requested",
		"batch.run.failed",
		"identity.user.invited",
	}
}
