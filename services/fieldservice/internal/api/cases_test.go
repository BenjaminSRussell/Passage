package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
	"github.com/BenjaminSRussell/passage/services/fieldservice/migrations"
)

// Validation tests stop before the store, so the nil store of newTestRouter is never reached.

func TestCaseProcess_RejectsBrokenStages(t *testing.T) {
	h := newTestRouter()
	body := `{"code":"HR-PROC","name":"HR","stages":[{"code":"INTAKE","sequence":1},{"code":"INTAKE","sequence":3}]}`
	rec := do(t, h, http.MethodPost, "/v1/case-processes", body, map[string]string{"Idempotency-Key": "p1"}, "fieldservice.case-setup.admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, fields := problemOf(t, rec); fields["stages"] == "" {
		t.Fatalf("errors %v lack stages", fields)
	}
}

func TestCaseProcess_DutyRequired(t *testing.T) {
	h := newTestRouter()
	body := `{"code":"HR-PROC","name":"HR","stages":[{"code":"INTAKE","sequence":1}]}`
	rec := do(t, h, http.MethodPost, "/v1/case-processes", body, map[string]string{"Idempotency-Key": "p2"}, "fieldservice.case.maintain")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCaseCategory_Validation(t *testing.T) {
	h := newTestRouter()
	body := `{"code":"x","name":"","categoryType":"FINANCE","parentId":"nope","reopenWindowDays":400}`
	rec := do(t, h, http.MethodPost, "/v1/case-categories", body, map[string]string{"Idempotency-Key": "c1"}, "fieldservice.case-setup.admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	_, fields := problemOf(t, rec)
	for _, f := range []string{"code", "name", "categoryType", "parentId", "reopenWindowDays"} {
		if fields[f] == "" {
			t.Errorf("errors %v lack %s", fields, f)
		}
	}
}

func TestOpenGenericCase_Validation(t *testing.T) {
	h := newTestRouter()
	body := `{"categoryId":"nope","legalEntityId":"` + uuid1 + `","subject":"   ","priority":9}`
	rec := do(t, h, http.MethodPost, "/v1/cases", body, map[string]string{"Idempotency-Key": "g1"}, "fieldservice.case.create")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	_, fields := problemOf(t, rec)
	for _, f := range []string{"categoryId", "subject", "priority"} {
		if fields[f] == "" {
			t.Errorf("errors %v lack %s", fields, f)
		}
	}
}

func TestOpenGenericCase_DutyRequired(t *testing.T) {
	h := newTestRouter()
	body := `{"categoryId":"` + uuid1 + `","legalEntityId":"` + uuid1 + `","subject":"hi","priority":2}`
	rec := do(t, h, http.MethodPost, "/v1/cases", body, map[string]string{"Idempotency-Key": "g2"}, "fieldservice.case.maintain")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdvanceCaseStage_RequiresIfMatch(t *testing.T) {
	h := newTestRouter()
	body := `{"toStage":"INVESTIGATION"}`
	rec := do(t, h, http.MethodPost, "/v1/cases/"+uuid1+"/stage", body, map[string]string{"Idempotency-Key": "s1"}, "fieldservice.case.maintain")
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTransitionCase_RejectsOpenTarget(t *testing.T) {
	h := newTestRouter()
	body := `{"to":"OPEN"}`
	rec := do(t, h, http.MethodPost, "/v1/cases/"+uuid1+":transition", body,
		map[string]string{"Idempotency-Key": "t1", "If-Match": "1"}, "fieldservice.case.maintain")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, fields := problemOf(t, rec); fields["to"] == "" {
		t.Fatalf("errors %v lack to", fields)
	}
}

func TestAssociateCase_Validation(t *testing.T) {
	h := newTestRouter()
	body := `{"entityType":"spaceship","entityId":"nope","role":"BOSS"}`
	rec := do(t, h, http.MethodPost, "/v1/cases/"+uuid1+"/associations", body, map[string]string{"Idempotency-Key": "a1"}, "fieldservice.case.maintain")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	_, fields := problemOf(t, rec)
	for _, f := range []string{"entityType", "entityId", "role"} {
		if fields[f] == "" {
			t.Errorf("errors %v lack %s", fields, f)
		}
	}
}

func TestListCases_RejectsUnknownStatus(t *testing.T) {
	h := newTestRouter()
	rec := do(t, h, http.MethodGet, "/v1/cases?status=PENDING", "", nil, "fieldservice.case.maintain")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, fields := problemOf(t, rec); fields["status"] == "" {
		t.Fatalf("errors %v lack status", fields)
	}
}

func TestAssociateCase_RefusesSelfAndResolutionOutsideResolve(t *testing.T) {
	h := newTestRouter()
	rec := do(t, h, http.MethodPost, "/v1/cases/"+uuid1+"/associations", `{"entityType":"case","entityId":"`+uuid1+`"}`,
		map[string]string{"Idempotency-Key": "a2"}, "fieldservice.case.maintain")
	if _, fields := problemOf(t, rec); rec.Code != http.StatusBadRequest || fields["entityId"] == "" {
		t.Fatalf("self association: %d %v", rec.Code, fields)
	}
	rec = do(t, h, http.MethodPost, "/v1/cases/"+uuid1+":transition", `{"to":"CANCELED","resolutionCode":"FIXED"}`,
		map[string]string{"Idempotency-Key": "t2", "If-Match": `"1"`}, "fieldservice.case.maintain")
	if _, fields := problemOf(t, rec); rec.Code != http.StatusBadRequest || fields["resolutionCode"] == "" {
		t.Fatalf("resolution on cancel: %d %v", rec.Code, fields)
	}
	rec = do(t, h, http.MethodPost, "/v1/cases/"+uuid1+":reopen", `{"reason":"  "}`,
		map[string]string{"Idempotency-Key": "r1", "If-Match": `"1"`}, "fieldservice.case.maintain")
	if _, fields := problemOf(t, rec); rec.Code != http.StatusBadRequest || fields["reason"] == "" {
		t.Fatalf("reopen without a reason: %d %v", rec.Code, fields)
	}
}

func TestCaseSetupLists_AcceptAnyCaseDuty(t *testing.T) {
	h := newTestRouter()
	if rec := do(t, h, http.MethodGet, "/v1/case-categories", "", nil, "fieldservice.inquire"); rec.Code != http.StatusForbidden {
		t.Fatalf("categories without a case duty: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/cases?entityType=invoice", "", nil, "fieldservice.case.maintain"); rec.Code != http.StatusBadRequest {
		t.Fatalf("entityType without entityId: %d", rec.Code)
	}
}

// genericRouter mounts the routes over a migrated store; as(user, duties...) is a
// caller of tenant 1 with those duties and no legal-entity scope.
func genericRouter(t *testing.T) (http.Handler, func(user string, duties ...string) context.Context) {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	as := func(user string, duties ...string) context.Context {
		d := testkit.ScopeDuties("")
		for _, x := range duties {
			d[x] = struct{}{}
		}
		return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: user, Duties: d})
	}
	if err := dbx.Migrate(as("setup"), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	New(store.New(pool)).Register(r)
	return r, as
}

// AC-45.10 and AC-45.11 over HTTP: setup, open, the ETag and If-Match flow, the
// stage gate (CASE-STG-001), the replay header, 403 CASE-SEC-001 for a collector
// without the HR duty, and a reopen that answers with the case.
func TestAPI_GenericCaseFlow(t *testing.T) {
	h, as := genericRouter(t)
	admin := as(ids.New(), dutyCaseSetup)
	partner := as(ids.New(), dutyGenCreate, dutyGenMaintain, "HrCaseAccess")
	collector := as(ids.New(), dutyGenCreate, dutyGenMaintain)
	decode := func(rec *httptest.ResponseRecorder, v any) {
		t.Helper()
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("%v: %s", err, rec.Body.String())
		}
	}

	rec := call(t, h, admin, http.MethodPost, "/v1/case-processes", `{"code":"HR-PROC","name":"HR","stages":[
		{"code":"INTAKE","sequence":1},{"code":"INVESTIGATION","sequence":2,"requiredActivities":["Interview"]},
		{"code":"RESOLUTION","sequence":3}]}`, map[string]string{"Idempotency-Key": "proc"})
	var proc store.Process
	decode(rec, &proc)
	if rec.Code != http.StatusCreated || len(proc.Stages) != 3 || proc.Stages[0].RequiredActivities == nil {
		t.Fatalf("process: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, h, admin, http.MethodPost, "/v1/case-categories", `{"code":"HR-CASE","name":"HR","categoryType":"HR","processId":"`+
		proc.ID+`","accessDuty":"HrCaseAccess","resolutionCodes":["FOUND"]}`, map[string]string{"Idempotency-Key": "cat"})
	var cat store.Category
	decode(rec, &cat)
	if rec.Code != http.StatusCreated || cat.ReopenWindowDays != 14 || cat.DefaultPriority != 3 {
		t.Fatalf("category: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, collector, http.MethodGet, "/v1/case-categories", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("a case worker reads the categories: %d", rec.Code)
	}

	open := `{"categoryId":"` + cat.ID + `","legalEntityId":"` + ids.New() + `","subject":"Grievance","associations":[{"entityType":"worker","entityId":"` + ids.New() + `","role":"SUBJECT"}]}`
	rec = call(t, h, partner, http.MethodPost, "/v1/cases", open, map[string]string{"Idempotency-Key": "case"})
	var c store.GenericCase
	decode(rec, &c)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || c.StageCode != "INTAKE" || len(c.Associations) != 1 {
		t.Fatalf("open: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if rec := call(t, h, partner, http.MethodPost, "/v1/cases", open, map[string]string{"Idempotency-Key": "case"}); rec.Code != http.StatusOK ||
		rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("open replay: %d %v", rec.Code, rec.Header())
	}

	// AC-45.11: the collector without the HR duty gets 403 on read and write, and no list row.
	if rec := call(t, h, collector, http.MethodGet, "/v1/cases/"+c.ID, "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("collector read: %d %s", rec.Code, rec.Body.String())
	}
	if code, _ := problemOf(t, call(t, h, collector, http.MethodPost, "/v1/cases/"+c.ID+"/stage", `{"toStage":"INVESTIGATION"}`,
		map[string]string{"Idempotency-Key": "col-stage", "If-Match": `"1"`})); code != "CASE-SEC-001" {
		t.Fatalf("collector stage move: %s", code)
	}
	var list store.Page[store.GenericCase]
	decode(call(t, h, collector, http.MethodGet, "/v1/cases", "", nil), &list)
	if len(list.Items) != 0 {
		t.Fatalf("collector lists %d cases", len(list.Items))
	}

	stage := func(to, version, key string) *httptest.ResponseRecorder {
		return call(t, h, partner, http.MethodPost, "/v1/cases/"+c.ID+"/stage", `{"toStage":"`+to+`"}`,
			map[string]string{"Idempotency-Key": key, "If-Match": version})
	}
	if rec := call(t, h, partner, http.MethodPost, "/v1/cases/"+c.ID+"/stage", `{"toStage":"INVESTIGATION"}`,
		map[string]string{"Idempotency-Key": "no-version"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("stage without If-Match: %d", rec.Code)
	}
	if rec := stage("INVESTIGATION", `"1"`, "s1"); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("advance: %d %s", rec.Code, rec.Body.String())
	}
	if rec := stage("RESOLUTION", `"1"`, "s2"); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", rec.Code)
	}
	if code, _ := problemOf(t, stage("RESOLUTION", `"2"`, "s3")); code != "CASE-STG-001" {
		t.Fatalf("gate with the Interview missing: %s", code)
	}

	rec = call(t, h, partner, http.MethodPost, "/v1/cases/"+c.ID+":transition", `{"to":"RESOLVED","resolutionCode":"FOUND"}`,
		map[string]string{"Idempotency-Key": "res", "If-Match": `"2"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, h, partner, http.MethodPost, "/v1/cases/"+c.ID+":reopen", `{"reason":"more to do"}`,
		map[string]string{"Idempotency-Key": "reopen", "If-Match": `"3"`})
	var reopened store.ReopenResult
	decode(rec, &reopened)
	if rec.Code != http.StatusOK || reopened.Created || reopened.Case.ID != c.ID || reopened.Case.Status != "OPEN" {
		t.Fatalf("reopen: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, h, partner, http.MethodGet, "/v1/cases?stage=INVESTIGATION&status=OPEN", "", nil)
	decode(rec, &list)
	if rec.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != c.ID {
		t.Fatalf("partner lists: %d %s", rec.Code, rec.Body.String())
	}
}
