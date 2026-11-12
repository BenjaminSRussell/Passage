package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// slice3 is the part of fakeStore behind the slice 3 endpoints. Activation
// plans come from the real domain function, so the DTO mapping is checked
// against real arithmetic; the store's own rules are covered by the DB tests.
type slice3 struct {
	version       store.CostingVersion
	pending       []domain.PendingCost
	alreadyActive bool
	onHand        store.OnHand
	expected      int
	committed     int
	listFilter    store.VersionFilter
	listAfter     string
	listLimit     int
	itemAfter     string
	copySource    string
	copyPercent   string
	cost          *store.CostLookup
	costAsOf      string
}

func (f *fakeStore) ListVersions(_ context.Context, vf store.VersionFilter, after string, limit int) (store.VersionPage, error) {
	f.listFilter, f.listAfter, f.listLimit = vf, after, limit
	return store.VersionPage{Items: []store.CostingVersion{f.version}, Next: "STD-2027|" + f.version.ID}, nil
}

func (f *fakeStore) GetVersion(_ context.Context, id string) (store.CostingVersion, error) {
	if id != f.version.ID {
		return store.CostingVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodeVersionNotFound, "costing version not found")
	}
	return f.version, nil
}

func (f *fakeStore) ListItemCosts(_ context.Context, _, after string, _ int) (store.ItemCostPage, error) {
	f.itemAfter = after
	return store.ItemCostPage{Items: []store.ItemCostRow{}}, nil
}

func (f *fakeStore) CopyItemCosts(_ context.Context, _, source string, percent decimal.Decimal) (int, error) {
	if _, err := domain.CopyCost(decimal.NewFromInt(10), percent); err != nil {
		return 0, err
	}
	f.copySource, f.copyPercent = source, percent.String()
	return 2, nil
}

func (f *fakeStore) plan(onHand store.OnHand) (store.ActivationResult, error) {
	f.onHand = onHand
	res := store.ActivationResult{Version: f.version, AlreadyActive: f.alreadyActive}
	if f.alreadyActive {
		return res, nil
	}
	if err := domain.CheckActivatable(domain.VersionState{Type: f.version.Type, EffectiveFrom: f.version.EffectiveFrom,
		Blocked: f.version.BlockActivation}, "2026-10-27"); err != nil {
		return res, err
	}
	pending := make([]domain.PendingCost, 0, len(f.pending))
	for _, p := range f.pending {
		oh := onHand[store.OnHandKey(p.ProductID, p.SiteID)]
		p.OnHandQty, p.BookValue = oh.Qty, oh.BookValue
		pending = append(pending, p)
	}
	plan, err := domain.PlanActivation(false, 2, pending)
	res.Plan = plan
	return res, err
}

func (f *fakeStore) PreviewActivation(_ context.Context, _ string, onHand store.OnHand) (store.ActivationResult, error) {
	return f.plan(onHand)
}

func (f *fakeStore) ActivateVersion(_ context.Context, _ string, expected int, onHand store.OnHand) (store.ActivationResult, error) {
	f.expected = expected
	if !f.alreadyActive && expected != f.version.Version {
		return store.ActivationResult{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeConflictVersion, "changed")
	}
	res, err := f.plan(onHand)
	if err == nil && !res.AlreadyActive {
		f.committed++
		res.Version.Status, res.Version.Version = "ACTIVE", f.version.Version+1
	}
	return res, err
}

func (f *fakeStore) CurrentCost(_ context.Context, _, _, asOf string) (store.CostLookup, error) {
	f.costAsOf = asOf
	if f.cost == nil {
		return store.CostLookup{}, httpx.NewProblem(http.StatusNotFound, store.CodeCostNotFound, "no standard cost")
	}
	return *f.cost, nil
}

func draftVersion() store.CostingVersion {
	return store.CostingVersion{ID: ids.New(), LegalEntityID: ids.New(), Code: "STD-2026", Type: "STANDARD", Status: "DRAFT",
		Currency: "EUR", EffectiveFrom: "2026-01-01", Version: 3}
}

func problemCode(t *testing.T, body map[string]any) string {
	t.Helper()
	code, _ := body["code"].(string)
	return code
}

