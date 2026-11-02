package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestPerformanceRoutesRequireTheirDuty(t *testing.T) {
	id := testkit.TenantID(8)
	manager := []struct{ method, target, body string }{
		{http.MethodPost, "/v1/hr/workers/" + id + "/goals", `{}`},
		{http.MethodGet, "/v1/hr/workers/" + id + "/goals", ""},
		{http.MethodPost, "/v1/hr/goals/" + id + "/progress", `{}`},
		{http.MethodPost, "/v1/hr/workers/" + id + "/reviews", `{}`},
		{http.MethodGet, "/v1/hr/workers/" + id + "/reviews", ""},
		{http.MethodGet, "/v1/hr/reviews/" + id, ""},
		{http.MethodPost, "/v1/hr/reviews/" + id + "/sign-off", `{}`},
	}
	self := []struct{ method, target, body string }{
		{http.MethodGet, "/v1/hr/me/goals", ""},
		{http.MethodGet, "/v1/hr/me/reviews", ""},
		{http.MethodPost, "/v1/hr/me/reviews/" + id + "/self-assessment", `{}`},
	}
	jobs := []struct{ method, target, body string }{
		{http.MethodGet, "/v1/hr/jobs/" + id + "/skill-requirements", ""},
		{http.MethodPost, "/v1/hr/jobs/" + id + "/skill-requirements", `{}`},
		{http.MethodDelete, "/v1/hr/jobs/" + id + "/skill-requirements/WELDING", ""},
		{http.MethodGet, "/v1/hr/jobs/" + id + "/skill-gaps", ""},
	}
	for _, c := range append(append(manager, self...), jobs...) {
		if rec := serve(t, tid(), c.method, c.target, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without a duty: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
	for _, c := range manager {
		if rec := serve(t, tid(), c.method, c.target, c.body, dutyPerfSelf); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with only the self duty: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
	for _, c := range self {
		if rec := serve(t, tid(), c.method, c.target, c.body, dutyPerfManage, dutyPerfAdmin); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with only manager duties: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
	for _, c := range jobs[1:3] {
		if rec := serve(t, tid(), c.method, c.target, c.body, dutyOrgInquire); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a read-only partner: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
}

func TestPerformanceRequestsNameEveryBadField(t *testing.T) {
	id := testkit.TenantID(8)
	cases := []struct {
		name, method, target, body string
		duty                       string
		fields                     []string
	}{
		{"goal", http.MethodPost, "/v1/hr/workers/" + id + "/goals", `{"title":"  ","dueOn":"2026-02-30"}`, dutyPerfManage, []string{`"title"`, `"dueOn"`}},
		{"progress", http.MethodPost, "/v1/hr/goals/" + id + "/progress", `{"status":"DONE","progressPercent":101}`, dutyPerfManage, []string{`"status"`, `"progressPercent"`}},
		{"empty progress", http.MethodPost, "/v1/hr/goals/" + id + "/progress", `{}`, dutyPerfManage, []string{`"status"`}},
		{"review", http.MethodPost, "/v1/hr/workers/" + id + "/reviews", `{"periodStart":"2026-06-01","periodEnd":"2026-01-01"}`, dutyPerfManage, []string{`"periodEnd"`}},
		{"review dates", http.MethodPost, "/v1/hr/workers/" + id + "/reviews", `{}`, dutyPerfManage, []string{`"periodStart"`, `"periodEnd"`}},
		{"sign-off", http.MethodPost, "/v1/hr/reviews/" + id + "/sign-off", `{"overallRating":6}`, dutyPerfManage, []string{`"overallRating"`}},
		{"self-assessment", http.MethodPost, "/v1/hr/me/reviews/" + id + "/self-assessment", `{"achievements":"   "}`, dutyPerfSelf, []string{`"achievements"`}},
		{"job gap needs one filter", http.MethodGet, "/v1/hr/workers/" + id + "/skill-gap", "", dutyOrgInquire, []string{`"positionId"`}},
		{"job gap rejects both", http.MethodGet, "/v1/hr/workers/" + id + "/skill-gap?positionId=" + id + "&jobId=" + id, "", dutyOrgInquire, []string{`"positionId"`}},
		{"job gap bad job id", http.MethodGet, "/v1/hr/workers/" + id + "/skill-gap?jobId=nope", "", dutyOrgInquire, []string{`"jobId"`}},
		{"job requirement", http.MethodPost, "/v1/hr/jobs/" + id + "/skill-requirements", `{"skillCode":"bad code"}`, dutyOrgMaintain, []string{`"skillCode"`, `"minRating"`}},
		{"job gap date", http.MethodGet, "/v1/hr/jobs/" + id + "/skill-gaps?asOf=2026-13-01", "", dutyOrgInquire, []string{`"asOf"`}},
	}
	for _, c := range cases {
		rec := serve(t, tid(), c.method, c.target, c.body, c.duty)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", c.name, rec.Code, rec.Body.String())
			continue
		}
		for _, field := range c.fields {
			if !strings.Contains(rec.Body.String(), field) {
				t.Errorf("%s: response does not name %s: %s", c.name, field, rec.Body.String())
			}
		}
	}
	if rec := serve(t, tid(), http.MethodGet, "/v1/hr/reviews/not-a-uuid", "", dutyPerfManage); rec.Code != http.StatusNotFound {
		t.Errorf("malformed review id: status = %d, want 404", rec.Code)
	}
	if rec := serve(t, tid(), http.MethodGet, "/v1/hr/jobs/not-a-uuid/skill-gaps", "", dutyOrgInquire); rec.Code != http.StatusNotFound {
		t.Errorf("malformed job id: status = %d, want 404", rec.Code)
	}
}
