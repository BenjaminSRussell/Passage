package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type exposureStore struct {
	store.Store
	profiles []store.CreditProfile
	le       string
	customer string
	batches  int
}

// ScanCreditProfiles pages the fixture like the store does: the customer
// filter applied, batch profiles at a time, stopping on an error from fn.
func (s *exposureStore) ScanCreditProfiles(_ context.Context, le, customer string, batch int, fn func([]store.CreditProfile) error) error {
	s.le, s.customer, s.batches = le, customer, 0
	var match []store.CreditProfile
	for _, c := range s.profiles {
		if customer == "" || c.CustomerID == customer {
			match = append(match, c)
		}
	}
	for start := 0; start < len(match); start += batch {
		s.batches++
		if err := fn(match[start:min(start+batch, len(match))]); err != nil {
			return err
		}
	}
	return nil
}

func exposureProfile(customer, limitType, limit, openAR string) store.CreditProfile {
	d := decimal.RequireFromString
	return store.CreditProfile{LegalEntityID: testLE, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(limit), Currency: "USD", LimitType: limitType, TolerancePct: d("5"), ToleranceAmount: d("1000")},
		Exposure: domain.Exposure{OpenAR: d(openAR)}}
}

type exposureBody struct {
	Items []struct {
		CustomerID     string  `json:"customerId"`
		Status         string  `json:"status"`
		UtilizationPct *string `json:"utilizationPct"`
		Balance        string  `json:"balance"`
		Threshold      string  `json:"threshold"`
	} `json:"items"`
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
}

func exposureCall(t *testing.T, s *exposureStore, query string) (int, exposureBody) {
	t.Helper()
	rec := serveBody(t, s, http.MethodGet, creditExposure+query, "", principal(dutyRead), nil)
	var body exposureBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, body
}

func TestCreditExposure_RanksByUtilization(t *testing.T) {
	a, b, c, none := ids.New(), ids.New(), ids.New(), ids.New()
	s := &exposureStore{profiles: []store.CreditProfile{
		exposureProfile(none, domain.LimitNone, "0", "500"),
		exposureProfile(a, domain.LimitBalance, "10000", "2500"),
		exposureProfile(b, domain.LimitBalance, "10000", "10500"),
		exposureProfile(c, domain.LimitBalance, "10000", "12000"),
	}}
	if rec := serveBody(t, s, http.MethodGet, creditExposure, "", principal(), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty required: %d", rec.Code)
	}
	code, body := exposureCall(t, s, "")
	if code != http.StatusOK || s.le != testLE {
		t.Fatalf("status %d le %q", code, s.le)
	}
	order := []string{c, b, a, none}
	statuses := []string{domain.UtilOverTolerance, domain.UtilOverLimit, domain.UtilWithin, domain.UtilNoLimit}
	if len(body.Items) != 4 || body.Total != 4 || body.Truncated {
		t.Fatalf("body %+v", body)
	}
	for i := range order {
		if body.Items[i].CustomerID != order[i] || body.Items[i].Status != statuses[i] {
			t.Fatalf("row %d: %+v", i, body.Items[i])
		}
	}
	if *body.Items[0].UtilizationPct != "120" || body.Items[0].Threshold != "11000" || body.Items[3].UtilizationPct != nil {
		t.Fatalf("figures: %+v", body.Items)
	}
}

