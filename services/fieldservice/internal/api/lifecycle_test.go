package api

import (
	"net/http"
	"testing"
)

// FR-45.11: the KPI window is required, ordered, at most 366 days, and the
// territory must be a UUID.
func TestKPIs_ValidatesWindowAndTerritory(t *testing.T) {
	h := newTestRouter()
	tests := []struct {
		name   string
		query  string
		fields []string
	}{
		{"no window", "", []string{"from", "to"}},
		{"not RFC 3339", "?from=2026-10-01&to=2026-11-01", []string{"from", "to"}},
		{"to before from", "?from=2026-10-02T00:00:00Z&to=2026-10-01T00:00:00Z", []string{"to"}},
		{"empty window", "?from=2026-10-02T00:00:00Z&to=2026-10-02T00:00:00Z", []string{"to"}},
		{"window over 366 days", "?from=2024-01-01T00:00:00Z&to=2026-01-01T00:00:00Z", []string{"to"}},
		{"territory not a uuid", "?from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:00Z&territory=nope", []string{"territory"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, "/v1/service/kpis"+tc.query, "", nil, dutyInquire)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			_, fields := problemOf(t, rec)
			for _, f := range tc.fields {
				if fields[f] == "" {
					t.Errorf("errors %v lack %s", fields, f)
				}
			}
		})
	}
}

func TestSLAScan_BoundsTheBatch(t *testing.T) {
	h := newTestRouter()
	for _, limit := range []string{"0", "5001", "x"} {
		rec := do(t, h, http.MethodPost, "/v1/service/sla:scan?limit="+limit, "", nil, dutyCaseMaintain)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("limit=%s: status %d: %s", limit, rec.Code, rec.Body.String())
		}
		if _, fields := problemOf(t, rec); fields["limit"] == "" {
			t.Fatalf("limit=%s: errors %v lack limit", limit, fields)
		}
	}
}

// FR-45.5: a case transition needs If-Match (428 when absent) and may only
// target the statuses a caller can request by hand.
func TestTransitionCase_RequiresIfMatchAndKnownStatus(t *testing.T) {
	h := newTestRouter()
	path := "/v1/service/cases/" + uuid1 + ":transition"
	rec := do(t, h, http.MethodPost, path, `{"to":"RESOLVED"}`, nil, dutyCaseMaintain)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("transition without If-Match: status %d", rec.Code)
	}
	for _, body := range []string{`{"to":"NEW"}`, `{"to":"PAUSED"}`, `{}`} {
		rec = do(t, h, http.MethodPost, path, body, map[string]string{"If-Match": `"1"`}, dutyCaseMaintain)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d: %s", body, rec.Code, rec.Body.String())
		}
		if _, fields := problemOf(t, rec); fields["to"] == "" {
			t.Fatalf("%s: errors %v lack to", body, fields)
		}
	}
	rec = do(t, h, http.MethodPost, "/v1/service/cases/nope:transition", `{"to":"RESOLVED"}`, map[string]string{"If-Match": "1"}, dutyCaseMaintain)
	if code, _ := problemOf(t, rec); rec.Code != http.StatusNotFound || code != "SVC-CASE-001" {
		t.Fatalf("malformed id: status %d code %s, want 404 SVC-CASE-001", rec.Code, code)
	}
}

// BR-45.5: a follow-up is a create, so it needs an Idempotency-Key as well as If-Match.
func TestFollowUp_RequiresIfMatchAndIdempotencyKey(t *testing.T) {
	h := newTestRouter()
	path := "/v1/service/work-orders/" + uuid1 + ":follow-up"
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "k"}, dutyWorkOrder); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("follow-up without If-Match: status %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, "", map[string]string{"If-Match": "1"}, dutyWorkOrder)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("follow-up without key: status %d: %s", rec.Code, rec.Body.String())
	}
	if _, fields := problemOf(t, rec); fields["Idempotency-Key"] == "" {
		t.Fatalf("errors %v lack Idempotency-Key", fields)
	}
}

// PRD 8.45.17: transitions and the monitor need SvcCaseMaintain, follow-ups
// SvcWoMaintain and KPIs SvcInquire; reading duties are not enough.
func TestLifecycleRoutes_EnforceDuties(t *testing.T) {
	h := newTestRouter()
	for _, tc := range []struct{ method, path, duty string }{
		{http.MethodPost, "/v1/service/cases/" + uuid1 + ":transition", dutyInquire},
		{http.MethodPost, "/v1/service/sla:scan", dutyInquire},
		{http.MethodPost, "/v1/service/work-orders/" + uuid1 + ":follow-up", dutyInquire},
		{http.MethodGet, "/v1/service/kpis?from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:00Z", dutyCaseCreate},
	} {
		if rec := do(t, h, tc.method, tc.path, `{"to":"RESOLVED"}`, map[string]string{"If-Match": "1", "Idempotency-Key": "k"}, tc.duty); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with only %s: status %d, want 403", tc.method, tc.path, tc.duty, rec.Code)
		}
	}
}
