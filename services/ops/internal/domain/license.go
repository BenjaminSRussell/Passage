package domain

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"
)

// Tenant licence rules (PSG-50, FR-50.12, BR-50.8, AC-50.8); docs/ops/tools/license_state.py is the same rule table in Python.
const (
	DefaultGraceDays  = 30
	MaxGraceDays      = 60
	ExpiryWarningDays = 30

	CodeLicenseExpiring  = "LIC-1801"
	CodeLicenseGrace     = "LIC-1802"
	CodeLicenseExpired   = "LIC-1803"
	CodeLicenseSignature = "LIC-1804"
	CodeLicenseInvalid   = "LIC-1805"
	CodeLicenseStale     = "LIC-1806"
)

// LicenseState is VALID, GRACE or EXPIRED.
type LicenseState string

const (
	LicenseValid   LicenseState = "VALID"
	LicenseGrace   LicenseState = "GRACE"
	LicenseExpired LicenseState = "EXPIRED"
)

// License is the signed licence terms. Dates are days (YYYY-MM-DD).
type License struct {
	TenantID         string `json:"tenantId"`
	IssuedAt         string `json:"issuedAt"`
	ExpiresAt        string `json:"expiresAt"`
	FullUsers        int    `json:"fullUsers"`
	OperationalUsers int    `json:"operationalUsers"`
	SelfServiceUsers int    `json:"selfserviceUsers"`
	// GraceDays is license.grace_days (0 to 60); absent means 30.
	GraceDays *int `json:"graceDays,omitempty"`
}

// Grace returns the grace period in days.
func (l License) Grace() int {
	if l.GraceDays == nil {
		return DefaultGraceDays
	}
	return *l.GraceDays
}

func parseDay(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }

// ParseLicense reads a licence payload strictly: unknown fields, bad dates, an expiry before the issue date, negative counts
// and a grace period outside 0 to 60 are refused (LIC-1805).
func ParseLicense(payload []byte) (License, error) {
	var l License
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return l, fail(CodeLicenseInvalid, "licence payload: %v", err)
	}
	issued, err := parseDay(l.IssuedAt)
	if err != nil {
		return l, fail(CodeLicenseInvalid, "issuedAt must be a date (YYYY-MM-DD)")
	}
	expires, err := parseDay(l.ExpiresAt)
	if err != nil {
		return l, fail(CodeLicenseInvalid, "expiresAt must be a date (YYYY-MM-DD)")
	}
	switch {
	case l.TenantID == "":
		return l, fail(CodeLicenseInvalid, "tenantId is required")
	case expires.Before(issued):
		return l, fail(CodeLicenseInvalid, "expiresAt must not be before issuedAt")
	case l.FullUsers < 0 || l.OperationalUsers < 0 || l.SelfServiceUsers < 0:
		return l, fail(CodeLicenseInvalid, "user counts must not be negative")
	case l.GraceDays != nil && (*l.GraceDays < 0 || *l.GraceDays > MaxGraceDays):
		return l, fail(CodeLicenseInvalid, "graceDays must be between 0 and %d", MaxGraceDays)
	}
	return l, nil
}

// VerifyLicense checks the Ed25519 signature over the exact payload bytes against the commercial operations public key
// (LIC-1804), then parses the payload.
func VerifyLicense(pub ed25519.PublicKey, payload, signature []byte) (License, error) {
	if len(pub) != ed25519.PublicKeySize {
		return License{}, fail(CodeLicenseSignature, "no licence public key is configured")
	}
	if !ed25519.Verify(pub, payload, signature) {
		return License{}, fail(CodeLicenseSignature, "licence signature invalid")
	}
	return ParseLicense(payload)
}

// LicenseStatus is the evaluated state and the access rules that follow from it.
type LicenseStatus struct {
	State             LicenseState `json:"state"`
	DaysToExpiry      int          `json:"daysToExpiry"`
	LoginAllowed      bool         `json:"loginAllowed"`
	AdminLoginAllowed bool         `json:"adminLoginAllowed"`
	ExportAllowed     bool         `json:"exportAllowed"`
	Banner            bool         `json:"banner"`
	Codes             []string     `json:"codes"`
}

func daysBetween(from, to time.Time) int { return int(to.Sub(from).Hours() / 24) }

// EvaluateLicense gives the state on day today with activeFull full users (BR-50.8):
//   - VALID: today is not after the expiry and the full users are within the licence;
//   - GRACE: up to grace days after the expiry, or full users above the licence (no time limit, until the over-use ends);
//   - EXPIRED: more than grace days after the expiry. New sign-in is blocked; admin sign-in and data export never are.
//
// Only full users are enforced. LIC-1801 applies 0 to 30 days before the expiry, LIC-1802 in GRACE, LIC-1803 in EXPIRED.
func EvaluateLicense(l License, activeFull int, today time.Time) (LicenseStatus, error) {
	expires, err := parseDay(l.ExpiresAt)
	if err != nil {
		return LicenseStatus{}, fail(CodeLicenseInvalid, "expiresAt must be a date (YYYY-MM-DD)")
	}
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	days := daysBetween(today, expires)
	graceEnd := expires.AddDate(0, 0, l.Grace())
	st := LicenseStatus{DaysToExpiry: days, AdminLoginAllowed: true, ExportAllowed: true, Codes: []string{}}
	switch {
	case today.After(graceEnd):
		st.State = LicenseExpired
	case today.After(expires) || activeFull > l.FullUsers:
		st.State = LicenseGrace
	default:
		st.State = LicenseValid
	}
	st.LoginAllowed = st.State != LicenseExpired
	st.Banner = st.State == LicenseGrace
	if days >= 0 && days <= ExpiryWarningDays {
		st.Codes = append(st.Codes, CodeLicenseExpiring)
	}
	switch st.State {
	case LicenseGrace:
		st.Codes = append(st.Codes, CodeLicenseGrace)
	case LicenseExpired:
		st.Codes = append(st.Codes, CodeLicenseExpired)
	}
	return st, nil
}

// LicenseMessage renders the catalogue text of a LIC code (PRD 9.50.18).
func LicenseMessage(code string, l License, activeFull int, st LicenseStatus) string {
	switch code {
	case CodeLicenseExpiring:
		return fmt.Sprintf("License expires in %d days.", st.DaysToExpiry)
	case CodeLicenseGrace:
		return fmt.Sprintf("License in grace period: %d full users active, %d licensed.", activeFull, l.FullUsers)
	case CodeLicenseExpired:
		return "License expired; sign-in blocked. Administrators can still sign in and export data."
	case CodeLicenseSignature:
		return "License signature invalid."
	}
	return code
}
