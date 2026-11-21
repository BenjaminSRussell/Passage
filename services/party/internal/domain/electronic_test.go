package domain_test

import (
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
)

func TestElectronicAddress_CleanAndValidate(t *testing.T) {
	long := strings.Repeat("a", 250) + "@example.com"
	for _, tc := range []struct {
		name, kind, value, field, want string
	}{
		{"email trimmed and lowercased", "email", "  Info@Example.COM ", "", "info@example.com"},
		{"phone formatting stripped", "phone", "+49 (30) 123-4567", "", "+49301234567"},
		{"url kept", "url", " https://example.com/a ", "", "https://example.com/a"},
		{"email without at", "email", "info.example.com", "value", ""},
		{"email with two ats", "email", "a@b@example.com", "value", ""},
		{"email with display name", "email", "Bob <bob@example.com>", "value", ""},
		{"email too long", "email", long, "value", ""},
		{"empty value", "email", "", "value", ""},
		{"phone with letters", "phone", "12345ab", "value", ""},
		{"phone too short", "phone", "123456", "value", ""},
		{"phone too long", "phone", "1234567890123456", "value", ""},
		{"plus not leading", "phone", "12+3456789", "value", ""},
		{"url without scheme", "url", "example.com", "value", ""},
		{"url with other scheme", "url", "ftp://example.com", "value", ""},
		{"url without host", "url", "https://", "value", ""},
		{"unknown kind", "fax", "123456789", "kind", ""},
		{"empty kind", "", "x", "kind", ""},
	} {
		in := domain.NewElectronicAddress{Kind: tc.kind, Value: tc.value}.Clean()
		errs := in.Validate()
		if tc.field == "" && (len(errs) != 0 || in.Value != tc.want) {
			t.Errorf("%s: value %q errors %v, want %q valid", tc.name, in.Value, errs, tc.want)
		}
		if tc.field != "" && errs[tc.field] == "" {
			t.Errorf("%s: errors %v, want field %s", tc.name, errs, tc.field)
		}
	}
}

func TestElectronicAddress_NormalizedUrlIgnoresCaseAndTrailingSlash(t *testing.T) {
	a := domain.NewElectronicAddress{Kind: "url", Value: "HTTPS://Example.com/"}.Clean()
	if got := a.Normalized(); got != "https://example.com" {
		t.Fatalf("Normalized = %q", got)
	}
}
