package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/engineering"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

const (
	rollPath  = "/v1/product-costing/versions/" + someID + "/roll"
	calculate = "CostingCalculate"
	rollLE    = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6e01"
	parentID  = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6c01"
	childID   = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6c02"
)

type fakeEngineering struct {
	boms   map[string]*engineering.BOM
	routes map[string]*engineering.Route
	err    error
	asked  []string
}

func (f *fakeEngineering) ActiveBOM(_ context.Context, _, product, _, _ string) (*engineering.BOM, error) {
	f.asked = append(f.asked, product)
	return f.boms[product], f.err
}

func (f *fakeEngineering) ActiveRoute(_ context.Context, _, product, _, _ string) (*engineering.Route, error) {
	return f.routes[product], f.err
}

func rollRouter(f *fakeStore, eng *fakeEngineering) http.Handler {
	r := storedRouter(f)
	if eng == nil {
		return r
	}
	return rollRouterWith(f, eng)
}

func rollRouterWith(f *fakeStore, eng *fakeEngineering) http.Handler {
	a := New(nil, Config{}).WithStore(f).WithEngineering(eng)
	r := httpx.NewRouter()
	a.Register(r)
	return r
}

func parentBOM() *fakeEngineering {
	return &fakeEngineering{
		boms:   map[string]*engineering.BOM{parentID: {ID: "bom-1", Lines: []engineering.BOMLine{{ComponentProductID: childID, Quantity: "2", Series: "1", LineType: "item"}}}},
		routes: map[string]*engineering.Route{parentID: {ID: "route-1", Operations: []engineering.Operation{{OperationID: "OP10", Priority: "primary", RunH: "0.5", ProcessQty: "1"}}}},
	}
}

func rollBody(site, extra string) string {
	return `{"siteId":"` + site + `","itemIds":["` + parentID + `"],"prices":{"` + childID + `":"3.00"},"operationRates":{"OP10":"10.00"}` + extra + `}`
}

func TestRollStoredVersion_ReadsStructureFromEngineeringAndRefusesItInTheRequest(t *testing.T) {
	f := &fakeStore{versionLE: rollLE}
	eng := parentBOM()
	h := rollRouter(f, eng)
	site := ids.New()
	if rec := (call{http.MethodPost, rollPath, rollBody(site, ""), view, "k", ""}).do(h); rec.Code != http.StatusForbidden {
		t.Fatalf("without CostingCalculate: %d", rec.Code)
	}
	for name, c := range map[string]call{
		"no key":        {http.MethodPost, rollPath, rollBody(site, ""), calculate, "", ""},
		"bad site":      {http.MethodPost, rollPath, rollBody("site-1", ""), calculate, "k", ""},
		"no items":      {http.MethodPost, rollPath, `{"siteId":"` + site + `","itemIds":[]}`, calculate, "k", ""},
		"bad item id":   {http.MethodPost, rollPath, `{"siteId":"` + site + `","itemIds":["FG-100"]}`, calculate, "k", ""},
		"duplicate ids": {http.MethodPost, rollPath, `{"siteId":"` + site + `","itemIds":["` + parentID + `","` + parentID + `"]}`, calculate, "k", ""},
		"BOM in body":   {http.MethodPost, rollPath, rollBody(site, `,"items":[{"id":"`+parentID+`","kind":"Purchased","price":"0.01"}]`), calculate, "k", ""},
		"sheet in body": {http.MethodPost, rollPath, rollBody(site, `,"sheets":{}`), calculate, "k", ""},
	} {
		if rec := c.do(h); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if f.calls != 0 || len(eng.asked) != 0 {
		t.Fatal("a refused request reached the store or engineering")
	}
	rec := call{http.MethodPost, rollPath, rollBody(site, ""), calculate, " roll-1 ", rollLE}.do(h)
	if rec.Code != http.StatusCreated || f.roll == nil {
		t.Fatalf("roll: %d %s", rec.Code, rec.Body.String())
	}
	got := f.roll
	if got.Key != "roll-1" || got.SiteID != site || got.Sources[parentID].BOMVersionID != "bom-1" || got.Sources[parentID].RouteVersionID != "route-1" || len(got.Roots) != 1 {
		t.Fatalf("store got %+v", got)
	}
	var parent domain.Result
	for _, r := range got.Run.Results {
		if r.ItemID == parentID {
			parent = r
		}
	}
	if parent.Unit.String() != "11" {
		t.Fatalf("parent unit cost %s, want 2 x 3.00 from the BOM plus 0.5h x 10.00 from the route", parent.Unit)
	}
}

func TestRollStoredVersion_FailsClosedWithoutEngineeringOrAPriceInput(t *testing.T) {
	site := ids.New()
	f := &fakeStore{versionLE: rollLE}
	if rec := (call{http.MethodPost, rollPath, rollBody(site, ""), calculate, "k", ""}).do(storedRouter(f)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no engineering configured: %d", rec.Code)
	}
	eng := parentBOM()
	eng.err = errors.New("down")
	if rec := (call{http.MethodPost, rollPath, rollBody(site, ""), calculate, "k", rollLE}).do(rollRouter(f, eng)); rec.Code != http.StatusBadGateway {
		t.Fatalf("engineering down: %d", rec.Code)
	}
	noPrice := `{"siteId":"` + site + `","itemIds":["` + parentID + `"],"operationRates":{"OP10":"10.00"}}`
	if rec := (call{http.MethodPost, rollPath, noPrice, calculate, "k", rollLE}).do(rollRouter(f, parentBOM())); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a purchased component without a price: %d", rec.Code)
	}
	noRate := `{"siteId":"` + site + `","itemIds":["` + parentID + `"],"prices":{"` + childID + `":"3.00"}}`
	if rec := (call{http.MethodPost, rollPath, noRate, calculate, "k", rollLE}).do(rollRouter(f, parentBOM())); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an operation without a rate: %d", rec.Code)
	}
	if f.roll != nil {
		t.Fatal("a failed read stored a roll")
	}
}

