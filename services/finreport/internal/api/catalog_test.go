package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// catalogStore records what the standard-definition, retire and hierarchy
// handlers pass to the store. Unset methods panic through the nil Store.
type catalogStore struct {
	Store

	installed  []domain.Definition
	actor      string
	retired    string
	retireN    int
	retireErr  error
	savedH     *domain.Hierarchy
	ifMatch    *int
	owner      string
	members    []domain.Member
	pubCode    string
	memVersion int
}

func (c *catalogStore) InstallStandard(_ context.Context, defs []domain.Definition, actor string) ([]store.InstallResult, error) {
	c.installed, c.actor = defs, actor
	out := []store.InstallResult{}
	for _, d := range defs {
		out = append(out, store.InstallResult{Code: d.Code, Action: "installed", Version: 1})
	}
	return out, nil
}

func (c *catalogStore) RetireDefinition(_ context.Context, code string) (int, error) {
	c.retired = code
	return c.retireN, c.retireErr
}

func (c *catalogStore) ListHierarchies(context.Context) ([]store.HierarchySummary, error) {
	return []store.HierarchySummary{{Code: "HIER-BS", Name: "Balance sheet", Subject: "main_account", LatestVersion: 2, UnassignedCount: 1}}, nil
}

func (c *catalogStore) SaveHierarchy(_ context.Context, h domain.Hierarchy, ifMatch *int, owner string) (domain.Hierarchy, int, error) {
	c.savedH, c.ifMatch, c.owner = &h, ifMatch, owner
	return h, 4, nil
}

func (c *catalogStore) GetHierarchy(_ context.Context, code string, version int) (domain.Hierarchy, int, error) {
	c.memVersion = version
	return domain.Hierarchy{Code: code, Name: "x"}, 3, nil
}

func (c *catalogStore) PublishHierarchy(_ context.Context, code, actor string, members []domain.Member) (domain.Hierarchy, domain.Membership, error) {
	c.pubCode, c.actor, c.members = code, actor, members
	return domain.Hierarchy{Code: code, Version: 1, Status: "published"},
		domain.Membership{Assigned: map[string]string{"a1": "CASH"}, Unassigned: []string{}}, nil
}

func (c *catalogStore) HierarchyMembership(_ context.Context, _ string, version int) (domain.Membership, int, error) {
	c.memVersion = version
	return domain.Membership{Assigned: map[string]string{"a1": "CASH"}, Unassigned: []string{"a9"}}, 3, nil
}

func catalogRouter(c *catalogStore) chi.Router {
	r := chi.NewRouter()
	New(c).Register(r)
	return r
}

func TestStandardDefinitions_ListAndInstall(t *testing.T) {
	c := &catalogStore{}
	w := call(catalogRouter(c), http.MethodGet, "/v1/reporting/standard-definitions", "", nil, DutyMaintain)
	if w.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", w.Code, w.Body.String())
	}
	var list struct{ Items []standardDTO }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range list.Items {
		got[it.Code] = it.Rows > 0 && it.Columns > 0
	}
	for _, code := range []string{"TB-STD", "BS-STD", "PL-STD", "PL-FUNC", "CF-STD"} {
		if !got[code] {
			t.Errorf("standard list misses %s or its shape: %s", code, w.Body.String())
		}
	}

	w = call(catalogRouter(c), http.MethodPost, "/v1/reporting/standard-definitions:install", "", nil, DutyMaintain)
	if w.Code != http.StatusOK {
		t.Fatalf("install status %d: %s", w.Code, w.Body.String())
	}
	if len(c.installed) != 5 || c.actor != "u1" {
		t.Fatalf("installed %d definitions as %q, want 5 as u1", len(c.installed), c.actor)
	}
}

func TestStandardDefinitions_InstallNeedsMaintainDuty(t *testing.T) {
	c := &catalogStore{}
	w := call(catalogRouter(c), http.MethodPost, "/v1/reporting/standard-definitions:install", "", nil, DutyView)
	if w.Code != http.StatusForbidden || c.installed != nil {
		t.Fatalf("status %d, installed %v; want 403 and no store call", w.Code, c.installed)
	}
}

