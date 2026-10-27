package domain

import (
	"errors"
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

// TestParseGS1_AC28_9: (01)00614141000012(10)LOT7(37)5 gives GTIN, batch LOT7 and qty 5.
func TestParseGS1_AC28_9(t *testing.T) {
	s, err := ParseGS1("(01)00614141000012(10)LOT7(37)5")
	if err != nil {
		t.Fatal(err)
	}
	if s.GTIN != "00614141000012" || s.Batch != "LOT7" || s.Count != 5 {
		t.Fatalf("got %+v", s)
	}
}

func TestParseGS1(t *testing.T) {
	sscc := "006141410123456789"
	tests := []struct {
		name    string
		in      string
		want    Scan
		wantErr error
	}{
		{"sscc human", "(00)" + sscc, Scan{SSCC: sscc}, nil},
		{"raw with fnc1", "01" + "00614141000012" + "10LOT7" + "\x1d" + "37" + "5", Scan{GTIN: "00614141000012", Count: 5, Batch: "LOT7"}, nil},
		{"expiry and serial", "(01)00614141000012(17)260131(21)SER9", Scan{GTIN: "00614141000012", Expiry: "260131", Serial: "SER9"}, nil},
		{"bad gtin check digit", "(01)00614141000013", Scan{}, ErrGS1Value},
		{"bad sscc check digit", "(00)006141410123456780", Scan{}, ErrGS1Value},
		{"unknown ai", "(99)123", Scan{}, ErrUnknownAI},
		{"fixed length short", "(01)006141", Scan{}, ErrGS1Parse},
		{"batch too long", "(10)ABCDEFGHIJKLMNOPQRSTU", Scan{}, ErrGS1Parse},
		{"empty variable", "(10)(37)5", Scan{}, ErrGS1Parse},
		{"non-numeric count", "(37)5X", Scan{}, ErrGS1Value},
		{"missing paren", "01006141000012", Scan{}, ErrGS1Parse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGS1(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRequireGTIN(t *testing.T) {
	if _, err := (Scan{}).RequireGTIN(); !errors.Is(err, ErrGS1Missing) {
		t.Fatalf("err = %v", err)
	}
}

// TestParseGS1_RoundTrip: any batch, serial and count encoded in the
// human-readable form parses back to the same values.
func TestParseGS1_RoundTrip(t *testing.T) {
	const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	rapid.Check(t, func(t *rapid.T) {
		body := rapid.StringMatching(`[0-9]{13}`).Draw(t, "gtinBody")
		cd, _ := CheckDigit(body)
		gtin := body + fmt.Sprint(cd)
		batch := rapid.StringOfN(rapid.SampledFrom([]rune(alnum)), 1, 20, -1).Draw(t, "batch")
		serial := rapid.StringOfN(rapid.SampledFrom([]rune(alnum)), 1, 20, -1).Draw(t, "serial")
		count := rapid.IntRange(1, 99999999).Draw(t, "count")

		in := fmt.Sprintf("(01)%s(10)%s(21)%s(37)%d", gtin, batch, serial, count)
		got, err := ParseGS1(in)
		if err != nil {
			t.Fatalf("parse %q: %v", in, err)
		}
		if got.GTIN != gtin || got.Batch != batch || got.Serial != serial || got.Count != count {
			t.Fatalf("round trip mismatch: %+v", got)
		}
	})
}
