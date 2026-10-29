package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Electronic-signature codes (FR-48.13, BR-48.11, BR-48.12).
const (
	CodeESignHashMismatch = "ESG-0602"
	CodeESignLocked       = "ESG-0603"

	// SignComponentsFull is the two-component signing of 21 CFR 11.200: user ID plus
	// password, or the second factor (TOTP or WebAuthn) of the requirement.
	SignComponentsFull = 2
	// SignComponentsReuse is the single-component signing inside a continuous session.
	SignComponentsReuse = 1

	DefaultLockoutAttempts = 3
	DefaultLockoutMinutes  = 30
)

// Meaning is the meaning of a signature on a record (FR-48.13).
type Meaning string

const (
	MeaningAuthored Meaning = "AUTHORED"
	MeaningReviewed Meaning = "REVIEWED"
	MeaningApproved Meaning = "APPROVED"
	MeaningReleased Meaning = "RELEASED"
	MeaningVerified Meaning = "VERIFIED"
)

// ValidMeaning reports whether m is one of the five meanings.
func ValidMeaning(m Meaning) bool {
	switch m {
	case MeaningAuthored, MeaningReviewed, MeaningApproved, MeaningReleased, MeaningVerified:
		return true
	}
	return false
}

// RecordVersionHash is the SHA-256 (lowercase hex) of the canonical JSON of a record
// version. encoding/json sorts map keys, so a map-based payload hashes the same on
// every call. The hash is what esign_record stores and what ESG-0602 compares.
func RecordVersionHash(record any) (string, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("canonicalize record: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// VerifySigningHash fails with ESG-0602 when the record changed between the dialog
// opening and the signing (BR-48.11). The expected hash is compared without
// regard to hex letter case.
func VerifySigningHash(expected string, record any) error {
	actual, err := RecordVersionHash(record)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, strings.TrimSpace(expected)) {
		return &Error{Code: CodeESignHashMismatch, Detail: "the record changed since you opened it; review the current version before signing"}
	}
	return nil
}

// RequiredComponents returns how many authentication components a signing needs
// (BR-48.12). The first signing of a session needs both. A later signing needs only
// one when the previous signing was no more than reuseMinutes ago. reuseMinutes 0
// disables reuse.
func RequiredComponents(lastSignedAt *time.Time, now time.Time, reuseMinutes int) int {
	if lastSignedAt == nil || reuseMinutes <= 0 {
		return SignComponentsFull
	}
	if now.Sub(*lastSignedAt) <= time.Duration(reuseMinutes)*time.Minute && !now.Before(*lastSignedAt) {
		return SignComponentsReuse
	}
	return SignComponentsFull
}

// Lockout tracks failed re-authentications for one signer (FR-48.13, ESG-0603).
// The zero value is an unlocked signer with no failures.
type Lockout struct {
	Failures    int       `json:"failures"`
	LockedUntil time.Time `json:"lockedUntil"`
}

// Check fails with ESG-0603 while the signer is locked at now.
func (l Lockout) Check(now time.Time) error {
	if l.LockedUntil.IsZero() || !now.Before(l.LockedUntil) {
		return nil
	}
	mins := int(math.Ceil(l.LockedUntil.Sub(now).Minutes()))
	return &Error{Code: CodeESignLocked, Detail: fmt.Sprintf("signing locked for %d min after repeated failed authentication", mins)}
}

// Fail records one failed re-authentication. The attempt that reaches maxAttempts locks
// signing for lockMinutes. A locked signer keeps the lock; failures are not counted then.
func (l *Lockout) Fail(now time.Time, maxAttempts, lockMinutes int) {
	if maxAttempts <= 0 {
		maxAttempts = DefaultLockoutAttempts
	}
	if lockMinutes <= 0 {
		lockMinutes = DefaultLockoutMinutes
	}
	if l.Check(now) != nil {
		return
	}
	l.Failures++
	if l.Failures >= maxAttempts {
		l.LockedUntil = now.Add(time.Duration(lockMinutes) * time.Minute)
		l.Failures = 0
	}
}

// Succeed clears the failure count after a successful re-authentication.
func (l *Lockout) Succeed(now time.Time) error {
	if err := l.Check(now); err != nil {
		return err
	}
	l.Failures = 0
	return nil
}