func TestCreditExposure_Filters(t *testing.T) {
	a, b, c := ids.New(), ids.New(), ids.New()
	s := &exposureStore{profiles: []store.CreditProfile{
		exposureProfile(a, domain.LimitBalance, "10000", "2500"),
		exposureProfile(b, domain.LimitBalance, "10000", "9000"),
		exposureProfile(c, domain.LimitBalance, "10000", "10500"),
	}}
	for query, want := range map[string]int{
		"?minUtilizationPct=90":          2,
		"?minUtilizationPct=90.01":       1,
		"?status=WITHIN":                 2,
		"?status=OVER_LIMIT":             1,
		"?customerId=" + b:               1,
		"?limit=1":                       1,
		"?status=NO_LIMIT":               0,
		"?minUtilizationPct=0":           3,
		"?status=WITHIN&customerId=" + c: 0,
	} {
		code, body := exposureCall(t, s, query)
		if code != http.StatusOK || len(body.Items) != want {
			t.Fatalf("%s: status %d items %d, want %d", query, code, len(body.Items), want)
		}
	}
	if _, body := exposureCall(t, s, "?limit=1"); body.Total != 3 {
		t.Fatalf("total is the match count before the page: %d", body.Total)
	}
	for _, bad := range []string{"?minUtilizationPct=-1", "?minUtilizationPct=abc", "?minUtilizationPct=1.234", "?status=OVER", "?customerId=x", "?limit=0", "?limit=201"} {
		if code, _ := exposureCall(t, s, bad); code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", bad, code)
		}
	}
}

// The ranking covers every profile, not the first batch: the customers using
// the most of their limit sit at the end of the scan order and still lead.
func TestCreditExposure_RanksAcrossBatches(t *testing.T) {
	s := &exposureStore{}
	for i := 0; i < 3*exposureBatch; i++ {
		s.profiles = append(s.profiles, exposureProfile(ids.New(), domain.LimitBalance, "100", "1"))
	}
	top, second := ids.New(), ids.New()
	s.profiles = append(s.profiles, exposureProfile(second, domain.LimitBalance, "100", "90"), exposureProfile(top, domain.LimitBalance, "100", "99"))
	code, body := exposureCall(t, s, "?limit=2")
	if code != http.StatusOK || s.batches != 4 || body.Truncated || body.Total != 3*exposureBatch+2 || len(body.Items) != 2 {
		t.Fatalf("status %d batches %d truncated %v total %d items %d", code, s.batches, body.Truncated, body.Total, len(body.Items))
	}
	if body.Items[0].CustomerID != top || body.Items[1].CustomerID != second {
		t.Fatalf("top of the ranking: %+v", body.Items)
	}
	// The customer filter reaches the store, so a customer late in the order is found.
	if code, body := exposureCall(t, s, "?customerId="+top); code != http.StatusOK || s.customer != top || len(body.Items) != 1 || body.Items[0].CustomerID != top {
		t.Fatalf("customer filter: status %d store %q items %+v", code, s.customer, body.Items)
	}
}

func TestCreditExposure_TruncatedPastTheCap(t *testing.T) {
	s := &exposureStore{}
	for i := 0; i < maxExposureProfiles; i++ {
		s.profiles = append(s.profiles, exposureProfile(ids.New(), domain.LimitBalance, "100", "1"))
	}
	if code, body := exposureCall(t, s, ""); code != http.StatusOK || body.Truncated || body.Total != maxExposureProfiles || len(body.Items) != defaultPageSize {
		t.Fatalf("exactly at the cap: status %d truncated %v total %d items %d", code, body.Truncated, body.Total, len(body.Items))
	}
	s.profiles = append(s.profiles, exposureProfile(ids.New(), domain.LimitBalance, "100", "50"))
	if code, body := exposureCall(t, s, ""); code != http.StatusOK || !body.Truncated || body.Total != maxExposureProfiles {
		t.Fatalf("past the cap: status %d truncated %v total %d", code, body.Truncated, body.Total)
	}
}

func TestListAgreements_ExpiryAndUsageFilters(t *testing.T) {
	s := &slice3Store{}
	rec := serveBody(t, s, http.MethodGet, agreements+"?expiringBy=2027-06-30&minUsedPct=80.5&status=ACTIVE", "", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || s.agreements == nil || s.agreements.ExpiringBy != "2027-06-30" || s.agreements.MinUsedPct != "80.5" {
		t.Fatalf("status %d filter %+v", rec.Code, s.agreements)
	}
	for _, bad := range []string{"expiringBy=soon", "expiringBy=2027-13-01", "minUsedPct=-1", "minUsedPct=x", "minUsedPct=1.234", "minUsedPct=1001"} {
		if rec := serveBody(t, s, http.MethodGet, agreements+"?"+bad, "", principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", bad, rec.Code)
		}
	}
}
