package domain

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"strings"
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
	keys   *fieldcrypt.Keys
	legacy cipher.AEAD
}

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

func (s *Sealer) Open(tenantID, field string, sealed []byte) (string, error) {
	if plain, err := s.keys.Open(tenantID, field, sealed); err == nil {
		return string(plain), nil
	}
	if s.legacy == nil || len(sealed) < s.legacy.NonceSize() {
		return "", fieldcrypt.ErrOpen
	}
	n := s.legacy.NonceSize()
	plain, err := s.legacy.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", fieldcrypt.ErrOpen
	}
	return string(plain), nil
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
