package domain

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Process message sources, destinations and states (FR-25.17).
const (
	SourceKiosk  = "kiosk"
	SourceEdge   = "edge"
	SourceManual = "manual"

	DestEBR     = "ebr"
	DestJournal = "journal"
	DestQuality = "quality"
	DestKafka   = "kafka"
	DestWebhook = "webhook"

	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryError     = "error"

	MessageReceived  = "received"
	MessageDelivered = "delivered"
	MessagePartial   = "partially-delivered"
	MessageError     = "error"

	OutcomeDelivered = "delivered"
	OutcomeFailed    = "failed"

	// MaxDeliveryAttempts is the initial attempt plus the four retries at 1, 5, 15 and 60 minutes.
	MaxDeliveryAttempts = 5

	maxSourceIDLen  = 128
	maxPhasePathLen = 255
	maxCharacters   = 50
	maxNameLen      = 64
	maxTextLen      = 500

	// CodeOutOfLimitRejected is raised when a reject-out-of-limit category receives an out-of-limit value (BR-25.13).
	CodeOutOfLimitRejected = "SF-0114"
	// CodeInvalidValue is raised when a characteristic value does not match its declared type or its limits are malformed.
	CodeInvalidValue = "SF-0115"
	// CodeNotReprocessable is raised when a delivery is not in the error state.
	CodeNotReprocessable = "SF-0116"
)

// Category is a process message category with its destinations (FR-25.17).
// The categories and their destinations are fixed defaults until category
// master data (O05C/O06C/O07C equivalents) is configurable per tenant.
type Category struct {
	Code             string
	Destinations     []string
	RejectOutOfLimit bool
}

// CategoryByCode returns the category definition for code.
func CategoryByCode(code string) (Category, bool) {
	switch code {
	case "phase-start", "phase-end":
		return Category{Code: code, Destinations: []string{DestEBR, DestKafka}}, true
	case "consumption", "yield":
		return Category{Code: code, Destinations: []string{DestJournal, DestEBR, DestKafka}}, true
	case "process-data":
		return Category{Code: code, Destinations: []string{DestEBR, DestKafka, DestWebhook}}, true
	case "quality-result":
		return Category{Code: code, Destinations: []string{DestQuality, DestEBR, DestKafka}}, true
	case "deviation":
		return Category{Code: code, Destinations: []string{DestEBR, DestQuality, DestKafka, DestWebhook}}, true
	case "order-status":
		return Category{Code: code, Destinations: []string{DestKafka, DestWebhook}}, true
	case "custom":
		return Category{Code: code, Destinations: []string{DestKafka}}, true
	}
	return Category{}, false
}

// KnownDestination reports whether d is one of the five destinations.
func KnownDestination(d string) bool {
	switch d {
	case DestEBR, DestJournal, DestQuality, DestKafka, DestWebhook:
		return true
	}
	return false
}

// Characteristic is one named value in a process message, checked against its type and limits.
type Characteristic struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Value      string  `json:"value"`
	Min        *string `json:"min,omitempty"`
	Max        *string `json:"max,omitempty"`
	Unit       *string `json:"unit,omitempty"`
	OutOfLimit bool    `json:"outOfLimit"`
}

// NewMessage is an incoming process message before storage.
type NewMessage struct {
	Source          string
	SourceMessageID string
	CategoryCode    string
	OrderID         string
	PhasePath       string
	OccurredAt      time.Time
	Values          []Characteristic
}

// FieldErrors checks the shape of a message. Keys are JSON field names.
func (m NewMessage) FieldErrors() map[string]string {
	f := map[string]string{}
	switch m.Source {
	case SourceKiosk, SourceEdge, SourceManual:
	default:
		f["source"] = "must be one of kiosk, edge, manual"
	}
	if n := utf8.RuneCountInString(m.SourceMessageID); n < 1 || n > maxSourceIDLen {
		f["sourceMessageId"] = fmt.Sprintf("must be 1 to %d characters", maxSourceIDLen)
	}
	if _, ok := CategoryByCode(m.CategoryCode); !ok {
		f["categoryCode"] = "is not a known category"
	}
	if !ids.Valid(m.OrderID) {
		f["orderId"] = "must be a UUID"
	}
	if utf8.RuneCountInString(m.PhasePath) > maxPhasePathLen {
		f["phasePath"] = fmt.Sprintf("must be at most %d characters", maxPhasePathLen)
	}
	if m.OccurredAt.IsZero() {
		f["occurredAt"] = "is required"
	}
	if len(m.Values) > maxCharacters {
		f["values"] = fmt.Sprintf("must hold at most %d characteristics", maxCharacters)
	}
	seen := map[string]bool{}
	for i, v := range m.Values {
		key := fmt.Sprintf("values[%d]", i)
		if n := utf8.RuneCountInString(v.Name); n < 1 || n > maxNameLen {
			f[key+".name"] = fmt.Sprintf("must be 1 to %d characters", maxNameLen)
		} else if seen[v.Name] {
			f[key+".name"] = "must be unique within the message"
		}
		seen[v.Name] = true
		switch v.Type {
		case "numeric", "text", "boolean":
		default:
			f[key+".type"] = "must be numeric, text or boolean"
		}
	}
	return f
}

func invalidValue(name, reason string) error {
	return &Error{Code: CodeInvalidValue, Detail: fmt.Sprintf("value of %s %s", name, reason)}
}

func parseBound(name string, s *string) (*decimal.Decimal, error) {
	if s == nil {
		return nil, nil
	}
	d, err := decimal.NewFromString(*s)
	if err != nil {
		return nil, invalidValue(name, "limit must be a decimal number")
	}
	return &d, nil
}

