package api_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
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

const (
	publishPath      = "/v1/org/hierarchy-versions/" + hierarchyVersionID + ":publish"
	asOfPath         = hierarchyTypesPath + "/" + hierarchyTypeID + "/as-of"
	publishDuties    = "org.read,org.hierarchy.publish"
	backdateDuties   = publishDuties + ",org.hierarchy.backdate"
	pinnedToday      = "2026-03-01"
	longReason       = "Correcting the reporting structure"
	backdatedBody    = `{"version":1,"effectiveFrom":"2025-12-01","reason":"` + longReason + `"}`
	forwardBody      = `{"version":1,"effectiveFrom":"2026-04-01"}`
	backdateDutyCode = "ORG-HIER-BACKDATE-DUTY"
)

func routerAt(f *fakeStore) http.Handler {
	r, a := newRouter(f)
	a.SetClock(func() time.Time { return time.Date(2026, 3, 1, 15, 0, 0, 0, time.UTC) })
	return r
}

func TestHierarchyPublish(t *testing.T) {
	scoped := []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}
	tests := []struct {
		name          string
		path          string
		body          string
		duties        string
		headers       []string
		withTenant    bool
		wantStatus    int
		wantBody      string
		wantCalls     int
		wantBackdated bool
	}{
		{"published", publishPath, forwardBody, publishDuties, nil, true, http.StatusOK, `"status":"published","effectiveFrom":"2026-04-01"`, 1, false},
		{"today is not backdated", publishPath, `{"version":1,"effectiveFrom":"` + pinnedToday + `"}`, publishDuties, nil, true, http.StatusOK, `"status":"published"`, 1, false},
		{"write duty alone", publishPath, forwardBody, hierarchyDuties, nil, true, http.StatusForbidden, "AUTH-0003", 0, false},
		{"no tenant", publishPath, forwardBody, "", nil, false, http.StatusUnauthorized, "AUTH-0001", 0, false},
		{"scoped caller", publishPath, forwardBody, publishDuties, scoped, true, http.StatusForbidden, "AUTH-0003", 0, false},
		{"malformed id", "/v1/org/hierarchy-versions/nope:publish", forwardBody, publishDuties, nil, true, http.StatusNotFound, "ORG-HIER-VERSION-NOT-FOUND", 0, false},
		{"missing version", publishPath, `{"effectiveFrom":"2026-04-01"}`, publishDuties, nil, true, http.StatusBadRequest, `"version"`, 0, false},
		{"missing date", publishPath, `{"version":1}`, publishDuties, nil, true, http.StatusBadRequest, `"effectiveFrom"`, 0, false},
		{"malformed date", publishPath, `{"version":1,"effectiveFrom":"2026-13-01"}`, publishDuties, nil, true, http.StatusBadRequest, `"effectiveFrom"`, 0, false},
		{"numeric date", publishPath, `{"version":1,"effectiveFrom":20260401}`, publishDuties, nil, true, http.StatusBadRequest, "COM-0001", 0, false},
		{"short reason when not backdated", publishPath, `{"version":1,"effectiveFrom":"2026-04-01","reason":"too short"}`, publishDuties, nil, true, http.StatusBadRequest, `"reason"`, 0, false},
		{"long reason when not backdated", publishPath, `{"version":1,"effectiveFrom":"2026-04-01","reason":"` + strings.Repeat("x", 501) + `"}`, publishDuties, nil, true, http.StatusBadRequest, `"reason"`, 0, false},
		{"reason kept when not backdated", publishPath, `{"version":1,"effectiveFrom":"2026-04-01","reason":"` + longReason + `"}`, publishDuties, nil, true, http.StatusOK, `"publishReason":"` + longReason + `"`, 1, false},
		{"unknown field", publishPath, `{"version":1,"effectiveFrom":"2026-04-01","extra":1}`, publishDuties, nil, true, http.StatusBadRequest, "COM-0001", 0, false},
		{"backdated without the duty", publishPath, backdatedBody, publishDuties, nil, true, http.StatusForbidden, backdateDutyCode, 0, false},
		{"backdated with the duty", publishPath, backdatedBody, backdateDuties, nil, true, http.StatusOK, `"publishedBy"`, 1, true},
		{"backdate duty without publish duty", publishPath, backdatedBody, "org.read,org.hierarchy.backdate", nil, true, http.StatusForbidden, "AUTH-0003", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			rec := do(routerAt(f), http.MethodPost, tc.path, tc.body, tc.duties, tc.withTenant, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if f.publishCalls != tc.wantCalls || f.publishBackdated != tc.wantBackdated {
				t.Fatalf("store calls = %d backdated = %v; want %d and %v", f.publishCalls, f.publishBackdated, tc.wantCalls, tc.wantBackdated)
			}
		})
	}
}

