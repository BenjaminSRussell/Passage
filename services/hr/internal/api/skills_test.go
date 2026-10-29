package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
)

// TestSkillRoutesRequireTheirDuty: every skill route answers 403 before any
// validation or store call without its duty, and a read-only HR partner
// (HcmWorkerInquire, HcmOrgInquire) cannot write the catalog, ratings,
// certificates or position profiles.
func TestSkillRoutesRequireTheirDuty(t *testing.T) {
	id := testkit.TenantID(8)
	writes := []struct{ method, target, body string }{
		{http.MethodPost, "/v1/hr/skills", `{}`},
		{http.MethodPost, "/v1/hr/workers/" + id + "/skills", `{}`},
		{http.MethodPost, "/v1/hr/workers/" + id + "/certifications", `{}`},
		{http.MethodPost, "/v1/hr/positions/" + id + "/skill-requirements", `{}`},
		{http.MethodDelete, "/v1/hr/positions/" + id + "/skill-requirements/WELDING", ""},
	}
	reads := []struct{ method, target, body string }{
		{http.MethodGet, "/v1/hr/skills", ""},
		{http.MethodGet, "/v1/hr/skills/search?skills=WELDING", ""},
		{http.MethodGet, "/v1/hr/workers/" + id + "/skills", ""},
		{http.MethodGet, "/v1/hr/workers/" + id + "/certifications", ""},
		{http.MethodGet, "/v1/hr/certifications/expiring", ""},
		{http.MethodGet, "/v1/hr/workers/" + id + "/skill-gap?positionId=" + id, ""},
		{http.MethodGet, "/v1/hr/positions/" + id + "/skill-requirements", ""},
	}
	for _, c := range append(writes, reads...) {
		if rec := serve(t, tid(), c.method, c.target, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without a duty: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
	for _, c := range writes {
		if rec := serve(t, tid(), c.method, c.target, c.body, dutyWorkerInquire, dutyOrgInquire); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a read-only HR partner: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
}

func TestCreateSkillNamesEveryBadField(t *testing.T) {
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/skills", `{"code":"weld ing","name":"  ","scaleMax":11}`, dutySetupAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{`"code"`, `"name"`, `"scaleMax"`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("response does not name %s: %s", field, rec.Body.String())
		}
	}
	if rec := serve(t, tid(), http.MethodPost, "/v1/hr/skills", `{"code":"WELDING","name":"Welding","scaleMax":0}`, dutySetupAdmin); rec.Code != http.StatusBadRequest {
		t.Fatalf("scale 0: status = %d, want 400", rec.Code)
	}
}

func TestRateWorkerSkillValidation(t *testing.T) {
	target := "/v1/hr/workers/" + testkit.TenantID(5) + "/skills"
	rec := serve(t, tid(), http.MethodPost, target, `{"skillCode":"","assessedOn":"2026-02-30"}`, dutyWorkerMaintain)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	for _, field := range []string{`"skillCode"`, `"rating"`, `"assessedOn"`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("response does not name %s: %s", field, rec.Body.String())
		}
	}
	future := time.Now().UTC().AddDate(0, 0, 2).Format(dateLayout)
	rec = serve(t, tid(), http.MethodPost, target, `{"skillCode":"WELDING","rating":3,"assessedOn":"`+future+`"}`, dutyWorkerMaintain)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "future") {
		t.Fatalf("future assessment: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, tid(), http.MethodPost, "/v1/hr/workers/not-a-uuid/skills", `{}`, dutyWorkerMaintain); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed worker id: status = %d, want 404", rec.Code)
	}
}

