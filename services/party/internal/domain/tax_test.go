package domain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

type stubChecker struct {
	status domain.Status
	err    error
	calls  int
}

func (s *stubChecker) Check(context.Context, string, string) (domain.Status, error) {
	s.calls++
	return s.status, s.err
}

func TestValidateFormat(t *testing.T) {
	tests := []struct {
		name    string
		kind    domain.TaxType
		country string
		number  string
		wantErr bool
	}{
		{"de vat ok", domain.TaxVAT, "DE", "DE 123.456.789", false},
		{"vat without prefix", domain.TaxVAT, "DE", "123456789", false},
		{"nl vat ok", domain.TaxVAT, "NL", "NL123456789B01", false},
		{"greece accepts GR country", domain.TaxVAT, "gr", "EL123456789", false},
		{"de vat too short", domain.TaxVAT, "DE", "DE12345", true},
		{"vat unknown country", domain.TaxVAT, "US", "123456789", true},
		{"vat missing country", domain.TaxVAT, "", "123456789", true},
		{"ein with hyphen", domain.TaxEIN, "", "12-3456789", false},
		{"ein too short", domain.TaxEIN, "", "12-345678", true},
		{"ein letters", domain.TaxEIN, "", "12-34567AB", true},
		{"gstin valid", domain.TaxGSTIN, "", "27AAPFU0939F1ZV", false},
		{"gstin valid lowercase", domain.TaxGSTIN, "", "27aapfu0939f1zv", false},
		{"gstin second vector", domain.TaxGSTIN, "", "29AABCT1332L1ZA", false},
		{"gstin bad check digit", domain.TaxGSTIN, "", "27AAPFU0939F1ZW", true},
		{"gstin wrong shape", domain.TaxGSTIN, "", "27AAPFU0939F1Z", true},
		{"empty number", domain.TaxVAT, "DE", "  ", true},
		{"unsupported type", domain.TaxType("ABN"), "AU", "51824753556", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidateFormat(tc.kind, tc.country, tc.number)
			var formatErr *domain.FormatError
			if tc.wantErr != errors.As(err, &formatErr) {
				t.Fatalf("got %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name      string
		checker   *stubChecker
		kind      domain.TaxType
		country   string
		number    string
		want      domain.Status
		wantErr   bool
		wantCalls int
	}{
		{"vat valid", &stubChecker{status: domain.StatusValid}, domain.TaxVAT, "DE", "DE123456789", domain.StatusValid, false, 1},
		{"vat invalid", &stubChecker{status: domain.StatusInvalid}, domain.TaxVAT, "DE", "DE123456789", domain.StatusInvalid, false, 1},
		{"deadline becomes unverified", &stubChecker{err: context.DeadlineExceeded}, domain.TaxVAT, "DE", "DE123456789", domain.StatusUnverified, false, 1},
		{"outage becomes unverified", &stubChecker{err: errors.New("503")}, domain.TaxVAT, "FR", "FR12345678901", domain.StatusUnverified, false, 1},
		{"ein is format only", &stubChecker{}, domain.TaxEIN, "", "12-3456789", domain.StatusFormatOnly, false, 0},
		{"gstin is format only", &stubChecker{}, domain.TaxGSTIN, "", "27AAPFU0939F1ZV", domain.StatusFormatOnly, false, 0},
		{"bad format never reaches the checker", &stubChecker{status: domain.StatusValid}, domain.TaxVAT, "DE", "DE1", "", true, 0},
		{"unsupported type never reaches the checker", &stubChecker{}, domain.TaxType("ABN"), "AU", "1", "", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.Validate(context.Background(), tc.checker, tc.kind, tc.country, tc.number)
			if (err != nil) != tc.wantErr || got != tc.want || tc.checker.calls != tc.wantCalls {
				t.Fatalf("got %q %v calls=%d; want %q err=%v calls=%d", got, err, tc.checker.calls, tc.want, tc.wantErr, tc.wantCalls)
			}
		})
	}
}
