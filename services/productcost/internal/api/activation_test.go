package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/inventory"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

const (
	itemOne = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6a01"
	siteOne = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6a02"
)

type fakeOnHand struct {
	positions map[inventory.Position]decimal.Decimal
	err       error
	calls     int
	legal     string
	asOf      string
	asked     []inventory.Position
}

func (f *fakeOnHand) OnHand(_ context.Context, legalEntityID, asOf string, positions []inventory.Position) (map[inventory.Position]decimal.Decimal, error) {
	f.calls++
	f.legal, f.asOf, f.asked = legalEntityID, asOf, positions
	return f.positions, f.err
}

func onHandOf(qty string) *fakeOnHand {
	return &fakeOnHand{positions: map[inventory.Position]decimal.Decimal{{ItemID: itemOne, SiteID: siteOne}: decimal.RequireFromString(qty)}}
}

func activationRouter(f *fakeStore, l ledgerclient.Ledger, oh inventory.Reader) (http.Handler, *API) {
	r := httpx.NewRouter()
	if f.positions == nil {
		f.positions = []inventory.Position{{ItemID: itemOne, SiteID: siteOne}}
	}
	a := New(nil, Config{}).WithStore(f)
	if l != nil {
		a.WithLedger(l)
	}
	if oh != nil {
		a.WithInventory(oh)
	}
	a.Register(r)
	return r, a
}

func activateAs(h http.Handler, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/product-costing/versions/"+someID+"/activate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
	req.Header.Set(tenant.HeaderDuties, activate)
	req.Header.Set(tenant.HeaderUser, "cost.controller")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func activationBody(extra string) string {
	return `{"version":1,"currency":"USD","postingDate":"2026-01-02","ledgerId":"` + ids.New() + `"` + extra + `}`
}

func TestActivation_RequestCannotSupplyAccountsStandardsQuantitiesOrPositions(t *testing.T) {
	f := &fakeStore{}
	ledger := ledgerclient.NewFake()
	h, _ := activationRouter(f, ledger, onHandOf("10"))
	bodies := []string{
		activationBody(`,"inventoryAccount":"9999-ATTACKER"`),
		activationBody(`,"costChangeAccount":"9999-ATTACKER"`),
		activationBody(`,"lines":[{"itemId":"` + itemOne + `","siteId":"` + siteOne + `"}]`),
		activationBody(`,"lines":[{"itemId":"` + itemOne + `","siteId":"` + siteOne + `","oldStd":"1.00","newStd":"900"}]`),
		activationBody(`,"lines":[]`),
	}
	for _, body := range bodies {
		if rec := activateAs(h, body, "k-1"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", body, rec.Code, rec.Body.String())
		}
	}
	bare := `{"version":1,"lines":[{"itemId":"` + itemOne + `","siteId":"` + siteOne + `"}]}`
	f.positions = []inventory.Position{}
	if rec := activateAs(h, bare, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("lines on a bare activation: %d", rec.Code)
	}
	if f.activation != nil || f.preview != nil || f.action != "" || len(ledger.Entries()) != 0 {
		t.Fatalf("a request that chose its own numbers reached the store or ledger: %v %v", f.activation, f.preview)
	}
}

func TestActivation_BareRequestRevaluesEveryStoredPositionAndNeedsItsFields(t *testing.T) {
	f := &fakeStore{}
	oh := onHandOf("10")
	h, _ := activationRouter(f, ledgerclient.NewFake(), oh)
	if rec := activateAs(h, `{"version":1}`, ""); rec.Code != http.StatusBadRequest || f.activation != nil || f.action != "" {
		t.Fatalf("a bare request for a version with stored positions must not skip the revaluation: %d %s", rec.Code, rec.Body.String())
	}
	if rec := activateAs(h, activationBody(""), "k-bare"); rec.Code != http.StatusOK || f.activation == nil || len(oh.asked) != 1 || f.action != "" {
		t.Fatalf("%d %s asked %v", rec.Code, rec.Body.String(), oh.asked)
	}
	f = &fakeStore{positions: []inventory.Position{}}
	h, _ = activationRouter(f, ledgerclient.NewFake(), nil)
	if rec := activateAs(h, `{"version":1}`, ""); rec.Code != http.StatusOK || f.action != domain.ActionActivate || f.activation != nil {
		t.Fatalf("a version with no stored positions only moves the status: %d %s", rec.Code, rec.Body.String())
	}
}

func TestActivation_RefusesAVersionWithTooManyItems(t *testing.T) {
	f := &fakeStore{}
	for i := 0; i <= maxActivationItems; i++ {
		f.positions = append(f.positions, inventory.Position{ItemID: ids.New(), SiteID: siteOne})
	}
	oh := onHandOf("1")
	h, _ := activationRouter(f, ledgerclient.NewFake(), oh)
	rec := activateAs(h, activationBody(""), "k-big")
	if code, _ := problemCode(t, rec); rec.Code != http.StatusUnprocessableEntity || code != domain.CodeActivationTooBig || oh.calls != 0 {
		t.Fatalf("%d %s calls %d", rec.Code, rec.Body.String(), oh.calls)
	}
}

func TestActivation_PassesPositionsAndInventoryOnHandToTheStore(t *testing.T) {
	f := &fakeStore{}
	ledger := ledgerclient.NewFake()
	oh := onHandOf("10")
	h, a := activationRouter(f, ledger, oh)
	a.now = func() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC) }
	if rec := activateAs(h, activationBody(""), "k-act"); rec.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", rec.Code, rec.Body.String())
	}
	in := f.activation
	if in == nil || in.Key != "k-act" || in.Ledger != ledger || in.Expected != 1 {
		t.Fatalf("store input %+v", in)
	}
	want := inventory.Position{ItemID: itemOne, SiteID: siteOne}
	if len(oh.asked) != 1 || oh.asked[0] != want || !in.OnHand[want].Equal(decimal.NewFromInt(10)) {
		t.Fatalf("asked %v on-hand %v, want the stored position with the inventory quantity", oh.asked, in.OnHand)
	}
	if oh.asOf != "2026-01-02" {
		t.Errorf("a past posting date reads the on-hand as of that date, got %q", oh.asOf)
	}
}