func TestRollStoredVersion_ACostedOperationWithoutAnIdIsRefused(t *testing.T) {
	eng := parentBOM()
	eng.routes[parentID].Operations[0].OperationID = ""
	f := &fakeStore{versionLE: rollLE}
	rec := call{http.MethodPost, rollPath, rollBody(ids.New(), ""), calculate, "k", rollLE}.do(rollRouter(f, eng))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeRollInput) || f.roll != nil {
		t.Fatalf("a costed operation with no operationId: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRollStoredVersion_RefusesAnotherLegalEntityAndAVersionThatIsNotOpenBeforeReadingEngineering(t *testing.T) {
	site := ids.New()
	otherLE := ids.New()
	for name, tc := range map[string]struct {
		f     *fakeStore
		scope string
		code  string
		want  int
	}{
		"version of another legal entity": {&fakeStore{versionLE: rollLE}, otherLE, domain.CodeLegalEntityScope, http.StatusForbidden},
		"no legal entity in scope":        {&fakeStore{versionLE: rollLE}, "", domain.CodeLegalEntityScope, http.StatusForbidden},
		"version not open":                {&fakeStore{versionLE: rollLE, versionStatus: domain.VersionActive}, rollLE, domain.CodeVersionNotRollable, http.StatusUnprocessableEntity},
	} {
		eng := parentBOM()
		rec := call{http.MethodPost, rollPath, rollBody(site, ""), calculate, "k", tc.scope}.do(rollRouter(tc.f, eng))
		if code, _ := problemCode(t, rec); rec.Code != tc.want || code != tc.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		if len(eng.asked) != 0 || tc.f.roll != nil {
			t.Errorf("%s: reached engineering or stored a roll", name)
		}
	}
}

func TestRollStoredVersion_ARetryOfACommittedRollReplaysAfterActivation(t *testing.T) {
	stored := store.RollResult{VersionID: someID, SiteID: ids.New(), ItemCount: 1, StoredCount: 1, Errors: []store.RollError{}}
	f := &fakeStore{versionLE: rollLE, versionStatus: domain.VersionActive, rollReplay: &stored}
	eng := parentBOM()
	rec := call{http.MethodPost, rollPath, rollBody(stored.SiteID, ""), calculate, "roll-1", rollLE}.do(rollRouter(f, eng))
	if rec.Code != http.StatusCreated || rec.Header().Get(headerReplayed) != "true" || !strings.Contains(rec.Body.String(), `"storedCount":1`) {
		t.Fatalf("retry after activation: %d %s, want the stored 201", rec.Code, rec.Body.String())
	}
	if f.rollReplayKey != "roll-1" || len(eng.asked) != 0 || f.roll != nil {
		t.Fatalf("a replay read engineering or rolled again (key %q)", f.rollReplayKey)
	}
	f.rollReplay = nil
	rec = call{http.MethodPost, rollPath, rollBody(stored.SiteID, ""), calculate, "roll-2", rollLE}.do(rollRouter(f, eng))
	if code, _ := problemCode(t, rec); rec.Code != http.StatusUnprocessableEntity || code != domain.CodeVersionNotRollable {
		t.Fatalf("a new key on an active version: %d %s", rec.Code, rec.Body.String())
	}
}
