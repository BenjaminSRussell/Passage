package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/shopfloor"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

func (f *fakeStore) PutRecipeTarget(_ context.Context, t store.RecipeTarget) (store.RecipeTarget, error) {
	f.gotTarget, f.target = t, &t
	return t, f.err
}

func (f *fakeStore) GetRecipeTarget(context.Context, string, string) (store.RecipeTarget, bool, error) {
	if f.target == nil {
		return store.RecipeTarget{}, false, f.err
	}
	return *f.target, true, f.err
}

func (f *fakeStore) ListRecipeTargets(context.Context, int) ([]store.RecipeTarget, error) {
	return []store.RecipeTarget{}, f.err
}

func (f *fakeStore) DeleteRecipeTarget(context.Context, string, string) (bool, error) {
	return f.target != nil, f.err
}

func (f *fakeStore) SetControlRecipe(_ context.Context, _ string, id, errText *string) error {
	f.link, f.linkSet = store.ControlRecipeLink{ControlRecipeID: id, Error: errText}, f.linkSet+1
	return nil
}

func (f *fakeStore) SetPotencyAdjustments(_ context.Context, _ string, adjs []domain.PotencyAdjustment, _ string) (store.BatchOrder, error) {
	if _, err := domain.ApplyPotency([]domain.PotencyLine{{LineNumber: 1, Type: domain.LineActiveIngredient, Qty: decimal.RequireFromString("100")}, {LineNumber: 4, Type: domain.LineIngredient, Qty: decimal.RequireFromString("900")}}, adjs); err != nil {
		return store.BatchOrder{}, err
	}
	f.order.PotencyAdjustments = adjs
	return f.order, f.err
}

func (f *fakeStore) ClaimRecipeGeneration(context.Context, string) (store.BatchOrder, bool, error) {
	f.claims++
	return f.order, f.link.ControlRecipeID == nil, f.err
}

func (f *fakeStore) GetControlRecipeLink(context.Context, string) (store.ControlRecipeLink, error) {
	return f.link, f.err
}

type fakeSender struct {
	got  shopfloor.RecipeRequest
	key  string
	le   string
	err  error
	made int
}

func (s *fakeSender) CreateRecipe(_ context.Context, le, key string, req shopfloor.RecipeRequest) (string, error) {
	s.got, s.key, s.le = req, key, le
	if s.err != nil {
		return "", s.err
	}
	s.made++
	return "0195f0a0-0000-7000-8000-0000000000e1", nil
}

func releasedOrder() store.BatchOrder {
	return store.BatchOrder{ID: orderID, LegalEntityID: entityID, OrderNumber: "BO-7", FormulaCode: "DOUGH-1", FormulaVersion: 2, ItemID: itemOut, Unit: "kg",
		BatchSize: decimal.RequireFromString("2000"), Status: "released", Lines: []store.OrderLine{
			{LineNumber: 1, ItemID: itemA, Type: domain.LineIngredient, Qty: decimal.RequireFromString("1200")},
			{LineNumber: 2, ItemID: itemB, Type: domain.LinePhantom, Qty: decimal.RequireFromString("5")},
			{LineNumber: 3, ItemID: itemC, Type: domain.LineByProduct, Qty: decimal.RequireFromString("40")},
		}}
}

func kioskTarget() *store.RecipeTarget {
	return &store.RecipeTarget{LegalEntityID: entityID, ItemID: itemOut, ResourceID: "0195f0a0-0000-7000-8000-0000000000f1", Destination: "kiosk"}
}

