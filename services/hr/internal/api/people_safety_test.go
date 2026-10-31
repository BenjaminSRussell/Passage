package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestPeopleSafetyValidation(t *testing.T) {
	id := testkit.TenantID(9)
	cases := []struct {
		name, method, target, body, duty, field string
	}{
		{"incident type and times", http.MethodPost, "/v1/hr/incidents", `{"workerId":"x","incidentType":"BRUISE","occurredAt":"yesterday","location":"","description":""}`, dutyIncidentReport, "incidentType"},
		{"incident days", http.MethodPost, "/v1/hr/incidents", `{"workerId":"` + id + `","legalEntityId":"` + id + `","siteId":"` + id + `","incidentType":"INJURY","occurredAt":"2020-01-01T00:00:00Z","location":"a","description":"d","daysAway":999}`, dutyIncidentReport, "daysAway"},
		{"incident status", http.MethodPost, "/v1/hr/incidents/" + id + "/status", `{"status":"REPORTED"}`, dutyIncidentAdmin, "status"},
		{"incident list status", http.MethodGet, "/v1/hr/incidents?status=NOPE", ``, dutyIncidentAdmin, "status"},
		{"osha year", http.MethodGet, "/v1/hr/incidents/osha-extract?year=abc", ``, dutyIncidentAdmin, "year"},
		{"osha site", http.MethodGet, "/v1/hr/incidents/osha-extract?year=2026&siteId=1", ``, dutyIncidentAdmin, "siteId"},
		{"loan fields", http.MethodPost, "/v1/hr/workers/" + id + "/loaned-items", `{"loanType":"bad type","itemDescription":"","issuedOn":"01/02/2026"}`, dutyWorkerMaintain, "loanType"},
		{"loan close", http.MethodPost, "/v1/hr/loaned-items/" + id + "/close", `{"status":"BROKEN","closedOn":"2026-01-01"}`, dutyWorkerMaintain, "status"},
		{"outstanding date", http.MethodGet, "/v1/hr/loaned-items/outstanding?dueBy=soon", ``, dutyWorkerInquire, "dueBy"},
		{"template reserved task", http.MethodPost, "/v1/hr/checklist-templates", `{"code":"X","kind":"OFFBOARDING","name":"n","tasks":[{"code":"RETURN_LOANED_ITEMS","title":"t","ownerRole":"IT","dueOffsetDays":0}]}`, dutySetupAdmin, "tasks[0].code"},
		{"template offset", http.MethodPost, "/v1/hr/checklist-templates", `{"code":"X","kind":"ONBOARDING","name":"n","tasks":[{"code":"A","title":"t","ownerRole":"BOSS","dueOffsetDays":900}]}`, dutySetupAdmin, "tasks[0].ownerRole"},
		{"start checklist", http.MethodPost, "/v1/hr/workers/" + id + "/checklists", `{"templateId":"x","effectiveDate":"tomorrow"}`, dutyActionMaintain, "effectiveDate"},
		{"owned tasks", http.MethodGet, "/v1/hr/tasks?ownerWorkerId=x", ``, dutyWorkerInquire, "ownerWorkerId"},
		{"finish task", http.MethodPost, "/v1/hr/tasks/" + id + "/finish", `{"status":"OPEN"}`, dutyActionMaintain, "status"},
		{"questionnaire design", http.MethodPost, "/v1/hr/questionnaires", `{"code":"Q","name":"n","questions":[{"code":"A","text":"t","type":"ESSAY"}]}`, dutyQuestionnaireAdmin, "questions[0].type"},
		{"questionnaire empty", http.MethodPost, "/v1/hr/questionnaires", `{"code":"bad code","name":"","questions":[]}`, dutyQuestionnaireAdmin, "questions"},
		{"schedule window", http.MethodPost, "/v1/hr/questionnaires/schedules", `{"questionnaireId":"` + id + `","opensAt":"2026-03-02T00:00:00Z","closesAt":"2026-03-01T00:00:00Z"}`, dutyQuestionnaireAdmin, "closesAt"},
		{"answer worker", http.MethodPost, "/v1/hr/questionnaires/schedules/" + id + "/answers", `{"workerId":"x","answers":{}}`, dutyEssQuestionnaire, "workerId"},
		{"results by", http.MethodGet, "/v1/hr/questionnaires/schedules/" + id + "/results?by=team", ``, dutyQuestionnaireAdmin, "by"},
	}
	for _, c := range cases {
		rec := serve(t, tid(), c.method, c.target, c.body, c.duty)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("%s: status %d body %s, want 400 naming %s", c.name, rec.Code, rec.Body.String(), c.field)
		}
	}
}

func TestPeopleSafetyRoutesRefuseMalformedIDs(t *testing.T) {
	for target, duty := range map[string]string{
		"/v1/hr/incidents/not-a-uuid":                        dutyIncidentAdmin,
		"/v1/hr/checklists/not-a-uuid":                       dutyWorkerInquire,
		"/v1/hr/questionnaires/schedules/not-a-uuid":         dutyEssQuestionnaire,
		"/v1/hr/questionnaires/schedules/not-a-uuid/results": dutyQuestionnaireAdmin,
	} {
		if rec := serve(t, tid(), http.MethodGet, target, "", duty); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", target, rec.Code)
		}
	}
}
