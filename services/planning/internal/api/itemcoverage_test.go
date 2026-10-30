package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func (f *fakeStore) ListItemCoverages(_ context.Context, itemID string, limit int) ([]store.ItemCoverage, error) {
	f.lastLimit = limit
	out := []store.ItemCoverage{}
	for _, c := range f.covs {
		if itemID == "" || c.Spec.ItemID == itemID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) GetItemCoverage(_ context.Context, id string) (store.ItemCoverage, error) {
	for _, c := range f.covs {
		if c.ID == id {
			return c, nil
		}
	}
	return store.ItemCoverage{}, httpx.NewProblem(http.StatusNotFound, store.CodeItemCoverageNotFound, "item coverage not found")
}

// checkItemCoverage mirrors the store's constraints: the coverage group must
// exist and the item, site and scope must be free (skipping the row self).
func (f *fakeStore) checkItemCoverage(self string, spec domain.ItemCoverageSpec) (string, error) {
	code := ""
	for _, g := range f.groups {
		if g.ID == spec.CoverageGroupID {
			code = g.Spec.Code
		}
	}
	if code == "" {
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCoverageGroupUnknown, "no group")
	}
	for _, c := range f.covs {
		s := c.Spec
		if c.ID != self && s.ItemID == spec.ItemID && s.SiteID == spec.SiteID && s.PlanningScope == spec.PlanningScope &&
			s.WarehouseID == spec.WarehouseID && (s.PlanningScope != domain.PlanScopeSubcontractor || s.VendorID == spec.VendorID) {
			return "", httpx.NewProblem(http.StatusConflict, store.CodeItemCoverageExists, "exists")
		}
	}
	return code, nil
}

