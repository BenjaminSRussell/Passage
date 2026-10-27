package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

func riskOf(t *testing.T, body []byte) domain.RiskIssue {
	t.Helper()
	var r domain.RiskIssue
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode risk: %v (%s)", err, body)
	}
	return r
}

func TestRisks_CreateListScoreAndFilters(t *testing.T) {
	f := newFake()
	h := router(f)
	seedProject(t, f, "P-RISK")
	path := "/v1/projects/P-RISK/risks"
	low := `{"kind":"RISK","title":"Late steel delivery","probability":2,"impact":3,"mitigation":"Second supplier","dueDate":"2026-12-01"}`
	high := `{"kind":"ISSUE","title":"Crane unavailable","probability":5,"impact":4}`

	if rec := as(t, h, []string{DutyPlan}, http.MethodPost, path, low, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyInquire}, http.MethodPost, path, low, key("r0")); rec.Code != http.StatusForbidden {
		t.Fatalf("create without ProjPlan: %d", rec.Code)
	}
	bad := `{"kind":"RUMOUR","title":"","probability":0,"impact":6,"dueDate":"01/12/2026","ownerWorkerId":"bob","status":"CLOSED"}`
	rec := as(t, h, []string{DutyPlan}, http.MethodPost, path, bad, key("r1"))
	errs := fieldErrors(t, rec)
	for _, field := range []string{"kind", "title", "probability", "impact", "dueDate", "ownerWorkerId", "status"} {
		if errs[field] == "" {
			t.Errorf("expected a field error on %s, got %v", field, errs)
		}
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPost, "/v1/projects/NOPE/risks", low, key("r2")); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", rec.Code)
	}

	rec = as(t, h, []string{DutyPlan}, http.MethodPost, path, low, key("r3"))
	first := riskOf(t, rec.Body.Bytes())
	if rec.Code != http.StatusCreated || first.Status != domain.RiskOpen || first.Score != 6 || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPost, path, low, key("r3")); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPost, path, high, key("r3")); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != store.CodeIdempotencyReuse {
		t.Fatalf("key reused: %d", rec.Code)
	}
	as(t, h, []string{DutyPlan}, http.MethodPost, path, high, key("r4"))

	rec = as(t, h, []string{DutyInquire}, http.MethodGet, path, "", nil)
	var list riskList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 2 || list.Items[0].Score != 20 || list.Items[1].ID != first.ID {
		t.Fatalf("list is not highest score first: %v %s", err, rec.Body.String())
	}
	rec = as(t, h, []string{DutyInquire}, http.MethodGet, path+"?kind=RISK", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].Kind != domain.KindRisk {
		t.Fatalf("kind filter: %s", rec.Body.String())
	}
	if rec := as(t, h, []string{DutyInquire}, http.MethodGet, path+"?status=DONE", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter: %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyInquire}, http.MethodGet, "/v1/projects/NOPE/risks", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("register of unknown project: %d", rec.Code)
	}
}

func TestRisks_UpdateFollowsLifecycleAndVersion(t *testing.T) {
	f := newFake()
	h := router(f)
	seedProject(t, f, "P-RUPD")
	r := riskOf(t, as(t, h, []string{DutyPlan}, http.MethodPost, "/v1/projects/P-RUPD/risks",
		`{"kind":"RISK","title":"Permit delay","probability":3,"impact":3}`, key("u1")).Body.Bytes())
	path := "/v1/projects/P-RUPD/risks/" + r.ID
	mitigated := `{"kind":"RISK","title":"Permit delay","probability":2,"impact":3,"mitigation":"Pre-filed","status":"MITIGATED"}`

	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, path, mitigated, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, path, mitigated, etag(5)); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, path, `{"kind":"RISK","title":"Permit delay","probability":3,"impact":3}`, etag(1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("status missing on update: %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, "/v1/projects/P-RUPD/risks/x", mitigated, etag(1)); rec.Code != http.StatusNotFound {
		t.Fatalf("bad risk id: %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, "/v1/projects/OTHER/risks/"+r.ID, mitigated, etag(1)); rec.Code != http.StatusNotFound {
		t.Fatalf("risk under another project: %d", rec.Code)
	}
	rec := as(t, h, []string{DutyPlan}, http.MethodPut, path, mitigated, etag(1))
	if out := riskOf(t, rec.Body.Bytes()); rec.Code != http.StatusOK || out.Status != domain.RiskMitigated || out.Score != 6 || out.Version != 2 {
		t.Fatalf("mitigate: %d %s", rec.Code, rec.Body.String())
	}
	closed := `{"kind":"RISK","title":"Permit delay","probability":2,"impact":3,"status":"CLOSED"}`
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, path, closed, etag(2)); rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}
	reopen := `{"kind":"RISK","title":"Permit delay","probability":2,"impact":3,"status":"OPEN"}`
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, path, reopen, etag(3)); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeRiskStatus {
		t.Fatalf("reopen a CLOSED entry: %d %s", rec.Code, rec.Body.String())
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, path, closed, etag(3)); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeRiskStatus {
		t.Fatalf("edit a CLOSED entry: %d", rec.Code)
	}
}