func TestActivation_TodayReadsTheLiveOnHand(t *testing.T) {
	f := &fakeStore{}
	oh := onHandOf("3")
	h, a := activationRouter(f, ledgerclient.NewFake(), oh)
	a.now = func() time.Time { return time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC) }
	if rec := activateAs(h, activationBody(""), "k-live"); rec.Code != http.StatusOK || oh.asOf != "" {
		t.Fatalf("%d %s asOf=%q", rec.Code, rec.Body.String(), oh.asOf)
	}
}

func TestActivation_RefusesWithoutInventoryOrWhenItFails(t *testing.T) {
	f := &fakeStore{}
	h, _ := activationRouter(f, ledgerclient.NewFake(), nil)
	rec := activateAs(h, activationBody(""), "k-no-inv")
	if code, _ := problemCode(t, rec); rec.Code != http.StatusServiceUnavailable || code != domain.CodeOnHandUnavailable {
		t.Fatalf("no inventory: %d %s", rec.Code, rec.Body.String())
	}
	h, _ = activationRouter(f, ledgerclient.NewFake(), &fakeOnHand{err: errors.New("down")})
	rec = activateAs(h, activationBody(""), "k-inv-down")
	if code, _ := problemCode(t, rec); rec.Code != http.StatusBadGateway || code != domain.CodeOnHandUnavailable {
		t.Fatalf("inventory down: %d %s", rec.Code, rec.Body.String())
	}
	if f.activation != nil {
		t.Fatal("an activation without quantities reached the store")
	}
}

func TestActivation_StoreRefusalsMapToTheirCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&domain.CodedError{Code: domain.CodePostingAccounts, Detail: "none"}, http.StatusUnprocessableEntity, domain.CodePostingAccounts},
		{&domain.CodedError{Code: domain.CodeNoStoredStandard, Detail: "none"}, http.StatusUnprocessableEntity, domain.CodeNoStoredStandard},
		{&domain.CodedError{Code: domain.CodeNoOnHand, Detail: "none"}, http.StatusUnprocessableEntity, domain.CodeNoOnHand},
		{httpx.NewProblem(http.StatusConflict, domain.CodeActivationPending, "in flight"), http.StatusConflict, domain.CodeActivationPending},
		{&store.LedgerRefusal{Err: &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed}}, http.StatusBadGateway, domain.CodeActivationLedger},
	} {
		f := &fakeStore{activateErr: tc.err}
		h, a := activationRouter(f, ledgerclient.NewFake(), onHandOf("10"))
		rec := activateAs(h, activationBody(""), "k-refused")
		if code, _ := problemCode(t, rec); rec.Code != tc.status || code != tc.code {
			t.Fatalf("%v: %d %s", tc.err, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), ledgerclient.CodePeriodClosed) {
			t.Errorf("the ledger's own code leaked: %s", rec.Body.String())
		}
		var refused *store.LedgerRefusal
		if errors.As(tc.err, &refused) && a.activationPostFailures.Load() != 1 {
			t.Errorf("post failures %d, want 1", a.activationPostFailures.Load())
		}
	}
}

