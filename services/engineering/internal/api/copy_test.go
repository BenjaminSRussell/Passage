package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func (f *fakeStore) CancelRouteVersion(_ context.Context, id string) (domain.RouteVersion, error) {
	if f.err != nil {
		return domain.RouteVersion{}, f.err
	}
	for i, v := range f.routes {
		if v.ID != id {
			continue
		}
		if v.Status == domain.StatusCancelled {
			return v, nil
		}
		if err := v.EnsureDraft(); err != nil {
			return v, err
		}
		f.routes[i].Status = domain.StatusCancelled
		return f.routes[i], nil
	}
	return domain.RouteVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodeRouteNotFound, "route version not found")
}

func TestCopyBomVersion(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		body    string
		catalog store.Snapshot
		duties  string
		status  int
		want    string
	}{
		{"copies an active version into a draft", rid(2), `{"effectiveFrom":"2027-01-01"}`, nil, "engineering.bom.write", http.StatusCreated, `"status":"draft"`},
		{"copies a cancelled version too", rid(3), `{"effectiveFrom":"2027-01-01","effectiveTo":"2027-12-31"}`, nil, "engineering.bom.write", http.StatusCreated, `"effectiveTo":"2027-12-31"`},
		{"missing date", rid(2), `{}`, nil, "engineering.bom.write", http.StatusBadRequest, "effectiveFrom"},
		{"end before start", rid(2), `{"effectiveFrom":"2027-02-01","effectiveTo":"2027-01-01"}`, nil, "engineering.bom.write", http.StatusBadRequest, "effectiveTo"},
		{"unknown source", rid(99), `{"effectiveFrom":"2027-01-01"}`, nil, "engineering.bom.write", http.StatusNotFound, store.CodeBomNotFound},
		{"not a uuid", "nope", `{"effectiveFrom":"2027-01-01"}`, nil, "engineering.bom.write", http.StatusBadRequest, "COM-0001"},
		{"read duty alone is refused", rid(2), `{"effectiveFrom":"2027-01-01"}`, nil, "engineering.read", http.StatusForbidden, "AUTH-0003"},
		{"a cycle the copy would close is refused", rid(2), `{"effectiveFrom":"2027-01-01"}`, store.Snapshot{"WHEEL": {{ID: rid(9), ProductID: "WHEEL", EffectiveFrom: day("2025-01-01"), Status: domain.StatusActive, Lines: []domain.BomLine{{LineNo: 1, ComponentProductID: "BIKE", Quantity: dec("1"), Series: dec("1"), LineType: domain.LineItem}}}}}, "engineering.bom.write", http.StatusUnprocessableEntity, domain.CodeBomCycle},
		{"copying onto the component itself is a cycle", rid(2), `{"effectiveFrom":"2027-01-01","productId":"WHEEL"}`, nil, "engineering.bom.write", http.StatusUnprocessableEntity, domain.CodeBomCycle},
		{"an empty line filter is refused", rid(2), `{"effectiveFrom":"2027-01-01","lineNos":[]}`, nil, "engineering.bom.write", http.StatusBadRequest, "lineNos"},
		{"a line the source does not have is refused", rid(2), `{"effectiveFrom":"2027-01-01","lineNos":[1,7]}`, nil, "engineering.bom.write", http.StatusBadRequest, "line 7 is not on the source version"},
		{"a site past the column limit is refused", rid(2), `{"effectiveFrom":"2027-01-01","siteId":"` + strings.Repeat("s", 65) + `"}`, nil, "engineering.bom.write", http.StatusBadRequest, "siteId"},
		{"an unknown field is refused", rid(2), `{"effectiveFrom":"2027-01-01","status":"active"}`, nil, "engineering.bom.write", http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixtures := lifecycleFixtures()
			fixtures[2].Status = domain.StatusCancelled
			f := &fakeStore{listed: fixtures, catalog: tc.catalog}
			r, _ := newRouter(f, approves)
			rec := do(r, http.MethodPost, versions+"/"+tc.id+":copy", tc.body, tc.duties, true)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("copy = %d %s; want %d containing %q", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
}

func TestCopyBomVersion_CopiesTheLinesAndGetsANewID(t *testing.T) {
	f := &fakeStore{listed: lifecycleFixtures()}
	r, a := newRouter(f, approves)
	rec := do(r, http.MethodPost, versions+"/"+rid(2)+":copy", `{"effectiveFrom":"2027-01-01"}`, "engineering.bom.write", true, "Idempotency-Key", "k1")
	var got struct {
		ID, ProductID, Status string
		Lines                 []struct{ ComponentProductID string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("copy = %d %s, %v", rec.Code, rec.Body, err)
	}
	if got.ID == rid(2) || got.ProductID != "BIKE" || got.Status != "draft" || len(got.Lines) != 1 || got.Lines[0].ComponentProductID != "WHEEL" {
		t.Fatalf("copy = %+v; want a new BIKE draft with the WHEEL line", got)
	}
	again := do(r, http.MethodPost, versions+"/"+rid(2)+":copy", `{"effectiveFrom":"2027-01-01"}`, "engineering.bom.write", true, "Idempotency-Key", "k1")
	if again.Code != http.StatusCreated || again.Body.String() != rec.Body.String() {
		t.Fatalf("replay = %d %s; want the same body", again.Code, again.Body)
	}
	other := do(r, http.MethodPost, versions+"/"+rid(3)+":copy", `{"effectiveFrom":"2027-01-01"}`, "engineering.bom.write", true, "Idempotency-Key", "k1")
	if other.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key for another source = %d %s; want 422", other.Code, other.Body)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "engineering_bom_versions_copied_total 2") {
		t.Fatalf("metrics = %s", metrics.Body)
	}
}

func TestCopyBomVersion_ToAnotherProductAndSiteWithChosenLines(t *testing.T) {
	minQty := dec("10")
	source := domain.BomVersion{ID: rid(5), ProductID: "BIKE", SiteID: "S1", EffectiveFrom: day("2025-01-01"), Status: domain.StatusExpired, FromQty: &minQty, Lines: []domain.BomLine{
		{LineNo: 10, ComponentProductID: "WHEEL", Quantity: dec("2"), Series: dec("1"), LineType: domain.LineItem},
		{LineNo: 20, ComponentProductID: "BELL", Quantity: dec("1"), Series: dec("1"), LineType: domain.LineItem},
		{LineNo: 30, ComponentProductID: "SEAT", Quantity: dec("1"), Series: dec("1"), LineType: domain.LineItem},
	}}
	f := &fakeStore{listed: []domain.BomVersion{source}}
	r, _ := newRouter(f, approves)
	rec := do(r, http.MethodPost, versions+"/"+rid(5)+":copy", `{"effectiveFrom":"2027-01-01","productId":"EBIKE","siteId":"","lineNos":[30,10]}`, "engineering.bom.write", true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("copy = %d %s", rec.Code, rec.Body)
	}
	got := f.created[len(f.created)-1]
	if got.ProductID != "EBIKE" || got.SiteID != "" || got.CopiedFrom != rid(5) || got.Status != domain.StatusDraft || got.FromQty == nil || !got.FromQty.Equal(dec("10")) {
		t.Fatalf("copy = %+v; want an all-site EBIKE draft from %s keeping the quantity range", got, rid(5))
	}
	if len(got.Lines) != 2 || got.Lines[0].LineNo != 10 || got.Lines[1].LineNo != 30 {
		t.Fatalf("lines = %+v; want lines 10 and 30 in source order", got.Lines)
	}
	if !strings.Contains(rec.Body.String(), `"copiedFromVersionId":"`+rid(5)+`"`) {
		t.Fatalf("body = %s; want the lineage", rec.Body)
	}
	kept := do(r, http.MethodPost, versions+"/"+rid(5)+":copy", `{"effectiveFrom":"2027-01-01"}`, "engineering.bom.write", true)
	if last := f.created[len(f.created)-1]; kept.Code != http.StatusCreated || last.ProductID != "BIKE" || last.SiteID != "S1" || len(last.Lines) != 3 {
		t.Fatalf("plain copy = %d %+v; want the source product, site and every line", kept.Code, last)
	}
}

func TestCopyRouteVersion_ToAnotherSiteKeepsLineage(t *testing.T) {
	f := &fakeStore{routes: routeFixtures()}
	r, _ := newRouter(f, approves)
	rec := do(r, http.MethodPost, routesPath+"/"+rid(3)+":copy", `{"effectiveFrom":"2027-01-01","productId":"EBIKE","siteId":"S9"}`, routeWriter, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("copy = %d %s", rec.Code, rec.Body)
	}
	got := f.routes[len(f.routes)-1]
	if got.ProductID != "EBIKE" || got.SiteID != "S9" || got.RouteCode != "R" || got.CopiedFrom != rid(3) || len(got.Operations) != 1 {
		t.Fatalf("copy = %+v; want an EBIKE route R at S9 copied from %s", got, rid(3))
	}
	if !strings.Contains(rec.Body.String(), `"copiedFromVersionId":"`+rid(3)+`"`) {
		t.Fatalf("body = %s; want the lineage", rec.Body)
	}
	if rec := do(r, http.MethodPost, routesPath+"/"+rid(3)+":copy", `{"effectiveFrom":"2027-01-01","lineNos":[10]}`, routeWriter, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("lineNos on a route copy = %d; want 400", rec.Code)
	}
}

func TestCopyRouteVersion(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		body   string
		duties string
		status int
		want   string
	}{
		{"copies into a draft with the next number", rid(3), `{"effectiveFrom":"2027-01-01"}`, routeWriter, http.StatusCreated, `"status":"draft"`},
		{"missing date", rid(3), `{}`, routeWriter, http.StatusBadRequest, "effectiveFrom"},
		{"unknown source", rid(99), `{"effectiveFrom":"2027-01-01"}`, routeWriter, http.StatusNotFound, store.CodeRouteNotFound},
		{"bom writer is refused", rid(3), `{"effectiveFrom":"2027-01-01"}`, "engineering.bom.write", http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{routes: routeFixtures()}
			r, a := newRouter(f, approves)
			rec := do(r, http.MethodPost, routesPath+"/"+tc.id+":copy", tc.body, tc.duties, true)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("copy = %d %s; want %d containing %q", rec.Code, rec.Body, tc.status, tc.want)
			}
			if tc.status == http.StatusCreated {
				var got struct {
					ID, RouteCode, ProductID, Status string
					VersionNo, OperationCount        int
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.ID == rid(3) || got.RouteCode != "R" || got.ProductID != "BIKE" || got.OperationCount != 1 || got.VersionNo != 7 {
					t.Fatalf("copy = %+v, %v; want a new BIKE R draft numbered 7 with the operation", got, err)
				}
				metrics := httptest.NewRecorder()
				a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				if !strings.Contains(metrics.Body.String(), "engineering_route_versions_copied_total 1") {
					t.Fatalf("metrics = %s", metrics.Body)
				}
			}
		})
	}
}