// Releasing an order of an item with a target generates its control recipe as the shopfloor service expects
// it, keyed by the order, and the order keeps the recipe's id; a replay of the release generates nothing.
func TestReleaseGeneratesTheControlRecipe(t *testing.T) {
	fs := &fakeStore{order: releasedOrder(), target: kioskTarget()}
	sender := &fakeSender{}
	h := httpxRouter(New(fs).WithShopfloor(sender))
	rec := call{method: "POST", path: orderPath + ":release", body: `{"version":1}`, duties: DutyBatchOrderWrite, headers: key("k1")}.do(h)
	if rec.Code != http.StatusOK || sender.made != 1 {
		t.Fatalf("release: %d %s; recipes made %d", rec.Code, rec.Body, sender.made)
	}
	if want, _ := RecipeKey(orderID, sender.got); sender.key != want || !strings.HasPrefix(sender.key, "batch-order-recipe-"+orderID+"-") || sender.le != entityID || sender.got.OrderID != orderID || sender.got.Destination != "kiosk" || sender.got.ResourceID != kioskTarget().ResourceID {
		t.Fatalf("request %+v key %q le %q", sender.got, sender.key, sender.le)
	}
	if fs.link.ControlRecipeID == nil || *fs.link.ControlRecipeID == "" || fs.link.Error != nil {
		t.Fatalf("the order keeps %+v", fs.link)
	}
	proc := sender.got.Recipe["procedure"].(map[string]any)
	if proc["name"] != "Batch BO-7" {
		t.Fatalf("procedure %v", proc["name"])
	}
	// The phantom line is not charged; the by-product is listed with its role.
	ph := proc["unitProcedures"].([]map[string]any)[0]["operations"].([]map[string]any)[0]["phases"].([]map[string]any)[0]
	ins := ph["instructions"].([]map[string]any)
	mats := ins[1]["materials"].([]map[string]any)
	if len(mats) != 2 || mats[0]["itemId"] != itemA || mats[0]["qty"] != "1200" || mats[0]["role"] != "ingredient" || mats[1]["role"] != "by-product" {
		t.Fatalf("materials %v", mats)
	}
	if !strings.Contains(ins[0]["text"].(string), "2000 kg") || !strings.Contains(ins[0]["text"].(string), "BO-7") {
		t.Fatalf("make instruction %q", ins[0]["text"])
	}

	// A replay of the release is not a new release.
	fs2 := &fakeStore{order: releasedOrder(), target: kioskTarget(), replay: true}
	sender2 := &fakeSender{}
	call{method: "POST", path: orderPath + ":release", body: `{"version":1}`, duties: DutyBatchOrderWrite, headers: key("k1")}.do(httpxRouter(New(fs2).WithShopfloor(sender2)))
	if sender2.made != 0 {
		t.Fatal("a replayed release generated a recipe")
	}
	// Starting an order does not generate one either.
	sender3 := &fakeSender{}
	call{method: "POST", path: orderPath + ":start", body: `{"version":1}`, duties: DutyBatchOrderWrite, headers: key("k2")}.do(httpxRouter(New(&fakeStore{order: releasedOrder(), target: kioskTarget()}).WithShopfloor(sender3)))
	if sender3.made != 0 {
		t.Fatal("start generated a recipe")
	}
}

// An item with no target, or a deployment with no shopfloor client, releases as before; a refusal by the
// shopfloor service shows on the order and does not undo the release.
func TestReleaseWithoutATargetOrWithAFailingShopfloor(t *testing.T) {
	release := func(fs *fakeStore, sender *fakeSender) int {
		api := New(fs)
		if sender != nil {
			api = api.WithShopfloor(sender)
		}
		return call{method: "POST", path: orderPath + ":release", body: `{"version":1}`, duties: DutyBatchOrderWrite, headers: key("k")}.do(httpxRouter(api)).Code
	}
	noTarget := &fakeStore{order: releasedOrder()}
	sender := &fakeSender{}
	if code := release(noTarget, sender); code != http.StatusOK || sender.made != 0 || noTarget.linkSet != 0 {
		t.Fatalf("no target: %d, made %d, link written %d times", code, sender.made, noTarget.linkSet)
	}
	if code := release(&fakeStore{order: releasedOrder(), target: kioskTarget()}, nil); code != http.StatusOK {
		t.Fatalf("no shopfloor client: %d", code)
	}
	failing := &fakeStore{order: releasedOrder(), target: kioskTarget()}
	if code := release(failing, &fakeSender{err: errors.New("the shopfloor service refused the control recipe (422 SF-0105): the resource is unknown")}); code != http.StatusOK {
		t.Fatalf("a refusal undid the release: %d", code)
	}
	if failing.link.ControlRecipeID != nil || failing.link.Error == nil || !strings.Contains(*failing.link.Error, "the resource is unknown") {
		t.Fatalf("the order keeps %+v", failing.link)
	}
}

