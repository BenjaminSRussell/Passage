package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCheckConvertible_StateAndExpiry(t *testing.T) {
	validUntil := day("2027-03-31")
	cases := []struct {
		name   string
		status string
		today  string
		want   error
	}{
		{"sent within validity", QuoteSent, "2027-03-10", nil},
		{"sent on last valid day", QuoteSent, "2027-03-31", nil},
		{"sent after validity", QuoteSent, "2027-04-01", ErrQuotationExpired},
		{"partially converted continues", QuoteConverted, "2027-03-10", nil},
		{"draft cannot convert", QuoteDraft, "2027-03-10", ErrQuotationState},
		{"lost cannot convert", QuoteLost, "2027-03-10", ErrQuotationState},
		{"expired status cannot convert", QuoteExpired, "2027-03-10", ErrQuotationState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckConvertible(tc.status, validUntil, day(tc.today))
			if !errors.Is(err, tc.want) && !(err == nil && tc.want == nil) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestQuotationTransitions(t *testing.T) {
	validUntil := day("2027-03-31")
	sendCases := []struct {
		status, today string
		want          error
	}{
		{QuoteDraft, "2027-03-31", nil},
		{QuoteDraft, "2027-04-01", ErrQuotationExpired},
		{QuoteSent, "2027-03-10", ErrQuotationState},
		{QuoteLost, "2027-03-10", ErrQuotationState},
	}
	for _, tc := range sendCases {
		if err := CheckSend(tc.status, validUntil, day(tc.today)); !errors.Is(err, tc.want) && !(err == nil && tc.want == nil) {
			t.Errorf("CheckSend(%s, %s) = %v, want %v", tc.status, tc.today, err, tc.want)
		}
	}
	for status, ok := range map[string]bool{QuoteDraft: true, QuoteSent: true, QuoteConverted: false, QuoteLost: false, QuoteExpired: false} {
		if err := CheckLose(status); (err == nil) != ok {
			t.Errorf("CheckLose(%s) = %v, want ok=%v", status, err, ok)
		}
	}
	for _, s := range []string{QuoteDraft, QuoteSent, QuoteConverted, QuoteLost, QuoteExpired, QuoteCancelled} {
		if !IsQuotationStatus(s) {
			t.Errorf("%s must be a quotation status", s)
		}
	}
	if IsQuotationStatus("OPEN") {
		t.Error("OPEN is not a quotation status")
	}
}

func TestQuotationValidUntil_DefaultThirtyDays(t *testing.T) {
	got := QuotationValidUntil(time.Date(2027, 3, 1, 15, 45, 0, 0, time.UTC), QuotationExpiryDays)
	if want := day("2027-03-31"); !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestValidateProbability(t *testing.T) {
	for p, ok := range map[int]bool{-1: false, 0: true, 50: true, 100: true, 101: false} {
		if err := ValidateProbability(p); (err == nil) != ok {
			t.Errorf("ValidateProbability(%d) err=%v, want ok=%v", p, err, ok)
		}
	}
}

func TestSelectLines(t *testing.T) {
	cases := []struct {
		name      string
		open      []int
		requested []int
		want      []int
		wantErr   bool
	}{
		{"empty request selects every open line", []int{3, 1, 2}, nil, []int{1, 2, 3}, false},
		{"subset in order", []int{1, 2, 3}, []int{3, 1}, []int{1, 3}, false},
		{"converted line not open", []int{1, 3}, []int{2}, nil, true},
		{"duplicate rejected", []int{1, 2}, []int{1, 1}, nil, true},
		{"nothing left to convert", nil, nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectLines(tc.open, tc.requested)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestFormatQuotationNumber(t *testing.T) {
	if got := FormatQuotationNumber(42); got != "SQ-0000042" {
		t.Fatalf("got %s", got)
	}
}

func TestCheckCancel_DraftAndSentOnly(t *testing.T) {
	for status, want := range map[string]error{QuoteDraft: nil, QuoteSent: nil, QuoteConverted: ErrQuotationState,
		QuoteLost: ErrQuotationState, QuoteExpired: ErrQuotationState, QuoteCancelled: ErrQuotationState} {
		if got := CheckCancel(status); !errors.Is(got, want) && got != want {
			t.Errorf("CheckCancel(%s) = %v, want %v", status, got, want)
		}
	}
}

func TestCheckChange_StateLinesAndExpiry(t *testing.T) {
	validUntil := day("2027-03-31")
	cases := []struct {
		name   string
		status string
		today  string
		lines  bool
		want   error
	}{
		{"draft takes lines", QuoteDraft, "2027-03-10", true, nil},
		{"draft takes validity", QuoteDraft, "2027-03-31", false, nil},
		{"sent extends validity", QuoteSent, "2027-03-10", false, nil},
		{"sent takes no lines", QuoteSent, "2027-03-10", true, ErrQuotationState},
		{"lapsed draft", QuoteDraft, "2027-04-01", false, ErrQuotationExpired},
		{"lapsed sent", QuoteSent, "2027-04-01", false, ErrQuotationExpired},
		{"converted", QuoteConverted, "2027-03-10", false, ErrQuotationState},
		{"cancelled", QuoteCancelled, "2027-03-10", false, ErrQuotationState},
		{"lost", QuoteLost, "2027-03-10", false, ErrQuotationState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckChange(tc.status, validUntil, day(tc.today), tc.lines); !errors.Is(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