// TestIssueCertificationValidation: expiresOn and validityMonths exclude each
// other, the expiry must follow the issue date, validity is 1-240 months and
// a certificate cannot be issued in the future.
func TestIssueCertificationValidation(t *testing.T) {
	target := "/v1/hr/workers/" + testkit.TenantID(5) + "/certifications"
	cases := []struct {
		name, body, field string
	}{
		{"both expiry forms", `{"code":"FORKLIFT","name":"Forklift","issuedOn":"2026-05-10","expiresOn":"2028-05-10","validityMonths":24}`, "validityMonths"},
		{"expiry before issue", `{"code":"FORKLIFT","name":"Forklift","issuedOn":"2026-05-10","expiresOn":"2026-05-10"}`, "expiresOn"},
		{"malformed expiry", `{"code":"FORKLIFT","name":"Forklift","issuedOn":"2026-05-10","expiresOn":"10/05/2028"}`, "expiresOn"},
		{"zero months", `{"code":"FORKLIFT","name":"Forklift","issuedOn":"2026-05-10","validityMonths":0}`, "validityMonths"},
		{"too many months", `{"code":"FORKLIFT","name":"Forklift","issuedOn":"2026-05-10","validityMonths":241}`, "validityMonths"},
		{"future issue", `{"code":"FORKLIFT","name":"Forklift","issuedOn":"` + time.Now().UTC().AddDate(1, 0, 0).Format(dateLayout) + `"}`, "issuedOn"},
		{"bad code and name", `{"code":"fork lift","name":"","issuedOn":"2026-05-10"}`, "code"},
		{"long issuer", `{"code":"FORKLIFT","name":"Forklift","issuedOn":"2026-05-10","issuer":"` + strings.Repeat("x", 121) + `"}`, "issuer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(t, tid(), http.MethodPost, target, c.body, dutyWorkerMaintain)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
				t.Fatalf("status %d, want 400 naming %s: %s", rec.Code, c.field, rec.Body.String())
			}
		})
	}
}

func TestSkillSearchValidation(t *testing.T) {
	cases := []struct{ query, field string }{
		{"", "skills"},
		{"skills=WELDING:x", "skills"},
		{"skills=WELDING&minRating=11", "minRating"},
		{"certs=bad%20code", "certs"},
		{"skills=WELDING&availableFrom=2026-05-01", "availableFrom"},
		{"skills=WELDING&asOf=2026-13-01", "asOf"},
	}
	for _, c := range cases {
		rec := serve(t, tid(), http.MethodGet, "/v1/hr/skills/search?"+c.query, "", dutyOrgInquire)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("?%s: status %d, want 400 naming %s: %s", c.query, rec.Code, c.field, rec.Body.String())
		}
	}
}

func TestSkillGapAndRequirementValidation(t *testing.T) {
	id := testkit.TenantID(5)
	rec := serve(t, tid(), http.MethodGet, "/v1/hr/workers/"+id+"/skill-gap?positionId=nope", "", dutyOrgInquire)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "positionId") {
		t.Fatalf("bad positionId: status %d body %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, tid(), http.MethodPost, "/v1/hr/positions/"+id+"/skill-requirements", `{"skillCode":"w"}`, dutyOrgMaintain)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "skillCode") || !strings.Contains(rec.Body.String(), "minRating") {
		t.Fatalf("bad requirement: status %d body %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, tid(), http.MethodDelete, "/v1/hr/positions/"+id+"/skill-requirements/bad%20code", "", dutyOrgMaintain)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("malformed skill code on delete: status = %d, want 404", rec.Code)
	}
	rec = serve(t, tid(), http.MethodGet, "/v1/hr/certifications/expiring?withinDays=0", "", dutyWorkerInquire)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "withinDays") {
		t.Fatalf("withinDays 0: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestParseSkillTerms(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		defaultMin int
		want       []domain.Requirement
		wantErr    bool
	}{
		{"empty is no terms", "", 1, nil, false},
		{"a bare code takes the default minimum", "welding", 2, []domain.Requirement{{Skill: "WELDING", MinRating: 2}}, false},
		{"an explicit minimum wins", "WELDING:3, PLC", 2, []domain.Requirement{{Skill: "WELDING", MinRating: 3}, {Skill: "PLC", MinRating: 2}}, false},
		{"minimum above the widest scale", "WELDING:11", 1, nil, true},
		{"minimum zero", "WELDING:0", 1, nil, true},
		{"minimum not a number", "WELDING:x", 1, nil, true},
		{"bad code shape", "wel ding", 1, nil, true},
		{"trailing comma", "WELDING,", 1, nil, true},
		{"too many terms", "A,B,C,D,E,F,G,H,I,J,K,L,M,N,O,P,Q,R,S,T,U", 1, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := parseSkillTerms(tc.raw, tc.defaultMin)
			if (msg != "") != tc.wantErr {
				t.Fatalf("message = %q, wantErr %v", msg, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("terms = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseCodes(t *testing.T) {
	got, msg := parseCodes("osha10, forklift")
	if msg != "" || !reflect.DeepEqual(got, []string{"OSHA10", "FORKLIFT"}) {
		t.Fatalf("codes = %v, message = %q", got, msg)
	}
	if _, msg := parseCodes("bad code!"); msg == "" {
		t.Fatal("a malformed certificate code must be refused")
	}
}
