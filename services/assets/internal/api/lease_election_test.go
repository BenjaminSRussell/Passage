package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

func TestLease_ElectionsCreateWithoutLiabilityAndAreValidated(t *testing.T) {
	h := server(newFake())
	body := `{"legalEntityId":"` + testLE + `","leaseNumber":"L-ST","commencementDate":"2026-01-01","termMonths":12,"paymentsPerYear":4,"paymentAmount":"750",
		"paymentTiming":"advance","ibrRate":"0.05","currency":"USD","election":"ShortTerm"}`
	rec := call(h, http.MethodPost, "/v1/leases", body, "assets.lease.maintain", true)
	var lease store.Lease
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &lease) != nil {
		t.Fatalf("short-term: %d %s", rec.Code, rec.Body)
	}
	if lease.Election != domain.ElectionShortTerm || !lease.Liability.IsZero() || !lease.ROUAsset.IsZero() || len(lease.Schedule) != 4 || !lease.Schedule[0].Payment.Equal(lease.PaymentAmount) {
		t.Fatalf("lease: %+v", lease)
	}
	low := strings.Replace(body, `"ShortTerm"`, `"LowValue","underlyingAssetValue":"4800"`, 1)
	rec = call(h, http.MethodPost, "/v1/leases", strings.Replace(strings.Replace(low, `"termMonths":12`, `"termMonths":36`, 1), `"L-ST"`, `"L-LV"`, 1), "assets.lease.maintain", true)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"underlyingAssetValue":"4800"`) || !strings.Contains(rec.Body.String(), `"underlyingAssetCurrency":"USD"`) {
		t.Fatalf("low value: %d %s", rec.Code, rec.Body)
	}
	for name, c := range map[string]struct{ body, code string }{
		"short over 12 months":    {strings.Replace(body, `"termMonths":12`, `"termMonths":24`, 1), domain.CodeLeaseElection},
		"low value over limit":    {strings.Replace(low, `"4800"`, `"5001"`, 1), domain.CodeLeaseElection},
		"low value without value": {strings.Replace(body, `"ShortTerm"`, `"LowValue"`, 1), domain.CodeLeaseElection},
		"value without low value": {strings.Replace(body, `"ShortTerm"`, `"None","underlyingAssetValue":"100"`, 1), domain.CodeLeaseElection},
		"unknown election":        {strings.Replace(body, `"ShortTerm"`, `"Other"`, 1), domain.CodeLeaseElection},
		"short with purchase":     {strings.Replace(body, `"ShortTerm"`, `"ShortTerm","purchaseOption":true`, 1), domain.CodeLeaseElection},
		// A GBP lease states the value in GBP by default, a currency the threshold is not set in.
		"low value in GBP": {strings.Replace(strings.Replace(low, `"currency":"USD"`, `"currency":"GBP"`, 1), `"termMonths":12`, `"termMonths":36`, 1), domain.CodeLeaseElection},
	} {
		rec := call(h, http.MethodPost, "/v1/leases", c.body, "assets.lease.maintain", true)
		if rec.Code != http.StatusUnprocessableEntity || problem(t, rec).Code != c.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// The default currency of the value is the lease currency, and a GBP lease may state it in EUR.
	if !strings.Contains(rec.Body.String(), `"purchaseOption":false`) {
		t.Fatalf("purchaseOption missing: %s", rec.Body)
	}
	gbp := strings.Replace(strings.Replace(low, `"currency":"USD"`, `"currency":"GBP"`, 1), `"4800"`, `"4800","underlyingAssetCurrency":"EUR"`, 1)
	if rec := call(h, http.MethodPost, "/v1/leases", strings.Replace(strings.Replace(gbp, `"termMonths":12`, `"termMonths":36`, 1), `"L-ST"`, `"L-GBP"`, 1), "assets.lease.maintain", true); rec.Code != http.StatusCreated ||
		!strings.Contains(rec.Body.String(), `"underlyingAssetCurrency":"EUR"`) || !strings.Contains(rec.Body.String(), `"currency":"GBP"`) {
		t.Fatalf("GBP lease, EUR value: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, http.MethodPost, "/v1/leases", strings.Replace(gbp, `"EUR"`, `"eur"`, 1), "assets.lease.maintain", true); rec.Code != http.StatusBadRequest {
		t.Errorf("lower-case value currency: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, http.MethodPost, "/v1/leases", strings.Replace(low, `"4800"`, `"abc"`, 1), "assets.lease.maintain", true); rec.Code != http.StatusBadRequest {
		t.Errorf("non-numeric value: %d %s", rec.Code, rec.Body)
	}
}
