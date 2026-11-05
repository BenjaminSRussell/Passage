package domain

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// DefaultAckSLAMinutes is the 997 expectation when a profile does not set one.
const DefaultAckSLAMinutes = 60

var (
	isaQualifierRe = regexp.MustCompile(`^[A-Z0-9]{2}$`)
	isaIDRe        = regexp.MustCompile(`^[A-Za-z0-9]{1,15}$`)
	// UNB S002/S003: 0004 an..35 and 0007 an..4.
	edifactIDRe        = regexp.MustCompile(`^[A-Za-z0-9._-]{1,35}$`)
	edifactQualifierRe = regexp.MustCompile(`^[A-Za-z0-9]{1,4}$`)
)

// When a partner's inbound EDIFACT interchanges are answered with a CONTRL.
const (
	ContrlRequested = "REQUESTED" // when the UNB asks for one (0031 = 1); the default
	ContrlAlways    = "ALWAYS"    // always, whatever the UNB asks
	ContrlNever     = "NEVER"     // never
)

// EdifactIdentity is the UNB sender identification of a partner (id and
// code qualifier, the qualifier may be empty) and when its interchanges are
// answered with a CONTRL (ContrlRequested, ContrlAlways or ContrlNever).
type EdifactIdentity struct {
	ID        string
	Qualifier string `json:",omitempty"`
	Contrl    string `json:",omitempty"`
}

// MatchesSender reports whether a UNB sender identifies this partner: the
// id and the qualifier must both match (an empty qualifier matches only an
// empty one), like the ISA sender check.
func (e EdifactIdentity) MatchesSender(sender EdifactParty) bool {
	return sender.ID == e.ID && sender.Qualifier == e.Qualifier
}

// WantsContrl reports whether a received interchange is answered with a
// CONTRL: always, never, or as its UNB asks (0031 = 1).
func (e EdifactIdentity) WantsContrl(ackRequested bool) bool {
	switch e.Contrl {
	case ContrlAlways:
		return true
	case ContrlNever:
		return false
	}
	return ackRequested
}

// PartnerProfile is the X12 identity, the optional EDIFACT identity and the SLA
// of one trading partner (FR-35.1).
type PartnerProfile struct {
	Name          string
	ISA           Party
	Environment   string
	AckSLAMinutes int
	// EDIFACT, when set, lets the partner send EDIFACT interchanges: their
	// UNB sender resolves the partner the way ISA05/ISA06 do for X12.
	EDIFACT *EdifactIdentity `json:",omitempty"`
}

// Validate returns field errors keyed by field name; an empty map means valid.
// A zero AckSLAMinutes is allowed and means the default.
func (p PartnerProfile) Validate() map[string]string {
	fields := map[string]string{}
	if n := utf8.RuneCountInString(strings.TrimSpace(p.Name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if !isaQualifierRe.MatchString(p.ISA.Qualifier) {
		fields["isaQualifier"] = "must be two upper-case letters or digits"
	}
	if !isaIDRe.MatchString(p.ISA.ID) {
		fields["isaId"] = "must be 1 to 15 letters or digits"
	}
	if p.Environment != "TEST" && p.Environment != "PROD" {
		fields["environment"] = "must be TEST or PROD"
	}
	if p.AckSLAMinutes != 0 && (p.AckSLAMinutes < 5 || p.AckSLAMinutes > 1440) {
		fields["ackSlaMinutes"] = "must be 5 to 1440"
	}
	if e := p.EDIFACT; e != nil {
		if !edifactIDRe.MatchString(e.ID) {
			fields["edifactId"] = "must be 1 to 35 letters, digits, '.', '_' or '-'"
		}
		if e.Qualifier != "" && !edifactQualifierRe.MatchString(e.Qualifier) {
			fields["edifactQualifier"] = "must be 1 to 4 letters or digits"
		}
		switch e.Contrl {
		case "", ContrlRequested, ContrlAlways, ContrlNever:
		default:
			fields["edifactContrl"] = "must be REQUESTED, ALWAYS or NEVER"
		}
	}
	return fields
}

// WithDefaults fills the fields a caller may leave out.
func (p PartnerProfile) WithDefaults() PartnerProfile {
	if p.AckSLAMinutes == 0 {
		p.AckSLAMinutes = DefaultAckSLAMinutes
	}
	if p.EDIFACT != nil && p.EDIFACT.Contrl == "" {
		e := *p.EDIFACT
		e.Contrl = ContrlRequested
		p.EDIFACT = &e
	}
	return p
}

// MatchesSender reports whether an interchange sender identifies this partner
// (BR-35.1: the ISA qualifier and id must both match the profile).
func (p PartnerProfile) MatchesSender(sender Party) bool {
	return sender.Qualifier == p.ISA.Qualifier && sender.ID == p.ISA.ID
}