// TestActivation_PreviewThenActivate_FR205: the preview writes nothing, the
// activation needs the previewed version in If-Match, and both report
// 100 × (10 − 9) + (20 × 5 − 90.00 book value) = 110.00 for the site.
func TestActivation_PreviewThenActivate_FR205(t *testing.T) {
	h, fs := server()
	fs.version = draftVersion()
	p1, p2, s1 := ids.New(), ids.New(), ids.New()
	fs.pending = []domain.PendingCost{
		{ProductID: p1, SiteID: s1, NewCost: dec("10"), PreviousCost: dec("9"), HasPrevious: true},
		{ProductID: p2, SiteID: s1, NewCost: dec("5")},
	}
	body := fmt.Sprintf(`{"onHand":[{"productId":%q,"siteId":%q,"qty":"100"},{"productId":%q,"siteId":%q,"qty":"20","bookValue":"90"}]}`,
		p1, s1, strings.ToUpper(p2), s1)
	base := "/v1/costing/versions/" + fs.version.ID

	rec := call(h, http.MethodPost, base+":preview-activation", body, "costing.read", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := decodeMap(rec)
	if m["status"] != "PREVIEW" || m["revaluationTotal"] != "110.00" || m["version"] != float64(3) || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("preview body %v etag %s", m, rec.Header().Get("ETag"))
	}
	top := m["topDeltas"].([]any)
	first := top[0].(map[string]any)
	if first["productId"] != p1 || first["previousCost"] != "9" || first["revaluation"] != "100.00" {
		t.Errorf("largest delta = %v, want p1 at 100.00 from 9", first)
	}
	second := top[1].(map[string]any)
	if _, has := second["previousCost"]; has || second["bookValue"] != "90.00" || second["revaluation"] != "10.00" {
		t.Errorf("first-standard delta = %v, want book value 90.00 and no previous cost", second)
	}
	if fs.committed != 0 {
		t.Fatalf("preview committed %d times", fs.committed)
	}

	if rec = call(h, http.MethodPost, base+":activate", body, allDuties, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("activate without If-Match status %d, want 428", rec.Code)
	}
	if rec = call(h, http.MethodPost, base+":activate", body, allDuties, map[string]string{"If-Match": "abc"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("activate with a malformed If-Match status %d, want 400", rec.Code)
	}
	if rec = call(h, http.MethodPost, base+":activate", body, allDuties, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("activate with a stale If-Match status %d, want 412", rec.Code)
	}
	rec = call(h, http.MethodPost, base+":activate", body, allDuties, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("activate status %d: %s", rec.Code, rec.Body.String())
	}
	m, _ = decodeMap(rec)
	if m["status"] != "ACTIVATED" || m["revaluationTotal"] != "110.00" || fs.committed != 1 || fs.expected != 3 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("activate body %v, committed %d, expected %d, etag %s", m, fs.committed, fs.expected, rec.Header().Get("ETag"))
	}
	sites := m["sites"].([]any)
	if len(sites) != 1 || sites[0].(map[string]any)["revaluation"] != "110.00" {
		t.Errorf("site vouchers = %v", sites)
	}
}

func TestActivation_RefusalsAreProblems(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*slice3)
		status int
		code   string
	}{
		{"blocked", func(s *slice3) { s.version.BlockActivation = true }, http.StatusUnprocessableEntity, domain.CodeActivationBlocked},
		{"planned", func(s *slice3) { s.version.Type = "PLANNED" }, http.StatusUnprocessableEntity, domain.CodeNotStandard},
		{"future effective date", func(s *slice3) { s.version.EffectiveFrom = "2099-01-01" }, http.StatusUnprocessableEntity, domain.CodeFutureEffective},
		{"nothing pending", func(s *slice3) { s.pending = nil }, http.StatusUnprocessableEntity, domain.CodeNoPending},
		{"zero cost", func(s *slice3) { s.pending[0].NewCost = decimal.Zero }, http.StatusUnprocessableEntity, domain.CodeZeroCost},
		{"stock without a book value", func(s *slice3) { s.pending[0].HasPrevious = false }, http.StatusUnprocessableEntity, domain.CodeBookValueMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, fs := server()
			fs.version = draftVersion()
			p, s := ids.New(), ids.New()
			fs.pending = []domain.PendingCost{{ProductID: p, SiteID: s, NewCost: dec("2"), PreviousCost: dec("1"), HasPrevious: true}}
			tc.mutate(&fs.slice3)
			body := fmt.Sprintf(`{"onHand":[{"productId":%q,"siteId":%q,"qty":"5"}]}`, p, s)
			rec := call(h, http.MethodPost, "/v1/costing/versions/"+fs.version.ID+":preview-activation", body, allDuties, nil)
			m, _ := decodeMap(rec)
			if rec.Code != tc.status || problemCode(t, m) != tc.code {
				t.Errorf("status %d code %v, want %d %s", rec.Code, m["code"], tc.status, tc.code)
			}
		})
	}
}

