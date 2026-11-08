package domain

import (
	"strings"
	"testing"
)

const shareTenant = "0190f0a0-0000-7000-8000-000000000001"

func TestShareToken_RoundTripsAndStoresOnlyTheHash(t *testing.T) {
	token, hash, err := NewShareToken(shareTenant)
	if err != nil {
		t.Fatal(err)
	}
	tenantID, parsedHash, ok := ParseShareToken(token)
	if !ok || tenantID != shareTenant || parsedHash != hash {
		t.Fatalf("parse %q: %q %q %v, want %q %q", token, tenantID, parsedHash, ok, shareTenant, hash)
	}
	if len(hash) != 64 || strings.Contains(token, hash) {
		t.Fatalf("hash %q must be hex SHA-256 and not part of the token", hash)
	}
	other, otherHash, _ := NewShareToken(shareTenant)
	if other == token || otherHash == hash {
		t.Fatal("two tokens must differ")
	}
}

func TestShareToken_RefusesAnythingElse(t *testing.T) {
	token, _, _ := NewShareToken(shareTenant)
	secret := token[strings.Index(token, ".")+1:]
	for name, bad := range map[string]string{
		"empty":         "",
		"no dot":        strings.ReplaceAll(token, ".", ""),
		"short tenant":  "abc." + secret,
		"non hex":       strings.Repeat("g", 32) + "." + secret,
		"upper hex":     strings.ToUpper(token[:32]) + "." + secret,
		"short secret":  token[:32] + "." + secret[:40],
		"long secret":   token + "AA",
		"bad alphabet":  token[:32] + "." + strings.Repeat("+", 43),
		"padded secret": token[:32] + "." + secret + "=",
		"nil tenant":    strings.Repeat("0", 32) + "." + secret,
	} {
		if _, _, ok := ParseShareToken(bad); ok {
			t.Errorf("%s: %q was accepted", name, bad)
		}
	}
	if _, _, err := NewShareToken("not-a-uuid"); err == nil {
		t.Error("a tenant id that is not a UUID was accepted")
	}
}
