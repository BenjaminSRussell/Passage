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
)

// PartnerProfile is the X12 identity and SLA of one trading partner (FR-35.1).
type PartnerProfile struct {
	Name          string
	ISA           Party
	Environment   string
	AckSLAMinutes int
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
	return fields
}

// WithDefaults fills the fields a caller may leave out.
func (p PartnerProfile) WithDefaults() PartnerProfile {
	if p.AckSLAMinutes == 0 {
		p.AckSLAMinutes = DefaultAckSLAMinutes
	}
	return p
}

// MatchesSender reports whether an interchange sender identifies this partner
// (BR-35.1: the ISA qualifier and id must both match the profile).
func (p PartnerProfile) MatchesSender(sender Party) bool {
	return sender.Qualifier == p.ISA.Qualifier && sender.ID == p.ISA.ID
}
