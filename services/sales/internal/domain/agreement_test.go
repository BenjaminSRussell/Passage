package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// TestReleaseAgreement_Table covers FR-27.10 / AC-27.9: a fully released
// commitment is EXHAUSTED and the next release is refused.
func TestReleaseAgreement_Table(t *testing.T) {
	d := decimal.NewFromInt
	tests := []struct {
		name       string
		commitment decimal.Decimal
		released   decimal.Decimal
		qty        decimal.Decimal
		wantNext   decimal.Decimal
		wantStatus string
		wantErr    error
	}{
		{name: "partial release stays active", commitment: d(1000), released: d(0), qty: d(400), wantNext: d(400), wantStatus: AgreementActive},
		{name: "exact draw-down exhausts", commitment: d(1000), released: d(600), qty: d(400), wantNext: d(1000), wantStatus: AgreementExhausted},
		{name: "over draw refused and unchanged", commitment: d(1000), released: d(1000), qty: d(1), wantNext: d(1000), wantErr: ErrAgreementExhausted},
		{name: "overshoot refused and unchanged", commitment: d(1000), released: d(900), qty: d(101), wantNext: d(900), wantErr: ErrAgreementExhausted},
		{name: "non-positive quantity refused", commitment: d(1000), released: d(0), qty: d(0), wantNext: d(0), wantErr: errAny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next, status, err := ReleaseAgreement(tc.commitment, tc.released, tc.qty)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("want error, got next=%s status=%q", next, status)
				}
				if tc.wantErr != errAny && !errors.Is(err, tc.wantErr) {
					t.Fatalf("error %v, want %v", err, tc.wantErr)
				}
				if !next.Equal(tc.wantNext) {
					t.Fatalf("released changed on refusal: %s", next)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !next.Equal(tc.wantNext) || status != tc.wantStatus {
				t.Fatalf("got %s/%q want %s/%q", next, status, tc.wantNext, tc.wantStatus)
			}
		})
	}
}

// errAny marks a case where any error is acceptable.
var errAny = errors.New("any error")

// TestReleaseAgreement_NeverOverdraws is a property: however releases are
// sequenced, released quantity never exceeds the commitment.
func TestReleaseAgreement_NeverOverdraws(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		commitment := decimal.NewFromInt(rapid.Int64Range(1, 10_000).Draw(t, "commitment"))
		released := decimal.Zero
		steps := rapid.IntRange(1, 30).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			qty := decimal.NewFromInt(rapid.Int64Range(1, 2_000).Draw(t, "qty"))
			next, status, err := ReleaseAgreement(commitment, released, qty)
			if err != nil {
				if !errors.Is(err, ErrAgreementExhausted) {
					t.Fatalf("unexpected error %v", err)
				}
				if !next.Equal(released) {
					t.Fatalf("refused release changed released qty")
				}
				continue
			}
			released = next
			if released.GreaterThan(commitment) {
				t.Fatalf("released %s exceeds commitment %s", released, commitment)
			}
			if (status == AgreementExhausted) != released.Equal(commitment) {
				t.Fatalf("status %q inconsistent with released %s of %s", status, released, commitment)
			}
		}
	})
}

// TestAgreementValidOn: validity bounds are inclusive and nil bounds are open.
func TestAgreementValidOn(t *testing.T) {
	from, to := listingDay("2027-01-01"), listingDay("2027-12-31")
	cases := []struct {
		name     string
		from, to *time.Time
		day      string
		want     bool
	}{
		{"open both ends", nil, nil, "2030-06-01", true},
		{"first day", from, to, "2027-01-01", true},
		{"last day", from, to, "2027-12-31", true},
		{"day before", from, to, "2026-12-31", false},
		{"day after", from, to, "2028-01-01", false},
		{"open end", from, nil, "2040-01-01", true},
		{"open start", nil, to, "2001-01-01", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AgreementValidOn(c.from, c.to, *listingDay(c.day)); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestFormatAgreementNumber(t *testing.T) {
	if got := FormatAgreementNumber(42); got != "SA-0000042" {
		t.Fatalf("got %s", got)
	}
}
