package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

var (
	uxRecorder = map[string]string{tenant.HeaderDuties: DutyUXRecord}
	uxViewer   = map[string]string{tenant.HeaderDuties: DutyUXView}
)

const stepEvent = `{"sessionId":"sess-1234abcd","release":"2026.11.1","type":"funnel_step","flow":"procure_to_pay","step":"%s"}`

func TestUX_DutiesGuardEveryRoute(t *testing.T) {
	h := newTestServer()
	body := `{"events":[` + strings.Replace(stepEvent, "%s", "requisition_created", 1) + `]}`
	for _, tc := range []struct {
		method, path, body string
		duties             map[string]string
	}{
		{http.MethodPost, "/v1/analytics/ux-events", body, nil},
		{http.MethodPost, "/v1/analytics/ux-events", body, uxViewer},
		{http.MethodPost, "/v1/analytics/ux-events", body, viewer},
		{http.MethodGet, "/v1/analytics/ux-funnels?flow=onboarding&from=2026-11-01&to=2026-11-10", "", uxRecorder},
		{http.MethodGet, "/v1/analytics/ux-signals?from=2026-11-01&to=2026-11-10", "", viewer},
	} {
		if rec := do(t, h, tc.method, tc.path, tc.body, tc.duties); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with %v: status %d, want 403", tc.method, tc.path, tc.duties, rec.Code)
		}
	}
}

// Bad input is refused before the (nil) store is reached.
func TestUX_ValidationBeforeStore(t *testing.T) {
	h := newTestServer()
	bad := map[string]string{
		"empty batch":  `{"events":[]}`,
		"not json":     `{"events":`,
		"unknown step": `{"events":[{"sessionId":"sess-1234abcd","release":"r1","type":"funnel_step","flow":"onboarding","step":"requisition_created"}]}`,
		"user id":      `{"events":[{"sessionId":"jane.doe@example.com","release":"r1","type":"form_abandoned","route":"/x"}]}`,
	}
	for name, body := range bad {
		rec := do(t, h, http.MethodPost, "/v1/analytics/ux-events", body, uxRecorder)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{
		"/v1/analytics/ux-funnels?flow=payroll&from=2026-11-01&to=2026-11-10",
		"/v1/analytics/ux-funnels?flow=onboarding&from=nope&to=2026-11-10",
		"/v1/analytics/ux-signals?from=2026-11-10&to=2026-11-01",
	} {
		rec := do(t, h, http.MethodGet, path, "", uxViewer)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", path, rec.Code, rec.Body.String())
		}
	}
}

func post(t *testing.T, h http.Handler, events ...string) UXEventsResponse {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/v1/analytics/ux-events", `{"events":[`+strings.Join(events, ",")+`]}`, uxRecorder)
	var out UXEventsResponse
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("post: %d %s", rec.Code, rec.Body.String())
	}
	return out
}

func sessionStep(session, release, step string, at time.Time) string {
	return `{"sessionId":"` + session + `","release":"` + release + `","type":"funnel_step","flow":"procure_to_pay","step":"` + step + `","occurredAt":"` + at.Format(time.RFC3339) + `"}`
}

