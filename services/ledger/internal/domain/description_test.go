package domain

import (
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"
)

func TestValidateDescriptionTemplate(t *testing.T) {
	cases := []struct {
		name     string
		template string
		bindings map[string]string
		want     map[string]string
	}{
		{"AC-11.19 template", "Journal %1 of %2", map[string]string{"1": "voucher", "2": "document_date"}, map[string]string{}},
		{"no placeholders no bindings", "Plain text", nil, map[string]string{}},
		{"repeated placeholder bound once", "%1 / %1", map[string]string{"1": "period"}, map[string]string{}},
		{"blank template", "   ", nil, map[string]string{"template": "template is required"}},
		{"too long", strings.Repeat("x", 256), nil, map[string]string{"template": "template must be at most 255 characters"}},
		{"255 multibyte runes fit", strings.Repeat("é", 255), nil, map[string]string{}},
		{"unknown field", "Ref %1", map[string]string{"1": "secret"}, map[string]string{"fieldBindings": "unknown field secret for %1"}},
		{"bad placeholder key", "Ref %1", map[string]string{"0": "voucher"}, map[string]string{"fieldBindings": "placeholder keys must be 1 to 9"}},
		{"two-digit key", "Ref %1", map[string]string{"10": "voucher"}, map[string]string{"fieldBindings": "placeholder keys must be 1 to 9"}},
		{"lowest unbound placeholder reported", "Ref %3 %2 %1", map[string]string{"1": "voucher"},
			map[string]string{"fieldBindings": "placeholder %2 has no binding"}},
		{"unused binding", "Ref %1", map[string]string{"1": "voucher", "2": "period"},
			map[string]string{"template": "binding %2 does not appear in the template"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateDescriptionTemplate(tc.template, tc.bindings)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("%s: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestExpandDescription(t *testing.T) {
	values := map[string]string{"voucher": "GJ-000123", "document_date": "2026-03-31", "period": "2026-03", "amount_txn": "10.00"}
	cases := []struct {
		name     string
		template string
		bindings map[string]string
		want     string
		trunc    bool
	}{
		{"spec example", "Journal %1 of %2", map[string]string{"1": "voucher", "2": "document_date"}, "Journal GJ-000123 of 2026-03-31", false},
		{"repeated placeholder", "%1/%1", map[string]string{"1": "period"}, "2026-03/2026-03", false},
		{"unbound field value is empty", "A %1 B", map[string]string{"1": "party_name"}, "A  B", false},
		{"no placeholders", "Monthly accrual", nil, "Monthly accrual", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, trunc := ExpandDescription(tc.template, tc.bindings, values)
			if got != tc.want || trunc != tc.trunc {
				t.Fatalf("got %q (truncated %v), want %q (truncated %v)", got, trunc, tc.want, tc.trunc)
			}
		})
	}
}

func TestExpandDescriptionTruncatesWithEllipsis(t *testing.T) {
	long := strings.Repeat("é", 300)
	got, trunc := ExpandDescription("%1", map[string]string{"1": "party_name"}, map[string]string{"party_name": long})
	if !trunc {
		t.Fatal("a 300-rune value must be reported as truncated")
	}
	if utf8.RuneCountInString(got) != MaxDescriptionLength || !strings.HasSuffix(got, "…") {
		t.Fatalf("want %d runes ending in an ellipsis, got %d runes", MaxDescriptionLength, utf8.RuneCountInString(got))
	}
}

func TestExpandDescriptionNeverExceedsLimit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		value := rapid.StringN(0, 600, -1).Draw(t, "value")
		tpl := rapid.SampledFrom([]string{"%1", "x %1 y", "%1%1%1"}).Draw(t, "template")
		got, trunc := ExpandDescription(tpl, map[string]string{"1": "party_name"}, map[string]string{"party_name": value})
		if n := utf8.RuneCountInString(got); n > MaxDescriptionLength {
			t.Fatalf("expansion has %d runes", n)
		}
		if !trunc && tpl == "%1" && got != value {
			t.Fatalf("untruncated expansion changed the value: %q", got)
		}
	})
}
