package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const keyedSpecMapping = `{"entity":"customer","version":1,"naturalKey":["name"],"fields":[{"source":"Name","target":"name","type":"string"},{"source":"Group","target":"segment","type":"string","enum":{"10":"retail"}}],"tests":[{"name":"retail","input":{"Name":"Acme","Group":"10"},"expect":{"name":"Acme","segment":"retail"}}]}`

type scopedSpec struct {
	ID         string   `json:"id"`
	Version    int      `json:"version"`
	ProjectID  string   `json:"projectId"`
	NaturalKey []string `json:"naturalKey"`
}

func publishIn(r http.Handler, project, mapping string) (int, scopedSpec, string) {
	body := `{"mapping":` + mapping
	if project != "" {
		body += `,"projectId":"` + project + `"`
	}
	rec := do(r, http.MethodPost, specsPath, body+`}`, writeDuty, true)
	var out scopedSpec
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func migrateProject(t *testing.T, r http.Handler, name string) string {
	t.Helper()
	rec := do(r, http.MethodPost, projectsPath, `{"name":"`+name+`","projectType":"migrate","sourceSystem":"ax2012"}`, writeDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("project = %d %s", rec.Code, rec.Body)
	}
	return decodeProject(t, rec).ID
}

func TestPublishSpec_ProjectScopeAndNaturalKey(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()}, domain.NewMemLoader())
	pa, pb := migrateProject(t, r, "AX A"), migrateProject(t, r, "AX B")

	code, wide, body := publishIn(r, "", keyedSpecMapping)
	if code != http.StatusCreated || wide.ProjectID != "" || len(wide.NaturalKey) != 1 || wide.NaturalKey[0] != "name" {
		t.Fatalf("tenant-wide = %d %s", code, body)
	}
	// The same entity and version publishes once per project.
	code, inA, body := publishIn(r, pa, keyedSpecMapping)
	if code != http.StatusCreated || inA.ProjectID != pa || inA.ID == wide.ID {
		t.Fatalf("project A = %d %s", code, body)
	}
	if code, inB, _ := publishIn(r, pb, keyedSpecMapping); code != http.StatusCreated || inB.ID == inA.ID {
		t.Fatalf("project B = %d", code)
	}
	// Publishing the same content again in a scope replays; different content under the same version conflicts there only.
	if code, again, _ := publishIn(r, pa, keyedSpecMapping); code != http.StatusOK || again.ID != inA.ID {
		t.Fatalf("replay = %d", code)
	}
	changed := strings.ReplaceAll(keyedSpecMapping, `"retail"`, `"wholesale"`)
	if code, _, body := publishIn(r, pa, changed); code != http.StatusConflict || !strings.Contains(body, store.CodeSpecVersionUsed) {
		t.Fatalf("conflict = %d %s", code, body)
	}

	// Listing narrows to a project or to the tenant-wide specs.
	ids := func(q string) string {
		rec := do(r, http.MethodGet, specsPath+q, "", duties, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s = %d %s", q, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	if l := ids("?projectId=" + pa); !strings.Contains(l, inA.ID) || strings.Contains(l, wide.ID) || strings.Contains(l, pb) {
		t.Fatalf("project A list = %s", l)
	}
	if l := ids("?projectId=none"); !strings.Contains(l, wide.ID) || strings.Contains(l, inA.ID) {
		t.Fatalf("tenant-wide list = %s", l)
	}
	if l := ids(""); !(strings.Contains(l, wide.ID) && strings.Contains(l, inA.ID)) {
		t.Fatalf("full list = %s", l)
	}
	// Three specs share (customer, 1): a page of one must still walk them all.
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 5; i++ {
		rec := do(r, http.MethodGet, specsPath+"?limit=1"+cursor, "", duties, true)
		var page struct {
			Items []scopedSpec `json:"items"`
			Next  string       `json:"nextCursor"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &page)
		for _, it := range page.Items {
			seen[it.ID] = true
		}
		if page.Next == "" {
			break
		}
		cursor = "&cursor=" + page.Next
	}
	if len(seen) != 3 {
		t.Fatalf("paging saw %d of 3 specs", len(seen))
	}
	if rec := do(r, http.MethodGet, specsPath+"?projectId=nope", "", duties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad projectId filter = %d", rec.Code)
	}
}

func TestPublishSpec_ProjectRefusals(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()}, domain.NewMemLoader())
	copyID := createCopy(t, r).ID
	archived := migrateProject(t, r, "Old")
	if rec := do(r, http.MethodPatch, projectsPath+"/"+archived, `{"version":1,"status":"archived"}`, writeDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("archive = %d", rec.Code)
	}
	for name, c := range map[string]struct {
		project string
		status  int
		text    string
	}{
		"not a uuid":   {"x", http.StatusBadRequest, "must be a UUID"},
		"unknown":      {testkit.TenantID(5), http.StatusUnprocessableEntity, "MIG-PROJECT-UNKNOWN"},
		"copy project": {copyID, http.StatusUnprocessableEntity, "only a migrate project"},
		"archived":     {archived, http.StatusConflict, "MIG-PROJECT-ARCHIVED"},
	} {
		if code, _, body := publishIn(r, c.project, keyedSpecMapping); code != c.status || !strings.Contains(body, c.text) {
			t.Errorf("%s = %d %s", name, code, body)
		}
	}
	// A natural key the rules do not produce stops the publish.
	bad := strings.Replace(keyedSpecMapping, `"naturalKey":["name"]`, `"naturalKey":["customer_no"]`, 1)
	if code, _, body := publishIn(r, "", bad); code != http.StatusUnprocessableEntity && code != http.StatusBadRequest || !strings.Contains(body, "not produced by any field rule") {
		t.Fatalf("bad key = %d %s", code, body)
	}
}

func TestCreateRun_FromProjectSpecAdoptsItsProject(t *testing.T) {
	loader := domain.NewMemLoader()
	r := router(store.NewMem(), rowsExtractor{records()}, loader)
	pa, pb := migrateProject(t, r, "AX A"), migrateProject(t, r, "AX B")
	_, inA, _ := publishIn(r, pa, keyedSpecMapping)

	run := func(project string) (int, string) {
		extra := ""
		if project != "" {
			extra = `,"projectId":"` + project + `"`
		}
		rec := do(r, http.MethodPost, runsPath, `{"sourceSystem":"ax2012","mode":"rehearsal","mappingSpecId":"`+inA.ID+`"`+extra+`}`, duties, true)
		return rec.Code, rec.Body.String()
	}
	if code, body := run(pb); code != http.StatusUnprocessableEntity || !strings.Contains(body, "MIG-SPEC-PROJECT") {
		t.Fatalf("another project's spec = %d %s", code, body)
	}
	code, body := run("")
	if code != http.StatusAccepted || !strings.Contains(body, `"projectId":"`+pa+`"`) {
		t.Fatalf("spec project adopted = %d %s", code, body)
	}
	if code, body := run(pa); code != http.StatusAccepted {
		t.Fatalf("explicit project = %d %s", code, body)
	}
}

// A scheduled run reads the newest spec of its own project, then the tenant-wide one, and never another project's,
// even when the binding (shared by every project on its connection profile) names that spec.
func TestSync_ScheduledRunResolvesTheSpecOfItsOwnProject(t *testing.T) {
	s := newSyncRig(t) // publishes tenant-wide party v1
	const pubDuties = liveDuties + ",migration.spec.write"
	other := do(s.r, http.MethodPost, projectsPath, `{"name":"Other cutover","projectType":"migrate","sourceSystem":"custom_acme"}`, liveDuties, true)
	if other.Code != http.StatusCreated {
		t.Fatalf("other project = %d %s", other.Code, other.Body)
	}
	otherID := decodeProject(t, other).ID
	publish := func(project string, version int) string {
		t.Helper()
		body := strings.Replace(partyPublish, `"version":1`, `"version":`+itoa(version), 1)
		body = strings.TrimSuffix(body, "}") + `,"projectId":"` + project + `"}`
		rec := do(s.r, http.MethodPost, specsPath, body, pubDuties, true)
		var out scopedSpec
		if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("publish in %s = %d %s", project, rec.Code, rec.Body)
		}
		return out.ID
	}
	// The other project's spec is the newest version of the entity; this project's is older.
	otherSpec := publish(otherID, 9)
	ownSpec := publish(s.project.ID, 2)

	lastSpec := func() string {
		t.Helper()
		rec := do(s.r, http.MethodGet, runsPath+"?bindingId="+s.binding, "", liveDuties, true)
		var out struct {
			Items []struct {
				MappingSpecID string `json:"mappingSpecId"`
				ProjectID     string `json:"projectId"`
			} `json:"items"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Items) == 0 {
			t.Fatalf("runs = %d %s", rec.Code, rec.Body)
		}
		if out.Items[0].ProjectID != s.project.ID {
			t.Fatalf("run project = %s, want %s", out.Items[0].ProjectID, s.project.ID)
		}
		return out.Items[0].MappingSpecID
	}
	s.sched.Tick(context.Background())
	if st := s.status(); st.LastStatus != "succeeded" {
		t.Fatalf("scheduled run: %+v", st)
	}
	if got := lastSpec(); got != ownSpec {
		t.Fatalf("scheduled run used spec %s, want the project's own %s (other project's is %s)", got, ownSpec, otherSpec)
	}

	// The binding names the other project's spec: the scheduled run fails instead of loading with it.
	rec := do(s.r, http.MethodGet, bindPath+"/"+s.binding, "", liveDuties, true)
	var b struct{ Version int }
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if rec := do(s.r, http.MethodPatch, bindPath+"/"+s.binding, `{"version":`+itoa(b.Version)+`,"mappingSpecId":"`+otherSpec+`"}`, liveDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("bind other spec = %d %s", rec.Code, rec.Body)
	}
	st := s.runNow()
	if st.LastStatus == "succeeded" || !strings.Contains(st.LastError, "MIG-SPEC-PROJECT") {
		t.Fatalf("binding naming another project's spec: %+v", st)
	}
	if got := lastSpec(); got == otherSpec {
		t.Fatal("a run was recorded with another project's spec")
	}
}
