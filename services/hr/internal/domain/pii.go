package domain

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// PIIReadDuty is the duty that permits an unmasked read (FR-42.1).
const PIIReadDuty = "HcmPIIRead"

// DefaultReasonMinLength is the minimum length of a PII reveal reason (8.42.13).
const DefaultReasonMinLength = 10

// Sealer encrypts P3 values column by column with AES-256-GCM. The key is
// 32 bytes; it comes from the environment in this slice and from Vault later.
// Output layout is nonce || ciphertext.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer builds a sealer from a 32-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, errors.New("PII key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plain with a fresh random nonce.
func (s *Sealer) Seal(plain string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

// Open decrypts a value produced by Seal.
func (s *Sealer) Open(sealed []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("sealed value too short")
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", err
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
