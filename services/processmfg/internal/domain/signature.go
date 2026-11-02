package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"time"
)

// Codes of the signature service (PRD 4.24.18).
const (
	CodeSignatureRejected = "PMF-0010"
	CodeSignatureLocked   = "PMF-0011"
	CodeSameAuthor        = "PMF-0012"
	CodeSignatureInvalid  = "PMF-0013"
)

// Signing policy (BR-24.14, FR-24.22): three failed re-authentications lock
// signing for 15 minutes; a continuous session ends after 10 minutes idle.
const (
	LockoutAttempts = 3
	LockoutWindow   = 15 * time.Minute
	SessionIdle     = 10 * time.Minute
)

// Auth methods required for a signature (21 CFR 11.200(a)(1)).
const (
	AuthPasswordAndOTP = "password+otp"
	AuthPassword       = "password"
)

// Lockout tracks failed re-authentications for one signer.
type Lockout struct {
	Failures    int
	LockedUntil time.Time
}

// Locked reports whether signing is locked at now.
func (l Lockout) Locked(now time.Time) bool { return now.Before(l.LockedUntil) }

// Fail records one failed re-authentication. The third consecutive failure locks
// signing until now + LockoutWindow and resets the counter.
func (l *Lockout) Fail(now time.Time) {
	l.Failures++
	if l.Failures >= LockoutAttempts {
		l.LockedUntil = now.Add(LockoutWindow)
		l.Failures = 0
	}
}

// Succeed clears the failure count after a successful re-authentication.
func (l *Lockout) Succeed() { l.Failures = 0 }

// CheckAttempt returns PMF-0011 while the signer is locked, and nil otherwise.
func (l Lockout) CheckAttempt(now time.Time) error {
	if l.Locked(now) {
		return errorf(CodeSignatureLocked, "signing locked for %d minutes after %d failed attempts", int(LockoutWindow.Minutes()), LockoutAttempts)
	}
	return nil
}

// ContinuousSession reports whether the session is still continuous at now: no
// logout and no idle period longer than SessionIdle (BR-24.14).
func ContinuousSession(lastActivity, now time.Time, loggedOut bool) bool {
	return !loggedOut && now.Sub(lastActivity) <= SessionIdle
}

// RequiredAuth returns the re-authentication a signing needs. The first signing
// in a session needs both password and second factor; later signings in the same
// continuous session need the password only (BR-24.14).
func RequiredAuth(firstInSession bool) string {
	if firstInSession {
		return AuthPasswordAndOTP
	}
	return AuthPassword
}

// CheckSignerSubject rejects a signing whose signer is not the session subject
// (BR-24.14, PMF-0010). The caller logs the rejection.
func CheckSignerSubject(sessionSubject, signerSubject string) error {
	if sessionSubject == "" || sessionSubject != signerSubject {
		return errorf(CodeSignatureRejected, "signature rejected: re-authentication failed")
	}
	return nil
}

// CheckDistinctSigner enforces that the signer is not the record author (PMF-0012).
func CheckDistinctSigner(author, signer string) error {
	if author == signer {
		return errorf(CodeSameAuthor, "signer must differ from the record author")
	}
	return nil
}

// RecordHash returns the SHA-256 of the canonical JSON of v (FR-24.22). Canonical
// means compact with object keys sorted and numbers kept digit for digit, so
// equal records hash equally no matter how their maps or structs were built.
func RecordHash(v any) ([]byte, error) {
	canon, err := CanonicalJSON(v)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canon)
	return sum[:], nil
}

// CanonicalJSON encodes v as compact UTF-8 JSON: object keys sorted by their
// bytes at every level, no insignificant whitespace, no HTML escaping of <, >
// and & (so the output matches a plain sorted-keys encoder in another language),
// and numbers kept digit for digit as they were first encoded: they are decoded
// as json.Number, because a float round trip would change large or long decimal
// values and so the hash. Money and quantities are encoded as decimal strings by
// their types, so no float formatting enters the document.
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, errorf(CodeInvalid, "record is not encodable: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, errorf(CodeInvalid, "record is not encodable: %v", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, errorf(CodeInvalid, "record is not encodable: %v", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// CheckSignatureValid returns PMF-0013 when the signed hash no longer matches the
// current record hash: the signature is invalidated until re-signed (11.70).
func CheckSignatureValid(signed, current []byte) error {
	if !bytes.Equal(signed, current) {
		return errorf(CodeSignatureInvalid, "record changed after signing; signature invalidated")
	}
	return nil
}