func TestRetireDefinition(t *testing.T) {
	c := &catalogStore{retireN: 2}
	w := call(catalogRouter(c), http.MethodPost, "/v1/reporting/definitions/BS-STD:retire", "", nil, DutyMaintain)
	if w.Code != http.StatusOK || c.retired != "BS-STD" {
		t.Fatalf("status %d retired %q: %s", w.Code, c.retired, w.Body.String())
	}
	var body struct {
		Status          string
		RetiredVersions int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Status != "retired" || body.RetiredVersions != 2 {
		t.Fatalf("body %s (err %v)", w.Body.String(), err)
	}

	c = &catalogStore{retireErr: httpx.NewProblem(http.StatusNotFound, store.CodeDefinitionNotFound, "no published version")}
	w = call(catalogRouter(c), http.MethodPost, "/v1/reporting/definitions/NEW-DEF:retire", "", nil, DutyMaintain)
	if w.Code != http.StatusNotFound || problemCode(t, w) != store.CodeDefinitionNotFound {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

const hierarchyBody = `{"code":"HIER-BS","name":"Balance sheet","subject":"main_account",%s
	"nodes":[{"code":"ROOT","label":"Total"},{"code":"CASH","label":"Cash","parent":"ROOT","rules":[{"from":"1000","to":"1099"}]}]}`

func hierarchyJSON(extra string) string {
	return fmt.Sprintf(hierarchyBody, extra)
}

func TestPutHierarchy(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		headers    map[string]string
		wantStatus int
		wantCode   string
		wantStrict bool
	}{
		{"strict defaults to true for accounts", "HIER-BS", hierarchyJSON(""), nil, http.StatusOK, "", true},
		{"explicit strict false is kept", "HIER-BS", hierarchyJSON(`"strict":false,`), nil, http.StatusOK, "", false},
		{"If-Match is passed on", "HIER-BS", hierarchyJSON(""), map[string]string{"If-Match": `"3"`}, http.StatusOK, "", true},
		{"path and body codes differ", "HIER-PL", hierarchyJSON(""), nil, http.StatusUnprocessableEntity, "COM-0001", false},
		{"unknown field", "HIER-BS", hierarchyJSON(`"colour":"red",`), nil, http.StatusBadRequest, "COM-0002", false},
		{"rules on a parent node", "HIER-BS", `{"code":"HIER-BS","name":"x","subject":"main_account","nodes":[
			{"code":"ROOT","label":"r","rules":[{"values":["*"]}]},{"code":"A","label":"a","parent":"ROOT"}]}`,
			nil, http.StatusUnprocessableEntity, "COM-0001", false},
		{"bad If-Match", "HIER-BS", hierarchyJSON(""), map[string]string{"If-Match": "abc"}, http.StatusBadRequest, "COM-0002", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &catalogStore{}
			w := call(catalogRouter(c), http.MethodPut, "/v1/reporting/hierarchies/"+tc.path, tc.body, tc.headers, DutyMaintain)
			if w.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantCode != "" {
				if got := problemCode(t, w); got != tc.wantCode {
					t.Fatalf("code %s, want %s", got, tc.wantCode)
				}
				if c.savedH != nil {
					t.Fatal("an invalid hierarchy reached the store")
				}
				return
			}
			if c.savedH.Strict != tc.wantStrict || c.owner != "u1" || w.Header().Get("ETag") != `"4"` {
				t.Fatalf("saved strict=%v owner=%q etag=%q", c.savedH.Strict, c.owner, w.Header().Get("ETag"))
			}
			if tc.headers["If-Match"] != "" && (c.ifMatch == nil || *c.ifMatch != 3) {
				t.Fatalf("If-Match = %v, want 3", c.ifMatch)
			}
		})
	}
}

func TestPutHierarchy_DimensionHierarchyDefaultsToNonStrict(t *testing.T) {
	c := &catalogStore{}
	body := `{"name":"Cost centers","subject":"dimension_value","dimensionAttribute":"COST_CENTER",
		"nodes":[{"code":"EMEA","label":"EMEA","rules":[{"attribute":"region","values":["EMEA"]}]}]}`
	w := call(catalogRouter(c), http.MethodPut, "/v1/reporting/hierarchies/HIER-CC", body, nil, DutyMaintain)
	if w.Code != http.StatusOK || c.savedH.Strict || c.savedH.Code != "HIER-CC" {
		t.Fatalf("status %d strict=%v code=%q: %s", w.Code, c.savedH != nil && c.savedH.Strict, c.savedH.Code, w.Body.String())
	}
}

func TestPublishHierarchy(t *testing.T) {
	c := &catalogStore{}
	w := call(catalogRouter(c), http.MethodPost, "/v1/reporting/hierarchies/HIER-BS:publish",
		`{"members":[{"id":"a1","key":"1000"},{"id":"a2","key":"1050","attributes":{"type":"bank"}}]}`, nil, DutyMaintain)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if c.pubCode != "HIER-BS" || c.actor != "u1" || len(c.members) != 2 || c.members[1].Attributes["type"] != "bank" {
		t.Fatalf("publish got code=%q actor=%q members=%+v", c.pubCode, c.actor, c.members)
	}

	for name, tc := range map[string]struct {
		path, body string
		status     int
	}{
		"unknown action":     {"HIER-BS:retire", "", http.StatusNotFound},
		"no action":          {"HIER-BS", "", http.StatusNotFound},
		"unknown body field": {"HIER-BS:publish", `{"accounts":[]}`, http.StatusBadRequest},
	} {
		c := &catalogStore{}
		w := call(catalogRouter(c), http.MethodPost, "/v1/reporting/hierarchies/"+tc.path, tc.body, nil, DutyMaintain)
		if w.Code != tc.status || c.pubCode != "" {
			t.Errorf("%s: status %d, store called with %q; want %d and no call", name, w.Code, c.pubCode, tc.status)
		}
	}
}

func TestHierarchyMembers(t *testing.T) {
	c := &catalogStore{}
	w := call(catalogRouter(c), http.MethodGet, "/v1/reporting/hierarchies/HIER-BS/members?version=3", "", nil, DutyMaintain)
	if w.Code != http.StatusOK || c.memVersion != 3 {
		t.Fatalf("status %d version %d: %s", w.Code, c.memVersion, w.Body.String())
	}
	var body struct {
		Version    int
		Assigned   map[string]string
		Unassigned []string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Version != 3 || body.Assigned["a1"] != "CASH" || len(body.Unassigned) != 1 {
		t.Fatalf("body %s (err %v)", w.Body.String(), err)
	}
	for _, q := range []string{"draft", "0", "x"} {
		w := call(catalogRouter(&catalogStore{}), http.MethodGet, "/v1/reporting/hierarchies/HIER-BS/members?version="+q, "", nil, DutyMaintain)
		if w.Code != http.StatusBadRequest {
			t.Errorf("version=%s: status %d, want 400", q, w.Code)
		}
	}
}

func TestGetHierarchy_DraftCarriesETag(t *testing.T) {
	c := &catalogStore{}
	w := call(catalogRouter(c), http.MethodGet, "/v1/reporting/hierarchies/HIER-BS?version=draft", "", nil, DutyMaintain)
	if w.Code != http.StatusOK || w.Header().Get("ETag") != `"3"` || c.memVersion != store.Draft {
		t.Fatalf("status %d etag %q version %d", w.Code, w.Header().Get("ETag"), c.memVersion)
	}
	w = call(catalogRouter(c), http.MethodGet, "/v1/reporting/hierarchies", "", nil, DutyView)
	if w.Code != http.StatusForbidden {
		t.Fatalf("hierarchy list with the view duty only: status %d, want 403", w.Code)
	}
}

// draftAndPublished lists one published hierarchy with a draft in progress and
// one that was never published.
type draftAndPublished struct{ catalogStore }

func (draftAndPublished) ListHierarchies(context.Context) ([]store.HierarchySummary, error) {
	return []store.HierarchySummary{
		{Code: "HIER-BS", Name: "Balance sheet", Subject: "main_account", LatestVersion: 2, HasDraft: true},
		{Code: "HIER-NEW", Name: "Not yet published", Subject: "dimension_value", HasDraft: true},
	}, nil
}

// A statement runner lists only published hierarchies for the run form; a
// maintainer still sees drafts.
func TestListHierarchies_RunnerSeesPublishedOnly(t *testing.T) {
	r := chi.NewRouter()
	New(&draftAndPublished{}).Register(r)
	var body struct{ Items []store.HierarchySummary }
	w := call(r, http.MethodGet, "/v1/reporting/hierarchies", "", nil, DutyRun)
	if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != http.StatusOK || err != nil {
		t.Fatalf("runner: status %d: %s", w.Code, w.Body.String())
	}
	if len(body.Items) != 1 || body.Items[0].Code != "HIER-BS" || body.Items[0].HasDraft {
		t.Fatalf("runner items = %+v, want HIER-BS only, without its draft flag", body.Items)
	}
	w = call(r, http.MethodGet, "/v1/reporting/hierarchies", "", nil, DutyMaintain)
	if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != http.StatusOK || err != nil || len(body.Items) != 2 || !body.Items[0].HasDraft {
		t.Fatalf("maintainer: status %d items %+v", w.Code, body.Items)
	}
	if w := call(r, http.MethodGet, "/v1/reporting/hierarchies/HIER-BS", "", nil, DutyRun); w.Code != http.StatusForbidden {
		t.Fatalf("runner reading a hierarchy body: status %d, want 403", w.Code)
	}
}
