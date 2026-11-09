package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type parametersStore struct {
	store.Store
	saved   store.SalesParameters
	version int
	actor   string
	puts    int
}

func (s *parametersStore) GetParameters(_ context.Context, le string) (store.SalesParameters, error) {
	return store.SalesParameters{LegalEntityID: le, Parameters: domain.DefaultParameters()}, nil
}

func (s *parametersStore) PutParameters(_ context.Context, in store.SalesParameters, version int, actor string) (store.SalesParameters, error) {
	s.puts++
	s.saved, s.version, s.actor = in, version, actor
	in.Version = version + 1
	return in, nil
}

const validParameters = `{"quotationExpiryDays":45,"quotationProbabilityDefault":40,"creditReleaseValidityDays":10,"creditHoldSlaBusinessDays":3,"defaultOrderType":"SUBSCRIPTION"}`

func TestParameters_GetReturnsDefaultsAtVersionZero(t *testing.T) {
	rec := serve(t, &parametersStore{}, http.MethodGet, parameters+"?legalEntityId="+testLE, principal(dutyRead), nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"0"` {
		t.Fatalf("status %d etag %s: %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	for _, want := range []string{`"quotationExpiryDays":30`, `"creditReleaseValidityDays":7`, `"creditHoldSlaBusinessDays":2`, `"quotationProbabilityDefault":50`, `"defaultOrderType":"ORDER"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, rec.Body.String())
		}
	}
}

