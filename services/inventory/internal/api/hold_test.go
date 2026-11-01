package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

type holdFake struct {
	placeIn      store.HoldInput
	placeReplay  bool
	releaseID    string
	releaseVer   int
	releaseQty   *decimal.Decimal
	sourceType   string
	sourceRef    string
	sourceKeep   decimal.Decimal
	sourceMove   *store.HoldSource
	filter       store.HoldFilter
	placeErr     error
	releasedHold domain.Hold
}

func (f *fakeStore) PlaceHold(_ context.Context, in store.HoldInput) (store.HoldResult, error) {
	f.hold.placeIn = in
	if f.hold.placeErr != nil {
		return store.HoldResult{}, f.hold.placeErr
	}
	h := domain.Hold{ID: ids.New(), LegalEntityID: in.LegalEntityID, ProductID: in.ProductID, Dims: in.Dims, Qty: in.Qty, ReleasedQty: decimal.Zero,
		Reason: in.Reason, SourceType: in.SourceType, SourceRef: in.SourceRef, State: domain.HoldActive, Version: 1}
	return store.HoldResult{Holds: []domain.Hold{h}, Replayed: f.hold.placeReplay}, nil
}

func (f *fakeStore) ReleaseHold(_ context.Context, id string, version int, qty *decimal.Decimal) (domain.Hold, error) {
	f.hold.releaseID, f.hold.releaseVer, f.hold.releaseQty = id, version, qty
	return domain.Hold{ID: id, Qty: decimal.NewFromInt(5), ReleasedQty: decimal.NewFromInt(5), State: domain.HoldReleased, Version: version + 1}, nil
}

func (f *fakeStore) ReleaseHoldsBySource(_ context.Context, sourceType, sourceRef string, keep decimal.Decimal, moveTo *store.HoldSource) (store.SourceRelease, error) {
	f.hold.sourceType, f.hold.sourceRef, f.hold.sourceKeep, f.hold.sourceMove = sourceType, sourceRef, keep, moveTo
	return store.SourceRelease{Known: true, Released: []domain.Hold{{ID: ids.New(), Qty: decimal.NewFromInt(5), ReleasedQty: decimal.NewFromInt(3), State: domain.HoldActive, Version: 2}}}, nil
}

func (f *fakeStore) ListHolds(_ context.Context, filter store.HoldFilter) ([]domain.Hold, error) {
	f.hold.filter = filter
	return []domain.Hold{}, nil
}

func holdBody(extra string) string {
	return `{"legalEntityId":"` + ids.New() + `","productId":"` + ids.New() + `","dimensions":{"site":"S1","warehouse":"W1","batch":"B1"},` +
		`"qty":"5","reason":"QUALITY","sourceType":"QUALITY_ORDER","sourceRef":"qo-1"` + extra + `}`
}

func TestPlaceHold_AcceptsTheUserAndTheServiceDutyAndNoOther(t *testing.T) {
	for duty, want := range map[string]int{"inventory.hold.write": http.StatusCreated, "inventory.hold.apply": http.StatusCreated,
		"inventory.read": http.StatusForbidden, "inventory.blocking.write": http.StatusForbidden} {
		fs := &fakeStore{}
		rec := do(t, newServer(t, fs), http.MethodPost, "/v1/inventory/holds", holdBody(""), []string{duty}, map[string]string{"Idempotency-Key": "k1"})
		if rec.Code != want {
			t.Errorf("%s: %d, want %d (%s)", duty, rec.Code, want, rec.Body)
		}
	}
}

func TestPlaceHold_PassesTheRequestToTheStoreAndMarksAReplay(t *testing.T) {
	fs := &fakeStore{hold: holdFake{placeReplay: true}}
	rec := do(t, newServer(t, fs), http.MethodPost, "/v1/inventory/holds", holdBody(`,"allowPartial":true,"note":" lot B1 "`),
		[]string{"inventory.hold.apply"}, map[string]string{"Idempotency-Key": "k1"})
	in := fs.hold.placeIn
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v", rec.Code, rec.Header())
	}
	if in.IdempotencyKey != "k1" || !in.AllowPartial || in.WholeLot || in.Note != "lot B1" || in.SourceType != "QUALITY_ORDER" || in.SourceRef != "qo-1" ||
		!in.Qty.Equal(decimal.NewFromInt(5)) || in.Dims[domain.DimBatch] != "B1" {
		t.Fatalf("store input %+v", in)
	}
}

