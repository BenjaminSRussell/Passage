package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func intp(v int) *int                                    { return &v }
func sp(v string) *string                                { return &v }
func hasField(fields map[string]string, key string) bool { _, ok := fields[key]; return ok }

// TestCourseRoutesRequireTheirDuty: every course route answers 403 without
// its duty, and an employee with only hr.learning.self cannot run the catalog,
// schedule sessions, read the attendance list, record completion or approve.
func TestCourseRoutesRequireTheirDuty(t *testing.T) {
	id := testkit.TenantID(9)
	admin := []struct{ method, target string }{
		{http.MethodPost, "/v1/hr/courses"},
		{http.MethodPost, "/v1/hr/courses/" + id + "/sessions"},
		{http.MethodGet, "/v1/hr/courses/sessions/" + id + "/registrations"},
		{http.MethodPost, "/v1/hr/courses/sessions/" + id + "/complete"},
		{http.MethodPost, "/v1/hr/courses/registrations/" + id + "/approve"},
	}
	employee := []struct{ method, target string }{
		{http.MethodGet, "/v1/hr/courses"},
		{http.MethodGet, "/v1/hr/courses/" + id},
		{http.MethodGet, "/v1/hr/courses/" + id + "/sessions"},
		{http.MethodGet, "/v1/hr/courses/sessions/" + id},
		{http.MethodPost, "/v1/hr/courses/sessions/" + id + "/registrations"},
		{http.MethodPost, "/v1/hr/courses/registrations/" + id + "/cancel"},
	}
	for _, c := range append(admin, employee...) {
		if rec := serve(t, tid(), c.method, c.target, `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without a duty: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
	for _, c := range admin {
		if rec := serve(t, tid(), c.method, c.target, `{}`, dutyEssLearning); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as an employee: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
	if rec := serve(t, tid(), http.MethodGet, "/v1/hr/courses/not-a-uuid", "", dutyEssLearning); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "HCM-CRS-NOT-FOUND") {
		t.Errorf("malformed course id: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, tid(), http.MethodGet, "/v1/hr/courses/sessions/nope", "", dutyEssLearning); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "HCM-CRS-SESSION-NOT-FOUND") {
		t.Errorf("malformed session id: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateCourseNamesEveryBadField(t *testing.T) {
	body := `{"code":"fork 101","name":" ","skillOutcomes":[{"skillCode":"WELD","rating":11},{"skillCode":"WELD","rating":2}],` +
		`"certificationCode":"FORKLIFT","validityMonths":300}`
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/courses", body, dutyLearningAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{`"code"`, `"name"`, `"skillOutcomes[0].rating"`, `"skillOutcomes[1].skillCode"`, `"certificationName"`, `"validityMonths"`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("response does not name %s: %s", field, rec.Body.String())
		}
	}
	if rec := serve(t, tid(), http.MethodPost, "/v1/hr/courses", `{"code":"X","name":"x","unknown":1}`, dutyLearningAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: status = %d, want 400", rec.Code)
	}
}

func TestValidateCourse(t *testing.T) {
	tests := []struct {
		name      string
		req       courseRequest
		wantField string
	}{
		{"plain course", courseRequest{Code: "FORK-101", Name: "Forklift"}, ""},
		{"certificate with validity", courseRequest{Code: "FORK", Name: "Forklift", CertificationCode: sp("FORKLIFT"), CertificationName: sp("Licence"), ValidityMonths: intp(24)}, ""},
		{"certificate that does not expire", courseRequest{Code: "FORK", Name: "Forklift", CertificationCode: sp("FORKLIFT"), CertificationName: sp("Licence")}, ""},
		{"lower-case code", courseRequest{Code: "fork-101", Name: "x"}, "code"},
		{"certificate code without a name", courseRequest{Code: "X", Name: "x", CertificationCode: sp("CERT")}, "certificationName"},
		{"bad certificate code", courseRequest{Code: "X", Name: "x", CertificationCode: sp("cert!"), CertificationName: sp("c")}, "certificationCode"},
		{"validity without a certificate", courseRequest{Code: "X", Name: "x", ValidityMonths: intp(12)}, "validityMonths"},
		{"negative validity", courseRequest{Code: "X", Name: "x", CertificationCode: sp("C"), CertificationName: sp("c"), ValidityMonths: intp(-1)}, "validityMonths"},
		{"rating missing", courseRequest{Code: "X", Name: "x", SkillOutcomes: []courseOutcomeRequest{{SkillCode: "WELD"}}}, "skillOutcomes[0].rating"},
		{"group too long", courseRequest{Code: "X", Name: "x", CourseGroup: sp(strings.Repeat("g", crsMaxGroup+1))}, "courseGroup"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			course, fields := validateCourse(tc.req)
			if tc.wantField == "" {
				if len(fields) != 0 {
					t.Fatalf("fields = %v, want none", fields)
				}
				if tc.req.ValidityMonths == nil && course.ValidityMonths != 0 {
					t.Fatalf("validity = %d, want 0 when absent", course.ValidityMonths)
				}
				return
			}
			if !hasField(fields, tc.wantField) {
				t.Fatalf("fields = %v, want a problem on %q", fields, tc.wantField)
			}
		})
	}
}

func TestValidateSession(t *testing.T) {
	course := ids.New()
	base := func() sessionRequest {
		return sessionRequest{StartsAt: "2026-06-01T09:00:00Z", EndsAt: "2026-06-01T17:00:00Z", Location: sp("Yard B"), Capacity: intp(10)}
	}
	with := func(f func(*sessionRequest)) sessionRequest { r := base(); f(&r); return r }
	tests := []struct {
		name      string
		req       sessionRequest
		wantField string
	}{
		{"classroom session", base(), ""},
		{"virtual session", with(func(r *sessionRequest) { r.Location = nil; r.VirtualURL = sp("https://learn.example/s1") }), ""},
		{"cost in yen", with(func(r *sessionRequest) { r.CostPerAttendee = sp("1500"); r.Currency = sp("JPY") }), ""},
		{"internal instructor", with(func(r *sessionRequest) { r.InstructorWorkerID = sp(ids.New()) }), ""},
		{"ends before it starts", with(func(r *sessionRequest) { r.EndsAt = "2026-06-01T08:00:00Z" }), "endsAt"},
		{"no place at all", with(func(r *sessionRequest) { r.Location = sp("  ") }), "location"},
		{"capacity missing", with(func(r *sessionRequest) { r.Capacity = nil }), "capacity"},
		{"capacity zero", with(func(r *sessionRequest) { r.Capacity = intp(0) }), "capacity"},
		{"waitlist over the parameter maximum", with(func(r *sessionRequest) { r.WaitlistCapacity = intp(101) }), "waitlistCapacity"},
		{"link must be http(s)", with(func(r *sessionRequest) { r.VirtualURL = sp("ftp://x") }), "virtualUrl"},
		{"not a timestamp", with(func(r *sessionRequest) { r.StartsAt = "tomorrow" }), "startsAt"},
		{"two instructors", with(func(r *sessionRequest) { r.InstructorWorkerID = sp(ids.New()); r.InstructorPartyID = sp(ids.New()) }), "instructorPartyId"},
		{"cost without currency", with(func(r *sessionRequest) { r.CostPerAttendee = sp("100.00") }), "currency"},
		{"cost with too many places", with(func(r *sessionRequest) { r.CostPerAttendee = sp("10.005"); r.Currency = sp("USD") }), "costPerAttendee"},
		{"negative cost", with(func(r *sessionRequest) { r.CostPerAttendee = sp("-1"); r.Currency = sp("USD") }), "costPerAttendee"},
		{"yen with decimals", with(func(r *sessionRequest) { r.CostPerAttendee = sp("1.5"); r.Currency = sp("JPY") }), "costPerAttendee"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, fields := validateSession(course, tc.req)
			if tc.wantField == "" {
				if len(fields) != 0 {
					t.Fatalf("fields = %v, want none", fields)
				}
				if s.CourseID != course || s.WaitlistCapacity != 5 {
					t.Fatalf("session = %+v, want the course id and the default waitlist of 5", s)
				}
				return
			}
			if !hasField(fields, tc.wantField) {
				t.Fatalf("fields = %v, want a problem on %q", fields, tc.wantField)
			}
		})
	}
	s, _ := validateSession(course, with(func(r *sessionRequest) { r.CostPerAttendee = sp("250.5"); r.Currency = sp("USD") }))
	if s.CostPerAttendee == nil || *s.CostPerAttendee != "250.50" {
		t.Errorf("cost = %v, want 250.50 at the currency's places", s.CostPerAttendee)
	}
}

func TestParseCompletion(t *testing.T) {
	a, b := ids.New(), ids.New()
	out, fields := parseCompletion([]completionLine{{WorkerID: a, Outcome: "ATTENDED", Result: "PASS", Score: sp("92.5")}, {WorkerID: b, Outcome: "NO_SHOW"}})
	if len(fields) != 0 || len(out) != 2 || out[0].Score != "92.50" || out[1].Status != "NO_SHOW" {
		t.Fatalf("valid lines: out=%+v fields=%v", out, fields)
	}
	cases := []struct {
		name  string
		lines []completionLine
		field string
	}{
		{"empty", nil, "results"},
		{"a no-show with a result", []completionLine{{WorkerID: a, Outcome: "NO_SHOW", Result: "PASS"}}, "results[0]"},
		{"an unknown outcome", []completionLine{{WorkerID: a, Outcome: "CONFIRMED"}}, "results[0]"},
		{"a score without a result", []completionLine{{WorkerID: a, Outcome: "ATTENDED", Score: sp("50")}}, "results[0]"},
		{"a score out of range", []completionLine{{WorkerID: a, Outcome: "ATTENDED", Result: "PASS", Score: sp("1000")}}, "results[0].score"},
		{"a malformed worker id", []completionLine{{WorkerID: "nope", Outcome: "ATTENDED"}}, "results[0].workerId"},
		{"the same worker twice", []completionLine{{WorkerID: a, Outcome: "ATTENDED"}, {WorkerID: a, Outcome: "NO_SHOW"}}, "results[1].workerId"},
	}
	for _, tc := range cases {
		if _, fields := parseCompletion(tc.lines); !hasField(fields, tc.field) {
			t.Errorf("%s: fields = %v, want a problem on %q", tc.name, fields, tc.field)
		}
	}
}

func TestRegisterAndCompleteValidateBeforeTheStore(t *testing.T) {
	session := "/v1/hr/courses/sessions/" + testkit.TenantID(4)
	if rec := serve(t, tid(), http.MethodPost, session+"/registrations", `{"workerId":"nope"}`, dutyEssLearning); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), `"workerId"`) {
		t.Errorf("bad worker id: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, tid(), http.MethodPost, session+"/complete", `{"results":[]}`, dutyLearningAdmin); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), `"results"`) {
		t.Errorf("empty completion: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, tid(), http.MethodGet, "/v1/hr/courses?limit=0", "", dutyEssLearning); rec.Code != http.StatusBadRequest {
		t.Errorf("bad page size: %d", rec.Code)
	}
}