func TestCancelRouteVersion(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		duties string
		status int
		want   string
	}{
		{"cancels a draft", rid(4), routeWriter, http.StatusOK, `"status":"cancelled"`},
		{"repeat is unchanged", rid(4), routeWriter, http.StatusOK, `"status":"cancelled"`},
		{"an active version is refused", rid(3), routeWriter, http.StatusConflict, domain.CodeRouteState},
		{"unknown id", rid(99), routeWriter, http.StatusNotFound, store.CodeRouteNotFound},
		{"read duty alone is refused", rid(4), "engineering.read", http.StatusForbidden, "AUTH-0003"},
		{"not a uuid", "nope", routeWriter, http.StatusBadRequest, "COM-0001"},
	}
	f := &fakeStore{routes: routeFixtures()}
	r, a := newRouter(f, approves)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodPost, routesPath+"/"+tc.id+":cancel", "", tc.duties, true)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("cancel = %d %s; want %d containing %q", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
	if rec := do(r, http.MethodPost, routesPath+"/"+rid(4)+":cancel", "", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant = %d; want 401", rec.Code)
	}
	if rec := do(r, http.MethodGet, routesPath+"?status=cancelled", "", "engineering.read", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), rid(4)) {
		t.Fatalf("list cancelled = %d %s", rec.Code, rec.Body)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "engineering_route_versions_cancelled_total 2") {
		t.Fatalf("metrics = %s", metrics.Body)
	}
}