func TestHierarchyPublishMapsStoreErrorsAndForwardsUser(t *testing.T) {
	f := &fakeStore{}
	do(routerAt(f), http.MethodPost, publishPath, forwardBody, publishDuties, true, "X-Passage-User", "user-7")
	if f.publishedBy != "user-7" {
		t.Fatalf("publishedBy = %q; want the principal user id", f.publishedBy)
	}
	rec := do(routerAt(&fakeStore{err: &domain.Error{Code: domain.CodeHierarchyMultiRoot, Detail: "A version must have exactly one root; found 0."}}), http.MethodPost, publishPath, forwardBody, publishDuties, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "ORG-HIER-MULTI-ROOT") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHierarchyAsOf(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"answers", asOfPath + "?date=2026-02-01&node=" + unitA, "org.read", http.StatusOK, `"orgUnitId":"` + unitA + `","kind":"","parentOrgUnitId":null,"ancestors":[],"descendants":[]`},
		{"uppercase node is canonicalised", asOfPath + "?date=2026-02-01&node=" + strings.ToUpper(unitA), "org.read", http.StatusOK, `"orgUnitId":"` + unitA + `"`},
		{"missing date", asOfPath + "?node=" + unitA, "org.read", http.StatusBadRequest, `"date"`},
		{"malformed date", asOfPath + "?date=2026-02-30&node=" + unitA, "org.read", http.StatusBadRequest, `"date"`},
		{"datetime", asOfPath + "?date=2026-02-01T00:00:00Z&node=" + unitA, "org.read", http.StatusBadRequest, `"date"`},
		{"missing node", asOfPath + "?date=2026-02-01", "org.read", http.StatusBadRequest, `"node"`},
		{"non-uuid node", asOfPath + "?date=2026-02-01&node=x", "org.read", http.StatusBadRequest, `"node"`},
		{"malformed type id", hierarchyTypesPath + "/nope/as-of?date=2026-02-01&node=" + unitA, "org.read", http.StatusNotFound, "ORG-HIER-TYPE-NOT-FOUND"},
		{"without org.read", asOfPath + "?date=2026-02-01&node=" + unitA, "org.hierarchy.write", http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{})
			rec := do(r, http.MethodGet, tc.path, "", tc.duties, true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestHierarchyPublishAndAsOfMetrics(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	do(r, http.MethodPost, publishPath, forwardBody, backdateDuties, true)
	do(r, http.MethodGet, asOfPath+"?date=2026-02-01&node="+unitA, "", "org.read", true)
	do(r, http.MethodGet, asOfPath+"?date=bad&node="+unitA, "", "org.read", true)
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"org_hierarchy_versions_published_total 1", "org_hierarchy_as_of_queries_total 1"} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Fatalf("metrics lack %q: %s", want, metrics.Body.String())
		}
	}
}

var _ store.Store = (*fakeStore)(nil)

func TestHierarchyTypeReads(t *testing.T) {
	scoped := []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}
	seeded := []store.HierarchyType{{Code: "A1"}, {Code: "B2"}, {Code: "C3"}}
	tests := []struct {
		name       string
		path       string
		duties     string
		headers    []string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"get", hierarchyTypesPath + "/" + hierarchyTypeID, "org.read", nil, true, http.StatusOK, `"code":"REP"`},
		{"get by scoped caller", hierarchyTypesPath + "/" + hierarchyTypeID, "org.read", scoped, true, http.StatusOK, `"code":"REP"`},
		{"get malformed id", hierarchyTypesPath + "/nope", "org.read", nil, true, http.StatusNotFound, "ORG-HIER-TYPE-NOT-FOUND"},
		{"get without duty", hierarchyTypesPath + "/" + hierarchyTypeID, "org.hierarchy.write", nil, true, http.StatusForbidden, "AUTH-0003"},
		{"list without duty", hierarchyTypesPath, "org.hierarchy.write", nil, true, http.StatusForbidden, "AUTH-0003"},
		{"list without tenant", hierarchyTypesPath, "", nil, false, http.StatusUnauthorized, "AUTH-0001"},
		{"list by scoped caller", hierarchyTypesPath, "org.read", scoped, true, http.StatusOK, `"code":"C3"`},
		{"list pages", hierarchyTypesPath + "?limit=2", "org.read", nil, true, http.StatusOK, `"nextCursor":"QjI"`},
		{"list bad purpose", hierarchyTypesPath + "?purpose=bogus", "org.read", nil, true, http.StatusBadRequest, `"purpose"`},
		{"list wrong-case purpose", hierarchyTypesPath + "?purpose=Reporting", "org.read", nil, true, http.StatusBadRequest, `"purpose"`},
		{"list bad status", hierarchyTypesPath + "?status=bogus", "org.read", nil, true, http.StatusBadRequest, `"status"`},
		{"list zero limit", hierarchyTypesPath + "?limit=0", "org.read", nil, true, http.StatusBadRequest, "COM-0001"},
		{"list bad cursor", hierarchyTypesPath + "?cursor=***", "org.read", nil, true, http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{hierarchyTypes: seeded})
			rec := do(r, http.MethodGet, tc.path, "", tc.duties, tc.withTenant, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestHierarchyTypeListPassesFiltersAndCursorToTheStore(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	rec := do(r, http.MethodGet, hierarchyTypesPath+"?purpose=security&status=active&cursor=QjI", "", "org.read", true)
	if rec.Code != http.StatusOK || f.typeFilter != (store.HierarchyTypeFilter{Purpose: "security", Status: "active"}) || f.typeAfter != "B2" || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("got %d %s filter=%+v after=%q", rec.Code, rec.Body.String(), f.typeFilter, f.typeAfter)
	}
}

