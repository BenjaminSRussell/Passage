package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	projectsPath = "/v1/integration/projects"
	writeDuties  = "migration.run,migration.project.write,tenant.administer"
	leSource     = "00000000-0000-7000-8000-0000000000a1"
	leTarget     = "00000000-0000-7000-8000-0000000000b1"
	copyBody     = `{"name":"USMF to CAMF","projectType":"copy","sourceLegalEntityId":"` + leSource + `","targetLegalEntityId":"` + leTarget + `"}`
)

type projectBody struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ProjectType string `json:"projectType"`
	Status      string `json:"status"`
	Version     int    `json:"version"`
	CutoverAt   string `json:"cutoverAt"`
	Cron        string `json:"scheduleCron"`
}

func decodeProject(t *testing.T, rec *httptest.ResponseRecorder) projectBody {
	t.Helper()
	var p projectBody
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return p
}

func newProjectRouter() (http.Handler, *store.Mem) {
	s := store.NewMem()
	return router(s, rowsExtractor{}, domain.NewMemLoader()), s
}

func createCopy(t *testing.T, r http.Handler) projectBody {
	t.Helper()
	rec := do(r, http.MethodPost, projectsPath, copyBody, writeDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	return decodeProject(t, rec)
}

func TestProjects_CreateReplaysByKeyAndRejectsReuseAndDuplicateName(t *testing.T) {
	r, _ := newProjectRouter()
	rec := do(r, http.MethodPost, projectsPath, copyBody, writeDuties, true, "Idempotency-Key", "k1")
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") == "" {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	first := decodeProject(t, rec)
	if first.Version != 1 || first.Status != "active" || first.ProjectType != "copy" {
		t.Fatalf("created %+v", first)
	}
	again := do(r, http.MethodPost, projectsPath, copyBody, writeDuties, true, "Idempotency-Key", "k1")
	if again.Code != http.StatusOK || decodeProject(t, again).ID != first.ID {
		t.Fatalf("replay = %d %s", again.Code, again.Body)
	}
	other := strings.Replace(copyBody, "USMF to CAMF", "Another", 1)
	if rec := do(r, http.MethodPost, projectsPath, other, writeDuties, true, "Idempotency-Key", "k1"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPost, projectsPath, copyBody, writeDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-PROJECT-NAME-EXISTS") {
		t.Fatalf("duplicate name = %d %s", rec.Code, rec.Body)
	}
}

func TestProjects_CreateValidation(t *testing.T) {
	r, _ := newProjectRouter()
	for name, c := range map[string]struct{ payload, field string }{
		"blank name":        {`{"name":"","projectType":"copy"}`, `"name"`},
		"unknown type":      {`{"name":"x","projectType":"sync"}`, `"projectType"`},
		"copy no entities":  {`{"name":"x","projectType":"copy"}`, `"sourceLegalEntityId"`},
		"same entity":       {`{"name":"x","projectType":"copy","sourceLegalEntityId":"` + leSource + `","targetLegalEntityId":"` + leSource + `"}`, `"targetLegalEntityId"`},
		"migrate no system": {`{"name":"x","projectType":"migrate"}`, `"sourceSystem"`},
		"bad cron":          {`{"name":"x","projectType":"migrate","sourceSystem":"ax2012","scheduleCron":"nope"}`, `"scheduleCron"`},
		"bad cutover":       {`{"name":"x","projectType":"migrate","sourceSystem":"ax2012","cutoverAt":"tomorrow"}`, `"cutoverAt"`},
		"unknown field":     {`{"name":"x","projectType":"config_package","legalEntity":"x"}`, `COM-`},
	} {
		rec := do(r, http.MethodPost, projectsPath, c.payload, writeDuties, true)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.field) {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body)
		}
	}
	ok := `{"name":"AX","projectType":"migrate","sourceSystem":"ax2012","scheduleCron":"0 2 * * *","cutoverAt":"2027-03-01T05:00:00+01:00"}`
	rec := do(r, http.MethodPost, projectsPath, ok, writeDuties, true)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"cutoverAt":"2027-03-01T04:00:00Z"`) {
		t.Fatalf("valid migrate = %d %s", rec.Code, rec.Body)
	}
}

func TestProjects_DutiesAndScope(t *testing.T) {
	r, _ := newProjectRouter()
	if rec := do(r, http.MethodPost, projectsPath, copyBody, duties, true); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty must not create: %d", rec.Code)
	}
	p := createCopy(t, r)
	if rec := do(r, http.MethodGet, projectsPath, "", "org.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("list without duty: %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, projectsPath+"/"+p.ID, "", duties, true); rec.Code != http.StatusOK {
		t.Fatalf("read duty reads: %d", rec.Code)
	}
	if rec := do(r, http.MethodPatch, projectsPath+"/"+p.ID, `{"version":1,"name":"x"}`, duties, true); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty must not patch: %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, projectsPath, "", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, projectsPath, copyBody, writeDuties, true, "X-Passage-Legal-Entity", leSource); rec.Code != http.StatusForbidden {
		t.Fatalf("legal-entity scoped caller: %d", rec.Code)
	}
}

func TestProjects_ListPagesFiltersAndGet(t *testing.T) {
	r, _ := newProjectRouter()
	for _, n := range []string{"C", "A", "B"} {
		rec := do(r, http.MethodPost, projectsPath, `{"name":"`+n+`","projectType":"migrate","sourceSystem":"sap_ecc"}`, writeDuties, true)
		if rec.Code != http.StatusCreated {
			t.Fatal(rec.Body)
		}
	}
	createCopy(t, r)
	var names []string
	cursor := ""
	for i := 0; i < 5; i++ {
		rec := do(r, http.MethodGet, projectsPath+"?limit=2&cursor="+cursor, "", duties, true)
		var page struct {
			Items      []projectBody `json:"items"`
			NextCursor string        `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("list = %d %s", rec.Code, rec.Body)
		}
		for _, p := range page.Items {
			names = append(names, p.Name)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if strings.Join(names, ",") != "A,B,C,USMF to CAMF" {
		t.Fatalf("paged names %v", names)
	}
	rec := do(r, http.MethodGet, projectsPath+"?projectType=copy&status=active", "", duties, true)
	if !strings.Contains(rec.Body.String(), "USMF to CAMF") || strings.Contains(rec.Body.String(), `"name":"A"`) {
		t.Fatalf("filter = %s", rec.Body)
	}
	if rec := do(r, http.MethodGet, projectsPath+"?status=archived", "", duties, true); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty list must be an array: %s", rec.Body)
	}
	for _, q := range []string{"?projectType=x", "?status=x", "?cursor=!!", "?limit=0"} {
		if rec := do(r, http.MethodGet, projectsPath+q, "", duties, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", q, rec.Code)
		}
	}
	if rec := do(r, http.MethodGet, projectsPath+"/00000000-0000-7000-8000-00000000ffff", "", duties, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project = %d", rec.Code)
	}
}

