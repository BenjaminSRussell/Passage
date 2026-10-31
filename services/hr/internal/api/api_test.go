package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

// serve runs one request through the HR routes with a principal carrying the
// given tenant and duties. It uses a bare chi router so the kernel's header
// middleware cannot replace the principal. The API has no store here, so these
// tests only reach paths that answer before any database call.
func serve(t *testing.T, tenantID, method, target, body string, duties ...string) *httptest.ResponseRecorder {
	t.Helper()
	p, _ := tenant.FromContext(testkit.Ctx(tenantID, duties...))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	New(nil).Register(r)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func tid() string { return testkit.TenantID(1) }

func TestCreateWorkerRequiresDuty(t *testing.T) {
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/workers", `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestCreateWorkerRejectsUnknownFieldsAndBadValues(t *testing.T) {
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/workers", `{"unexpected":true}`, dutyWorkerMaintain)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: status = %d, want 400", rec.Code)
	}

	body := `{"personnelNumber":"bad number!","workerType":"ALIEN","partyPersonId":"nope","legalName":"","nationalId":""}`
	rec = serve(t, tid(), http.MethodPost, "/v1/hr/workers", body, dutyWorkerMaintain)
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("invalid worker: status = %d, want 4xx", rec.Code)
	}
	for _, field := range []string{"personnelNumber", "workerType", "partyPersonId", "legalName", "nationalId"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("response does not name field %q: %s", field, rec.Body.String())
		}
	}
}

// TestRevealPIIRequiresDutyAndReason is AC-42.7's gate: the unmasked read needs
// HcmPIIRead, and the reason must be at least ten characters (HCM-PII-001).
func TestRevealPIIRequiresDutyAndReason(t *testing.T) {
	target := "/v1/hr/workers/" + testkit.TenantID(9) + "/pii?reason=" + strings.Repeat("x", 12)
	rec := serve(t, tid(), http.MethodGet, target, "", dutyWorkerInquire)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("without HcmPIIRead: status = %d, want 403", rec.Code)
	}

	for _, reason := range []string{"", "short", "   nine chr"} {
		target := "/v1/hr/workers/" + testkit.TenantID(9) + "/pii?reason=" + strings.ReplaceAll(reason, " ", "%20")
		rec := serve(t, tid(), http.MethodGet, target, "", dutyPIIRead)
		if rec.Code < 400 || !strings.Contains(rec.Body.String(), "HCM-PII-001") {
			t.Fatalf("reason %q: status %d body %s; want HCM-PII-001", reason, rec.Code, rec.Body.String())
		}
	}
}

func TestAsOfRejectsMissingOrBadDate(t *testing.T) {
	for _, target := range []string{
		"/v1/hr/workers/" + testkit.TenantID(9) + "/as-of",
		"/v1/hr/workers/" + testkit.TenantID(9) + "/as-of?date=2026-02-30",
		"/v1/hr/workers/" + testkit.TenantID(9) + "/as-of?date=tomorrow",
	} {
		rec := serve(t, tid(), http.MethodGet, target, "", dutyWorkerInquire)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want a validation error", target, rec.Code)
		}
	}
}

func TestMalformedIDsAreNotFoundAndTenantIsRequired(t *testing.T) {
	rec := serve(t, tid(), http.MethodGet, "/v1/hr/workers/not-a-uuid", "", dutyWorkerInquire)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("malformed worker id: status = %d, want 404", rec.Code)
	}
	rec = serve(t, "", http.MethodGet, "/v1/hr/workers/"+testkit.TenantID(9), "", dutyWorkerInquire)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing tenant: status = %d, want 401", rec.Code)
	}
}

// TestCreatePositionValidatesFTE is FR-42.3: FTE is a decimal string between
// 0.05 and 2.00 with at most two decimals; floats and out-of-range values fail.
func TestCreatePositionValidatesFTE(t *testing.T) {
	base := `{"positionNumber":"POS-1","jobCode":"MGR","departmentId":"` + testkit.TenantID(2) +
		`","legalEntityId":"` + testkit.TenantID(3) + `","validFrom":"2026-01-01","fte":"%s"}`
	for _, fte := range []string{"2.50", "0.04", "0.333", "abc"} {
		rec := serve(t, tid(), http.MethodPost, "/v1/hr/positions", strings.Replace(base, "%s", fte, 1), dutyOrgMaintain)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("fte %s: status = %d, want a validation error", fte, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "fte") {
			t.Errorf("fte %s: response does not name the field: %s", fte, rec.Body.String())
		}
	}
}

func TestMetricsExposeCountersWithoutStore(t *testing.T) {
	rec := httptest.NewRecorder()
	New(nil).Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, name := range []string{"hr_pii_reveals_total", "hr_as_of_queries_total", "hr_workers_created_total"} {
		if !strings.Contains(rec.Body.String(), name) {
			t.Errorf("metrics missing %s", name)
		}
	}
}

