package domain

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"testing"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
)

func testKeys(t *testing.T, fill byte) *fieldcrypt.Keys {
	t.Helper()
	keys, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{fill}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func testSealer(t *testing.T) *Sealer {
	t.Helper()
	s, err := NewSealer(testKeys(t, 7), nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func legacySeal(t *testing.T, key []byte, plain string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{1}, aead.NonceSize())
	return aead.Seal(nonce, nonce, []byte(plain), nil)
}

func TestSealer_RoundTripAndNonce(t *testing.T) {
	s := testSealer(t)
	a, err := s.Seal("t1", "national_id", "123-45-6789")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Seal("t1", "national_id", "123-45-6789")
	if bytes.Equal(a, b) {
		t.Error("two seals of the same value must use fresh nonces")
	}
	got, err := s.Open("t1", "national_id", a)
	if err != nil || got != "123-45-6789" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	a[len(a)-1] ^= 1
	if _, err := s.Open("t1", "national_id", a); err == nil {
		t.Error("tampered ciphertext must fail authentication")
	}
}

func TestSealer_BoundToTenantAndField(t *testing.T) {
	s := testSealer(t)
	sealed, err := s.Seal("t1", "national_id", "123-45-6789")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open("t2", "national_id", sealed); err == nil {
		t.Error("a value sealed for one tenant must not open for another")
	}
	if _, err := s.Open("t1", "bank_account", sealed); err == nil {
		t.Error("a value sealed for one field must not open for another")
	}
}

func TestSealer_OpensLegacyValues(t *testing.T) {
	legacyKey := bytes.Repeat([]byte{9}, 32)
	legacy := legacySeal(t, legacyKey, "123-45-6789")
	s, err := NewSealer(testKeys(t, 7), legacyKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Open("t1", "national_id", legacy)
	if err != nil || got != "123-45-6789" {
		t.Fatalf("legacy Open = %q, %v", got, err)
	}
	if _, err := testSealer(t).Open("t1", "national_id", legacy); err == nil {
		t.Error("without the legacy key a legacy value must not open")
	}
	other, err := NewSealer(testKeys(t, 7), bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open("t1", "national_id", legacy); err == nil {
		t.Error("a legacy value must not open under a different legacy key")
	}
}

func TestSealer_RejectsBadKeys(t *testing.T) {
	if _, err := NewSealer(testKeys(t, 7), []byte("short")); err == nil {
		t.Error("a legacy key that is not 32 bytes must be rejected")
	}
	if _, err := NewSealer(nil, nil); err == nil {
		t.Error("missing fieldcrypt keys must be rejected")
	}
}

func TestMasks(t *testing.T) {
	cases := []struct {
		name, got, want string
	}{
		{"national id keeps last four", MaskNationalID("123-45-1234"), "***-**-1234"},
		{"short national id is fully hidden", MaskNationalID("12"), "***-**-****"},
		{"bank keeps last four", MaskBank("GB29NWBK60161331926819"), "****6819"},
		{"short bank is fully hidden", MaskBank("1"), "****"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// Property: masking never reveals more than the last four characters.
func TestMasks_NeverLeakPrefix(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := rapid.StringMatching(`[0-9A-Z]{5,30}`).Draw(t, "value")
		masked := MaskNationalID(v)
		if masked[:7] != "***-**-" || len(masked) != 11 {
			t.Fatalf("unexpected shape %q", masked)
		}
		if masked[7:] != v[len(v)-4:] {
			t.Fatalf("masked %q does not show the last four of %q", masked, v)
		}
	})
}

func TestCheckReveal(t *testing.T) {
	cases := []struct {
		name    string
		duty    bool
		reason  string
		wantErr string
	}{
		{"holder with a full reason may reveal", true, "payroll correction", ""},
		{"holder with a short reason is refused", true, "fix", "HCM-PII-001"},
		{"whitespace does not count toward the reason", true, "   short   ", "HCM-PII-001"},
		{"without the duty nothing is revealed", false, "payroll correction", "AUTH-0003"},
	}
	for _, c := range cases {
		err := CheckReveal(c.duty, c.reason, DefaultReasonMinLength)
		code := ""
		if e, ok := err.(*Error); ok {
			code = e.Code
		}
		if code != c.wantErr {
			t.Errorf("%s: code %q, want %q", c.name, code, c.wantErr)
		}
	}
}