func TestActivation_WithoutALedgerIs503(t *testing.T) {
	f := &fakeStore{}
	h, _ := activationRouter(f, nil, onHandOf("10"))
	rec := activateAs(h, activationBody(""), "k-noledger")
	if code, _ := problemCode(t, rec); rec.Code != http.StatusServiceUnavailable || code != domain.CodeActivationLedger {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestActivation_DryRunPreviewsWithoutALedgerOrKey(t *testing.T) {
	f := &fakeStore{}
	h, _ := activationRouter(f, nil, onHandOf("10"))
	body := `{"version":1,"dryRun":true,"currency":"USD","postingDate":"2026-01-02"}`
	rec := activateAs(h, body, "")
	if rec.Code != http.StatusOK || f.preview == nil || f.activation != nil || !strings.Contains(rec.Body.String(), `"dryRun":true`) {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body.String())
	}
}

func TestActivation_DryRunOfANonStandardVersionIsAStatusOnlyPreview(t *testing.T) {
	f := &fakeStore{versionType: domain.VersionPlanned}
	oh := onHandOf("10")
	h, _ := activationRouter(f, nil, oh)
	rec := activateAs(h, `{"version":1,"dryRun":true,"currency":"USD","postingDate":"2026-01-02"}`, "")
	if rec.Code != http.StatusOK || f.preview != nil || oh.calls != 0 || !strings.Contains(rec.Body.String(), `"lines":[]`) || !strings.Contains(rec.Body.String(), `"total":"0"`) {
		t.Fatalf("status-only preview: %d %s inventory calls %d", rec.Code, rec.Body.String(), oh.calls)
	}
}

func TestActivation_ReversalReadsNothingFromTheRequestOrInventory(t *testing.T) {
	f := &fakeStore{}
	oh := onHandOf("10")
	h, _ := activationRouter(f, ledgerclient.NewFake(), oh)
	target := ids.New()
	body := `{"version":2,"reversesActivationId":"` + target + `","postingDate":"2026-01-03"}`
	if rec := activateAs(h, body, "k-rev"); rec.Code != http.StatusOK {
		t.Fatalf("reversal: %d %s", rec.Code, rec.Body.String())
	}
	in := f.activation
	if in == nil || in.ReversesID != target || in.OnHand != nil || oh.calls != 0 || in.Ledger == nil {
		t.Fatalf("store input %+v, inventory calls %d", in, oh.calls)
	}
	f.activation = nil
	for _, extra := range []string{`,"currency":"USD"`, `,"ledgerId":"` + ids.New() + `"`, `,"lines":[{"itemId":"` + itemOne + `","siteId":"` + siteOne + `"}]`} {
		if rec := activateAs(h, strings.TrimSuffix(body, "}")+extra+"}", "k-rev-2"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", extra, rec.Code)
		}
	}
	if rec := activateAs(h, `{"version":2,"reversesActivationId":"nope"}`, "k-rev-3"); rec.Code != http.StatusBadRequest || f.activation != nil {
		t.Errorf("malformed target: %d", rec.Code)
	}
}

func TestActivation_UppercaseIdsReachTheStoreCanonical(t *testing.T) {
	f := &fakeStore{}
	h, _ := activationRouter(f, ledgerclient.NewFake(), nil)
	target := ids.New()
	path := "/v1/product-costing/versions/" + strings.ToUpper(someID) + "/activate"
	body := `{"version":2,"reversesActivationId":"` + strings.ToUpper(target) + `"}`
	do := func(b string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
		req.Header.Set(tenant.HeaderDuties, activate)
		req.Header.Set(tenant.HeaderUser, "cost.controller")
		req.Header.Set("Idempotency-Key", "k-upper")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(body); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if in := f.activation; in == nil || in.VersionID != someID || in.ReversesID != target {
		t.Fatalf("store input %+v, want lowercase ids", in)
	}
	lower := f.activation.Fingerprint
	if rec := do(strings.ToLower(body)); rec.Code != http.StatusOK || !reflect.DeepEqual(f.activation.Fingerprint, lower) {
		t.Fatalf("the same request in lowercase must fingerprint equal: %d", rec.Code)
	}
}

func TestActivation_ReversalErrorsAreTheStoresAnswers(t *testing.T) {
	body := `{"version":2,"reversesActivationId":"` + ids.New() + `"}`
	f := &fakeStore{activateErr: httpx.NewProblem(http.StatusConflict, domain.CodeReversalTarget, "already reversed")}
	h, _ := activationRouter(f, ledgerclient.NewFake(), nil)
	if rec := activateAs(h, body, "k-dup"); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body.String())
	}
	f = &fakeStore{activateErr: &domain.CodedError{Code: domain.CodeReversalTarget, Detail: "not an activation of this version"}}
	h, _ = activationRouter(f, ledgerclient.NewFake(), nil)
	if rec := activateAs(h, body, "k-wrong"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong version: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPostingAccounts_AreMaintainedPerLegalEntityInScope(t *testing.T) {
	f := &fakeStore{}
	h := storedRouter(f)
	le := ids.New()
	path := "/v1/product-costing/posting-accounts/" + le
	put := func(body, duties, scope string) *httptest.ResponseRecorder {
		return call{http.MethodPut, path, body, duties, "", scope}.do(h)
	}
	good := `{"inventoryAccount":" 1400-INV ","costChangeAccount":"5900-PPV"}`
	if rec := put(good, view, le); rec.Code != http.StatusForbidden {
		t.Errorf("view duty may not write: %d", rec.Code)
	}
	if rec := put(good, maintain, ""); rec.Code != http.StatusForbidden {
		t.Errorf("no legal-entity scope: %d", rec.Code)
	}
	if rec := put(good, maintain, ids.New()); rec.Code != http.StatusForbidden {
		t.Errorf("other legal entity: %d", rec.Code)
	}
	for _, body := range []string{`{}`, `{"inventoryAccount":"A","costChangeAccount":"A"}`, `{"inventoryAccount":"A","costChangeAccount":"B","legalEntityId":"x"}`} {
		if rec := put(body, maintain, le); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	if f.calls != 0 {
		t.Fatalf("refused requests reached the store %d times", f.calls)
	}
	rec := put(good, maintain, le)
	if rec.Code != http.StatusOK || f.accounts.LegalEntityID != le || f.accounts.InventoryAccount != "1400-INV" {
		t.Fatalf("put: %d %s %+v", rec.Code, rec.Body.String(), f.accounts)
	}
	if rec = (call{http.MethodGet, path, "", view, "", le}).do(h); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"costChangeAccount":"5900-PPV"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	if rec = (call{http.MethodGet, "/v1/product-costing/posting-accounts/nope", "", view, "", le}).do(h); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id: %d", rec.Code)
	}
}

func TestPostingAccounts_NothingConfiguredIs404(t *testing.T) {
	f := &fakeStore{accountsErr: &domain.CodedError{Code: domain.CodePostingAccounts, Detail: "none"}}
	le := ids.New()
	rec := call{http.MethodGet, "/v1/product-costing/posting-accounts/" + le, "", view, "", le}.do(storedRouter(f))
	if code, _ := problemCode(t, rec); rec.Code != http.StatusNotFound || code != domain.CodePostingAccounts {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
