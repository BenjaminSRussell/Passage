package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

func (f *fakeStore) CreateHierarchy(_ context.Context, _, _ string, in store.Hierarchy) (int, []byte, error) {
	f.calls++
	f.hierarchy = in
	return http.StatusCreated, []byte(`{"id":"h"}`), nil
}

func (f *fakeStore) CreateHierarchyNode(_ context.Context, _, _ string, in store.HierarchyNode) (int, []byte, error) {
	f.calls++
	f.node = in
	return http.StatusCreated, []byte(`{"id":"n"}`), nil
}

func (f *fakeStore) CollectAtNode(_ context.Context, _, _ string, in store.CollectRequest) (int, []byte, error) {
	f.calls++
	f.collect = in
	return http.StatusCreated, []byte(`{"entryId":"e"}`), nil
}

// RunHierarchyDistribution answers like a node that holds nothing, so the
// handler's relay of a store problem is tested.
func (f *fakeStore) RunHierarchyDistribution(_ context.Context, _, _ string, in store.DistributionRequest) (int, []byte, error) {
	f.calls++
	f.distribution = in
	return 0, nil, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeNothingToDistribute, "the node holds no cost")
}

func (f *fakeStore) ListHierarchies(_ context.Context, le string) ([]store.Hierarchy, error) {
	f.calls++
	f.filter = le
	return nil, nil
}

func (f *fakeStore) GetHierarchy(context.Context, string) (store.Hierarchy, error) {
	f.calls++
	return store.Hierarchy{}, httpx.NewProblem(http.StatusNotFound, store.CodeHierarchyNotFound, "cost object hierarchy not found")
}

func (f *fakeStore) HierarchyBalances(_ context.Context, id, period string) ([]store.NodeBalance, error) {
	f.calls++
	f.filter = id + "/" + period
	return nil, nil
}

func (f *fakeStore) ListDistributionRuns(_ context.Context, le, hierarchyID string) ([]store.DistributionRun, error) {
	f.calls++
	f.filter = le + "|" + hierarchyID
	return nil, nil
}

func (f *fakeStore) GetDistributionRun(context.Context, string) (store.DistributionRun, error) {
	f.calls++
	return store.DistributionRun{ID: objA}, nil
}

const nodeID = "0196a6a4-0000-7000-8000-000000000016"

func TestCreateHierarchy_DutyAndValidation(t *testing.T) {
	body := `{"legalEntityId":"` + entityID + `","code":"PG-ALL","name":"Product groups","type":"Collective"}`
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodPost, "/v1/controlling/cost-object-hierarchies", body, DutyRun, "k"); rec.Code != http.StatusForbidden {
		t.Fatalf("without controlling.maintain: status %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodPost, "/v1/controlling/cost-object-hierarchies", body, DutyMaintain, "k"); rec.Code != http.StatusCreated ||
		st.hierarchy.Type != "Collective" || st.hierarchy.Code != "PG-ALL" {
		t.Fatalf("status %d store %+v", rec.Code, st.hierarchy)
	}
	for name, bad := range map[string]string{
		"unknown type": `{"legalEntityId":"` + entityID + `","code":"PG","name":"x","type":"Tree"}`,
		"bad code":     `{"legalEntityId":"` + entityID + `","code":"p g","name":"x","type":"Reporting"}`,
		"no name":      `{"legalEntityId":"` + entityID + `","code":"PG","name":"","type":"Reporting"}`,
	} {
		st := &fakeStore{}
		if rec := serve(t, st, http.MethodPost, "/v1/controlling/cost-object-hierarchies", bad, DutyMaintain, "k"); rec.Code != http.StatusBadRequest || st.calls != 0 {
			t.Errorf("%s: status %d calls %d", name, rec.Code, st.calls)
		}
	}
}

func TestCreateHierarchyNode_BasisOnlyOnCollectingNodes(t *testing.T) {
	path := "/v1/controlling/cost-object-hierarchies/" + objC + "/nodes"
	good := `{"parentNodeId":"` + nodeID + `","costObjectId":"` + objA + `","collects":true,"distributionBasis":"FixedPercent"}`
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodPost, path, good, DutyMaintain, "k"); rec.Code != http.StatusCreated ||
		st.node.HierarchyID != objC || st.node.ParentNodeID != nodeID || !st.node.Collects || st.node.DistributionBasis != "FixedPercent" {
		t.Fatalf("status %d store %+v", rec.Code, st.node)
	}
	for name, bad := range map[string]string{
		"collecting without basis":  `{"costObjectId":"` + objA + `","collects":true}`,
		"basis on a non-collecting": `{"costObjectId":"` + objA + `","collects":false,"distributionBasis":"ActualOutput"}`,
		"unknown basis":             `{"costObjectId":"` + objA + `","collects":true,"distributionBasis":"Revenue"}`,
		"parent is not a UUID":      `{"parentNodeId":"root","costObjectId":"` + objA + `"}`,
		"cost object is not a UUID": `{"costObjectId":"CC-1"}`,
		"unknown field is rejected": `{"costObjectId":"` + objA + `","level":2}`,
	} {
		st := &fakeStore{}
		if rec := serve(t, st, http.MethodPost, path, bad, DutyMaintain, "k"); rec.Code != http.StatusBadRequest || st.calls != 0 {
			t.Errorf("%s: status %d calls %d", name, rec.Code, st.calls)
		}
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/controlling/cost-object-hierarchies/x/nodes", good, DutyMaintain, "k"); rec.Code != http.StatusNotFound {
		t.Errorf("malformed hierarchy id: status %d", rec.Code)
	}
}