// ApplyLimits checks each characteristic against its type and limits (BR-25.13).
// An out-of-limit numeric is flagged and accepted, unless the category rejects
// out-of-limit values, in which case the message fails with SF-0014.
func ApplyLimits(cat Category, in []Characteristic) ([]Characteristic, error) {
	out := make([]Characteristic, 0, len(in))
	for _, c := range in {
		c.OutOfLimit = false
		switch c.Type {
		case "numeric":
			v, err := decimal.NewFromString(c.Value)
			if err != nil {
				return nil, invalidValue(c.Name, "must be a decimal number")
			}
			lo, err := parseBound(c.Name, c.Min)
			if err != nil {
				return nil, err
			}
			hi, err := parseBound(c.Name, c.Max)
			if err != nil {
				return nil, err
			}
			if lo != nil && hi != nil && lo.GreaterThan(*hi) {
				return nil, invalidValue(c.Name, "has a minimum above its maximum")
			}
			if (lo != nil && v.LessThan(*lo)) || (hi != nil && v.GreaterThan(*hi)) {
				c.OutOfLimit = true
				if cat.RejectOutOfLimit {
					return nil, &Error{Code: CodeOutOfLimitRejected, Detail: fmt.Sprintf("value %s of %s is outside %s–%s and category %s rejects out-of-limit values",
						c.Value, c.Name, bound(c.Min), bound(c.Max), cat.Code)}
				}
			}
		case "boolean":
			if c.Value != "true" && c.Value != "false" {
				return nil, invalidValue(c.Name, "must be true or false")
			}
			if c.Min != nil || c.Max != nil {
				return nil, invalidValue(c.Name, "cannot have limits")
			}
		case "text":
			if utf8.RuneCountInString(c.Value) > maxTextLen {
				return nil, invalidValue(c.Name, fmt.Sprintf("must be at most %d characters", maxTextLen))
			}
			if c.Min != nil || c.Max != nil {
				return nil, invalidValue(c.Name, "cannot have limits")
			}
		default:
			return nil, invalidValue(c.Name, "has an unknown type")
		}
		out = append(out, c)
	}
	return out, nil
}

// AnyOutOfLimit reports whether a characteristic was flagged out of limit, which
// the EBR consumer records as an exception (BR-25.13).
func AnyOutOfLimit(values []Characteristic) bool {
	for _, v := range values {
		if v.OutOfLimit {
			return true
		}
	}
	return false
}

func bound(s *string) string {
	if s == nil {
		return "…"
	}
	return *s
}

// RetryDelay returns the wait after the given number of failed attempts (FR-25.17: 1, 5, 15, 60 minutes).
func RetryDelay(failedAttempts int) (time.Duration, bool) {
	switch failedAttempts {
	case 1:
		return time.Minute, true
	case 2:
		return 5 * time.Minute, true
	case 3:
		return 15 * time.Minute, true
	case 4:
		return time.Hour, true
	}
	return 0, false
}

// Delivery is the state of one message at one destination.
type Delivery struct {
	Destination   string     `json:"destination"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	LastError     *string    `json:"lastError"`
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
	DeliveredAt   *time.Time `json:"deliveredAt"`
}

// Report applies one outcome reported for the destination. A delivered
// destination is final, so reporting again is a no-op. After MaxDeliveryAttempts
// failures the destination moves to error and waits for a manual reprocess.
func (d Delivery) Report(outcome string, errText *string, now time.Time) (Delivery, error) {
	if d.Status == DeliveryDelivered {
		return d, nil
	}
	switch outcome {
	case OutcomeDelivered:
		at := now
		d.Status, d.DeliveredAt, d.LastError, d.NextAttemptAt = DeliveryDelivered, &at, nil, nil
		return d, nil
	case OutcomeFailed:
		if d.Status == DeliveryError {
			return d, nil
		}
		d.Attempts++
		msg := "delivery failed"
		if errText != nil && *errText != "" {
			msg = *errText
		}
		d.LastError = &msg
		if d.Attempts >= MaxDeliveryAttempts {
			d.Status, d.NextAttemptAt = DeliveryError, nil
			return d, nil
		}
		delay, _ := RetryDelay(d.Attempts)
		next := now.Add(delay)
		d.Status, d.NextAttemptAt = DeliveryPending, &next
		return d, nil
	}
	return d, &Error{Code: "COM-0001", Detail: "outcome must be delivered or failed"}
}

// Reprocess moves a destination in error back to pending for a manual retry.
func (d Delivery) Reprocess(now time.Time) (Delivery, error) {
	if d.Status != DeliveryError {
		return d, &Error{Code: CodeNotReprocessable, Detail: "only a destination in error can be reprocessed"}
	}
	d.Status, d.Attempts, d.NextAttemptAt = DeliveryPending, 0, &now
	return d, nil
}

// RollupStatus derives the message status from its deliveries (FR-25.17):
// delivered when every destination is, partially-delivered when some are, received
// while any is still pending, and error when none is delivered and none can retry.
func RollupStatus(ds []Delivery) string {
	if len(ds) == 0 {
		return MessageReceived
	}
	delivered, pending := 0, 0
	for _, d := range ds {
		switch d.Status {
		case DeliveryDelivered:
			delivered++
		case DeliveryPending:
			pending++
		}
	}
	switch {
	case delivered == len(ds):
		return MessageDelivered
	case delivered > 0:
		return MessagePartial
	case pending > 0:
		return MessageReceived
	}
	return MessageError
}
