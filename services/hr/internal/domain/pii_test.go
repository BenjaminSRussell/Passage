package domain

import (
	"bytes"
	"testing"

	"pgregory.net/rapid"
)

func testSealer(t *testing.T) *Sealer {
	t.Helper()
	s, err := NewSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSealer_RoundTripAndNonce(t *testing.T) {
	s := testSealer(t)
	a, err := s.Seal("123-45-6789")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Seal("123-45-6789")
	if bytes.Equal(a, b) {
		t.Error("two seals of the same value must use fresh nonces")
	}
	got, err := s.Open(a)
	if err != nil || got != "123-45-6789" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	a[len(a)-1] ^= 1
	if _, err := s.Open(a); err == nil {
		t.Error("tampered ciphertext must fail authentication")
	}
}

func TestSealer_RejectsBadKey(t *testing.T) {
	if _, err := NewSealer([]byte("short")); err == nil {
		t.Error("a key that is not 32 bytes must be rejected")
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