func TestHierarchyVersionGet(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"get", hierarchyVersionPath, "org.read", http.StatusOK, `"status":"draft"`},
		{"malformed id", "/v1/org/hierarchy-versions/nope", "org.read", http.StatusNotFound, "ORG-HIER-VERSION-NOT-FOUND"},
		{"publish suffix is not an id", hierarchyVersionPath + ":publish", "org.read", http.StatusNotFound, "ORG-HIER-VERSION-NOT-FOUND"},
		{"without duty", hierarchyVersionPath, "org.hierarchy.write", http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{})
			rec := do(r, http.MethodGet, tc.path, "", tc.duties, true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

const hierarchyVersionPath = "/v1/org/hierarchy-versions/" + hierarchyVersionID

func TestHierarchyDraftDelete(t *testing.T) {
	scoped := []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}
	tests := []struct {
		name       string
		path       string
		duties     string
		headers    []string
		err        error
		wantStatus int
		wantBody   string
		wantCalls  int
	}{
		{"deleted", hierarchyVersionPath + "?version=3", hierarchyDuties, nil, nil, http.StatusNoContent, "", 1},
		{"missing version", hierarchyVersionPath, hierarchyDuties, nil, nil, http.StatusBadRequest, `"version"`, 0},
		{"empty version", hierarchyVersionPath + "?version=", hierarchyDuties, nil, nil, http.StatusBadRequest, `"version"`, 0},
		{"non-integer version", hierarchyVersionPath + "?version=abc", hierarchyDuties, nil, nil, http.StatusBadRequest, `"version"`, 0},
		{"fractional version", hierarchyVersionPath + "?version=1.5", hierarchyDuties, nil, nil, http.StatusBadRequest, `"version"`, 0},
		{"overflowing version", hierarchyVersionPath + "?version=99999999999999999999", hierarchyDuties, nil, nil, http.StatusBadRequest, `"version"`, 0},
		{"malformed id", "/v1/org/hierarchy-versions/nope?version=1", hierarchyDuties, nil, nil, http.StatusNotFound, "ORG-HIER-VERSION-NOT-FOUND", 0},
		{"read duty only", hierarchyVersionPath + "?version=1", "org.read", nil, nil, http.StatusForbidden, "AUTH-0003", 0},
		{"legal-entity scoped caller", hierarchyVersionPath + "?version=1", hierarchyDuties, scoped, nil, http.StatusForbidden, "AUTH-0003", 0},
		{"store conflict passes through", hierarchyVersionPath + "?version=1", hierarchyDuties, nil, httpx.NewProblem(http.StatusConflict, store.CodeHierarchyVersionImmutable, "published"), http.StatusConflict, "ORG-HIER-VERSION-IMMUTABLE", 1},
		{"store failure is hidden", hierarchyVersionPath + "?version=1", hierarchyDuties, nil, errors.New("db down"), http.StatusInternalServerError, "COM-0500", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{err: tc.err}
			r, a := newRouter(f)
			rec := do(r, http.MethodDelete, tc.path, "", tc.duties, true, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) || f.deleteCalls != tc.wantCalls {
				t.Fatalf("got %d %q calls=%d; want %d containing %q calls=%d", rec.Code, rec.Body.String(), f.deleteCalls, tc.wantStatus, tc.wantBody, tc.wantCalls)
			}
			metrics := httptest.NewRecorder()
			a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			want := "org_hierarchy_drafts_deleted_total 0"
			if tc.wantStatus == http.StatusNoContent {
				want = "org_hierarchy_drafts_deleted_total 1"
			}
			if !strings.Contains(metrics.Body.String(), want) {
				t.Fatalf("metrics lack %q: %s", want, metrics.Body.String())
			}
		})
	}
}
