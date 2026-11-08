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
			next, status, err := ReleaseAgreement(tc.commitment, decimal.Zero, tc.released, tc.qty)
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
// TestReleaseAgreement_NeverOverdraws is a property: however releases are
// sequenced, released quantity never exceeds the commitment.
func TestReleaseAgreement_NeverOverdraws(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		commitment := decimal.NewFromInt(rapid.Int64Range(1, 10_000).Draw(t, "commitment"))
		released := decimal.Zero
		steps := rapid.IntRange(1, 30).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			qty := decimal.NewFromInt(rapid.Int64Range(1, 2_000).Draw(t, "qty"))
			next, status, err := ReleaseAgreement(commitment, decimal.Zero, released, qty)
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

func sp(s string) *string { return &s }

func dp(n int) *decimal.Decimal { v := d(n); return &v }

func TestReviseAgreement_Table(t *testing.T) {
	cur := AgreementState{Commitment: d(1000), Released: d(600), UnitPrice: d(12), CumTolerance: d(0), ValidFrom: sp("2027-01-01"), ValidTo: sp("2027-12-31"), Status: AgreementActive}
	cases := []struct {
		name    string
		cur     AgreementState
		rev     AgreementRevision
		err     error
		status  string
		fields  []string
		commit  int
		validTo *string
	}{
		{name: "raise commitment", cur: cur, rev: AgreementRevision{Commitment: dp(1500)}, status: AgreementActive, fields: []string{"commitmentQty"}, commit: 1500, validTo: sp("2027-12-31")},
		{name: "cut to released exhausts", cur: cur, rev: AgreementRevision{Commitment: dp(600)}, status: AgreementExhausted, fields: []string{"commitmentQty", "status"}, commit: 600, validTo: sp("2027-12-31")},
		{name: "below released refused", cur: cur, rev: AgreementRevision{Commitment: dp(599)}, err: ErrCommitmentBelowReleased},
		{name: "exhausted reopens on raise", cur: AgreementState{Commitment: d(600), Released: d(600), UnitPrice: d(12), Status: AgreementExhausted},
			rev: AgreementRevision{Commitment: dp(700)}, status: AgreementActive, fields: []string{"commitmentQty", "status"}, commit: 700},
		{name: "price and tolerance", cur: cur, rev: AgreementRevision{UnitPrice: dp(13), CumTolerance: dp(5)}, status: AgreementActive, fields: []string{"unitPrice", "cumTolerance"}, commit: 1000, validTo: sp("2027-12-31")},
		{name: "same values change nothing", cur: cur, rev: AgreementRevision{Commitment: dp(1000), UnitPrice: dp(12), ValidTo: sp("2027-12-31")}, status: AgreementActive, commit: 1000, validTo: sp("2027-12-31")},
		{name: "clear validTo", cur: cur, rev: AgreementRevision{ValidTo: sp("")}, status: AgreementActive, fields: []string{"validTo"}, commit: 1000},
		{name: "validTo before validFrom", cur: cur, rev: AgreementRevision{ValidTo: sp("2026-12-31")}, err: ErrInvalidValidity},
		{name: "closed refused", cur: AgreementState{Commitment: d(10), Status: AgreementClosed}, rev: AgreementRevision{Commitment: dp(20)}, err: ErrAgreementClosed},
		{name: "zero commitment refused", cur: cur, rev: AgreementRevision{Commitment: dp(0)}, err: errAny},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			next, diff, err := ReviseAgreement(c.cur, c.rev)
			if c.err == errAny {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != c.err {
				t.Fatalf("err %v, want %v", err, c.err)
			}
			if err != nil {
				return
			}
			if next.Status != c.status || !next.Commitment.Equal(d(c.commit)) || (c.validTo != nil && dayText(next.ValidTo) != *c.validTo) {
				t.Fatalf("next %+v", next)
			}
			var got []string
			for _, f := range diff {
				got = append(got, f.Field)
			}
			if len(got) != len(c.fields) {
				t.Fatalf("diff %v, want %v", got, c.fields)
			}
			for i := range got {
				if got[i] != c.fields[i] {
					t.Fatalf("diff %v, want %v", got, c.fields)
				}
			}
		})
	}
}

var errAny = errors.New("any error")

