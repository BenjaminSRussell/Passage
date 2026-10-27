package domain

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// TestSSCC_AC28_3 covers the worked example of AC-28.3: prefix 0614141 and
// serial 12345678 give the 18-digit body 006141410123456789 (check digit 9).
func TestSSCC_AC28_3(t *testing.T) {
	got, err := SSCC(0, "0614141", 12345678)
	if err != nil {
		t.Fatal(err)
	}
	if got != "006141410123456789" {
		t.Fatalf("SSCC = %s, want 006141410123456789", got)
	}
	if err := ValidSSCC(got); err != nil {
		t.Fatalf("generated SSCC invalid: %v", err)
	}
}

// TestSSCC_GS1Example is the worked example in the GS1 General Specifications:
// extension 1, company prefix 0614141 and serial reference 123456789 give
// (00)106141411234567897.
func TestSSCC_GS1Example(t *testing.T) {
	got, err := SSCC(1, "0614141", 123456789)
	if err != nil {
		t.Fatal(err)
	}
	if got != "106141411234567897" {
		t.Fatalf("SSCC = %s, want 106141411234567897", got)
	}
	scan, err := ParseGS1("(00)" + got)
	if err != nil || scan.SSCC != got {
		t.Fatalf("label does not scan back: %+v, %v", scan, err)
	}
}

func TestCheckDigit(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
		err  bool
	}{
		{"gtin-14 example", "0061414100001", 2, false},
		{"all zeros", "0000000000000", 0, false},
		{"sscc body", "00614141012345678", 9, false},
		{"non digit", "00X", 0, true},
		{"empty", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CheckDigit(tt.body)
			if (err != nil) != tt.err {
				t.Fatalf("err = %v, want error %v", err, tt.err)
			}
			if err == nil && got != tt.want {
				t.Fatalf("CheckDigit(%q) = %d, want %d", tt.body, got, tt.want)
			}
		})
	}
}

func TestValidSSCC(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"valid", "006141410123456789", nil},
		{"bad check digit", "006141410123456780", ErrCheckDigit},
		{"too short", "00614141012345678", ErrSSCCFormat},
		{"too long", "0061414101234567890", ErrSSCCFormat},
		{"letters", "00614141012345678X", ErrSSCCFormat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidSSCC(tt.in); !errors.Is(err, tt.want) && err != tt.want {
				t.Fatalf("ValidSSCC(%s) = %v, want %v", tt.in, err, tt.want)
			}
		})
	}
}

func TestSSCCRejectsBadInputs(t *testing.T) {
	tests := []struct {
		name      string
		ext       int
		prefix    string
		serial    uint64
		wantError error
	}{
		{"extension out of range", 10, "0614141", 1, ErrExtension},
		{"prefix too short", 0, "12345", 1, ErrPrefix},
		{"prefix too long", 0, "1234567890123", 1, ErrPrefix},
		{"prefix not digits", 0, "06141A1", 1, ErrPrefix},
		{"serial too large for prefix", 0, "0614141", 1000000000, ErrSerialRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := SSCC(tt.ext, tt.prefix, tt.serial); !errors.Is(err, tt.wantError) {
				t.Fatalf("err = %v, want %v", err, tt.wantError)
			}
		})
	}
}

// TestSSCC_Property: every generated SSCC is 18 digits with a valid check digit,
// and any single-digit change is detected (mod-10 with weights 3 and 1).
func TestSSCC_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ext := rapid.IntRange(0, 9).Draw(t, "ext")
		plen := rapid.IntRange(6, 12).Draw(t, "prefixLen")
		pattern := "[0-9]{" + strconv.Itoa(plen) + "}"
		prefix := rapid.StringMatching(pattern).Draw(t, "prefix")
		width := SSCCLength - 2 - plen
		maxSerial := pow10(width) - 1
		serial := rapid.Uint64Range(0, maxSerial).Draw(t, "serial")

		sscc, err := SSCC(ext, prefix, serial)
		if err != nil {
			t.Fatalf("SSCC: %v", err)
		}
		if err := ValidSSCC(sscc); err != nil {
			t.Fatalf("generated %s invalid: %v", sscc, err)
		}
		lastPos := SSCCLength - 1
		pos := rapid.IntRange(0, lastPos).Draw(t, "pos")
		delta := rapid.IntRange(1, 9).Draw(t, "delta")
		b := []byte(sscc)
		digit := int(b[pos]) - '0'
		sum := digit + delta
		b[pos] = '0' + byte(sum%10)
		if err := ValidSSCC(string(b)); err == nil {
			t.Fatalf("single-digit change %s -> %s not detected", sscc, b)
		}
	})
}

// BR-28.5: an SSCC stays blocked for sscc_no_reuse_months from its UTC issue date.
func TestSSCCNoReuseUntil(t *testing.T) {
	ny := time.FixedZone("EST", -5*3600)
	cases := []struct {
		name   string
		issued time.Time
		months int
		want   string
	}{
		{"default window", time.Date(2026, 10, 21, 15, 4, 5, 0, time.UTC), SSCCNoReuseMonths, "2027-10-21"},
		{"issue date is the UTC date", time.Date(2026, 10, 21, 22, 0, 0, 0, ny), 12, "2027-10-22"},
		{"leap day rolls to 1 March", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC), 12, "2029-03-01"},
		{"longest window", time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), 60, "2031-01-31"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SSCCNoReuseUntil(tc.issued, tc.months).Format("2006-01-02"); got != tc.want {
				t.Fatalf("SSCCNoReuseUntil = %s, want %s", got, tc.want)
			}
		})
	}
}
