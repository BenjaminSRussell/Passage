package domain_test

import (
	"encoding/csv"
	"errors"
	"os"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

func dec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestConvert_RoundTripsFiftyFixturePairs(t *testing.T) {
	f, err := os.Open("../../testdata/uom-pairs.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	rows = rows[1:]
	if len(rows) != 50 {
		t.Fatalf("fixture has %d pairs, want 50", len(rows))
	}
	for _, r := range rows {
		conv := domain.Conversion{From: r[0], To: r[1], Class: r[4], Factor: dec(t, r[2]), Offset: dec(t, r[3])}
		qty := dec(t, r[5])
		forward, err := domain.Convert([]domain.Conversion{conv}, r[0], r[1], qty)
		if err != nil {
			t.Fatalf("%s->%s: %v", r[0], r[1], err)
		}
		if got := domain.Display(forward, 12); !got.Equal(dec(t, r[6])) {
			t.Errorf("%s->%s: got %s, want %s", r[0], r[1], got, r[6])
		}
		back, err := domain.Convert([]domain.Conversion{conv}, r[1], r[0], forward)
		if err != nil {
			t.Fatalf("%s->%s: %v", r[1], r[0], err)
		}
		if got := domain.Display(back, 12); !got.Equal(qty) {
			t.Errorf("%s->%s->%s: got %s, want %s", r[0], r[1], r[0], got, qty)
		}
	}
}

func TestConvert_Paths(t *testing.T) {
	convs := domain.StandardConversions()
	tests := []struct {
		name, from, to, qty, want string
		wantCode                  string
	}{
		{"same unit", "kg", "kg", "5", "5", ""},
		{"direct", "kg", "g", "2", "2000", ""},
		{"temperature offset", "C", "F", "100", "212", ""},
		{"two hops through shared unit", "K", "F", "373.15", "212", ""},
		{"two hops through chained unit", "ft", "mm", "1", "304.8", ""},
		{"unknown unit", "kg", "furlong", "1", "", domain.CodeNoPath},
		{"cross class", "kg", "m", "1", "", domain.CodeNoPath},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.Convert(convs, tc.from, tc.to, dec(t, tc.qty))
			var de *domain.Error
			if tc.wantCode != "" {
				if !errors.As(err, &de) || de.Code != tc.wantCode {
					t.Fatalf("got %v, want code %s", err, tc.wantCode)
				}
				return
			}
			if err != nil || !domain.Display(got, 9).Equal(dec(t, tc.want)) {
				t.Fatalf("got %s, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestConvert_ZeroFactorAndThreeHopsAreUnreachable(t *testing.T) {
	one := decimal.NewFromInt(1)
	zero := []domain.Conversion{{From: "a", To: "b", Class: "x", Factor: decimal.Zero}}
	if _, err := domain.Convert(zero, "b", "a", one); err == nil {
		t.Fatal("zero factor must not produce a path")
	}
	chain := []domain.Conversion{{From: "a", To: "b", Class: "x", Factor: one}, {From: "b", To: "c", Class: "x", Factor: one}, {From: "c", To: "d", Class: "x", Factor: one}}
	if _, err := domain.Convert(chain, "a", "d", one); err == nil {
		t.Fatal("a three-hop path must be rejected")
	}
	if _, err := domain.Convert(chain, "a", "c", one); err != nil {
		t.Fatalf("two hops must work: %v", err)
	}
}
