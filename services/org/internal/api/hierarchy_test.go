package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	hierarchyTypesPath = "/v1/org/hierarchy-types"
	hierarchyDuties    = "org.read,org.hierarchy.write"
	hierarchyTypeID    = "0190b6c0-0000-7000-8000-0000000000d1"
	hierarchyVersionID = "0190b6c0-0000-7000-8000-0000000000d2"
	unitA              = "0190b6c0-0000-7000-8000-0000000000e1"
	unitB              = "0190b6c0-0000-7000-8000-0000000000e2"
	nodesPath          = "/v1/org/hierarchy-versions/" + hierarchyVersionID + "/nodes"
	createType         = `{"code":"rep","name":" Reporting ","purposes":["reporting","security"]}`
)

func TestHierarchyTypeCreate(t *testing.T) {
	scoped := []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}
	tests := []struct {
		name       string
		body       string
		duties     string
		headers    []string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"created with defaults", createType, hierarchyDuties, nil, true, http.StatusCreated, `"code":"REP","name":"Reporting","purposes":["reporting","security"],"backdateAllowed":false`},
		{"backdate allowed", `{"code":"REP","name":"n","purposes":["custom"],"backdateAllowed":true}`, hierarchyDuties, nil, true, http.StatusCreated, `"backdateAllowed":true`},
		{"empty purposes", `{"code":"REP","name":"n","purposes":[]}`, hierarchyDuties, nil, true, http.StatusBadRequest, `"purposes"`},
		{"absent purposes", `{"code":"REP","name":"n"}`, hierarchyDuties, nil, true, http.StatusBadRequest, `"purposes"`},
		{"unknown purpose", `{"code":"REP","name":"n","purposes":["marketing"]}`, hierarchyDuties, nil, true, http.StatusBadRequest, `"purposes"`},
		{"duplicate purpose", `{"code":"REP","name":"n","purposes":["security","security"]}`, hierarchyDuties, nil, true, http.StatusBadRequest, `"purposes"`},
		{"bad code", `{"code":"a","name":"n","purposes":["custom"]}`, hierarchyDuties, nil, true, http.StatusBadRequest, `"code"`},
		{"empty name", `{"code":"REP","name":" ","purposes":["custom"]}`, hierarchyDuties, nil, true, http.StatusBadRequest, `"name"`},
		{"malformed body", `{`, hierarchyDuties, nil, true, http.StatusBadRequest, "COM-0001"},
		{"string for purposes", `{"code":"REP","name":"n","purposes":"custom"}`, hierarchyDuties, nil, true, http.StatusBadRequest, "COM-0001"},
		{"no tenant", createType, "", nil, false, http.StatusUnauthorized, "AUTH-0001"},
		{"read duty only", createType, "org.read", nil, true, http.StatusForbidden, "AUTH-0003"},
		{"legal-entity scoped caller", createType, hierarchyDuties, scoped, true, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{})
			rec := do(r, http.MethodPost, hierarchyTypesPath, tc.body, tc.duties, tc.withTenant, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestHierarchyDraftCreate(t *testing.T) {
	draftPath := hierarchyTypesPath + "/" + hierarchyTypeID + "/versions"
	scoped := []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}
	tests := []struct {
		name       string
		path       string
		duties     string
		headers    []string
		wantStatus int
		wantBody   string
	}{
		{"created", draftPath, hierarchyDuties, nil, http.StatusCreated, `"status":"draft","effectiveFrom":null`},
		{"non-uuid type", hierarchyTypesPath + "/nope/versions", hierarchyDuties, nil, http.StatusNotFound, "ORG-HIER-TYPE-NOT-FOUND"},
		{"read duty only", draftPath, "org.read", nil, http.StatusForbidden, "AUTH-0003"},
		{"legal-entity scoped caller", draftPath, hierarchyDuties, scoped, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{})
			rec := do(r, http.MethodPost, tc.path, `{"ignored":true}`, tc.duties, true, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestHierarchyReplaceNodes(t *testing.T) {
	scoped := []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}
	valid := fmt.Sprintf(`{"version":1,"nodes":[{"orgUnitId":%q},{"orgUnitId":%q,"parentOrgUnitId":%q,"sortOrder":4}]}`, unitA, unitB, unitA)
	tests := []struct {
		name       string
		path       string
		body       string
		duties     string
		headers    []string
		wantStatus int
		wantBody   string
	}{
		{"replaced", nodesPath, valid, hierarchyDuties, nil, http.StatusOK, `"nodeCount":2,"copiedFromVersionId":null,"version":2`},
		{"empty parent is the root", nodesPath, fmt.Sprintf(`{"version":1,"nodes":[{"orgUnitId":%q,"parentOrgUnitId":""}]}`, unitA), hierarchyDuties, nil, http.StatusOK, `"parentOrgUnitId":null`},
		{"non-uuid version id", "/v1/org/hierarchy-versions/nope/nodes", valid, hierarchyDuties, nil, http.StatusNotFound, "ORG-HIER-VERSION-NOT-FOUND"},
		{"non-uuid org unit", nodesPath, `{"version":1,"nodes":[{"orgUnitId":"x"}]}`, hierarchyDuties, nil, http.StatusBadRequest, `"nodes[0].orgUnitId"`},
		{"non-uuid parent", nodesPath, fmt.Sprintf(`{"version":1,"nodes":[{"orgUnitId":%q,"parentOrgUnitId":"x"}]}`, unitA), hierarchyDuties, nil, http.StatusBadRequest, `"nodes[0].parentOrgUnitId"`},
		{"oversized sort order", nodesPath, fmt.Sprintf(`{"version":1,"nodes":[{"orgUnitId":%q,"sortOrder":2147483648}]}`, unitA), hierarchyDuties, nil, http.StatusBadRequest, `"nodes[0].sortOrder"`},
		{"negative sort order", nodesPath, fmt.Sprintf(`{"version":1,"nodes":[{"orgUnitId":%q,"sortOrder":-1}]}`, unitA), hierarchyDuties, nil, http.StatusBadRequest, `"nodes[0].sortOrder"`},
		{"missing version", nodesPath, `{"nodes":[]}`, hierarchyDuties, nil, http.StatusBadRequest, `"version"`},
		{"string for version", nodesPath, `{"version":"1","nodes":[]}`, hierarchyDuties, nil, http.StatusBadRequest, "COM-0001"},
		{"read duty only", nodesPath, valid, "org.read", nil, http.StatusForbidden, "AUTH-0003"},
		{"legal-entity scoped caller", nodesPath, valid, hierarchyDuties, scoped, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			r, _ := newRouter(f)
			rec := do(r, http.MethodPut, tc.path, tc.body, tc.duties, true, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus != http.StatusOK && f.hierarchyNodes != nil {
				t.Fatal("store was called for a rejected request")
			}
		})
	}
}

func TestHierarchyReplaceNodesCapsShapeErrorsAtTwenty(t *testing.T) {
	nodes := strings.TrimSuffix(strings.Repeat(`{"orgUnitId":"x"},`, 30), ",")
	r, _ := newRouter(&fakeStore{})
	rec := do(r, http.MethodPut, nodesPath, `{"version":1,"nodes":[`+nodes+`]}`, hierarchyDuties, true)
	if rec.Code != http.StatusBadRequest || strings.Count(rec.Body.String(), "must be a UUID") != 20 {
		t.Fatalf("got %d %s; want 400 with exactly 20 node errors", rec.Code, rec.Body.String())
	}
}

func TestHierarchyReplaceNodesMapsDomainAndStoreErrors(t *testing.T) {
	r, _ := newRouter(&fakeStore{err: &domain.Error{Code: domain.CodeHierarchyCycle, Detail: "Moving a under b creates a cycle."}})
	rec := do(r, http.MethodPut, nodesPath, `{"version":1,"nodes":[]}`, hierarchyDuties, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "ORG-HIER-CYCLE") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHierarchyMetrics(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	do(r, http.MethodPost, hierarchyTypesPath, createType, hierarchyDuties, true)
	do(r, http.MethodPost, hierarchyTypesPath+"/"+hierarchyTypeID+"/versions", "", hierarchyDuties, true)
	do(r, http.MethodPut, nodesPath, `{"version":1,"nodes":[]}`, hierarchyDuties, true)
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"org_hierarchy_types_created_total 1", "org_hierarchy_drafts_created_total 1", "org_hierarchy_node_sets_replaced_total 1"} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Fatalf("metrics lack %q: %s", want, metrics.Body.String())
		}
	}
}

var _ store.Store = (*fakeStore)(nil)
