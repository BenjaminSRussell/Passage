package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/search/internal/store"
	"github.com/BenjaminSRussell/passage/services/search/migrations"
)

const (
	entityA = "00000000-0000-0000-0000-0000000000a1"
	entityB = "00000000-0000-0000-0000-0000000000b1"

	dutyParty   = "party.read"
	dutyProduct = "product.read"
	dutyPO      = "procurement.purchase-order.inquire"
	dutyMO      = "production.order.read"
	dutyWorker  = "hr.worker.inquire"
)

type harness struct {
	t        *testing.T
	h        http.Handler
	api      *API
	poID     map[string]string
	moID     map[string]string
	partID   string
	workerID string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(app)
	hr := &harness{t: t, poID: map[string]string{}, moID: map[string]string{}, partID: ids.New(), workerID: ids.New()}
	seed := func(le string, r store.Record) {
		r.At = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
		ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "seed", Duties: map[string]struct{}{}})
		if _, err := st.Apply(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []struct{ le, number string }{{entityA, "PO-ALPHA"}, {entityB, "PO-BRAVO"}} {
		id := ids.New()
		hr.poID[w.le] = id
		seed(w.le, store.Record{Type: store.TypePurchaseOrder, ID: id, LegalEntityID: w.le, Number: w.number, Title: w.number, Status: "CONFIRMED"})
	}
	for _, w := range []struct{ le, number string }{{entityA, "MO-ALPHA"}, {entityB, "MO-BRAVO"}} {
		id := ids.New()
		hr.moID[w.le] = id
		seed(w.le, store.Record{Type: store.TypeProductionOrder, ID: id, LegalEntityID: w.le, Number: w.number, Title: w.number, Status: "RELEASED"})
	}
	seed("", store.Record{Type: store.TypeWorker, ID: hr.workerID, Number: "WRK-ALPHA", Title: "WRK-ALPHA", Subtitle: "employee"})
	seed("", store.Record{Type: store.TypeParty, ID: hr.partID, Number: "P-ALPHA", Title: "Alpha Supplies", Subtitle: "organization", Status: "active"})
	seed("", store.Record{Type: store.TypeProduct, ID: ids.New(), Number: "SKU-ALPHA", Title: "Alpha Widget", Status: "active"})
	hr.api = New(st)
	router := httpx.NewRouter()
	hr.api.Register(router)
	hr.h = router
	return hr
}

type response struct {
	Results []map[string]string `json:"results"`
}

func (h *harness) get(rawQuery, scopes, duties string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/search?"+rawQuery, nil)
	r.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	r.Header.Set("X-Passage-Legal-Entity", scopes)
	r.Header.Set("X-Passage-User", "alice")
	r.Header.Set("X-Passage-Duties", duties)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, r)
	return rec
}

func (h *harness) results(rawQuery, scopes, duties string) []map[string]string {
	h.t.Helper()
	rec := h.get(rawQuery, scopes, duties)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("%s: status %d body %s", rawQuery, rec.Code, rec.Body)
	}
	var out response
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		h.t.Fatal(err)
	}
	return out.Results
}

func field(rs []map[string]string, key string) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r[key]
	}
	return out
}

func TestEntityANeverSeesEntityBRecords(t *testing.T) {
	h := newHarness(t)
	q := "q=" + url.QueryEscape("po-")
	for name, tc := range map[string]struct {
		scopes string
		want   []string
	}{
		"entity A":  {entityA, []string{"PO-ALPHA"}},
		"entity B":  {entityB, []string{"PO-BRAVO"}},
		"both":      {entityA + "," + entityB, []string{"PO-ALPHA", "PO-BRAVO"}},
		"no entity": {"", nil},
	} {
		got := field(h.results(q, tc.scopes, dutyPO), "number")
		if !slices.Equal(got, tc.want) && (len(got) != 0 || len(tc.want) != 0) {
			t.Errorf("%s sees %v, want %v", name, got, tc.want)
		}
	}
}

// Production orders (#506-2) follow the same legal-entity rule as purchase
// orders: entity A never sees entity B's order, and the link opens the order.
func TestEntityANeverSeesEntityBProductionOrders(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		scopes string
		want   []string
	}{
		"entity A":  {entityA, []string{"MO-ALPHA"}},
		"entity B":  {entityB, []string{"MO-BRAVO"}},
		"both":      {entityA + "," + entityB, []string{"MO-ALPHA", "MO-BRAVO"}},
		"no entity": {"", nil},
	} {
		rs := h.results("q=mo-", tc.scopes, dutyMO)
		if got := field(rs, "number"); !slices.Equal(got, tc.want) && (len(got) != 0 || len(tc.want) != 0) {
			t.Errorf("%s sees %v, want %v", name, got, tc.want)
		}
	}
	rs := h.results("q=mo-alpha", entityA, dutyMO)
	if len(rs) != 1 || rs[0]["type"] != "production_order" || rs[0]["link"] != "/production/"+h.moID[entityA] || rs[0]["legalEntityId"] != entityA {
		t.Fatalf("production order result %v", rs)
	}
	if got := field(h.results("q=mo-alpha", entityA, dutyPO+","+dutyWorker), "type"); slices.Contains(got, "production_order") {
		t.Errorf("a caller without production.order.read sees types %v", got)
	}
}

