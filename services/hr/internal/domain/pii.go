package domain

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
)

// PIIReadDuty is the duty that permits an unmasked read (FR-42.1).
const PIIReadDuty = "HcmPIIRead"

// DefaultReasonMinLength is the minimum length of a PII reveal reason (8.42.13).
const DefaultReasonMinLength = 10

// Sealer encrypts P3 values with platform fieldcrypt (tenant and field bound).
// Values written before fieldcrypt (nonce || ciphertext under the single
// HR_PII_KEY, no envelope) still open through the legacy key.
type Sealer struct {
	keys         *fieldcrypt.Keys
	legacy       cipher.AEAD
	legacyOpens  atomic.Int64
	openFailures atomic.Int64
}

// legacyPath is what the legacy key did for one open, logged beside a fieldcrypt failure.
type legacyPath string

const (
	legacyUnused        legacyPath = "unused"
	legacyOpened        legacyPath = "opened"
	legacyFailed        legacyPath = "failed"
	legacyNotConfigured legacyPath = "not_configured"
)

// NewSealer builds a sealer. legacyKey is optional (nil) and, when set, must be 32 bytes.
func NewSealer(keys *fieldcrypt.Keys, legacyKey []byte) (*Sealer, error) {
	if keys == nil {
		return nil, errors.New("fieldcrypt keys are required")
	}
	s := &Sealer{keys: keys}
	if legacyKey == nil {
		return s, nil
	}
	if len(legacyKey) != 32 {
		return nil, errors.New("PII key must be 32 bytes")
	}
	block, err := aes.NewCipher(legacyKey)
	if err != nil {
		return nil, err
	}
	if s.legacy, err = cipher.NewGCM(block); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Sealer) Seal(tenantID, field, plain string) ([]byte, error) {
	return s.keys.Seal(tenantID, field, []byte(plain))
}

// Open tries fieldcrypt, then the legacy key. An open that needed the legacy key
// and an open that failed both are counted (OpenCounts) and logged with the key id
// the envelope names and what each path did, never the value, so a bad rotation or
// a missed re-seal shows up before every read fails.
func (s *Sealer) Open(tenantID, field string, sealed []byte) (string, error) {
	plain, path, err := s.open(tenantID, field, sealed)
	switch {
	case err != nil:
		s.openFailures.Add(1)
		keyID, _ := fieldcrypt.KeyID(sealed)
		slog.Error("hr pii value did not open", "tenantId", tenantID, "field", field, "envelopeKeyId", keyID,
			"fieldcrypt", "failed", "legacy", string(path))
	case path == legacyOpened:
		s.legacyOpens.Add(1)
		slog.Warn("hr pii value opened only with the legacy HR_PII_KEY; run hr reseal-pii", "tenantId", tenantID, "field", field)
	}
	return plain, err
}

func (s *Sealer) open(tenantID, field string, sealed []byte) (string, legacyPath, error) {
	if plain, err := s.keys.Open(tenantID, field, sealed); err == nil {
		return string(plain), legacyUnused, nil
	}
	if s.legacy == nil {
		return "", legacyNotConfigured, fieldcrypt.ErrOpen
	}
	n := s.legacy.NonceSize()
	if len(sealed) < n {
		return "", legacyFailed, fieldcrypt.ErrOpen
	}
	plain, err := s.legacy.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", legacyFailed, fieldcrypt.ErrOpen
	}
	return string(plain), legacyOpened, nil
}

// OpenCounts reports opens that needed the legacy key and opens that failed both keys.
func (s *Sealer) OpenCounts() (legacy, failed int64) {
	return s.legacyOpens.Load(), s.openFailures.Load()
}

// Reseal returns sealed re-encrypted under the current fieldcrypt key when it opens
// only through the legacy key or names an older key id; changed is false when the
// value is already sealed under the current key. It neither counts nor logs per value.
func (s *Sealer) Reseal(tenantID, field string, sealed []byte) (out []byte, changed bool, err error) {
	plain, path, err := s.open(tenantID, field, sealed)
	if err != nil {
		return nil, false, err
	}
	if keyID, _ := fieldcrypt.KeyID(sealed); path == legacyUnused && keyID == s.keys.CurrentID() {
		return sealed, false, nil
	}
	if out, err = s.Seal(tenantID, field, plain); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// MaskNationalID shows only the last four characters: ***-**-1234 (AC-42.7).
func MaskNationalID(v string) string {
	last, ok := lastFour(v)
	if !ok {
		return "***-**-****"
	}
	return "***-**-" + last
}

// MaskBank shows only the last four characters: ****1234.
func MaskBank(v string) string {
	last, ok := lastFour(v)
	if !ok {
		return "****"
	}
	return "****" + last
}

func lastFour(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) < 4 {
		return "", false
	}
	r := []rune(v)
	return string(r[len(r)-4:]), true
}

// CheckReveal gates an unmasked read: the caller must hold HcmPIIRead and give
// a reason of at least minLength characters (HCM-PII-001).
func CheckReveal(hasDuty bool, reason string, minLength int) error {
	if !hasDuty {
		return &Error{Code: "AUTH-0003", Detail: "missing duty " + PIIReadDuty}
	}
	if utf8.RuneCountInString(strings.TrimSpace(reason)) < minLength {
		return &Error{Code: "HCM-PII-001", Detail: fmt.Sprintf("Reason of at least %d characters is required to reveal PII", minLength)}
	}
	return nil
}