func TestUX_FunnelCountsSessionsAndMedianTime(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	h := dbServer(t, now)
	steps := domain.Funnels["procure_to_pay"]
	// Session A runs the whole flow in 10 minutes, B in 30, C stops after the second step, A repeats a step.
	var events []string
	for i, s := range steps {
		events = append(events, sessionStep("sess-aaaaaaaa", "2026.11.1", s, now.Add(-time.Hour).Add(time.Duration(i)*2*time.Minute)))
		events = append(events, sessionStep("sess-bbbbbbbb", "2026.11.1", s, now.Add(-2*time.Hour).Add(time.Duration(i)*6*time.Minute)))
	}
	events = append(events, sessionStep("sess-aaaaaaaa", "2026.11.1", steps[0], now.Add(-30*time.Minute)))
	events = append(events, sessionStep("sess-cccccccc", "2026.11.0", steps[0], now.Add(-3*time.Hour)), sessionStep("sess-cccccccc", "2026.11.0", steps[1], now.Add(-3*time.Hour)))
	if got := post(t, h, events...); got.Accepted != len(events) || got.Dropped != 0 {
		t.Fatalf("accepted %+v", got)
	}

	get := func(query string) domain.Funnel {
		rec := do(t, h, http.MethodGet, "/v1/analytics/ux-funnels?flow=procure_to_pay&from=2026-11-10&to=2026-11-10"+query, "", uxViewer)
		var f domain.Funnel
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &f) != nil {
			t.Fatalf("funnel: %d %s", rec.Code, rec.Body.String())
		}
		return f
	}
	f := get("")
	if f.Steps[0].Sessions != 3 || f.Steps[1].Sessions != 3 || f.Steps[2].Sessions != 2 || f.Steps[5].Sessions != 2 {
		t.Fatalf("sessions per step: %+v", f.Steps)
	}
	if f.Steps[2].ConversionFromPrevious < 0.66 || f.Steps[2].ConversionFromPrevious > 0.67 {
		t.Fatalf("conversion %v, want 2/3", f.Steps[2].ConversionFromPrevious)
	}
	// A: first event to last step is 10 minutes (the repeat is later but not the first); B: 30 minutes. Median 20 minutes.
	if f.SecondsToLastStep == nil || *f.SecondsToLastStep != 1200 {
		t.Fatalf("median seconds %v, want 1200", f.SecondsToLastStep)
	}
	if only := get("&release=2026.11.0"); only.Steps[0].Sessions != 1 || only.Steps[2].Sessions != 0 || only.SecondsToLastStep != nil {
		t.Fatalf("release filter: %+v", only)
	}
}

func TestUX_SignalsAndTenantIsolation(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	h := dbServer(t, now)
	val := func(route, field string) string {
		return `{"sessionId":"sess-1234abcd","release":"r1","type":"validation_error","route":"` + route + `","field":"` + field + `"}`
	}
	search := func(q string) string {
		return `{"sessionId":"sess-1234abcd","release":"r1","type":"search_no_results","query":"` + q + `"}`
	}
	aband := `{"sessionId":"sess-1234abcd","release":"r1","type":"form_abandoned","route":"/receiving/receipts/new","durationMs":4200}`
	got := post(t, h, val("/purchasing/requisitions/new", "lines[0].qty"), val("/purchasing/requisitions/new", "lines[0].qty"), val("/ap-invoices/post", "invoiceNumber"),
		search("Hose 2m"), search("hose 2m"), search("ann@example.com"), aband, aband)
	if got.Accepted != 7 || got.Dropped != 1 {
		t.Fatalf("post result %+v, want 7 accepted and the personal search dropped", got)
	}
	rec := do(t, h, http.MethodGet, "/v1/analytics/ux-signals?from=2026-11-10&to=2026-11-10", "", uxViewer)
	var s domain.UXSignals
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &s) != nil {
		t.Fatalf("signals: %d %s", rec.Code, rec.Body.String())
	}
	if len(s.ValidationErrors) != 2 || s.ValidationErrors[0].Count != 2 || s.ValidationErrors[0].Field != "lines[0].qty" {
		t.Fatalf("validation errors %+v", s.ValidationErrors)
	}
	if len(s.NoResultSearches) != 1 || s.NoResultSearches[0].Query != "hose 2m" || s.NoResultSearches[0].Count != 2 {
		t.Fatalf("searches %+v", s.NoResultSearches)
	}
	if len(s.AbandonedForms) != 1 || s.AbandonedForms[0].Count != 2 {
		t.Fatalf("abandoned %+v", s.AbandonedForms)
	}

	// Another tenant sees none of it.
	rec = doTenant(t, h, "00000000-0000-7000-8000-000000000002", http.MethodGet, "/v1/analytics/ux-signals?from=2026-11-10&to=2026-11-10", uxViewer)
	var none domain.UXSignals
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &none) != nil || len(none.ValidationErrors)+len(none.NoResultSearches)+len(none.AbandonedForms) != 0 {
		t.Fatalf("other tenant: %d %s", rec.Code, rec.Body.String())
	}
}

func doTenant(t *testing.T, h http.Handler, tenantID, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	merged := map[string]string{tenant.HeaderTenant: tenantID}
	for k, v := range headers {
		merged[k] = v
	}
	return do(t, h, method, path, "", merged)
}
