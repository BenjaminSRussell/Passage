package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Supplier portal remainders (#511): profile changes, prospect registration, invitations.

// ProfileChangeFields are the party fields a vendor may ask to change. Bank details, tax registrations, the vendor group and payment
// terms are never part of a profile change: they are the buyer's.
var ProfileChangeFields = []string{"legalName", "tradeName", "registrationNumber", "industryCode", "languageCode", "timeZone"}

// ProfileChange turns the vendor's requested changes into a party patch and returns the problems by field. Every value is text; an
// empty value clears the field (except legalName, which a party must keep).
func ProfileChange(changes map[string]string) (PartyFields, map[string]string) {
	errs := map[string]string{}
	var f PartyFields
	if len(changes) == 0 {
		errs["changes"] = "name at least one field to change"
		return f, errs
	}
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := strings.TrimSpace(changes[k])
		switch k {
		case "legalName":
			f.LegalName = &v
		case "tradeName":
			f.TradeName = &v
		case "registrationNumber":
			f.RegistrationNumber = &v
		case "industryCode":
			f.IndustryCode = &v
		case "languageCode":
			f.LanguageCode = &v
		case "timeZone":
			f.TimeZone = &v
		default:
			errs["changes."+k] = "is not a field a vendor can change; allowed: " + strings.Join(ProfileChangeFields, ", ")
		}
	}
	if len(errs) > 0 {
		return PartyFields{}, errs
	}
	if f.LegalName != nil && *f.LegalName == "" {
		errs["changes.legalName"] = "cannot be empty"
	}
	for k, v := range changes {
		if utf8.RuneCountInString(v) > MaxNameLength {
			errs["changes."+k] = fmt.Sprintf("must be at most %d characters", MaxNameLength)
		}
	}
	return f, errs
}

// NewProspect is what the public registration page sends.
type NewProspect struct {
	CompanyName string `json:"companyName"`
	ContactName string `json:"contactName"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Country     string `json:"country"`
	TaxID       string `json:"taxId"`
	Website     string `json:"website"`
	Message     string `json:"message"`
}

// Clean trims every field and upper-cases the country.
func (p NewProspect) Clean() NewProspect {
	p.CompanyName, p.ContactName, p.Email = strings.TrimSpace(p.CompanyName), strings.TrimSpace(p.ContactName), strings.ToLower(strings.TrimSpace(p.Email))
	p.Phone, p.TaxID, p.Website, p.Message = strings.TrimSpace(p.Phone), strings.TrimSpace(p.TaxID), strings.TrimSpace(p.Website), strings.TrimSpace(p.Message)
	p.Country = strings.ToUpper(strings.TrimSpace(p.Country))
	return p
}

// ValidEmail accepts a plain address (no display name, no list).
func ValidEmail(s string) bool {
	if len(s) < 3 || len(s) > 254 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && a.Name == "" && strings.Contains(s[strings.LastIndex(s, "@"):], ".")
}

// Validate returns the problems of a cleaned registration by field.
func (p NewProspect) Validate() map[string]string {
	errs := map[string]string{}
	rng := func(field, v string, lo, hi int) {
		if n := utf8.RuneCountInString(v); n < lo || n > hi {
			errs[field] = fmt.Sprintf("must be %d to %d characters", lo, hi)
		}
	}
	rng("companyName", p.CompanyName, 1, 200)
	rng("contactName", p.ContactName, 1, 140)
	if !ValidEmail(p.Email) {
		errs["email"] = "must be an email address"
	}
	if !countryPattern.MatchString(p.Country) {
		errs["country"] = "must be a two-letter ISO country code"
	}
	for _, f := range []struct {
		name, v string
		hi      int
	}{{"phone", p.Phone, 40}, {"taxId", p.TaxID, 40}, {"website", p.Website, 200}, {"message", p.Message, 2000}} {
		if utf8.RuneCountInString(f.v) > f.hi {
			errs[f.name] = fmt.Sprintf("must be at most %d characters", f.hi)
		}
	}
	return errs
}

const (
	ProspectNew      = "NEW"
	ProspectAccepted = "ACCEPTED"
	ProspectRejected = "REJECTED"

	InvitePending  = "PENDING"
	InviteAccepted = "ACCEPTED"
	InviteRevoked  = "REVOKED"
	InviteExpired  = "EXPIRED"

	// InviteTTL is how long an invitation can be accepted.
	InviteTTL = 7 * 24 * time.Hour
	// InviteRole is the only role an invitation can grant; a vendor administrator is made in the identity provider.
	InviteRole = "supplier-user"

	inviteTokenPrefix = "inv1"
)

// InviteToken is the secret an invitee holds. It names the invitation and where it lives so the portal can address the right
// tenant, legal entity and vendor before anyone is signed in; none of those parts is trusted, the secret decides.
type InviteToken struct {
	TenantID, LegalEntityID, VendorID, InviteID, Secret string
}

// NewInviteToken makes a fresh token for an invitation and the digest to store.
func NewInviteToken(tenantID, legalEntityID, vendorID, inviteID string) (token, hash string) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic("party: no randomness: " + err.Error())
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	return strings.Join([]string{inviteTokenPrefix, tenantID, legalEntityID, vendorID, inviteID, secret}, "."), HashInviteSecret(secret)
}

// ParseInviteToken splits a token; ok is false for anything that is not a well-formed token.
func ParseInviteToken(token string) (InviteToken, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 6 || parts[0] != inviteTokenPrefix || len(parts[5]) < 32 || len(parts[5]) > 64 {
		return InviteToken{}, false
	}
	return InviteToken{TenantID: parts[1], LegalEntityID: parts[2], VendorID: parts[3], InviteID: parts[4], Secret: parts[5]}, true
}

// HashInviteSecret is the stored digest of a secret.
func HashInviteSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// InviteSecretMatches compares a presented secret with the stored digest in constant time.
func InviteSecretMatches(secret, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashInviteSecret(secret)), []byte(storedHash)) == 1
}

// InviteStatus is the status shown for an invitation: a pending one past its expiry reads EXPIRED.
func InviteStatus(status string, expiresAt, now time.Time) string {
	if status == InvitePending && !now.Before(expiresAt) {
		return InviteExpired
	}
	return status
}
