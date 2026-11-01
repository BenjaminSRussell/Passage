package domain_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

func limit(t *testing.T, currency, spending, approval, by string) domain.SigningLimit {
	return domain.SigningLimit{Currency: currency, SpendingLimit: decimal.RequireFromString(spending),
		ApprovalLimit: decimal.RequireFromString(approval), RecertifyBy: day(t, by)}
}

func TestRequesterEscalates(t *testing.T) {
	today := day(t, "2026-03-01")
	usd := []domain.SigningLimit{limit(t, "USD", "5000", "5000", "2026-12-31"), limit(t, "EUR", "100", "100", "2026-12-31")}
	expired := []domain.SigningLimit{limit(t, "USD", "5000", "5000", "2026-02-28")}
	for _, tc := range []struct {
		name     string
		limits   []domain.SigningLimit
		amount   string
		currency string
		want     bool
		warn     string
	}{
		{"no limit at all is not escalated until default limits exist", nil, "9000000", "USD", false, ""},
		{"at the limit", usd, "5000", "USD", false, ""},
		{"above the limit", usd, "5000.01", "USD", true, ""},
		{"the limit of the document currency applies", usd, "150", "EUR", true, ""},
		{"a currency with no limit is not converted and escalates", usd, "1", "GBP", true, ""},
		{"zero in another currency needs no level", usd, "0", "GBP", false, ""},
		{"expired escalates with the warning", expired, "1", "USD", true, domain.CodeSigningExpired},
		{"due today still holds", []domain.SigningLimit{limit(t, "USD", "5000", "5000", "2026-03-01")}, "100", "USD", false, ""},
	} {
		got, warn := domain.RequesterEscalates(tc.limits, decimal.RequireFromString(tc.amount), tc.currency, today)
		if got != tc.want || (tc.warn == "") != (warn == nil) || (warn != nil && warn.Code != tc.warn) {
			t.Errorf("%s: escalates %v warning %+v, want %v %q", tc.name, got, warn, tc.want, tc.warn)
		}
	}
}

// AC-17.12 (limit half): an approver with an approval limit of 5,000 USD cannot approve 7,000 USD.
func TestCheckApprover(t *testing.T) {
	today := day(t, "2026-03-01")
	l := []domain.SigningLimit{limit(t, "USD", "1000", "5000", "2026-12-31")}
	for _, tc := range []struct {
		name   string
		limits []domain.SigningLimit
		amount string
		code   string
	}{
		{"no limit is an approval limit of zero", nil, "7000", domain.CodeSigningApprovalLimit},
		{"no limit still approves zero", nil, "0", ""},
		{"within the approval limit", l, "5000", ""},
		{"above the approval limit", l, "7000", domain.CodeSigningApprovalLimit},
		{"expired", []domain.SigningLimit{limit(t, "USD", "1000", "5000", "2026-02-01")}, "1", domain.CodeSigningExpired},
		{"a limit in another currency is not converted", []domain.SigningLimit{limit(t, "EUR", "999999", "999999", "2026-12-31")}, "7000", domain.CodeSigningApprovalLimit},
	} {
		err := domain.CheckApprover(tc.limits, decimal.RequireFromString(tc.amount), "USD", today)
		code := ""
		if err != nil {
			code = err.(*domain.Error).Code
		}
		if code != tc.code {
			t.Errorf("%s: code %q, want %q (%v)", tc.name, code, tc.code, err)
		}
	}
}

func TestRecertifyDateIsTwelveMonthsOn(t *testing.T) {
	if got := domain.RecertifyDate(day(t, "2026-03-01")).String(); got != "2027-03-01" {
		t.Errorf("got %s", got)
	}
}
