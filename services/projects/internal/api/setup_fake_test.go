package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// This file gives fakeStore the setup and plan methods of store.Store with the
// same If-Match, idempotency, plan-kind and scheduling rules as the Postgres
// store (which store_test.go checks against a database).

func (f *fakeStore) UpsertProjectGroup(_ context.Context, g domain.ProjectGroup, expected int) (domain.ProjectGroup, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, exists := f.groups[g.ID]
	if err := store.CheckExpected(expected, exists, cur.Version); err != nil {
		return domain.ProjectGroup{}, false, err
	}
	g.Version = cur.Version + 1
	g.UpdatedAt = time.Now().UTC()
	f.groups[g.ID] = g
	return g, !exists, nil
}

func (f *fakeStore) ListProjectGroups(context.Context) ([]domain.ProjectGroup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.ProjectGroup{}
	for _, g := range f.groups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) UpsertPostingProfile(_ context.Context, pr domain.PostingProfile, expected int) (domain.PostingProfile, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, exists := f.profiles[pr.Key]
	if err := store.CheckExpected(expected, exists, cur.Version); err != nil {
		return domain.PostingProfile{}, false, err
	}
	pr.Version = cur.Version + 1
	f.profiles[pr.Key] = pr
	return pr, !exists, nil
}

func (f *fakeStore) ListPostingProfiles(context.Context) ([]domain.PostingProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.PostingProfile, 0, len(f.profiles))
	for _, p := range f.profiles {
		out = append(out, p)
	}
	key := func(k domain.ProfileKey) string { return k.ProjectGroup + "|" + k.Category + "|" + k.LineProperty }
	sort.Slice(out, func(i, j int) bool { return key(out[i].Key) < key(out[j].Key) })
	return out, nil
}

func (f *fakeStore) CreatePlanVersion(_ context.Context, projectID string, in store.NewPlan, key string) (domain.PlanVersion, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[projectID]; !ok {
		return domain.PlanVersion{}, false, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "project not found")
	}
	if id, ok := f.planKeys[key]; ok {
		pv := f.plans[id]
		from := ""
		if pv.CopiedFromVersionID != nil {
			from = *pv.CopiedFromVersionID
		}
		if pv.ProjectID != projectID || pv.Kind != in.Kind || from != in.CopyFromVersionID {
			return domain.PlanVersion{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "key reused")
		}
		return pv, false, nil
	}
	var source *domain.PlanVersion
	if in.CopyFromVersionID != "" {
		src, ok := f.plans[in.CopyFromVersionID]
		if !ok || src.ProjectID != projectID {
			return domain.PlanVersion{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodePlanKind, "copyFromVersionId is not a plan version of this project")
		}
		source = &src
	}
	var kinds []domain.PlanKind
	seq := 0
	for _, pv := range f.plans {
		if pv.ProjectID == projectID {
			if pv.SupersededByVersionID == nil {
				kinds = append(kinds, pv.Kind)
			}
			seq++
		}
	}
	locked, err := domain.CheckNewPlan(in.Kind, source, kinds)
	if err != nil {
		return domain.PlanVersion{}, false, err
	}
	pv := domain.PlanVersion{ID: ids.New(), ProjectID: projectID, Kind: in.Kind, Sequence: seq + 1, Locked: locked, Version: 1, CreatedAt: time.Now().UTC()}
	if source != nil {
		from := source.ID
		pv.CopiedFromVersionID = &from
		f.nodes[pv.ID] = append([]domain.WBSNode(nil), f.nodes[source.ID]...)
		f.links[pv.ID] = append([]domain.WBSLink(nil), f.links[source.ID]...)
		if in.Kind == domain.KindSimulation {
			f.planBase[pv.ID] = domain.Fingerprints(domain.ContentByCode(f.nodes[source.ID], f.links[source.ID]))
		}
	}
	f.plans[pv.ID] = pv
	f.planKeys[key] = pv.ID
	return pv, true, nil
}