func TestActivation_AlreadyActiveReplays(t *testing.T) {
	h, fs := server()
	fs.version = draftVersion()
	fs.version.Status = "ACTIVE"
	fs.alreadyActive = true
	// A replay needs no current If-Match: activation is idempotent per version id.
	rec := call(h, http.MethodPost, "/v1/costing/versions/"+fs.version.ID+":activate", `{"onHand":[]}`, allDuties, map[string]string{"If-Match": `"1"`})
	m, _ := decodeMap(rec)
	if rec.Code != http.StatusOK || m["status"] != "ALREADY_ACTIVE" || fs.committed != 0 {
		t.Fatalf("status %d body %v committed %d", rec.Code, m, fs.committed)
	}
}

func TestActivation_ValidatesOnHand(t *testing.T) {
	h, fs := server()
	fs.version = draftVersion()
	p, s := ids.New(), ids.New()
	path := "/v1/costing/versions/" + fs.version.ID + ":preview-activation"
	cases := map[string]string{
		"onHand[0].productId": `{"onHand":[{"productId":"x","siteId":"` + s + `","qty":"1"}]}`,
		"onHand[0].qty":       `{"onHand":[{"productId":"` + p + `","siteId":"` + s + `","qty":"1.0000001"}]}`,
		"onHand[0].bookValue": `{"onHand":[{"productId":"` + p + `","siteId":"` + s + `","qty":"1","bookValue":"abc"}]}`,
		"onHand[1]":           `{"onHand":[{"productId":"` + p + `","siteId":"` + s + `","qty":"1"},{"productId":"` + strings.ToUpper(p) + `","siteId":"` + s + `","qty":"2"}]}`,
	}
	for field, body := range cases {
		rec := call(h, http.MethodPost, path, body, allDuties, nil)
		m, _ := decodeMap(rec)
		errs, _ := m["errors"].(map[string]any)
		if rec.Code != http.StatusBadRequest || errs[field] == nil {
			t.Errorf("%s: status %d errors %v", field, rec.Code, errs)
		}
	}
	// Negative physical on-hand is legal stock; it is not a validation error.
	neg := `{"onHand":[{"productId":"` + p + `","siteId":"` + s + `","qty":"-3"}]}`
	fs.pending = []domain.PendingCost{{ProductID: p, SiteID: s, NewCost: dec("2"), PreviousCost: dec("1"), HasPrevious: true}}
	rec := call(h, http.MethodPost, path, neg, allDuties, nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusOK || m["revaluationTotal"] != "-3.00" {
		t.Errorf("negative on-hand: status %d body %v", rec.Code, m)
	}
	if rec := call(h, http.MethodPost, "/v1/costing/versions/nope:preview-activation", `{"onHand":[]}`, allDuties, nil); rec.Code != http.StatusNotFound {
		t.Errorf("malformed version id status %d, want 404", rec.Code)
	}
}

func TestActivation_Duties(t *testing.T) {
	h, fs := server()
	fs.version = draftVersion()
	path := "/v1/costing/versions/" + fs.version.ID
	if rec := call(h, http.MethodPost, path+":activate", `{"onHand":[]}`, "costing.read,costing.version.write", map[string]string{"If-Match": "3"}); rec.Code != http.StatusForbidden {
		t.Errorf("activate without costing.version.activate: status %d, want 403", rec.Code)
	}
	if rec := call(h, http.MethodPost, path+":preview-activation", `{"onHand":[]}`, "costing.version.activate", nil); rec.Code != http.StatusForbidden {
		t.Errorf("preview without costing.read: status %d, want 403", rec.Code)
	}
	if rec := call(h, http.MethodPost, path+"/item-costs:copy", `{}`, "costing.read", nil); rec.Code != http.StatusForbidden {
		t.Errorf("copy without costing.version.write: status %d, want 403", rec.Code)
	}
}

func TestCopyItemCosts_PercentChange(t *testing.T) {
	h, fs := server()
	target, source := ids.New(), ids.New()
	path := "/v1/costing/versions/" + target + "/item-costs:copy"
	rec := call(h, http.MethodPost, path, `{"sourceVersionId":"`+strings.ToUpper(source)+`","percentChange":"-2.5"}`, allDuties, nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusOK || m["copied"] != float64(2) || fs.copyPercent != "-2.5" || fs.copySource != source {
		t.Fatalf("copy status %d body %v percent %s source %s", rec.Code, m, fs.copyPercent, fs.copySource)
	}
	rec = call(h, http.MethodPost, path, `{"sourceVersionId":"`+source+`","percentChange":"-100"}`, allDuties, nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusUnprocessableEntity || m["code"] != domain.CodeBadPercent {
		t.Errorf("-100 %% status %d body %v", rec.Code, m)
	}
	rec = call(h, http.MethodPost, path, `{"sourceVersionId":"x","percentChange":"ten"}`, allDuties, nil)
	m, _ := decodeMap(rec)
	errs, _ := m["errors"].(map[string]any)
	if rec.Code != http.StatusBadRequest || errs["sourceVersionId"] == nil || errs["percentChange"] == nil {
		t.Errorf("bad copy body: status %d errors %v", rec.Code, errs)
	}
}

func TestImportItemCosts_CSV(t *testing.T) {
	h, fs := server()
	p, s := ids.New(), ids.New()
	path := "/v1/costing/versions/" + ids.New() + "/item-costs:import"
	csvHeader := map[string]string{"Content-Type": "text/csv; charset=utf-8"}
	rec := call(h, http.MethodPost, path, "productId,siteId,unitCost\n"+strings.ToUpper(p)+","+s+",12.50\n", allDuties, csvHeader)
	if m, _ := decodeMap(rec); rec.Code != http.StatusOK || m["imported"] != float64(1) {
		t.Fatalf("csv import status %d body %v", rec.Code, m)
	}
	if len(fs.imported) != 1 || fs.imported[0].ProductID != p || fs.imported[0].UnitCost != "12.5" {
		t.Errorf("stored %+v, want the lower-cased product at 12.5", fs.imported)
	}
	rec = call(h, http.MethodPost, path, "productId,siteId,unitCost\n"+p+","+s+",-1\n", allDuties, csvHeader)
	if m, _ := decodeMap(rec); rec.Code != http.StatusUnprocessableEntity || m["code"] != domain.CodeImportInvalid ||
		!strings.Contains(m["detail"].(string), "line 2") {
		t.Errorf("bad csv: status %d body %v", rec.Code, m)
	}
	if len(fs.imported) != 1 {
		t.Errorf("a refused file reached the store: %d rows", len(fs.imported))
	}
}

func TestVersionReads(t *testing.T) {
	h, fs := server()
	fs.version = draftVersion()
	le := testLE
	rec := call(h, http.MethodGet, "/v1/costing/versions?legalEntityId="+le+"&status=DRAFT&type=STANDARD&limit=10", "", "costing.read", nil)
	m, _ := decodeMap(rec)
	if rec.Code != http.StatusOK || fs.listFilter != (store.VersionFilter{LegalEntityID: le, Status: "DRAFT", Type: "STANDARD"}) || fs.listLimit != 10 {
		t.Fatalf("list status %d filter %+v limit %d", rec.Code, fs.listFilter, fs.listLimit)
	}
	next, _ := m["nextCursor"].(string)
	if rec = call(h, http.MethodGet, "/v1/costing/versions?cursor="+next, "", "costing.read", nil); rec.Code != http.StatusOK || fs.listAfter != "STD-2027|"+fs.version.ID {
		t.Fatalf("next page status %d after %q", rec.Code, fs.listAfter)
	}
	for _, q := range []string{"status=OPEN", "type=ACTUAL", "legalEntityId=x", "limit=0", "limit=201", "cursor=%25%25"} {
		if rec = call(h, http.MethodGet, "/v1/costing/versions?"+q, "", "costing.read", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("list ?%s status %d, want 400", q, rec.Code)
		}
	}

	rec = call(h, http.MethodGet, "/v1/costing/versions/"+fs.version.ID, "", "costing.read", nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusOK || m["code"] != "STD-2026" || rec.Header().Get("ETag") != `"3"` {
		t.Errorf("get status %d body %v etag %s", rec.Code, m, rec.Header().Get("ETag"))
	}
	if rec = call(h, http.MethodGet, "/v1/costing/versions/"+ids.New(), "", "costing.read", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown version status %d, want 404", rec.Code)
	}

	cur := base64.RawURLEncoding.EncodeToString([]byte(ids.New() + "|" + ids.New()))
	if rec = call(h, http.MethodGet, "/v1/costing/versions/"+fs.version.ID+"/item-costs?cursor="+cur, "", "costing.read", nil); rec.Code != http.StatusOK || fs.itemAfter == "" {
		t.Errorf("item costs status %d after %q", rec.Code, fs.itemAfter)
	}
	// An item-cost cursor must be two UUIDs: a version-list cursor is refused.
	bad := base64.RawURLEncoding.EncodeToString([]byte("STD-2026|" + ids.New()))
	if rec = call(h, http.MethodGet, "/v1/costing/versions/"+fs.version.ID+"/item-costs?cursor="+bad, "", "costing.read", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("foreign cursor status %d, want 400", rec.Code)
	}
}

func TestCurrentCost_FR205Lookup(t *testing.T) {
	h, fs := server()
	product, site := ids.New(), ids.New()
	path := "/v1/costing/products/" + product + "/cost?site=" + site
	rec := call(h, http.MethodGet, path+"&date=2026-03-01", "", "costing.read", nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusNotFound || m["code"] != store.CodeCostNotFound {
		t.Fatalf("no cost: status %d body %v", rec.Code, m)
	}
	fs.cost = &store.CostLookup{ProductID: product, SiteID: site, UnitCost: "11", Currency: "EUR", VersionCode: "STD-2026"}
	rec = call(h, http.MethodGet, path+"&date=2026-03-01", "", "costing.read", nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusOK || m["unitCost"] != "11" || fs.costAsOf != "2026-03-01" {
		t.Errorf("lookup status %d body %v as of %s", rec.Code, m, fs.costAsOf)
	}
	if rec = call(h, http.MethodGet, path, "", "costing.read", nil); rec.Code != http.StatusOK || len(fs.costAsOf) != 10 {
		t.Errorf("default date: status %d as of %q", rec.Code, fs.costAsOf)
	}
	for _, p := range []string{"/v1/costing/products/x/cost?site=" + site, "/v1/costing/products/" + product + "/cost", path + "&date=03/01/2026"} {
		if rec = call(h, http.MethodGet, p, "", "costing.read", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s status %d, want 400", p, rec.Code)
		}
	}
}

// The packing slip poster records each line's unit cost and its cost version, so the slip duty reads the
// standard cost and the versions, and nothing else of costing.
func TestCostReads_TakeTheSlipPostersDuty(t *testing.T) {
	h, fs := server()
	fs.version = draftVersion()
	for _, path := range []string{"/v1/costing/versions", "/v1/costing/versions/" + fs.version.ID, "/v1/costing/products/" + ids.New() + "/cost?site=" + ids.New()} {
		if rec := call(h, http.MethodGet, path, "", dutySlipPost, nil); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Errorf("%s with %s: status %d, want the duty to be enough", path, dutySlipPost, rec.Code)
		}
	}
	if rec := call(h, http.MethodGet, "/v1/costing/valuation-ledgers", "", dutySlipPost, nil); rec.Code != http.StatusForbidden {
		t.Errorf("valuation ledgers with %s: status %d, want 403", dutySlipPost, rec.Code)
	}
}