func TestPlaceHold_RejectsMalformedRequests(t *testing.T) {
	cases := map[string]struct{ body, field string }{
		"no quantity":         {`{"legalEntityId":"` + ids.New() + `","productId":"` + ids.New() + `","reason":"QUALITY","sourceType":"QUALITY_ORDER","sourceRef":"x"}`, "qty"},
		"quantity on a lot":   {strings.Replace(holdBody(`,"wholeLot":true`), `"qty":"5",`, `"qty":"5",`, 1), "qty"},
		"unknown reason":      {strings.Replace(holdBody(""), "QUALITY\"", "BORED\"", 1), "reason"},
		"lowercase source":    {strings.Replace(holdBody(""), "QUALITY_ORDER", "quality", 1), "sourceType"},
		"negative quantity":   {strings.Replace(holdBody(""), `"5"`, `"-1"`, 1), "qty"},
		"bad legal entity id": {strings.Replace(holdBody(""), `"legalEntityId":"`, `"legalEntityId":"x`, 1), "legalEntityId"},
	}
	for name, c := range cases {
		rec := do(t, newServer(t, &fakeStore{}), http.MethodPost, "/v1/inventory/holds", c.body, []string{"inventory.hold.write"}, map[string]string{"Idempotency-Key": "k"})
		if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	rec := do(t, newServer(t, &fakeStore{}), http.MethodPost, "/v1/inventory/holds", holdBody(""), []string{"inventory.hold.write"}, nil)
	if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors["Idempotency-Key"] == "" {
		t.Errorf("missing key: %d %s", rec.Code, rec.Body)
	}
}

func TestReleaseHold_NeedsTheUserDutyAndIfMatchAndAPositiveQuantity(t *testing.T) {
	id := ids.New()
	path := "/v1/inventory/holds/" + id + ":release"
	fs := &fakeStore{}
	h := newServer(t, fs)
	if rec := do(t, h, http.MethodPost, path, `{}`, []string{"inventory.hold.apply"}, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
		t.Errorf("the service duty reached release-by-id: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{}`, []string{"inventory.hold.write"}, nil); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("without If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{"qty":"0"}`, []string{"inventory.hold.write"}, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusBadRequest {
		t.Errorf("zero quantity: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, `{"qty":"2"}`, []string{"inventory.hold.write"}, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || fs.hold.releaseID != id || fs.hold.releaseVer != 3 || fs.hold.releaseQty == nil || !fs.hold.releaseQty.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("release: %d id %s version %d qty %v", rec.Code, fs.hold.releaseID, fs.hold.releaseVer, fs.hold.releaseQty)
	}
	if rec.Header().Get("ETag") != `"4"` {
		t.Errorf("ETag %q", rec.Header().Get("ETag"))
	}
}

func TestReleaseHoldSource_ServiceDutyStatesTheLevelToKeep(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := `{"sourceType":"QUALITY_ORDER","sourceRef":"qo-1","keepQty":"2","moveKeptTo":{"sourceType":"NONCONFORMANCE","sourceRef":"n1"}}`
	if rec := do(t, h, http.MethodPost, "/v1/inventory/holds:release-source", body, []string{"inventory.read"}, nil); rec.Code != http.StatusForbidden {
		t.Errorf("read duty: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, "/v1/inventory/holds:release-source", body, []string{"inventory.hold.apply"}, nil)
	if rec.Code != http.StatusOK || fs.hold.sourceType != "QUALITY_ORDER" || fs.hold.sourceRef != "qo-1" || !fs.hold.sourceKeep.Equal(decimal.NewFromInt(2)) ||
		fs.hold.sourceMove == nil || fs.hold.sourceMove.Type != "NONCONFORMANCE" || fs.hold.sourceMove.Ref != "n1" || !strings.Contains(rec.Body.String(), `"known":true`) {
		t.Fatalf("release-source: %d %+v", rec.Code, fs.hold)
	}
	if rec := do(t, h, http.MethodPost, "/v1/inventory/holds:release-source", `{"sourceType":"QUALITY_ORDER","sourceRef":"q","keepQty":"-1"}`,
		[]string{"inventory.hold.apply"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("negative keep: %d", rec.Code)
	}
}

func TestListHolds_FiltersAndNeedsInventoryRead(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	if rec := do(t, h, http.MethodGet, "/v1/inventory/holds", "", []string{"inventory.hold.apply"}, nil); rec.Code != http.StatusForbidden {
		t.Errorf("service duty listing: %d", rec.Code)
	}
	rec := do(t, h, http.MethodGet, "/v1/inventory/holds?sourceType=QUALITY_ORDER&sourceRef=qo-1&state=ACTIVE", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK || fs.hold.filter.SourceType != "QUALITY_ORDER" || fs.hold.filter.SourceRef != "qo-1" || fs.hold.filter.State != "ACTIVE" {
		t.Fatalf("list: %d %+v", rec.Code, fs.hold.filter)
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/holds?state=GONE", "", []string{"inventory.read"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad state: %d", rec.Code)
	}
}
