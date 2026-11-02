// Package events defines the CloudEvents envelope Passage publishes and the
// naming rule for event types: dev.passage.<context>.<entity>.<verb>.v<n>.
package events

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Event is a CloudEvents 1.0 structured envelope with Passage extensions.
type Event struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject,omitempty"`
	Time            time.Time       `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	Data            json.RawMessage `json:"data"`
	// Passage extensions
	TenantID      string `json:"tenantid"`
	CorrelationID string `json:"correlationid,omitempty"`
	CausationID   string `json:"causationid,omitempty"`
	Actor         string `json:"actor,omitempty"`
	// LegalEntityID is copied from the payload's top-level legalEntityId or
	// legal_entity_id so consumers that act per legal entity (the integration
	// hub's REST push) can route without knowing every payload shape.
	LegalEntityID string `json:"legalentityid,omitempty"`
}

var typeRe = regexp.MustCompile(`^dev\.passage\.[a-z][a-z0-9]*\.[a-z][a-z0-9]*\.[a-z][a-z0-9]*\.v[0-9]+$`)

// Type builds a conforming event type name.
func Type(context, entity, verb string, version int) string {
	return fmt.Sprintf("dev.passage.%s.%s.%s.v%d", context, entity, verb, version)
}

// ValidType reports whether t follows the naming rule.
func ValidType(t string) bool { return typeRe.MatchString(t) }

// New creates an event with a fresh ULID id and the current time. data is
// marshalled to JSON. It panics on an invalid type name: that is a programming
// error that must fail in tests, not in production.
func New(typ, source, subject, tenantID string, data any) (Event, error) {
	if !ValidType(typ) {
		panic("events: invalid event type " + typ)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	return Event{SpecVersion: "1.0", ID: ids.New(), Source: source, Type: typ, Subject: subject, Time: time.Now().UTC(),
		DataContentType: "application/json", Data: raw, TenantID: tenantID, LegalEntityID: payloadLegalEntity(raw)}, nil
}

// payloadLegalEntity returns the legal entity a JSON object payload names at
// its top level, or "" for any other payload.
func payloadLegalEntity(raw json.RawMessage) string {
	var p struct {
		Camel string `json:"legalEntityId"`
		Snake string `json:"legal_entity_id"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	if p.Camel != "" {
		return p.Camel
	}
	return p.Snake
}

// Topic returns the Kafka topic for an event type: passage.<context>.<entity>.
func Topic(typ string) string {
	parts := regexp.MustCompile(`\.`).Split(typ, -1)
	if len(parts) < 6 {
		return "passage.invalid"
	}
	return "passage." + parts[2] + "." + parts[3]
}