func TestMetricsCountLegacyAndFailedPIIOpens(t *testing.T) {
	keys, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := domain.NewSealer(keys, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sealer.Open(tid(), "worker_pii.national_id", []byte("not sealed")); err == nil {
		t.Fatal("garbage must not open")
	}
	rec := httptest.NewRecorder()
	New(store.New(nil, sealer, 10)).Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range []string{"hr_pii_legacy_opens_total 0\n", "hr_pii_open_failures_total 1\n"} {
		if !strings.Contains(rec.Body.String(), line) {
			t.Errorf("metrics missing %q:\n%s", line, rec.Body.String())
		}
	}
}

func TestListRoutesEnforceDutiesAndPageParams(t *testing.T) {
	for _, c := range []struct {
		target, duty string
	}{
		{"/v1/hr/workers", dutyWorkerInquire},
		{"/v1/hr/positions", dutyOrgInquire},
		{"/v1/hr/positions/" + testkit.TenantID(9), dutyOrgInquire},
		{"/v1/hr/workers/" + testkit.TenantID(9) + "/employments", dutyWorkerInquire},
	} {
		if rec := serve(t, tid(), http.MethodGet, c.target, "", dutyWorkerMaintain); rec.Code != http.StatusForbidden {
			t.Errorf("%s without %s: status = %d, want 403", c.target, c.duty, rec.Code)
		}
	}
	for _, target := range []string{"/v1/hr/workers?limit=0", "/v1/hr/workers?limit=101", "/v1/hr/positions?cursor=%25%25", "/v1/hr/workers?q=" + strings.Repeat("a", 101)} {
		rec := serve(t, tid(), http.MethodGet, target, "", dutyWorkerInquire, dutyOrgInquire)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Errorf("%s: status = %d body %s, want 400 COM-0001", target, rec.Code, rec.Body.String())
		}
	}
	if rec := serve(t, tid(), http.MethodGet, "/v1/hr/positions/nope", "", dutyOrgInquire); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "HCM-POS-NOT-FOUND") {
		t.Errorf("malformed position id: status = %d body %s", rec.Code, rec.Body.String())
	}
}

// TestCreateEmploymentDateRules covers 8.42.5 validation: a termination date
// before the hire date is HCM-EMP-002, and a GLOBAL_HOST needs a different
// home entity and a planned end not before the hire date (BR-42.14).
func TestCreateEmploymentDateRules(t *testing.T) {
	target := "/v1/hr/workers/" + testkit.TenantID(9) + "/employments"
	le := testkit.TenantID(3)
	rec := serve(t, tid(), http.MethodPost, target,
		`{"legalEntityId":"`+le+`","employmentType":"FULL_TIME","hireDate":"2026-03-16","terminationDate":"2026-03-15"}`, dutyWorkerMaintain)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "HCM-EMP-002") {
		t.Fatalf("termination before hire: status = %d body %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, tid(), http.MethodPost, target,
		`{"legalEntityId":"`+le+`","employmentType":"FULL_TIME","assignmentKind":"GLOBAL_HOST","homeLegalEntityId":"`+le+`","hireDate":"2026-03-16"}`, dutyWorkerMaintain)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "homeLegalEntityId") || !strings.Contains(rec.Body.String(), "plannedEndDate") {
		t.Fatalf("global host without home entity and end: status = %d body %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, tid(), http.MethodPost, target,
		`{"legalEntityId":"`+le+`","employmentType":"FULL_TIME","hireDate":"2026-03-16","plannedEndDate":"2026-01-01"}`, dutyWorkerMaintain)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "plannedEndDate") {
		t.Fatalf("planned end before hire: status = %d body %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, tid(), http.MethodPost, target, `{"legalEntityId":"`+le+`","employmentType":"FULL_TIME","hireDate":"2026-03-16"}`, dutyWorkerInquire)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("without HcmWorkerMaintain: status = %d, want 403", rec.Code)
	}
}

func TestCreateAssignmentValidatesShareAndDates(t *testing.T) {
	target := "/v1/hr/positions/" + testkit.TenantID(9) + "/assignments"
	body := `{"workerId":"` + testkit.TenantID(4) + `","employmentId":"` + testkit.TenantID(5) + `","fteShare":"0.00","validFrom":"2026-02-01","validTo":"2026-02-01"}`
	rec := serve(t, tid(), http.MethodPost, target, body, dutyActionMaintain)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "fteShare") || !strings.Contains(rec.Body.String(), "validTo") {
		t.Fatalf("zero share and empty range: status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, tid(), http.MethodPost, target, body, dutyOrgMaintain); rec.Code != http.StatusForbidden {
		t.Fatalf("without HcmActionMaintain: status = %d, want 403", rec.Code)
	}
}

func TestRevealReasonIsBounded(t *testing.T) {
	target := "/v1/hr/workers/" + testkit.TenantID(9) + "/pii?reason=" + strings.Repeat("x", 501)
	rec := serve(t, tid(), http.MethodGet, target, "", dutyPIIRead)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reason") {
		t.Fatalf("501-character reason: status = %d body %s, want 400 naming reason", rec.Code, rec.Body.String())
	}
}