func TestProjects_PatchUnderVersion(t *testing.T) {
	r, _ := newProjectRouter()
	p := createCopy(t, r)
	path := projectsPath + "/" + p.ID
	rec := do(r, http.MethodPatch, path, `{"version":1,"name":"Renamed","scheduleCron":"0 4 * * 1","cutoverAt":"2027-01-01T00:00:00Z"}`, writeDuties, true)
	got := decodeProject(t, rec)
	if rec.Code != http.StatusOK || got.Version != 2 || got.Name != "Renamed" || got.Cron != "0 4 * * 1" || got.CutoverAt != "2027-01-01T00:00:00Z" {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPatch, path, `{"version":1,"name":"Stale"}`, writeDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-VERSION-CONFLICT") {
		t.Fatalf("stale version = %d %s", rec.Code, rec.Body)
	}
	for name, payload := range map[string]string{
		"no version":      `{"name":"x"}`,
		"bad status":      `{"version":2,"status":"paused"}`,
		"bad cron":        `{"version":2,"scheduleCron":"* *"}`,
		"blank name":      `{"version":2,"name":""}`,
		"bad cutover":     `{"version":2,"cutoverAt":"soon"}`,
		"immutable field": `{"version":2,"projectType":"migrate"}`,
	} {
		if rec := do(r, http.MethodPatch, path, payload, writeDuties, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	cleared := decodeProject(t, do(r, http.MethodPatch, path, `{"version":2,"cutoverAt":"","status":"archived"}`, writeDuties, true))
	if cleared.CutoverAt != "" || cleared.Status != "archived" || cleared.Cron != "0 4 * * 1" || cleared.Version != 3 {
		t.Fatalf("clear = %+v", cleared)
	}
	// Archived (version 3): edits are refused until the project is reactivated, which may carry edits of its own.
	if rec := do(r, http.MethodPatch, path, `{"version":3,"name":"Frozen"}`, writeDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-PROJECT-ARCHIVED") {
		t.Fatalf("edit while archived = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPut, path+"/key-rewrite-rules", `{"version":3,"rules":[]}`, writeDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-PROJECT-ARCHIVED") {
		t.Fatalf("rules while archived = %d %s", rec.Code, rec.Body)
	}
	back := decodeProject(t, do(r, http.MethodPatch, path, `{"version":3,"status":"active","cutoverAt":"2027-02-01T00:00:00Z","scheduleCron":null}`, writeDuties, true))
	if back.Status != "active" || back.CutoverAt != "2027-02-01T00:00:00Z" || back.Cron != "0 4 * * 1" || back.Version != 4 {
		t.Fatalf("reactivate = %+v (a null field is left as it is)", back)
	}
	if rec := do(r, http.MethodPatch, path, `{"version":4,"scheduleCron":"*/5 * * * *"}`, writeDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "15 minutes") {
		t.Fatalf("schedule under FR-10.16's 15 minutes = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPatch, projectsPath+"/00000000-0000-7000-8000-00000000ffff", `{"version":1}`, writeDuties, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project = %d", rec.Code)
	}
	other := decodeProject(t, do(r, http.MethodPost, projectsPath, strings.Replace(copyBody, "USMF to CAMF", "Second", 1), writeDuties, true))
	if rec := do(r, http.MethodPatch, projectsPath+"/"+other.ID, `{"version":1,"name":"Renamed"}`, writeDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-PROJECT-NAME-EXISTS") {
		t.Fatalf("rename onto a used name = %d %s", rec.Code, rec.Body)
	}
}

func TestRewriteRules_ReplaceReadAndVersioning(t *testing.T) {
	r, _ := newProjectRouter()
	p := createCopy(t, r)
	path := projectsPath + "/" + p.ID + "/key-rewrite-rules"
	if rec := do(r, http.MethodGet, path, "", duties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("empty = %d %s", rec.Code, rec.Body)
	}
	put := `{"version":1,"rules":[{"field":"@key","from":"USMF","to":"CAMF"},{"field":"dataAreaId","from":"USMF","to":"CAMF"}]}`
	rec := do(r, http.MethodPut, path, put, writeDuties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":2`) {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, path, "", duties, true); !strings.Contains(rec.Body.String(), `"dataAreaId"`) || !strings.Contains(rec.Body.String(), `"version":2`) {
		t.Fatalf("get = %s", rec.Body)
	}
	if rec := do(r, http.MethodPut, path, put, writeDuties, true); rec.Code != http.StatusConflict {
		t.Fatalf("stale put = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPut, path, `{"version":2,"rules":[]}`, writeDuties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("clear = %d %s", rec.Code, rec.Body)
	}
	for name, payload := range map[string]string{
		"no version":  `{"rules":[]}`,
		"empty from":  `{"version":3,"rules":[{"field":"@key","from":"","to":"B"}]}`,
		"same value":  `{"version":3,"rules":[{"field":"@key","from":"A","to":"A"}]}`,
		"shadowed":    `{"version":3,"rules":[{"field":"@key","from":"A","to":"B"},{"field":"@key","from":"A","to":"C"}]}`,
		"prefixed":    `{"version":3,"rules":[{"field":"@key","from":"US","to":"CA"},{"field":"@key","from":"USMF","to":"CAMF"}]}`,
		"blank field": `{"version":3,"rules":[{"field":" ","from":"A","to":"B"}]}`,
	} {
		if rec := do(r, http.MethodPut, path, payload, writeDuties, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPut, path, put, duties, true); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty must not replace rules: %d", rec.Code)
	}
}

func TestRewriteRules_OnlyCopyProjects(t *testing.T) {
	r, _ := newProjectRouter()
	rec := do(r, http.MethodPost, projectsPath, `{"name":"AX","projectType":"migrate","sourceSystem":"ax2012"}`, writeDuties, true)
	id := decodeProject(t, rec).ID
	path := projectsPath + "/" + id + "/key-rewrite-rules"
	for _, c := range []struct{ method, path, payload string }{
		{http.MethodGet, path, ""},
		{http.MethodPut, path, `{"version":1,"rules":[]}`},
		{http.MethodPost, path + ":preview", `{"keys":["A"]}`},
	} {
		if rec := do(r, c.method, c.path, c.payload, writeDuties, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "MIG-PROJECT-NOT-COPY") {
			t.Errorf("%s: %d %s", c.method, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodGet, projectsPath+"/00000000-0000-7000-8000-00000000ffff/key-rewrite-rules", "", duties, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project = %d", rec.Code)
	}
}

func TestRewriteRules_PreviewStoredAndTrialRules(t *testing.T) {
	r, _ := newProjectRouter()
	p := createCopy(t, r)
	path := projectsPath + "/" + p.ID + "/key-rewrite-rules"
	do(r, http.MethodPut, path, `{"version":1,"rules":[{"field":"@key","from":"USMF-","to":"CAMF-"}]}`, writeDuties, true)

	rec := do(r, http.MethodPost, path+":preview", `{"keys":["USMF-C001","DE-C002"]}`, duties, true)
	want := `{"key":"USMF-C001","rewritten":"CAMF-C001","changed":true},{"key":"DE-C002","rewritten":"DE-C002","changed":false}`
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("stored preview = %d %s", rec.Code, rec.Body)
	}
	trial := `{"keys":["USMF-C001"],"rules":[{"field":"@key","from":"USMF-","to":"MXMF-"}]}`
	if rec := do(r, http.MethodPost, path+":preview", trial, duties, true); !strings.Contains(rec.Body.String(), `"rewritten":"MXMF-C001"`) {
		t.Fatalf("trial preview = %s", rec.Body)
	}
	if rec := do(r, http.MethodGet, path, "", duties, true); !strings.Contains(rec.Body.String(), `"CAMF-"`) || strings.Contains(rec.Body.String(), "MXMF") || !strings.Contains(rec.Body.String(), `"version":2`) {
		t.Fatalf("preview must not save: %s", rec.Body)
	}
	if rec := do(r, http.MethodPost, path+":preview", `{"keys":["A"],"rules":[]}`, duties, true); !strings.Contains(rec.Body.String(), `"changed":false`) {
		t.Fatalf("explicit empty rules rewrite nothing: %s", rec.Body)
	}
	for name, payload := range map[string]string{
		"no keys":   `{"keys":[]}`,
		"blank key": `{"keys":[""]}`,
		"bad rules": `{"keys":["A"],"rules":[{"field":"@key","from":"A","to":"A"}]}`,
		"too many":  `{"keys":["` + strings.Repeat(`a","`, 200) + `a"]}`,
	} {
		if rec := do(r, http.MethodPost, path+":preview", payload, duties, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestProjects_StoreFailureIsAProblem(t *testing.T) {
	r, s := newProjectRouter()
	s.SaveErr = http.ErrAbortHandler
	if rec := do(r, http.MethodPost, projectsPath, copyBody, writeDuties, true); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "abort") {
		t.Fatalf("store failure = %d %s", rec.Code, rec.Body)
	}
}

const axProjectBody = `{"name":"AX customers","projectType":"migrate","sourceSystem":"ax2012"}`

type blockingExtractor struct {
	started chan struct{}
	release chan struct{}
}

func (b blockingExtractor) Extract(context.Context, string) ([]domain.Record, error) {
	close(b.started)
	<-b.release
	return records(), nil
}

func runWithProject(project string) string {
	return strings.Replace(body, `"mode"`, `"projectId":"`+project+`","mode"`, 1)
}

func TestProjectRuns_RecordProjectFilterAndInheritOnRerun(t *testing.T) {
	r, _ := newProjectRouter()
	pj := decodeProject(t, do(r, http.MethodPost, projectsPath, axProjectBody, writeDuties, true))
	rec := do(r, http.MethodPost, runsPath, runWithProject(pj.ID), duties, true)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"projectId":"`+pj.ID+`"`) {
		t.Fatalf("project run = %d %s", rec.Code, rec.Body)
	}
	first := decode(t, rec)
	// a free project can run again, and an unlinked run does not appear under the project
	if rec := do(r, http.MethodPost, runsPath, body, duties, true); rec.Code != http.StatusAccepted {
		t.Fatalf("unlinked run = %d %s", rec.Code, rec.Body)
	}
	again := do(r, http.MethodPost, runsPath+"/"+first.Run.ID+":rerun", "", duties, true)
	if again.Code != http.StatusAccepted || !strings.Contains(again.Body.String(), `"projectId":"`+pj.ID+`"`) {
		t.Fatalf("rerun must keep the project: %d %s", again.Code, again.Body)
	}
	list := do(r, http.MethodGet, runsPath+"?projectId="+pj.ID, "", duties, true)
	if list.Code != http.StatusOK || strings.Count(list.Body.String(), `"projectId"`) != 2 {
		t.Fatalf("filtered list = %d %s", list.Code, list.Body)
	}
	if rec := do(r, http.MethodGet, runsPath+"?projectId=nope", "", duties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad projectId filter = %d", rec.Code)
	}
}

func TestProjectRuns_Refusals(t *testing.T) {
	r, _ := newProjectRouter()
	pj := decodeProject(t, do(r, http.MethodPost, projectsPath, axProjectBody, writeDuties, true))
	cp := createCopy(t, r)
	for name, c := range map[string]struct {
		payload string
		status  int
		code    string
	}{
		"malformed id":    {runWithProject("nope"), http.StatusBadRequest, `"projectId"`},
		"unknown project": {runWithProject("00000000-0000-7000-8000-00000000ffff"), http.StatusUnprocessableEntity, "MIG-PROJECT-UNKNOWN"},
		"copy project":    {runWithProject(cp.ID), http.StatusUnprocessableEntity, "MIG-PROJECT-NOT-MIGRATE"},
		"other system":    {strings.Replace(runWithProject(pj.ID), "ax2012", "sap_s4", 1), http.StatusBadRequest, `"sourceSystem"`},
	} {
		rec := do(r, http.MethodPost, runsPath, c.payload, duties, true)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body)
		}
	}
	archived := `{"version":1,"status":"archived"}`
	if rec := do(r, http.MethodPatch, projectsPath+"/"+pj.ID, archived, writeDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("archive = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPost, runsPath, runWithProject(pj.ID), duties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-PROJECT-ARCHIVED") {
		t.Fatalf("archived project = %d %s", rec.Code, rec.Body)
	}
}

func TestProjectRuns_SecondStartWhileRunningIsConflictAndClaimReleases(t *testing.T) {
	s := store.NewMem()
	ex := blockingExtractor{started: make(chan struct{}), release: make(chan struct{})}
	slow := router(s, ex, domain.NewMemLoader())
	pj := decodeProject(t, do(slow, http.MethodPost, projectsPath, axProjectBody, writeDuties, true))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- do(slow, http.MethodPost, runsPath, runWithProject(pj.ID), duties, true) }()
	<-ex.started
	rec := do(slow, http.MethodPost, runsPath, runWithProject(pj.ID), duties, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-RUN-ACTIVE") {
		t.Fatalf("second start = %d %s", rec.Code, rec.Body)
	}
	conflict := rec.Body.String()
	close(ex.release)
	first := <-done
	if first.Code != http.StatusAccepted {
		t.Fatalf("first run = %d %s", first.Code, first.Body)
	}
	// BR-10.2: the 409 names the active run, and that is the id the run is recorded under when it finishes
	if id := decode(t, first).Run.ID; !strings.Contains(conflict, "Run "+id+" is already active") {
		t.Fatalf("409 %s does not name the active run %s", conflict, id)
	}
	fast := router(s, rowsExtractor{records()}, domain.NewMemLoader())
	if rec := do(fast, http.MethodPost, runsPath, runWithProject(pj.ID), duties, true); rec.Code != http.StatusAccepted {
		t.Fatalf("claim must be released after the run: %d %s", rec.Code, rec.Body)
	}
}

type panickingExtractor struct{}

func (panickingExtractor) Extract(context.Context, string) ([]domain.Record, error) {
	panic("extractor bug")
}

func TestProjectRuns_ClaimReleasedWhenTheRunFailsOrPanics(t *testing.T) {
	s := store.NewMem()
	free := router(s, rowsExtractor{records()}, domain.NewMemLoader())
	pj := decodeProject(t, do(free, http.MethodPost, projectsPath, axProjectBody, writeDuties, true))
	for name, ex := range map[string]domain.Extractor{"failure": failingExtractor{}, "panic": panickingExtractor{}} {
		if rec := do(router(s, ex, domain.NewMemLoader()), http.MethodPost, runsPath, runWithProject(pj.ID), duties, true); rec.Code < 500 {
			t.Fatalf("%s run = %d %s", name, rec.Code, rec.Body)
		}
		if rec := do(free, http.MethodPost, runsPath, runWithProject(pj.ID), duties, true); rec.Code != http.StatusAccepted {
			t.Fatalf("claim must be released after a %s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
