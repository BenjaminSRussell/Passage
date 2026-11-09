package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/engineering"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

const (
	rollPath  = "/v1/product-costing/versions/" + someID + "/roll"
	calculate = "productcost.calculate"
	rollLE    = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6e01"
	parentID  = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6c01"
	childID   = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6c02"
)

type fakeEngineering struct {
	boms   map[string]*engineering.BOM
	routes map[string]*engineering.Route
	err    error
	asked  []string
	listed int
}

func (f *fakeEngineering) ActiveBOM(_ context.Context, _, product, _, _ string) (*engineering.BOM, error) {
	f.asked = append(f.asked, product)
	return f.boms[product], f.err
}

func (f *fakeEngineering) ActiveRoute(_ context.Context, _, product, _, _ string) (*engineering.Route, error) {
	return f.routes[product], f.err
}

func (f *fakeEngineering) ActiveBOMProducts(_ context.Context, _, _, _ string, limit int) ([]string, error) {
	f.listed++
	var out []string
	for product := range f.boms {
		out = append(out, product)
	}
	slices.Sort(out)
	if len(out) > limit+1 {
		out = out[:limit+1]
	}
	return out, f.err
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
		t.Fatalf("without productcost.calculate: %d", rec.Code)
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

func selectionBody(site, extra string) string {
	return `{"siteId":"` + site + `","selection":"allActive","prices":{"` + childID + `":"3.00"}` + extra + `}`
}

func TestRollStoredVersion_ASelectionOfStoredBOMsRollsMoreItemsThanTheBodyCapCouldCarry(t *testing.T) {
	const items = 30000
	eng := &fakeEngineering{boms: map[string]*engineering.BOM{}, routes: map[string]*engineering.Route{}}
	var equivalent []domain.Item
	for i := range items {
		id := ids.New()
		eng.boms[id] = &engineering.BOM{ID: fmt.Sprintf("bom-%d", i), Lines: []engineering.BOMLine{{ComponentProductID: childID, Quantity: "2", Series: "1", LineType: "item"}}}
		equivalent = append(equivalent, domain.Item{ID: id, Kind: domain.KindManufactured, BOM: []domain.BOMLine{{ChildID: childID, Qty: decimal.NewFromInt(2)}}})
	}
	posted, err := json.Marshal(rollRequest{Items: append(equivalent, domain.Item{ID: childID, Kind: domain.KindPurchased})})
	if err != nil {
		t.Fatal(err)
	}
	if len(posted) <= 4<<20 {
		t.Fatalf("posting these BOMs takes %d bytes, which fits the 4 MiB body cap: the test selection is too small", len(posted))
	}
	f := &fakeStore{versionLE: rollLE}
	site := ids.New()
	body := selectionBody(site, "")
	if len(body) > 1024 {
		t.Fatalf("a selection request is %d bytes", len(body))
	}
	rec := call{http.MethodPost, rollPath, body, calculate, "sel-1", rollLE}.do(rollRouterWith(f, eng))
	if rec.Code != http.StatusCreated || f.roll == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(f.roll.Roots) != items || len(f.roll.Sources) != items || f.roll.SiteID != site || eng.listed != 1 {
		t.Fatalf("roots %d sources %d listed %d", len(f.roll.Roots), len(f.roll.Sources), eng.listed)
	}
	for _, r := range f.roll.Run.Results {
		if r.Status == domain.StatusError {
			t.Fatalf("item %s failed: %+v", r.ItemID, r.Diagnostics)
		}
	}
	if f.roll.Run.ItemCount < items {
		t.Fatalf("run rolled %d items", f.roll.Run.ItemCount)
	}
}

func TestRollStoredVersion_ASelectionNeedsExactlyOneWayToNameItemsAndSomethingToSelect(t *testing.T) {
	site := ids.New()
	f := &fakeStore{versionLE: rollLE}
	eng := parentBOM()
	h := rollRouterWith(f, eng)
	for name, body := range map[string]string{
		"both":          selectionBody(site, `,"itemIds":["`+parentID+`"]`),
		"unknown":       `{"siteId":"` + site + `","selection":"everything"}`,
		"neither":       `{"siteId":"` + site + `"}`,
		"item criteria": `{"siteId":"` + site + `","selection":"allActive","itemGroup":"RAW"}`,
	} {
		if rec := (call{http.MethodPost, rollPath, body, calculate, "k", rollLE}).do(h); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if eng.listed != 0 || f.roll != nil {
		t.Fatal("a refused request reached engineering or the store")
	}
	empty := &fakeEngineering{}
	rec := call{http.MethodPost, rollPath, selectionBody(site, ""), calculate, "k", rollLE}.do(rollRouterWith(f, empty))
	if code, _ := problemCode(t, rec); rec.Code != http.StatusUnprocessableEntity || code != domain.CodeRollInput || f.roll != nil {
		t.Fatalf("no active BOM at the site: %d %s", rec.Code, rec.Body.String())
	}
	down := parentBOM()
	down.err = errors.New("down")
	rec = call{http.MethodPost, rollPath, selectionBody(site, ""), calculate, "k", rollLE}.do(rollRouterWith(f, down))
	if code, _ := problemCode(t, rec); rec.Code != http.StatusBadGateway || code != domain.CodeEngineeringUnavailable {
		t.Fatalf("engineering down while listing: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRollStoredVersion_ASelectionRetryReplaysWithoutListingEngineeringAgain(t *testing.T) {
	stored := store.RollResult{VersionID: someID, SiteID: ids.New(), ItemCount: 2, StoredCount: 2, Errors: []store.RollError{}}
	f := &fakeStore{versionLE: rollLE, versionStatus: domain.VersionActive, rollReplay: &stored}
	eng := parentBOM()
	rec := call{http.MethodPost, rollPath, selectionBody(stored.SiteID, ""), calculate, "sel-1", rollLE}.do(rollRouterWith(f, eng))
	if rec.Code != http.StatusCreated || rec.Header().Get(headerReplayed) != "true" || eng.listed != 0 || f.roll != nil {
		t.Fatalf("%d %s listed %d", rec.Code, rec.Body.String(), eng.listed)
	}
}