func TestParameters_PutDutyVersionAndValidation(t *testing.T) {
	path := parameters + "?legalEntityId=" + testLE
	s := &parametersStore{}
	if rec := serveBody(t, s, http.MethodPut, path, validParameters, principal(dutyRead), map[string]string{"If-Match": `"0"`}); rec.Code != http.StatusForbidden || s.puts != 0 {
		t.Fatalf("without the maintain duty: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPut, path, validParameters, principal(dutyParameter), nil); rec.Code != http.StatusPreconditionRequired || s.puts != 0 {
		t.Fatalf("without If-Match: %d", rec.Code)
	}
	for name, tc := range map[string]struct{ body, field string }{
		"expiry zero":              {`{"quotationExpiryDays":0,"quotationProbabilityDefault":50,"creditReleaseValidityDays":10,"creditHoldSlaBusinessDays":3,"defaultOrderType":"ORDER"}`, "quotationExpiryDays"},
		"probability missing":      {`{"quotationExpiryDays":30,"creditReleaseValidityDays":10,"creditHoldSlaBusinessDays":3,"defaultOrderType":"ORDER"}`, "quotationProbabilityDefault"},
		"release validity over 30": {`{"quotationExpiryDays":30,"quotationProbabilityDefault":50,"creditReleaseValidityDays":31,"creditHoldSlaBusinessDays":3,"defaultOrderType":"ORDER"}`, "creditReleaseValidityDays"},
		"returned default":         {`{"quotationExpiryDays":30,"quotationProbabilityDefault":50,"creditReleaseValidityDays":7,"creditHoldSlaBusinessDays":2,"defaultOrderType":"RETURNED"}`, "defaultOrderType"},
	} {
		rec := serveBody(t, s, http.MethodPut, path, tc.body, principal(dutyParameter), map[string]string{"If-Match": `"0"`})
		if rec.Code != http.StatusBadRequest || s.puts != 0 || !strings.Contains(rec.Body.String(), tc.field) {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := serveBody(t, s, http.MethodPut, path, validParameters, principal(dutyParameter), map[string]string{"If-Match": `"4"`})
	if rec.Code != http.StatusOK || s.version != 4 || s.actor != "u1" || s.saved.LegalEntityID != testLE || s.saved.QuotationExpiryDays != 45 || s.saved.QuotationProbabilityDefault != 40 {
		t.Fatalf("put: %d version %d actor %q saved %+v", rec.Code, s.version, s.actor, s.saved)
	}
	if rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("etag %s", rec.Header().Get("ETag"))
	}
}

func TestParameters_OneTimeLimit(t *testing.T) {
	path := parameters + "?legalEntityId=" + testLE
	s := &parametersStore{}
	withLimit := func(extra string) string { return validParameters[:len(validParameters)-1] + "," + extra + "}" }
	for name, tc := range map[string]struct{ body, field string }{
		"amount without currency": {withLimit(`"oneTimeOrderLimit":"2500.00"`), "oneTimeOrderLimit"},
		"currency without amount": {withLimit(`"oneTimeOrderLimitCurrency":"USD"`), "oneTimeOrderLimit"},
		"not a decimal":           {withLimit(`"oneTimeOrderLimit":"lots","oneTimeOrderLimitCurrency":"USD"`), "oneTimeOrderLimit"},
		"negative":                {withLimit(`"oneTimeOrderLimit":"-1","oneTimeOrderLimitCurrency":"USD"`), "oneTimeOrderLimit"},
		"bad currency":            {withLimit(`"oneTimeOrderLimit":"10","oneTimeOrderLimitCurrency":"usd"`), "oneTimeOrderLimitCurrency"},
	} {
		rec := serveBody(t, s, http.MethodPut, path, tc.body, principal(dutyParameter), map[string]string{"If-Match": `"0"`})
		if rec.Code != http.StatusBadRequest || s.puts != 0 || !strings.Contains(rec.Body.String(), tc.field) {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := serveBody(t, s, http.MethodPut, path, withLimit(`"oneTimeOrderLimit":"2500.00","oneTimeOrderLimitCurrency":"GBP"`), principal(dutyParameter), map[string]string{"If-Match": `"0"`})
	if rec.Code != http.StatusOK || s.saved.OneTimeOrderLimit == nil || s.saved.OneTimeOrderLimit.String() != "2500" || *s.saved.OneTimeOrderLimitCurrency != "GBP" ||
		!strings.Contains(rec.Body.String(), `"oneTimeOrderLimit":"2500"`) {
		t.Fatalf("put with a limit: %d %s saved %+v", rec.Code, rec.Body.String(), s.saved)
	}
}

func TestParameters_RequiresLegalEntityInScope(t *testing.T) {
	if rec := serve(t, &parametersStore{}, http.MethodGet, parameters, allScopePrincipal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no legal entity: %d", rec.Code)
	}
	other := "00000000-0000-0000-0000-0000000000c2"
	s := &parametersStore{}
	if rec := serveBody(t, s, http.MethodPut, parameters+"?legalEntityId="+other, validParameters, principal(dutyParameter),
		map[string]string{"If-Match": `"0"`}); rec.Code != http.StatusForbidden || s.puts != 0 {
		t.Fatalf("a legal entity outside the caller's scope: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodGet, parameters+"?legalEntityId="+other, principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("reading outside the scope: %d", rec.Code)
	}
}

func TestParameters_CreditCheckPoints(t *testing.T) {
	path := parameters + "?legalEntityId=" + testLE
	withPoints := func(points string) string {
		return validParameters[:len(validParameters)-1] + `,"creditCheckPoints":` + points + "}"
	}
	s := &parametersStore{}
	for name, body := range map[string]string{"unknown": withPoints(`["ship"]`), "empty": withPoints(`[]`), "repeated": withPoints(`["entry","entry"]`),
		"no blocking point": withPoints(`["entry","release"]`), "packing slip not active": withPoints(`["confirm","packing_slip"]`)} {
		rec := serveBody(t, s, http.MethodPut, path, body, principal(dutyParameter), map[string]string{"If-Match": `"0"`})
		if rec.Code != http.StatusBadRequest || s.puts != 0 || !strings.Contains(rec.Body.String(), "creditCheckPoints") {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := serveBody(t, s, http.MethodPut, path, withPoints(`["entry","confirm"]`), principal(dutyParameter), map[string]string{"If-Match": `"0"`})
	if rec.Code != http.StatusOK || len(s.saved.CreditCheckPoints) != 2 || s.saved.CreditCheckPoints[0] != "entry" {
		t.Fatalf("put: %d %s %+v", rec.Code, rec.Body.String(), s.saved)
	}
	// Absent, the default applies.
	if rec := serveBody(t, s, http.MethodPut, path, validParameters, principal(dutyParameter), map[string]string{"If-Match": `"0"`}); rec.Code != http.StatusOK ||
		len(s.saved.CreditCheckPoints) != 2 || s.saved.CreditCheckPoints[0] != "confirm" {
		t.Fatalf("default: %d %+v", rec.Code, s.saved)
	}
}

func TestParameters_PartialDeliveryDefault(t *testing.T) {
	path := parameters + "?legalEntityId=" + testLE
	match := map[string]string{"If-Match": `"0"`}
	s := &parametersStore{}
	// Absent reaches the store empty, which keeps the saved policy (ALLOWED on create).
	if rec := serveBody(t, s, http.MethodPut, path, validParameters, principal(dutyParameter), match); rec.Code != http.StatusOK || s.saved.PartialDeliveryDefault != "" {
		t.Fatalf("absent keeps the saved value: %d %+v", rec.Code, s.saved)
	}
	body := strings.Replace(validParameters, `"defaultOrderType"`, `"partialDeliveryDefault":"COMPLETE_ONLY","defaultOrderType"`, 1)
	if rec := serveBody(t, s, http.MethodPut, path, body, principal(dutyParameter), match); rec.Code != http.StatusOK || s.saved.PartialDeliveryDefault != domain.PartialCompleteOnly ||
		!strings.Contains(rec.Body.String(), `"partialDeliveryDefault":"COMPLETE_ONLY"`) {
		t.Fatalf("COMPLETE_ONLY: %d %s", rec.Code, rec.Body)
	}
	puts := s.puts
	bad := strings.Replace(validParameters, `"defaultOrderType"`, `"partialDeliveryDefault":"SOMETIMES","defaultOrderType"`, 1)
	if rec := serveBody(t, s, http.MethodPut, path, bad, principal(dutyParameter), match); rec.Code != http.StatusBadRequest || s.puts != puts || !strings.Contains(rec.Body.String(), "partialDeliveryDefault") {
		t.Fatalf("unknown policy: %d %s", rec.Code, rec.Body)
	}
	empty := strings.Replace(validParameters, `"defaultOrderType"`, `"partialDeliveryDefault":"","defaultOrderType"`, 1)
	if rec := serveBody(t, s, http.MethodPut, path, empty, principal(dutyParameter), match); rec.Code != http.StatusBadRequest || s.puts != puts || !strings.Contains(rec.Body.String(), "partialDeliveryDefault") {
		t.Fatalf("an empty policy is refused, not read as absent: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, &parametersStore{}, http.MethodGet, path, principal(dutyRead), nil); !strings.Contains(rec.Body.String(), `"partialDeliveryDefault":"ALLOWED"`) {
		t.Fatalf("default read: %s", rec.Body)
	}
}
