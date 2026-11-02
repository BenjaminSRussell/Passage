package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

func activationRouter(f *fakeStore, l ledgerclient.Ledger) http.Handler {
	r := httpx.NewRouter()
	a := New(nil, Config{}).WithStore(f)
	if l != nil {
		a.WithLedger(l)
	}
	a.Register(r)
	return r
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
	return `{"version":1,"currency":"USD","postingDate":"2026-01-02","ledgerId":"` + ids.New() + `"` + extra +
		`,"lines":[{"itemId":"I1","siteId":"S1","oldStd":"1.00","newStd":"2.50","qty":"10"}]}`
}

var configured = store.PostingAccounts{InventoryAccount: "1400-INV", CostChangeAccount: "5900-PPV"}

func TestActivation_RequestCannotChooseTheLedgerAccounts(t *testing.T) {
	f := &fakeStore{accounts: configured, runPost: true}
	ledger := ledgerclient.NewFake()
	h := activationRouter(f, ledger)
	for _, extra := range []string{
		`,"inventoryAccount":"9999-ATTACKER"`,
		`,"costChangeAccount":"9999-ATTACKER"`,
		`,"inventoryAccount":"9999-ATTACKER","costChangeAccount":"9998-ATTACKER"`,
	} {
		if rec := activateAs(h, activationBody(extra), "k-1"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", extra, rec.Code, rec.Body.String())
		}
	}
	if f.activation != nil || len(ledger.Entries()) != 0 {
		t.Fatalf("a request that chose accounts reached the store or ledger: %v, %d entries", f.activation, len(ledger.Entries()))
	}
}

func TestActivation_PostsToTheConfiguredAccounts(t *testing.T) {
	f := &fakeStore{accounts: configured, runPost: true}
	ledger := ledgerclient.NewFake()
	h := activationRouter(f, ledger)
	if rec := activateAs(h, activationBody(""), "k-act"); rec.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", rec.Code, rec.Body.String())
	}
	entries := ledger.Entries()
	if len(entries) != 1 || len(entries[0].Lines) != 2 {
		t.Fatalf("entries %+v", entries)
	}
	got := map[string]bool{}
	for _, l := range entries[0].Lines {
		got[l.Account] = l.Credit
	}
	if debit, ok := got["1400-INV"]; !ok || debit {
		t.Errorf("inventory must be debited on a cost increase: %v", got)
	}
	if credit, ok := got["5900-PPV"]; !ok || !credit {
		t.Errorf("cost change variance must be credited: %v", got)
	}
	if f.activation.Key != "k-act" || f.activation.Preview.Total.String() != "15" {
		t.Fatalf("store got %+v", f.activation)
	}
}

func TestActivation_RefusesWithoutConfiguredAccounts(t *testing.T) {
	f := &fakeStore{accountsErr: &domain.CodedError{Code: domain.CodePostingAccounts, Detail: "none"}, runPost: true}
	ledger := ledgerclient.NewFake()
	rec := activateAs(activationRouter(f, ledger), activationBody(""), "k-none")
	if code, _ := problemCode(t, rec); rec.Code != http.StatusUnprocessableEntity || code != domain.CodePostingAccounts {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if f.activation != nil || len(ledger.Entries()) != 0 {
		t.Fatal("an activation without accounts was attempted")
	}
}

func TestActivation_LedgerRefusalFailsTheActivation(t *testing.T) {
	f := &fakeStore{accounts: configured, runPost: true}
	ledger := ledgerclient.NewFake()
	ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	h := activationRouter(f, ledger)
	rec := activateAs(h, activationBody(""), "k-closed")
	if code, _ := problemCode(t, rec); rec.Code != http.StatusBadGateway || code != domain.CodeActivationLedger {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "did not accept") || strings.Contains(rec.Body.String(), ledgerclient.CodePeriodClosed) {
		t.Errorf("body %s", rec.Body.String())
	}
}

func TestActivation_WithoutALedgerIs503(t *testing.T) {
	f := &fakeStore{accounts: configured}
	rec := activateAs(activationRouter(f, nil), activationBody(""), "k-noledger")
	if code, _ := problemCode(t, rec); rec.Code != http.StatusServiceUnavailable || code != domain.CodeActivationLedger {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
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