func (f *fakeStore) ListPlanVersions(_ context.Context, projectID string) ([]domain.PlanVersion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[projectID]; !ok {
		return nil, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "project not found")
	}
	out := []domain.PlanVersion{}
	for _, pv := range f.plans {
		if pv.ProjectID == projectID {
			out = append(out, pv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out, nil
}

// wbsOf returns the plan with copies of its nodes and links (lock held).
func (f *fakeStore) wbsOf(projectID, planID string) (store.WBS, error) {
	pv, ok := f.plans[planID]
	if !ok || pv.ProjectID != projectID {
		return store.WBS{}, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "plan version not found")
	}
	return store.WBS{Plan: pv, Nodes: append([]domain.WBSNode{}, f.nodes[planID]...), Links: append([]domain.WBSLink{}, f.links[planID]...)}, nil
}

func (f *fakeStore) LoadWBS(_ context.Context, projectID, planID string) (store.WBS, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wbsOf(projectID, planID)
}

func (f *fakeStore) ReplaceWBS(_ context.Context, projectID, planID string, expected int,
	nodes []domain.WBSNodeInput, deps []domain.WBSDependencyInput) (store.WBS, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, err := f.wbsOf(projectID, planID)
	if err != nil {
		return store.WBS{}, err
	}
	if w.Plan.Version != expected {
		return store.WBS{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	if w.Plan.Locked {
		return store.WBS{}, &domain.Error{Code: domain.CodePlanLocked, Detail: "locked"}
	}
	f.setWBS(planID, nodes, deps)
	w.Plan.Version++
	f.plans[planID] = w.Plan
	return f.wbsOf(projectID, planID)
}

// setWBS stores a WBS replacement under fresh node ids (lock held).
func (f *fakeStore) setWBS(planID string, nodes []domain.WBSNodeInput, deps []domain.WBSDependencyInput) {
	idOf := map[string]string{}
	for _, n := range nodes {
		idOf[n.WBSCode] = ids.New()
	}
	out := make([]domain.WBSNode, 0, len(nodes))
	for _, n := range nodes {
		var parent *string
		if n.ParentWBSCode != nil {
			pid := idOf[*n.ParentWBSCode]
			parent = &pid
		}
		out = append(out, domain.WBSNode{ID: idOf[n.WBSCode], PlanVersionID: planID, WBSCode: n.WBSCode, ParentID: parent,
			Name: n.Name, EffortHours: n.EffortHours, DurationDays: n.DurationDays, ConstraintType: n.ConstraintType,
			ConstraintDate: n.ConstraintDate, RoleID: n.RoleID, IsBillableMilestone: n.IsBillableMilestone})
	}
	links := make([]domain.WBSLink, 0, len(deps))
	for _, d := range deps {
		links = append(links, domain.WBSLink{PredecessorID: idOf[d.PredecessorWBSCode], SuccessorID: idOf[d.SuccessorWBSCode], Type: d.Type, LagDays: d.LagDays})
	}
	f.nodes[planID], f.links[planID] = out, links
}

func (f *fakeStore) SchedulePlan(_ context.Context, projectID, planID string) (store.ScheduleResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, err := f.wbsOf(projectID, planID)
	if err != nil {
		return store.ScheduleResult{}, err
	}
	if w.Plan.Locked {
		return store.ScheduleResult{}, &domain.Error{Code: domain.CodePlanLocked, Detail: "locked"}
	}
	out := store.ScheduleResult{WBS: w}
	if len(w.Nodes) == 0 {
		return out, nil
	}
	sched, err := domain.ScheduleWBS(domain.StandardCalendar(), f.projects[projectID].StartDate, w.Nodes, w.Links)
	if err != nil {
		return store.ScheduleResult{}, err
	}
	f.nodes[planID] = w.Nodes
	out.FinishDate = sched.FinishDate.Format(time.DateOnly)
	out.CriticalPath = sched.CriticalPath
	return out, nil
}

// as calls a route as a principal holding exactly the given duties.
func as(t *testing.T, h http.Handler, duties []string, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, ctxFor(duties...), method, path, body, headers)
}

func TestGroups_PutCreatesThenReplacesUnderIfMatch(t *testing.T) {
	h := router(newFake())
	if rec := as(t, h, []string{DutyInquire}, http.MethodPut, "/v1/projects/groups/ENG", `{"name":"Engineering"}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("put without ProjSetupAdmin: status %d", rec.Code)
	}
	rec := as(t, h, []string{DutySetup}, http.MethodPut, "/v1/projects/groups/ENG", `{"name":"Engineering","defaultProjectType":"FIXED_PRICE"}`, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create group: status %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if rec := as(t, h, []string{DutySetup}, http.MethodPut, "/v1/projects/groups/ENG", `{"name":"Engineering services"}`, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("replace without If-Match: status %d", rec.Code)
	}
	if rec := as(t, h, []string{DutySetup}, http.MethodPut, "/v1/projects/groups/ENG", `{"name":"x"}`, map[string]string{"If-Match": `"9"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: status %d", rec.Code)
	}
	rec = as(t, h, []string{DutySetup}, http.MethodPut, "/v1/projects/groups/ENG", `{"name":"Engineering services"}`, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("replace group: status %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := as(t, h, []string{DutySetup}, http.MethodPut, "/v1/projects/groups/NEW", `{"name":"x"}`, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-Match on a missing group: status %d", rec.Code)
	}

	rec = as(t, h, []string{DutyInquire}, http.MethodGet, "/v1/projects/groups", "", nil)
	var list struct {
		Items []domain.ProjectGroup `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].Name != "Engineering services" || list.Items[0].Version != 2 {
		t.Fatalf("list groups: %v %s", err, rec.Body.String())
	}
}

func TestGroups_RejectsBadIdsAndBodies(t *testing.T) {
	h := router(newFake())
	cases := []struct {
		name, path, body, field string
	}{
		{"id over 10 characters", "/v1/projects/groups/TOOLONGGROUP", `{"name":"x"}`, "id"},
		{"lower case id", "/v1/projects/groups/eng", `{"name":"x"}`, "id"},
		{"the wildcard", "/v1/projects/groups/*", `{"name":"x"}`, "id"},
		{"id differs from path", "/v1/projects/groups/ENG", `{"id":"OTHER","name":"x"}`, "id"},
		{"empty name", "/v1/projects/groups/ENG", `{"name":""}`, "name"},
		{"unknown type", "/v1/projects/groups/ENG", `{"name":"x","defaultProjectType":"GRANT"}`, "defaultProjectType"},
	}
	for _, c := range cases {
		rec := as(t, h, []string{DutySetup}, http.MethodPut, c.path, c.body, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("%s: status %d body %s", c.name, rec.Code, rec.Body.String())
		}
	}
}

// profileJSON builds a posting profile body with the given key and cost account.
func profileJSON(group, category, property, cost string) string {
	return `{"projectGroup":"` + group + `","category":"` + category + `","lineProperty":"` + property +
		`","costAccount":"` + cost + `","wipAccount":"1400","accruedRevenueAccount":"1410","invoicedRevenueAccount":"4000","onAccountAccount":"2400"}`
}

func resolve(t *testing.T, h http.Handler, group, category, property string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"projectGroup":"` + group + `","category":"` + category + `","lineProperty":"` + property + `"}`
	return as(t, h, []string{DutyInquire}, http.MethodPost, "/v1/projects/posting-profiles/resolve", body, nil)
}

func TestPostingProfiles_ResolveFollowsFallbackOrder(t *testing.T) {
	h := router(newFake())
	for _, body := range []string{
		profileJSON("ENG", "LABOR", "CHARGEABLE", "5100"),
		profileJSON("ENG", "*", "*", "5200"),
		profileJSON("*", "*", "*", "5900"),
	} {
		if rec := as(t, h, []string{DutySetup}, http.MethodPut, "/v1/projects/posting-profiles", body, nil); rec.Code != http.StatusCreated {
			t.Fatalf("put profile: status %d body %s", rec.Code, rec.Body.String())
		}
	}
	cases := []struct{ group, category, property, want string }{
		{"ENG", "LABOR", "CHARGEABLE", "5100"},
		{"ENG", "LABOR", "NON_CHARGEABLE", "5200"},
		{"ENG", "TRAVEL", "CHARGEABLE", "5200"},
		{"OTHER", "LABOR", "CHARGEABLE", "5900"},
	}
	for _, c := range cases {
		rec := resolve(t, h, c.group, c.category, c.property)
		var out struct {
			CostAccount string `json:"costAccount"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.CostAccount != c.want {
			t.Errorf("resolve %v: status %d body %s, want cost %s", c, rec.Code, rec.Body.String(), c.want)
		}
	}

	// Without a catch-all row the lookup fails and names the missing key.
	bare := router(newFake())
	as(t, bare, []string{DutySetup}, http.MethodPut, "/v1/projects/posting-profiles", profileJSON("ENG", "LABOR", "CHARGEABLE", "5100"), nil)
	rec := resolve(t, bare, "ENG", "TRAVEL", "CHARGEABLE")
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "PROJ-POST-001" || !strings.Contains(rec.Body.String(), "TRAVEL") {
		t.Fatalf("missing profile: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := resolve(t, bare, "ENG", "*", "CHARGEABLE"); rec.Code != http.StatusBadRequest {
		t.Fatalf("wildcard in a resolve key: status %d", rec.Code)
	}
}

func TestPostingProfiles_OnlyResolvableKeysAreStored(t *testing.T) {
	h := router(newFake())
	bad := []struct{ name, body, field string }{
		{"unknown line property", profileJSON("ENG", "LABOR", "BOTH", "5100"), "lineProperty"},
		{"category wildcard with concrete property", profileJSON("ENG", "*", "CHARGEABLE", "5100"), "lineProperty"},
		{"property wildcard with concrete category", profileJSON("ENG", "LABOR", "*", "5100"), "lineProperty"},
		{"catch-all group with concrete category", profileJSON("*", "LABOR", "CHARGEABLE", "5100"), "category"},
		{"lower case group", profileJSON("eng", "LABOR", "CHARGEABLE", "5100"), "projectGroup"},
		{"missing account", profileJSON("ENG", "LABOR", "CHARGEABLE", ""), "costAccount"},
	}
	for _, c := range bad {
		rec := as(t, h, []string{DutySetup}, http.MethodPut, "/v1/projects/posting-profiles", c.body, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("%s: status %d body %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if rec := as(t, h, []string{DutyInquire}, http.MethodPut, "/v1/projects/posting-profiles", profileJSON("ENG", "*", "*", "5200"), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("put without ProjSetupAdmin: status %d", rec.Code)
	}
}

func seedProject(t *testing.T, f *fakeStore, id string) {
	t.Helper()
	if _, _, err := f.CreateProject(context.Background(), sampleProject(id), ""); err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

func createPlan(t *testing.T, h http.Handler, project, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	return as(t, h, []string{DutyPlan}, http.MethodPost, "/v1/projects/"+project+"/plans", body, map[string]string{"Idempotency-Key": key})
}

func planOf(t *testing.T, rec *httptest.ResponseRecorder) domain.PlanVersion {
	t.Helper()
	var pv domain.PlanVersion
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil {
		t.Fatalf("decode plan: %v (%s)", err, rec.Body.String())
	}
	return pv
}

func TestPlans_KindsKeysAndBaselineLock(t *testing.T) {
	f := newFake()
	h := router(f)
	seedProject(t, f, "P-PLAN")
	path := "/v1/projects/P-PLAN/plans"

	if rec := as(t, h, []string{DutyPlan}, http.MethodPost, path, `{"kind":"WORKING"}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key: status %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyInquire}, http.MethodPost, path, `{"kind":"WORKING"}`, map[string]string{"Idempotency-Key": "p0"}); rec.Code != http.StatusForbidden {
		t.Fatalf("create without ProjPlan: status %d", rec.Code)
	}
	if rec := createPlan(t, h, "P-PLAN", `{"kind":"BASELINE"}`, "p1"); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodePlanKind {
		t.Fatalf("baseline without a source: status %d body %s", rec.Code, rec.Body.String())
	}
	rec := createPlan(t, h, "P-PLAN", `{"kind":"WORKING"}`, "p2")
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create working: status %d body %s", rec.Code, rec.Body.String())
	}
	working := planOf(t, rec)
	if replay := createPlan(t, h, "P-PLAN", `{"kind":"WORKING"}`, "p2"); replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || planOf(t, replay).ID != working.ID {
		t.Fatalf("replay: status %d body %s", replay.Code, replay.Body.String())
	}
	if rec := createPlan(t, h, "P-PLAN", `{"kind":"SIMULATION","copyFromVersionId":"`+working.ID+`"}`, "p2"); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != store.CodeIdempotencyReuse {
		t.Fatalf("key reused with another body: status %d", rec.Code)
	}
	if rec := createPlan(t, h, "P-PLAN", `{"kind":"WORKING"}`, "p3"); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodePlanKind {
		t.Fatalf("second working: status %d", rec.Code)
	}
	rec = createPlan(t, h, "P-PLAN", `{"kind":"BASELINE","copyFromVersionId":"`+working.ID+`"}`, "p4")
	baseline := planOf(t, rec)
	if rec.Code != http.StatusCreated || !baseline.Locked || baseline.CopiedFromVersionID == nil || *baseline.CopiedFromVersionID != working.ID {
		t.Fatalf("baseline from working: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := createPlan(t, h, "P-PLAN", `{"kind":"BASELINE","copyFromVersionId":"`+working.ID+`"}`, "p5"); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodePlanKind {
		t.Fatalf("second baseline: status %d", rec.Code)
	}
	if rec := createPlan(t, h, "P-PLAN", `{"kind":"SIMULATION","copyFromVersionId":"`+baseline.ID+`"}`, "p6"); rec.Code != http.StatusCreated || planOf(t, rec).Locked {
		t.Fatalf("simulation from baseline: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := createPlan(t, h, "P-PLAN", `{"kind":"DRAFT"}`, "p7"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind: status %d", rec.Code)
	}
	if rec := createPlan(t, h, "NOPE", `{"kind":"WORKING"}`, "p8"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: status %d", rec.Code)
	}

	// The locked baseline refuses WBS changes and scheduling.
	base := "/v1/projects/P-PLAN/plans/" + baseline.ID
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, base+"/wbs", `{"nodes":[{"wbsCode":"1","name":"A","durationDays":1}]}`, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodePlanLocked {
		t.Fatalf("replace on baseline: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPost, base+"/schedule", "", nil); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodePlanLocked {
		t.Fatalf("schedule on baseline: status %d", rec.Code)
	}

	rec = as(t, h, []string{DutyInquire}, http.MethodGet, path, "", nil)
	var list planList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 3 || list.Items[0].Kind != domain.KindWorking || list.Items[2].Sequence != 3 {
		t.Fatalf("list plans: %v %s", err, rec.Body.String())
	}
	if rec := as(t, h, []string{DutyInquire}, http.MethodGet, "/v1/projects/NOPE/plans", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("plans of unknown project: status %d", rec.Code)
	}
}

const sampleWBS = `{"nodes":[
	{"wbsCode":"1","name":"Design","effortHours":"40.00","durationDays":0},
	{"wbsCode":"1.1","parentWbsCode":"1","name":"Draft","durationDays":3},
	{"wbsCode":"1.2","parentWbsCode":"1","name":"Review","durationDays":2},
	{"wbsCode":"1.3","parentWbsCode":"1","name":"Side task","durationDays":1}],
	"dependencies":[{"predecessorWbsCode":"1.1","successorWbsCode":"1.2","type":"FS","lagDays":2}]}`

func TestWBS_ReplaceNeedsIfMatchValidatesAndSchedules(t *testing.T) {
	f := newFake()
	h := router(f)
	seedProject(t, f, "P-W") // starts Thursday 2026-10-01
	plan := planOf(t, createPlan(t, h, "P-W", `{"kind":"WORKING"}`, "w1"))
	path := "/v1/projects/P-W/plans/" + plan.ID

	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, path+"/wbs", sampleWBS, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, path+"/wbs", sampleWBS, map[string]string{"If-Match": `"9"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale plan version: status %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyInquire}, http.MethodPut, path+"/wbs", sampleWBS, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("replace without ProjPlan: status %d", rec.Code)
	}
	rec := as(t, h, []string{DutyPlan}, http.MethodPut, path+"/wbs", sampleWBS, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), `"constraintType":"ASAP"`) {
		t.Fatalf("replace wbs: status %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}

	dup := strings.Replace(sampleWBS, `{"wbsCode":"1.2"`, `{"wbsCode":"1.1"`, 1)
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, path+"/wbs", dup, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeWBSDuplicate {
		t.Fatalf("duplicate code: status %d body %s", rec.Code, rec.Body.String())
	}
	cycle := strings.Replace(sampleWBS, `"lagDays":2}`, `"lagDays":2},{"predecessorWbsCode":"1.2","successorWbsCode":"1.1","type":"FS","lagDays":0}`, 1)
	if rec := as(t, h, []string{DutyPlan}, http.MethodPut, path+"/wbs", cycle, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeWBSCycle {
		t.Fatalf("cycle: status %d body %s", rec.Code, rec.Body.String())
	}

	rec = as(t, h, []string{DutyInquire}, http.MethodGet, path+"/wbs", "", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), `"wbsCode":"1.2"`) {
		t.Fatalf("get wbs: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := as(t, h, []string{DutyInquire}, http.MethodGet, "/v1/projects/P-W/plans/not-a-uuid/wbs", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed plan id: status %d", rec.Code)
	}

	rec = as(t, h, []string{DutyPlan}, http.MethodPost, path+"/schedule", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("schedule: status %d body %s", rec.Code, rec.Body.String())
	}
	var sched scheduleView
	if err := json.Unmarshal(rec.Body.Bytes(), &sched); err != nil {
		t.Fatalf("decode schedule: %v", err)
	}
	byCode := map[string]domain.WBSNode{}
	for _, n := range sched.Nodes {
		byCode[n.WBSCode] = n
	}
	draft, review, side := byCode["1.1"], byCode["1.2"], byCode["1.3"]
	// Draft: Thu 1, Fri 2, Mon 5 Oct. FS lag 2 calendar days after Mon 5 is Thu 8;
	// Review runs Thu 8 and Fri 9, which is the finish date.
	if *draft.EarlyStart != "2026-10-01" || *draft.EarlyFinish != "2026-10-05" || *review.EarlyStart != "2026-10-08" || *review.EarlyFinish != "2026-10-09" {
		t.Fatalf("early dates: draft %s..%s review %s..%s", *draft.EarlyStart, *draft.EarlyFinish, *review.EarlyStart, *review.EarlyFinish)
	}
	if sched.FinishDate != "2026-10-09" || !*draft.Critical || !*review.Critical || *side.Critical || *side.SlackDays != 6 {
		t.Fatalf("finish %s, critical draft %v review %v side %v slack %d", sched.FinishDate, *draft.Critical, *review.Critical, *side.Critical, *side.SlackDays)
	}
	if len(sched.CriticalPath) < 2 || sched.CriticalPath[len(sched.CriticalPath)-1] != review.ID {
		t.Fatalf("critical path %v should end at Review %s", sched.CriticalPath, review.ID)
	}
}

func TestWBS_RejectsUnsupportedConstraintsAndBadDependencies(t *testing.T) {
	f := newFake()
	h := router(f)
	seedProject(t, f, "P-V")
	plan := planOf(t, createPlan(t, h, "P-V", `{"kind":"WORKING"}`, "v1"))
	path := "/v1/projects/P-V/plans/" + plan.ID + "/wbs"
	cases := []struct{ name, body, field string }{
		{"MSO is not scheduled yet", `{"nodes":[{"wbsCode":"1","name":"A","constraintType":"MSO","constraintDate":"2026-10-05"}]}`, "nodes[0].constraintType"},
		{"SNET without a date", `{"nodes":[{"wbsCode":"1","name":"A","constraintType":"SNET"}]}`, "nodes[0].constraintDate"},
		{"date on an ASAP node", `{"nodes":[{"wbsCode":"1","name":"A","constraintDate":"2026-10-05"}]}`, "nodes[0].constraintDate"},
		{"bad code", `{"nodes":[{"wbsCode":"A.1","name":"A"}]}`, "nodes[0].wbsCode"},
		{"repeated dependency", `{"nodes":[{"wbsCode":"1","name":"A"},{"wbsCode":"2","name":"B"}],"dependencies":[
			{"predecessorWbsCode":"1","successorWbsCode":"2","type":"FS"},{"predecessorWbsCode":"1","successorWbsCode":"2","type":"SS"}]}`, "dependencies[1]"},
		{"unknown parent", `{"nodes":[{"wbsCode":"1.1","parentWbsCode":"1","name":"A"}]}`, "nodes[0].parentWbsCode"},
	}
	for _, c := range cases {
		rec := as(t, h, []string{DutyPlan}, http.MethodPut, path, c.body, map[string]string{"If-Match": `"1"`})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("%s: status %d body %s", c.name, rec.Code, rec.Body.String())
		}
	}
}

func TestSchedule_EmptyPlanSchedulesNothing(t *testing.T) {
	f := newFake()
	h := router(f)
	seedProject(t, f, "P-E")
	plan := planOf(t, createPlan(t, h, "P-E", `{"kind":"WORKING"}`, "e1"))
	rec := as(t, h, []string{DutyPlan}, http.MethodPost, "/v1/projects/P-E/plans/"+plan.ID+"/schedule", "", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "finishDate") || !strings.Contains(rec.Body.String(), `"criticalPath":[]`) {
		t.Fatalf("empty schedule: status %d body %s", rec.Code, rec.Body.String())
	}
}