func TestCollectAtNode_RunDutyAndAmounts(t *testing.T) {
	path := "/v1/controlling/hierarchy-nodes/" + nodeID + ":collect"
	body := func(currency, amount string) string {
		return `{"period":"2026-09","currency":"` + currency + `","costElementId":"` + elemID + `","amount":"` + amount + `"}`
	}
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodPost, path, body("USD", "500.00"), DutyMaintain, "k"); rec.Code != http.StatusForbidden {
		t.Fatalf("without controlling.run: status %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodPost, path, body("USD", "-20.00"), DutyRun, "k"); rec.Code != http.StatusCreated ||
		st.collect.NodeID != nodeID || !st.collect.Amount.Equal(dec("-20")) {
		t.Fatalf("a correction: status %d store %+v", rec.Code, st.collect)
	}
	for name, bad := range map[string]string{
		"zero":               body("USD", "0"),
		"finer than cents":   body("USD", "1.005"),
		"yen with decimals":  body("JPY", "10.5"),
		"lowercase currency": body("usd", "1"),
	} {
		st := &fakeStore{}
		if rec := serve(t, st, http.MethodPost, path, bad, DutyRun, "k"); rec.Code != http.StatusBadRequest || st.calls != 0 {
			t.Errorf("%s: status %d calls %d", name, rec.Code, st.calls)
		}
	}
}

func TestRunHierarchyDistribution_ValidatesAndRelays(t *testing.T) {
	body := func(children string) string {
		return `{"nodeId":"` + nodeID + `","period":"2026-09","currency":"USD","costElementId":"` + elemID + `","children":` + children + `}`
	}
	good := body(`[{"nodeId":"` + objA + `","basis":"60"},{"nodeId":"` + objB + `","basis":"40"}]`)
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodPost, "/v1/controlling/hierarchy-distributions", good, DutyMaintain, "k"); rec.Code != http.StatusForbidden {
		t.Fatalf("without controlling.run: status %d", rec.Code)
	}
	rec := serve(t, st, http.MethodPost, "/v1/controlling/hierarchy-distributions", good, DutyRun, "k")
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != store.CodeNothingToDistribute {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	if len(st.distribution.Children) != 2 || !st.distribution.Children[0].Basis.Equal(dec("60")) {
		t.Fatalf("store got %+v", st.distribution)
	}
	for name, bad := range map[string]string{
		"no children":             body(`[]`),
		"node is its own child":   body(`[{"nodeId":"` + nodeID + `","basis":"1"}]`),
		"duplicate child":         body(`[{"nodeId":"` + objA + `","basis":"1"},{"nodeId":"` + objA + `","basis":"1"}]`),
		"negative basis":          body(`[{"nodeId":"` + objA + `","basis":"-1"}]`),
		"missing idempotency key": "",
	} {
		st := &fakeStore{}
		key := "k"
		if bad == "" {
			bad, key = good, ""
		}
		if rec := serve(t, st, http.MethodPost, "/v1/controlling/hierarchy-distributions", bad, DutyRun, key); rec.Code != http.StatusBadRequest || st.calls != 0 {
			t.Errorf("%s: status %d calls %d", name, rec.Code, st.calls)
		}
	}
}

func TestHierarchyReads(t *testing.T) {
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-object-hierarchies?legalEntityId="+entityID, "", DutyView, ""); rec.Code != http.StatusOK || st.filter != entityID {
		t.Fatalf("list: status %d filter %q", rec.Code, st.filter)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-object-hierarchies/"+objC, "", DutyView, ""); rec.Code != http.StatusNotFound ||
		problemCode(t, rec) != store.CodeHierarchyNotFound {
		t.Fatalf("get missing: status %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-object-hierarchies/"+objC+"/balances?period=2026-09", "", DutyView, ""); rec.Code != http.StatusOK ||
		st.filter != objC+"/2026-09" {
		t.Fatalf("balances: status %d filter %q", rec.Code, st.filter)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-object-hierarchies/"+objC+"/balances", "", DutyView, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("balances without period: status %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/hierarchy-distributions?hierarchyId="+objC, "", DutyView, ""); rec.Code != http.StatusOK || st.filter != "|"+objC {
		t.Fatalf("list runs: status %d filter %q", rec.Code, st.filter)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/hierarchy-distributions/not-a-uuid", "", DutyView, ""); rec.Code != http.StatusNotFound ||
		problemCode(t, rec) != store.CodeHierarchyRunNotFound {
		t.Fatalf("get malformed: status %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/hierarchy-distributions/"+objA, "", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("read without controlling.view: status %d", rec.Code)
	}
}
