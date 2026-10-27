package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// newTestRouter registers the routes over a nil store: every case below is
// rejected before the store is reached, so no database is needed.
func newTestRouter() http.Handler {
	r := chi.NewRouter()
	New(nil).Register(r)
	return r
}

func do(t *testing.T, h http.Handler, method, path, body string, headers map[string]string, duties ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(testkit.Ctx(testkit.TenantID(1), duties...))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) (code string, fields map[string]string) {
	t.Helper()
	var p struct {
		Code   string            `json:"code"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not a problem: %s", rec.Body.String())
	}
	return p.Code, p.Errors
}

const uuid1 = "0195f7a0-0000-7000-8000-000000000001"

func TestCreateCase_ValidationAndKey(t *testing.T) {
	h := newTestRouter()
	valid := `{"customerPartyId":"` + uuid1 + `","legalEntityId":"` + uuid1 + `","channel":"PHONE","priority":2,"description":"pump noise"}`
	tests := []struct {
		name     string
		body     string
		headers  map[string]string
		duties   []string
		status   int
		wantKey  string
		wantFail string
	}{
		{"missing idempotency key", valid, nil, []string{"SvcCaseCreate"}, http.StatusBadRequest, "", "Idempotency-Key"},
		{"bad channel", `{"customerPartyId":"` + uuid1 + `","legalEntityId":"` + uuid1 + `","channel":"FAX","priority":2}`,
			map[string]string{"Idempotency-Key": "k1"}, []string{"SvcCaseCreate"}, http.StatusBadRequest, "COM-0001", "channel"},
		{"priority out of range", `{"customerPartyId":"` + uuid1 + `","legalEntityId":"` + uuid1 + `","channel":"PHONE","priority":5}`,
			map[string]string{"Idempotency-Key": "k1"}, []string{"SvcCaseCreate"}, http.StatusBadRequest, "COM-0001", "priority"},
		{"customer not a uuid", `{"customerPartyId":"nope","legalEntityId":"` + uuid1 + `","channel":"PHONE","priority":1}`,
			map[string]string{"Idempotency-Key": "k1"}, []string{"SvcCaseCreate"}, http.StatusBadRequest, "COM-0001", "customerPartyId"},
		{"duty missing", valid, map[string]string{"Idempotency-Key": "k1"}, nil, http.StatusForbidden, "AUTH-0003", ""},
		{"unknown field rejected", `{"customerPartyId":"` + uuid1 + `","legalEntityId":"` + uuid1 + `","channel":"PHONE","priority":1,"x":1}`,
			map[string]string{"Idempotency-Key": "k1"}, []string{"SvcCaseCreate"}, http.StatusBadRequest, "COM-0001", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/v1/service/cases", tc.body, tc.headers, tc.duties...)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			code, fields := problemOf(t, rec)
			if tc.wantKey != "" && code != tc.wantKey {
				t.Errorf("code %s, want %s", code, tc.wantKey)
			}
			if tc.wantFail != "" {
				if _, ok := fields[tc.wantFail]; !ok {
					t.Errorf("errors %v lack %q", fields, tc.wantFail)
				}
			}
		})
	}
}

func TestCreateAgreement_RejectsObjectAndCategoryTogether(t *testing.T) {
	h := newTestRouter()
	body := `{"customerPartyId":"` + uuid1 + `","legalEntityId":"` + uuid1 + `","validFrom":"2026-01-01","slaProfileId":"` + uuid1 +
		`","billingModel":"PREPAID","priorityRank":1,"lines":[{"serviceObjectId":"` + uuid1 + `","categoryId":"` + uuid1 + `"}]}`
	rec := do(t, h, http.MethodPost, "/v1/service/agreements", body, map[string]string{"Idempotency-Key": "a1"}, "SvcAgreementMaintain")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, fields := problemOf(t, rec); fields["lines[0]"] == "" {
		t.Fatalf("errors %v lack lines[0]", fields)
	}
}

func TestCreateAgreement_ValidityOrderAndBillingModel(t *testing.T) {
	h := newTestRouter()
	body := `{"customerPartyId":"` + uuid1 + `","legalEntityId":"` + uuid1 + `","validFrom":"2026-06-01","validTo":"2026-01-01","slaProfileId":"` + uuid1 +
		`","billingModel":"FREE","priorityRank":1,"lines":[{"partsCovered":true}]}`
	rec := do(t, h, http.MethodPost, "/v1/service/agreements", body, map[string]string{"Idempotency-Key": "a2"}, "SvcAgreementMaintain")
	_, fields := problemOf(t, rec)
	for _, f := range []string{"validTo", "billingModel"} {
		if fields[f] == "" {
			t.Errorf("errors %v lack %s", fields, f)
		}
	}
}

func TestWorkOrderActions_RequireIfMatch(t *testing.T) {
	h := newTestRouter()
	path := "/v1/service/work-orders/" + uuid1
	rec := do(t, h, http.MethodPost, path+":post", "", map[string]string{"Idempotency-Key": "p1"}, "SvcBillingPost")
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("post without If-Match: status %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, path+":transition", `{"to":"COMPLETED"}`, nil, "SvcWoMaintain")
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("transition without If-Match: status %d", rec.Code)
	}
}

func TestWorkOrderActions_RequireIdempotencyKeyOnPost(t *testing.T) {
	h := newTestRouter()
	rec := do(t, h, http.MethodPost, "/v1/service/work-orders/"+uuid1+":post", "", map[string]string{"If-Match": "1"}, "SvcBillingPost")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("post without key: status %d", rec.Code)
	}
}

func TestTransition_RejectsStatusesNotRequestedByHand(t *testing.T) {
	h := newTestRouter()
	rec := do(t, h, http.MethodPost, "/v1/service/work-orders/"+uuid1+":transition", `{"to":"POSTED"}`,
		map[string]string{"If-Match": "2"}, "SvcWoMaintain")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateWorkOrder_ValidatesLinesAndCurrency(t *testing.T) {
	h := newTestRouter()
	body := `{"customerPartyId":"` + uuid1 + `","legalEntityId":"` + uuid1 + `","territoryId":"` + uuid1 +
		`","estDurationMinutes":60,"currency":"us","lines":[{"kind":"PART","qty":"0","unitPrice":"1"}]}`
	rec := do(t, h, http.MethodPost, "/v1/service/work-orders", body, map[string]string{"Idempotency-Key": "w1"}, "SvcWoMaintain")
	_, fields := problemOf(t, rec)
	if fields["currency"] == "" || fields["lines[0]"] == "" {
		t.Fatalf("errors %v lack currency and lines[0]", fields)
	}
}

func TestCreateSLAProfile_ValidatesTargetsAndHours(t *testing.T) {
	h := newTestRouter()
	body := `{"code":"GOLD","name":"Gold","timeZone":"America/Chicago","openMinute":1080,"closeMinute":480,` +
		`"targets":[{"priority":1,"responseMinutes":60,"resolutionMinutes":30}]}`
	rec := do(t, h, http.MethodPost, "/v1/service/sla-profiles", body, map[string]string{"Idempotency-Key": "s1"}, "SvcAgreementMaintain")
	_, fields := problemOf(t, rec)
	if fields["openMinute"] == "" || fields["targets"] == "" {
		t.Fatalf("errors %v lack openMinute and targets", fields)
	}
}

func TestCreateSLAProfile_RejectsMalformedHolidays(t *testing.T) {
	h := newTestRouter()
	body := `{"code":"GOLD","name":"Gold","timeZone":"America/Chicago","openMinute":480,"closeMinute":1080,"holidays":["2026-12-25","25/12/2026"],` +
		`"targets":[{"priority":1,"responseMinutes":60,"resolutionMinutes":480}]}`
	rec := do(t, h, http.MethodPost, "/v1/service/sla-profiles", body, map[string]string{"Idempotency-Key": "s2"}, "SvcAgreementMaintain")
	if _, fields := problemOf(t, rec); rec.Code != http.StatusBadRequest || fields["holidays"] == "" {
		t.Fatalf("status %d errors %v, want holidays rejected", rec.Code, fields)
	}
}

func TestLists_RequireInquireDuty(t *testing.T) {
	h := newTestRouter()
	for _, path := range []string{"/v1/service/sla-profiles", "/v1/service/agreements", "/v1/service/cases", "/v1/service/work-orders"} {
		if rec := do(t, h, http.MethodGet, path, "", nil, "SvcCaseCreate"); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without SvcInquire: status %d", path, rec.Code)
		}
	}
}