func dpct(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// TestReleaseAgreement_Tolerance covers BR-27.9: a release may draw up to the
// commitment plus overdelivery_pct, and the agreement is EXHAUSTED at that
// ceiling, not at the commitment.
func TestReleaseAgreement_Tolerance(t *testing.T) {
	next, status, err := ReleaseAgreement(d(1000), dpct("5"), d(900), d(150))
	if err != nil || !next.Equal(d(1050)) || status != AgreementExhausted {
		t.Fatalf("draw to the ceiling: %s %s %v", next, status, err)
	}
	next, status, err = ReleaseAgreement(d(1000), dpct("5"), d(0), d(1000))
	if err != nil || !next.Equal(d(1000)) || status != AgreementActive {
		t.Fatalf("the commitment itself leaves the tolerance open: %s %s %v", next, status, err)
	}
	if _, _, err = ReleaseAgreement(d(1000), dpct("5"), d(1000), d(51)); !errors.Is(err, ErrAgreementExhausted) {
		t.Fatalf("beyond the ceiling must be refused: %v", err)
	}
	if got := AgreementCeiling(d(3), dpct("0.5")); !got.Equal(dpct("3.015")) {
		t.Fatalf("ceiling %s", got)
	}
	if got := AgreementCeiling(dpct("0.000001"), dpct("50")); !got.Equal(dpct("0.000001")) {
		t.Fatalf("ceiling cut to 6 places must not exceed the exact figure: %s", got)
	}
	for _, bad := range []string{"-0.01", "100.01", "1.005"} {
		if ValidateOverdelivery(dpct(bad)) == nil {
			t.Fatalf("%s must be refused", bad)
		}
	}
	for _, ok := range []string{"0", "5", "12.5", "100"} {
		if err := ValidateOverdelivery(dpct(ok)); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
}

func TestReviseAgreement_Tolerance(t *testing.T) {
	cur := AgreementState{Commitment: d(1000), Released: d(1040), OverdeliveryPct: dpct("5"), UnitPrice: d(12), Status: AgreementActive}
	next, diff, err := ReviseAgreement(cur, AgreementRevision{OverdeliveryPct: ptr(dpct("4"))})
	if err != nil || next.Status != AgreementExhausted || len(diff) != 2 || diff[0].Field != "overdeliveryPct" {
		t.Fatalf("a tolerance that meets the released quantity exhausts: %+v %v %v", next, diff, err)
	}
	if _, _, err = ReviseAgreement(cur, AgreementRevision{OverdeliveryPct: ptr(dpct("3.9"))}); !errors.Is(err, ErrCommitmentBelowReleased) {
		t.Fatalf("a tolerance below the released quantity must be refused: %v", err)
	}
	if _, _, err = ReviseAgreement(cur, AgreementRevision{Commitment: dp(991)}); err != nil {
		t.Fatalf("the ceiling, not the commitment, bounds the released quantity: %v", err)
	}
	if _, _, err = ReviseAgreement(cur, AgreementRevision{OverdeliveryPct: ptr(dpct("101"))}); err == nil {
		t.Fatal("101 percent must be refused")
	}
	back, status, err := ReturnToAgreement(d(1000), dpct("5"), d(1050), d(60), AgreementExhausted)
	if err != nil || !back.Equal(d(990)) || status != AgreementActive {
		t.Fatalf("a return reopens an exhausted agreement: %s %s %v", back, status, err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestReturnToAgreement(t *testing.T) {
	next, status, err := ReturnToAgreement(d(1000), d(0), d(1000), d(400), AgreementExhausted)
	if err != nil || !next.Equal(d(600)) || status != AgreementActive {
		t.Fatalf("exhausted agreement reopens: %s %s %v", next, status, err)
	}
	if _, status, err = ReturnToAgreement(d(1000), d(0), d(600), d(100), AgreementClosed); err != nil || status != AgreementClosed {
		t.Fatalf("closed stays closed: %s %v", status, err)
	}
	if _, _, err = ReturnToAgreement(d(1000), d(0), d(50), d(100), AgreementActive); err == nil {
		t.Fatal("returning more than was released must fail")
	}
	if _, _, err = ReturnToAgreement(d(1000), d(0), d(50), d(0), AgreementActive); err == nil {
		t.Fatal("a zero return must fail")
	}
}