func (f *fakeStore) CreateItemCoverage(_ context.Context, req store.ItemCoverageCreate) (store.ItemCoverage, bool, error) {
	if hash, ok := f.covKeys[req.IdempotencyKey]; ok && req.IdempotencyKey != "" {
		if hash != req.InputHash {
			return store.ItemCoverage{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		return f.covs[len(f.covs)-1], true, nil
	}
	code, err := f.checkItemCoverage("", req.Spec)
	if err != nil {
		return store.ItemCoverage{}, false, err
	}
	if req.IdempotencyKey != "" {
		if f.covKeys == nil {
			f.covKeys = map[string]string{}
		}
		f.covKeys[req.IdempotencyKey] = req.InputHash
	}
	c := store.ItemCoverage{ID: testkit.TenantID(len(f.covs) + 20), Version: 1, CoverageGroupCode: code, Spec: req.Spec}
	f.covs = append(f.covs, c)
	return c, false, nil
}

func (f *fakeStore) UpdateItemCoverage(_ context.Context, id string, expected int, spec domain.ItemCoverageSpec) (store.ItemCoverage, error) {
	for i, c := range f.covs {
		if c.ID != id {
			continue
		}
		if c.Version != expected {
			return store.ItemCoverage{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
		}
		code, err := f.checkItemCoverage(id, spec)
		if err != nil {
			return store.ItemCoverage{}, err
		}
		f.covs[i] = store.ItemCoverage{ID: id, Version: c.Version + 1, CoverageGroupCode: code, Spec: spec}
		return f.covs[i], nil
	}
	return store.ItemCoverage{}, httpx.NewProblem(http.StatusNotFound, store.CodeItemCoverageNotFound, "item coverage not found")
}

const itemCoveragePath = "/v1/planning/item-coverages"

// seedGroup creates the coverage group REQ the item coverage tests reference
// and returns its id.
func seedGroup(t *testing.T, st *fakeStore) string {
	t.Helper()
	rec := do(t, st, http.MethodPost, coveragePath, `{"code":"REQ","coverageCode":"requirement"}`, dutySetup, true, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed group: %d %s", rec.Code, rec.Body.String())
	}
	return decodeGroup(t, rec).ID
}

func itemCoverageBody(itemID, groupID, extra string) string {
	return `{"itemId":"` + itemID + `","siteId":"` + siteID + `","coverageGroupId":"` + groupID + `"` + extra + `}`
}

func decodeItemCoverageDTO(t *testing.T, rec *httptest.ResponseRecorder) itemCoverageDTO {
	t.Helper()
	var c itemCoverageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return c
}

// TestCreateItemCoverage_AppliesDefaults: omitted fields take site scope,
// purchase orders, mts-net and no lot limits; absent references are null and
// the response names the coverage group's code.
func TestCreateItemCoverage_AppliesDefaults(t *testing.T) {
	st := &fakeStore{}
	g := seedGroup(t, st)
	rec := do(t, st, http.MethodPost, itemCoveragePath, itemCoverageBody(fgID, g, `,"warehouseId":null`), dutySetup, true, "")
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	got := decodeItemCoverageDTO(t, rec)
	if got.PlannedOrderType != domain.OrderPurchase || got.PlanningStrategy != domain.StrategyMTSNet ||
		got.PlanningScope != domain.PlanScopeSite || got.MinQty != "0" || got.MaxQty != "0" || got.Version != 1 ||
		got.CoverageGroupCode != "REQ" {
		t.Fatalf("defaults not applied: %+v", got)
	}
	if got.WarehouseID != nil || got.VendorID != nil || got.LeadTimeDays != nil || got.BufferProfileID != nil {
		t.Fatalf("absent references must be null: %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"warehouseId":null`) {
		t.Fatalf("null references are not sent as null: %s", rec.Body.String())
	}
}

// TestCreateItemCoverage_KeepsDecimalQuantities: quantities travel as exact
// decimal strings, never through a float.
func TestCreateItemCoverage_KeepsDecimalQuantities(t *testing.T) {
	st := &fakeStore{}
	g := seedGroup(t, st)
	rec := do(t, st, http.MethodPost, itemCoveragePath, itemCoverageBody(fgID, g, `,"minQty":"0.000001","maxQty":"1000000000000"`), dutySetup, true, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("max beyond numeric(18,6): %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, st, http.MethodPost, itemCoveragePath, itemCoverageBody(fgID, g, `,"minQty":"0.000001","maxQty":"999999999999.999999"`), dutySetup, true, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if c := decodeItemCoverageDTO(t, rec); c.MinQty != "0.000001" || c.MaxQty != "999999999999.999999" {
		t.Fatalf("quantities changed: %+v", c)
	}
}

func TestCreateItemCoverage_Refusals(t *testing.T) {
	cases := []struct {
		name, extra, duties string
		status              int
		code                string
		fields              []string
	}{
		{"quantities are strings, not JSON numbers", `,"maxQty":1`, dutySetup, http.StatusBadRequest, codeValidation, nil},
		{"quantity not a decimal is named", `,"minQty":"abc"`, dutySetup, http.StatusBadRequest, codeValidation, []string{"minQty"}},
		{"warehouse scope without warehouse, kanban", `,"planningScope":"warehouse","plannedOrderType":"kanban"`, dutySetup,
			http.StatusBadRequest, codeValidation, []string{"warehouseId", "plannedOrderType"}},
		{"max below min", `,"minQty":"100","maxQty":"50"`, dutySetup, http.StatusBadRequest, codeValidation, []string{"maxQty"}},
		{"subcontractor scope needs vendor and transfer", `,"planningScope":"subcontractor"`, dutySetup, http.StatusBadRequest,
			codeValidation, []string{"vendorId", "plannedOrderType"}},
		{"unknown field", `,"mrpType":"PD"`, dutySetup, http.StatusBadRequest, codeValidation, nil},
		{"read duty cannot create", ``, dutyRead, http.StatusForbidden, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{}
			g := seedGroup(t, st)
			rec := do(t, st, http.MethodPost, itemCoveragePath, itemCoverageBody(fgID, g, tc.extra), tc.duties, true, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if len(st.covs) != 0 {
				t.Fatalf("a refused create was stored: %+v", st.covs)
			}
			if tc.code == "" {
				return
			}
			p := problemOf(t, rec)
			if p.Code != tc.code {
				t.Fatalf("code %q, want %q", p.Code, tc.code)
			}
			for _, f := range tc.fields {
				if _, ok := p.Errors[f]; !ok {
					t.Fatalf("problem does not name %s: %v", f, p.Errors)
				}
			}
		})
	}
}

func TestCreateItemCoverage_DuplicateUnknownGroupAndIdempotency(t *testing.T) {
	st := &fakeStore{}
	g := seedGroup(t, st)
	body := itemCoverageBody(fgID, g, "")
	if rec := do(t, st, http.MethodPost, itemCoveragePath, body, dutySetup, true, "ic-1"); rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}
	replay := do(t, st, http.MethodPost, itemCoveragePath, body, dutySetup, true, "ic-1")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || len(st.covs) != 1 {
		t.Fatalf("replay: %d %q, %d rows", replay.Code, replay.Header().Get("Idempotent-Replayed"), len(st.covs))
	}
	reuse := do(t, st, http.MethodPost, itemCoveragePath, itemCoverageBody(fgID, g, `,"minQty":"5"`), dutySetup, true, "ic-1")
	if reuse.Code != http.StatusUnprocessableEntity || problemOf(t, reuse).Code != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %d %s", reuse.Code, reuse.Body.String())
	}
	otherEntity := do(t, st, http.MethodPost, itemCoveragePath, itemCoverageBody(fgID, g, `,"legalEntityId":"`+testkit.TenantID(77)+`"`), dutySetup, true, "ic-1")
	if otherEntity.Code != http.StatusUnprocessableEntity || problemOf(t, otherEntity).Code != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse across entities: %d %s", otherEntity.Code, otherEntity.Body.String())
	}
	dup := do(t, st, http.MethodPost, itemCoveragePath, body, dutySetup, true, "")
	if dup.Code != http.StatusConflict || problemOf(t, dup).Code != store.CodeItemCoverageExists {
		t.Fatalf("duplicate: %d %s", dup.Code, dup.Body.String())
	}
	unknown := do(t, st, http.MethodPost, itemCoveragePath, itemCoverageBody(testkit.TenantID(42), testkit.TenantID(99), ""), dutySetup, true, "")
	if unknown.Code != http.StatusUnprocessableEntity || problemOf(t, unknown).Code != store.CodeCoverageGroupUnknown {
		t.Fatalf("unknown group: %d %s", unknown.Code, unknown.Body.String())
	}
	if rec := do(t, st, http.MethodPost, itemCoveragePath, body, dutySetup, true, strings.Repeat("k", 256)); rec.Code != http.StatusBadRequest {
		t.Fatalf("overlong key: %d", rec.Code)
	}
}

func TestItemCoverage_ListGetAndUpdate(t *testing.T) {
	st := &fakeStore{}
	g := seedGroup(t, st)
	created := decodeItemCoverageDTO(t, do(t, st, http.MethodPost, itemCoveragePath, itemCoverageBody(fgID, g, ""), dutySetup, true, ""))
	other := testkit.TenantID(42)
	do(t, st, http.MethodPost, itemCoveragePath, itemCoverageBody(other, g, ""), dutySetup, true, "")

	list := do(t, st, http.MethodGet, itemCoveragePath+"?itemId="+fgID+"&limit=10", "", dutyRead, true, "")
	var page struct {
		Items []itemCoverageDTO `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	if len(page.Items) != 1 || page.Items[0].ItemID != fgID || st.lastLimit != 10 {
		t.Fatalf("filter by item: %+v (limit %d)", page.Items, st.lastLimit)
	}
	for _, q := range []string{"?itemId=FG-100", "?limit=0"} {
		if rec := do(t, st, http.MethodGet, itemCoveragePath+q, "", dutyRead, true, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}

	path := itemCoveragePath + "/" + created.ID
	get := do(t, st, http.MethodGet, path, "", dutyRead, true, "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"1"` || decodeItemCoverageDTO(t, get).ID != created.ID {
		t.Fatalf("get: %d etag %q", get.Code, get.Header().Get("ETag"))
	}
	for _, p := range []string{itemCoveragePath + "/" + testkit.TenantID(77), itemCoveragePath + "/not-a-uuid"} {
		if rec := do(t, st, http.MethodGet, p, "", dutyRead, true, ""); rec.Code != http.StatusNotFound || problemOf(t, rec).Code != store.CodeItemCoverageNotFound {
			t.Fatalf("get %s: %d", p, rec.Code)
		}
	}

	update := itemCoverageBody(fgID, g, `,"planningScope":"warehouse","warehouseId":"`+testkit.TenantID(7)+`","minQty":"10","maxQty":"40","leadTimeDays":3`)
	if rec := doIfMatch(t, st, http.MethodPut, path, update, "", dutySetup); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("update without If-Match: %d", rec.Code)
	}
	if rec := doIfMatch(t, st, http.MethodPut, path, update, `"1"`, dutyRead); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty update: %d", rec.Code)
	}
	rec := doIfMatch(t, st, http.MethodPut, path, update, `"1"`, dutySetup)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("update: %d %s etag %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	if c := decodeItemCoverageDTO(t, rec); c.PlanningScope != domain.PlanScopeWarehouse || c.WarehouseID == nil ||
		c.MinQty != "10" || c.LeadTimeDays == nil || *c.LeadTimeDays != 3 || c.Version != 2 {
		t.Fatalf("updated coverage: %+v", c)
	}
	stale := doIfMatch(t, st, http.MethodPut, path, update, `"1"`, dutySetup)
	if stale.Code != http.StatusPreconditionFailed || problemOf(t, stale).Code != store.CodeVersionConflict {
		t.Fatalf("stale update: %d %s", stale.Code, stale.Body.String())
	}
	if rec := doIfMatch(t, st, http.MethodPut, path, itemCoverageBody(fgID, g, `,"planningScope":"warehouse"`), `"2"`, dutySetup); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid update: %d", rec.Code)
	}
	if rec := doIfMatch(t, st, http.MethodPut, itemCoveragePath+"/"+testkit.TenantID(77), update, `"1"`, dutySetup); rec.Code != http.StatusNotFound {
		t.Fatalf("update of unknown coverage: %d", rec.Code)
	}
}
