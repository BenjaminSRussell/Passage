package domain

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Profile change kinds and statuses (ESS personal data, #267).
const (
	ChangePreferredName = "PREFERRED_NAME"
	ChangeHomeAddress   = "HOME_ADDRESS"
	ChangeBankAccount   = "BANK_ACCOUNT"

	ChangeApplied  = "APPLIED"
	ChangePending  = "PENDING"
	ChangeApproved = "APPROVED"
	ChangeRejected = "REJECTED"
	ChangeCanceled = "CANCELED"

	CodeProfileChangeState = "HCM-ESS-004"
	CodeProfileChangeHR    = "HCM-ESS-005"
	CodeProfileChangeSelf  = "HCM-ESS-006"

	maxPreferredName = 100
	maxAddressLine   = 200
	maxCity          = 100
	maxPostalCode    = 20
)

var (
	ProfileChangeKinds = []string{ChangePreferredName, ChangeHomeAddress, ChangeBankAccount}
	countryCode        = regexp.MustCompile(`^[A-Z]{2}$`)
	bankAccount        = regexp.MustCompile(`^[A-Z0-9]{8,34}$`)
)

// Address is a worker's home address, held sealed in the PII vault.
type Address struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city"`
	Region     string `json:"region,omitempty"`
	PostalCode string `json:"postalCode"`
	Country    string `json:"country"`
}

// Normalize trims every part and upper-cases the country code.
func (a Address) Normalize() Address {
	return Address{Line1: strings.TrimSpace(a.Line1), Line2: strings.TrimSpace(a.Line2), City: strings.TrimSpace(a.City),
		Region: strings.TrimSpace(a.Region), PostalCode: strings.TrimSpace(a.PostalCode), Country: strings.ToUpper(strings.TrimSpace(a.Country))}
}

// Validate reports field problems keyed by the request path (address.line1 ...).
// The address must already be normalized.
func (a Address) Validate() map[string]string {
	fields := map[string]string{}
	need := func(key, v string, max int) {
		switch {
		case v == "":
			fields["address."+key] = "is required"
		case utf8.RuneCountInString(v) > max:
			fields["address."+key] = "is too long"
		}
	}
	need("line1", a.Line1, maxAddressLine)
	need("city", a.City, maxCity)
	need("postalCode", a.PostalCode, maxPostalCode)
	if utf8.RuneCountInString(a.Line2) > maxAddressLine {
		fields["address.line2"] = "is too long"
	}
	if utf8.RuneCountInString(a.Region) > maxCity {
		fields["address.region"] = "is too long"
	}
	if !countryCode.MatchString(a.Country) {
		fields["address.country"] = "must be a two-letter ISO country code"
	}
	return fields
}

// Summary is what an approver and the masked profile show: city and country.
func (a Address) Summary() string { return a.City + ", " + a.Country }

// Format is the full one-line address, shown only on a reveal.
func (a Address) Format() string {
	parts := []string{a.Line1}
	if a.Line2 != "" {
		parts = append(parts, a.Line2)
	}
	parts = append(parts, a.City)
	if a.Region != "" {
		parts = append(parts, a.Region)
	}
	return strings.Join(append(parts, a.PostalCode, a.Country), ", ")
}

// NormalizeBankAccount drops spaces and hyphens and upper-cases the account; it
// must then be 8 to 34 letters and digits (an IBAN fits).
func NormalizeBankAccount(v string) (string, bool) {
	v = strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(v)))
	return v, bankAccount.MatchString(v)
}

// NormalizePreferredName trims the name; empty clears it.
func NormalizePreferredName(v string) (string, bool) {
	v = strings.TrimSpace(v)
	return v, utf8.RuneCountInString(v) <= maxPreferredName
}

// ChangeNeedsApproval: a home address or bank account change waits for approval;
// a preferred name applies at once.
func ChangeNeedsApproval(kind string) bool { return kind != ChangePreferredName }

// ManagerMayDecide: a manager decides their direct reports' address changes. A
// bank account change routes pay, so only HR (hr.worker.maintain) decides it.
func ManagerMayDecide(kind string) bool { return kind == ChangeHomeAddress }

// NextChangeStatus moves a PENDING change by decision (APPROVE, REJECT or
// CANCEL). Repeating the outcome a change already has is not a move.
func NextChangeStatus(status, decision string) (next string, moved bool, err error) {
	target := map[string]string{DecisionApprove: ChangeApproved, DecisionReject: ChangeRejected, "CANCEL": ChangeCanceled}[decision]
	switch {
	case target == "":
		return "", false, &Error{Code: "COM-0001", Detail: "decision must be APPROVE or REJECT"}
	case status == ChangePending:
		return target, true, nil
	case status == target:
		return status, false, nil
	}
	return "", false, &Error{Code: CodeProfileChangeState, Detail: "the change is no longer waiting for a decision"}
}