func TestSendRecipeAndRecipeTargetRoutes(t *testing.T) {
	fs := &fakeStore{order: releasedOrder(), target: kioskTarget()}
	sender := &fakeSender{}
	h := httpxRouter(New(fs).WithShopfloor(sender))
	if rec := (call{method: "POST", path: orderPath + ":send-recipe", duties: DutyBatchOrderWrite}).do(h); rec.Code != http.StatusOK || sender.made != 1 {
		t.Fatalf("send-recipe: %d %s", rec.Code, rec.Body)
	}
	planned := releasedOrder()
	planned.Status = "planned"
	if rec := (call{method: "POST", path: orderPath + ":send-recipe", duties: DutyBatchOrderWrite}).do(httpxRouter(New(&fakeStore{order: planned, target: kioskTarget()}).WithShopfloor(sender))); rec.Code != http.StatusConflict {
		t.Fatalf("send-recipe for a planned order: %d %s", rec.Code, rec.Body)
	}
	if rec := (call{method: "POST", path: orderPath + ":send-recipe", duties: DutyBatchOrderWrite}).do(httpxRouter(New(&fakeStore{order: releasedOrder()}).WithShopfloor(sender))); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("send-recipe with no target: %d %s", rec.Code, rec.Body)
	}
	if rec := (call{method: "POST", path: orderPath + ":send-recipe", duties: DutyBatchOrderWrite}).do(httpxRouter(New(&fakeStore{order: releasedOrder(), target: kioskTarget()}))); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("send-recipe without a shopfloor client: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: orderPath + ":send-recipe", duties: DutyRead}).do(h); rec.Code != http.StatusForbidden {
		t.Fatalf("send-recipe with the read duty: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: orderPath + "/control-recipe", duties: DutyRead}).do(h); rec.Code != http.StatusOK {
		t.Fatalf("link: %d", rec.Code)
	}

	targetPath := "/v1/production/recipe-targets/" + itemOut
	put := func(body string) int {
		return call{method: "PUT", path: targetPath, body: body, duties: DutyFormulaWrite}.do(h).Code
	}
	res := "0195f0a0-0000-7000-8000-0000000000f1"
	edge := "0195f0a0-0000-7000-8000-0000000000f2"
	if code := put(`{"resourceId":"` + res + `","destination":"kiosk"}`); code != http.StatusOK || fs.gotTarget.LegalEntityID != entityID {
		t.Fatalf("kiosk target: %d (legal entity defaults to the caller's: %q)", code, fs.gotTarget.LegalEntityID)
	}
	if code := put(`{"resourceId":"` + res + `","destination":"pcs-mqtt","edgeId":"` + edge + `","topic":" plant/r1/recipe "}`); code != http.StatusOK || *fs.gotTarget.Topic != "plant/r1/recipe" {
		t.Fatalf("mqtt target: %d", code)
	}
	for name, body := range map[string]string{
		"kiosk with an edge":  `{"resourceId":"` + res + `","destination":"kiosk","edgeId":"` + edge + `"}`,
		"pcs without an edge": `{"resourceId":"` + res + `","destination":"pcs-opcua"}`,
		"mqtt without topic":  `{"resourceId":"` + res + `","destination":"pcs-mqtt","edgeId":"` + edge + `"}`,
		"opcua with a topic":  `{"resourceId":"` + res + `","destination":"pcs-opcua","edgeId":"` + edge + `","topic":"x"}`,
		"unknown destination": `{"resourceId":"` + res + `","destination":"fax"}`,
		"no resource":         `{"destination":"kiosk"}`,
	} {
		if code := put(body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	if rec := (call{method: "PUT", path: targetPath, body: `{"resourceId":"` + res + `","destination":"kiosk"}`, duties: DutyRead}).do(h); rec.Code != http.StatusForbidden {
		t.Fatalf("put with the read duty: %d", rec.Code)
	}
	if rec := (call{method: "DELETE", path: targetPath, duties: DutyFormulaWrite}).do(h); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := (call{method: "DELETE", path: targetPath, duties: DutyFormulaWrite}).do(httpxRouter(New(&fakeStore{}))); rec.Code != http.StatusNotFound {
		t.Fatalf("delete of nothing: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: "/v1/production/recipe-targets", duties: DutyRead}).do(h); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
}

// The Idempotency-Key follows the request: the same request is the same key, so a retry replays the one recipe;
// an order rescaled after a failed generation is a new body and a new key, never shopfloor's key-reuse refusal.
// Once the order keeps a recipe, :send-recipe answers with it and sends nothing.
func TestRecipeKeyFollowsTheRequestAndARecipeIsMadeOnce(t *testing.T) {
	fs := &fakeStore{order: releasedOrder(), target: kioskTarget()}
	sender := &fakeSender{err: errors.New("the shopfloor service answered 503")}
	h := httpxRouter(New(fs).WithShopfloor(sender))
	send := func() int {
		return (call{method: "POST", path: orderPath + ":send-recipe", duties: DutyBatchOrderWrite}).do(h).Code
	}
	if code := send(); code != http.StatusOK || fs.link.Error == nil {
		t.Fatalf("a failed generation: %d %+v", code, fs.link)
	}
	first := sender.key
	sender.err = nil
	if send(); sender.key != first || sender.made != 1 {
		t.Fatalf("the retry of the same request: key %q, want %q; made %d", sender.key, first, sender.made)
	}
	// The order is rescaled (allowed: the failed generation left no recipe), and the recipe is made again.
	fs.link = store.ControlRecipeLink{}
	fs.order.BatchSize, fs.order.Lines[0].Qty = decimal.RequireFromString("3000"), decimal.RequireFromString("1800")
	if send(); sender.key == first || sender.made != 2 || sender.got.Recipe == nil {
		t.Fatalf("the rescaled order kept the key %q (made %d)", sender.key, sender.made)
	}
	// Now the order keeps its recipe: a repeat answers with it and makes none.
	rec := (call{method: "POST", path: orderPath + ":send-recipe", duties: DutyBatchOrderWrite}).do(h)
	if rec.Code != http.StatusOK || sender.made != 2 || !strings.Contains(rec.Body.String(), *fs.link.ControlRecipeID) {
		t.Fatalf("send-recipe with a recipe made: %d %s, made %d", rec.Code, rec.Body, sender.made)
	}
}

// A percent-controlled line is the batch × percent / 100, so its material carries the batch's unit; a line with
// a fixed quantity has no unit on record and none is sent.
func TestReleaseRecipeMaterialUnits(t *testing.T) {
	o := releasedOrder()
	o.Lines[0].PercentControlled = true
	doc, err := ReleaseRecipe(o, "kiosk")
	if err != nil {
		t.Fatal(err)
	}
	ins := doc["procedure"].(map[string]any)["unitProcedures"].([]map[string]any)[0]["operations"].([]map[string]any)[0]["phases"].([]map[string]any)[0]["instructions"].([]map[string]any)
	mats := ins[1]["materials"].([]map[string]any)
	if mats[0]["unit"] != "kg" {
		t.Fatalf("percent-controlled line unit %v, want kg", mats[0]["unit"])
	}
	if _, ok := mats[1]["unit"]; ok {
		t.Fatalf("a fixed line got a unit: %v", mats[1])
	}
}

func httpxRouter(a *API) http.Handler {
	r := httpx.NewRouter()
	a.Register(r)
	return r
}

// Potency-adjusted quantities in the generated recipe: an active ingredient of standard potency 80 % charged from a
// 100 % batch needs less, and the filler line takes the difference.
func TestReleaseRecipePotencyAdjusted(t *testing.T) {
	o := releasedOrder()
	o.Lines = append(o.Lines,
		store.OrderLine{LineNumber: 4, ItemID: itemA, Type: domain.LineActiveIngredient, Qty: decimal.RequireFromString("100")})
	o.PotencyAdjustments = []domain.PotencyAdjustment{{LineNumber: 4, StandardPotency: decimal.RequireFromString("80"), BatchPotency: decimal.RequireFromString("100"), CompensatingLine: 1, BatchRef: "API-0042"}}
	doc, err := ReleaseRecipe(o, "kiosk")
	if err != nil {
		t.Fatal(err)
	}
	ins := doc["procedure"].(map[string]any)["unitProcedures"].([]map[string]any)[0]["operations"].([]map[string]any)[0]["phases"].([]map[string]any)[0]["instructions"].([]map[string]any)
	mats := ins[1]["materials"].([]map[string]any)
	if mats[0]["qty"] != "1220" || mats[2]["qty"] != "80" {
		t.Fatalf("adjusted materials %v", mats)
	}
	if text := ins[1]["text"].(string); !strings.Contains(text, "Line 4 is potency-adjusted from batch API-0042 (100 against the standard 80); line 1 compensates.") {
		t.Fatalf("material text %q", text)
	}
	o.PotencyAdjustments[0].CompensatingLine = 3
	if _, err := ReleaseRecipe(o, "kiosk"); err == nil {
		t.Fatal("a by-product line cannot compensate")
	}
}

func TestPotencyAdjustmentsRoute(t *testing.T) {
	fs := &fakeStore{order: releasedOrder()}
	h := httpxRouter(New(fs))
	path := orderPath + "/potency-adjustments"
	body := `{"adjustments":[{"lineNumber":1,"standardPotency":"80","batchPotency":"100","compensatingLine":4,"batchRef":"API-1"}]}`
	if rec := (call{method: "PUT", path: path, body: body, duties: DutyRead}).do(h); rec.Code != http.StatusForbidden {
		t.Fatalf("without the write duty: %d", rec.Code)
	}
	if rec := (call{method: "PUT", path: path, body: `{"adjustments":[{"lineNumber":1,"standardPotency":"0","batchPotency":"100","compensatingLine":4}]}`, duties: DutyBatchOrderWrite}).do(h); rec.Code != http.StatusBadRequest {
		t.Fatalf("zero potency: %d %s", rec.Code, rec.Body)
	}
	if rec := (call{method: "PUT", path: path, body: `{"adjustments":[{"lineNumber":4,"standardPotency":"80","batchPotency":"100","compensatingLine":1}]}`, duties: DutyBatchOrderWrite}).do(h); rec.Code != http.StatusBadRequest {
		t.Fatalf("the filler as the active line: %d %s", rec.Code, rec.Body)
	}
	rec := (call{method: "PUT", path: path, body: body, duties: DutyBatchOrderWrite}).do(h)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"batchRef":"API-1"`) {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
}