// Workers are tenant-wide and need hr.worker.inquire; HR events carry no name,
// so a worker is found by personnel number.
func TestWorkersNeedTheWorkerInquireDuty(t *testing.T) {
	h := newHarness(t)
	for _, scopes := range []string{entityA, entityB} {
		rs := h.results("q=wrk-", scopes, dutyWorker)
		if len(rs) != 1 || rs[0]["type"] != "worker" || rs[0]["link"] != "/hr/workers/"+h.workerID || rs[0]["number"] != "WRK-ALPHA" {
			t.Errorf("scope %s: worker results %v", scopes, rs)
		}
	}
	if got := field(h.results("q=wrk-alpha", entityA, dutyParty+","+dutyMO), "type"); slices.Contains(got, "worker") {
		t.Errorf("a caller without hr.worker.inquire sees types %v", got)
	}
}

func TestACallerSeesOnlyTypesTheyHoldTheReadDutyFor(t *testing.T) {
	h := newHarness(t)
	q := "q=alpha"
	for name, tc := range map[string]struct {
		duties string
		want   []string
	}{
		"party only":   {dutyParty, []string{"party"}},
		"product only": {dutyProduct, []string{"product"}},
		"po only":      {dutyPO, []string{"purchase_order"}},
		"party and po": {dutyParty + "," + dutyPO, []string{"party", "purchase_order"}},
		"all three":    {dutyParty + "," + dutyProduct + "," + dutyPO, []string{"party", "product", "purchase_order"}},
	} {
		got := field(h.results(q, entityA, tc.duties), "type")
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s sees types %v, want %v", name, got, tc.want)
		}
	}
	if got := h.results("q=alpha&types=product", entityA, dutyParty); len(got) != 0 {
		t.Errorf("asking for a type without its duty returned %v", got)
	}
}

func TestResultsAreTypedAndLinked(t *testing.T) {
	h := newHarness(t)
	rs := h.results("q=alpha", entityA, dutyParty+","+dutyProduct+","+dutyPO)
	links := map[string]string{}
	for _, r := range rs {
		links[r["type"]] = r["link"]
	}
	want := map[string]string{"party": "/party/" + h.partID, "product": "/product/SKU-ALPHA", "purchase_order": "/purchasing/purchase-orders/" + h.poID[entityA]}
	for k, v := range want {
		if links[k] != v {
			t.Errorf("%s link %q, want %q", k, links[k], v)
		}
	}
	if len(rs) != 3 {
		t.Errorf("got %d results, want 3", len(rs))
	}
}

func TestRequestsAreRejectedWithoutADutyOrTenant(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		duties string
		want   int
	}{
		"no duties":       {"", http.StatusForbidden},
		"unrelated duty":  {"ledger.journal.read", http.StatusForbidden},
		"write only":      {"party.write", http.StatusForbidden},
		"one source duty": {dutyProduct, http.StatusOK},
	} {
		if code := h.get("q=alpha", entityA, tc.duties).Code; code != tc.want {
			t.Errorf("%s: status %d, want %d", name, code, tc.want)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/search?q=alpha", nil)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: status %d, want 401", rec.Code)
	}
}

func TestInputIsValidated(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("a", MaxQueryLength+1)
	for name, raw := range map[string]string{
		"missing q":      "",
		"one character":  "q=a",
		"blank":          "q=%20%20%20",
		"too long":       "q=" + long,
		"limit zero":     "q=alpha&limit=0",
		"limit too high": "q=alpha&limit=51",
		"limit text":     "q=alpha&limit=ten",
		"unknown type":   "q=alpha&types=invoice",
	} {
		rec := h.get(raw, entityA, dutyParty)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, rec.Code, rec.Body)
		}
	}
	if code := h.get("q=al&limit=50", entityA, dutyParty).Code; code != http.StatusOK {
		t.Errorf("boundary values: status %d, want 200", code)
	}
}

func TestInjectionAndWildcardsAreInertText(t *testing.T) {
	h := newHarness(t)
	for _, q := range []string{"'; drop table search_record; --", "%%", "__", `\\`, "zzz' or '1'='1"} {
		rs := h.results("q="+url.QueryEscape(q), entityA, dutyParty+","+dutyProduct+","+dutyPO)
		if len(rs) != 0 {
			t.Errorf("query %q matched %v", q, field(rs, "number"))
		}
	}
	if got := h.results("q=alpha", entityA, dutyParty); len(got) != 1 {
		t.Errorf("table damaged: %v", got)
	}
}

func TestLimitCapsTheResultCount(t *testing.T) {
	h := newHarness(t)
	rs := h.results("q=alpha&limit=2", entityA, dutyParty+","+dutyProduct+","+dutyPO)
	if len(rs) != 2 {
		t.Fatalf("got %d results, want 2", len(rs))
	}
}

func TestNoMatchIsAnEmptyListNotNull(t *testing.T) {
	h := newHarness(t)
	rec := h.get("q=zzzzzz", entityA, dutyParty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	metrics := httptest.NewRecorder()
	h.api.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "search_requests_empty_total 1") {
		t.Errorf("metrics %s", metrics.Body)
	}
}
