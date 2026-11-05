package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

const resolveDuty = "productcost.activation-attempt.resolve"

func attemptCall(h http.Handler, method, path, body, duties, user, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
	req.Header.Set(tenant.HeaderDuties, duties)
	if user != "" {
		req.Header.Set(tenant.HeaderUser, user)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAttempts_AreOperatorDutyOnly(t *testing.T) {
	h := storedRouter(&fakeStore{})
	resolve := "/v1/product-costing/versions/" + someID + "/activation-attempt/resolve"
	body := `{"version":1,"outcome":"not-posted","reason":"checked"}`
	for _, duty := range []string{view, maintain, activate, "productcost.calculate", "productcost.variance.compute"} {
		if rec := attemptCall(h, http.MethodGet, "/v1/product-costing/activation-attempts", "", duty, "ops", ""); rec.Code != http.StatusForbidden {
			t.Errorf("list with %s: %d, want 403", duty, rec.Code)
		}
		if rec := attemptCall(h, http.MethodPost, resolve, body, duty, "ops", "k"); rec.Code != http.StatusForbidden {
			t.Errorf("resolve with %s: %d, want 403", duty, rec.Code)
		}
	}
	if rec := attemptCall(h, http.MethodGet, "/v1/product-costing/activation-attempts", "", resolveDuty, "ops", ""); rec.Code != http.StatusOK {
		t.Errorf("list with the operator duty: %d %s", rec.Code, rec.Body)
	}
}

func TestAttempts_ListReturnsTheStoresItems(t *testing.T) {
	f := &fakeStore{attempts: []store.Attempt{{VersionID: someID, NeedsOperator: true}}}
	rec := attemptCall(storedRouter(f), http.MethodGet, "/v1/product-costing/activation-attempts", "", resolveDuty, "ops", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"needsOperator":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAttempts_ResolveValidatesBeforeTheStore(t *testing.T) {
	path := "/v1/product-costing/versions/" + someID + "/activation-attempt/resolve"
	entry := ids.New()
	for name, c := range map[string]struct{ body, user, key, field string }{
		"no version":            {`{"outcome":"not-posted","reason":"r"}`, "ops", "k", "version"},
		"bad outcome":           {`{"version":1,"outcome":"maybe","reason":"r"}`, "ops", "k", "outcome"},
		"posted without entry":  {`{"version":1,"outcome":"posted","reason":"r"}`, "ops", "k", "journalEntryId"},
		"posted bad entry":      {`{"version":1,"outcome":"posted","journalEntryId":"x","reason":"r"}`, "ops", "k", "journalEntryId"},
		"not-posted with entry": {`{"version":1,"outcome":"not-posted","journalEntryId":"` + entry + `","reason":"r"}`, "ops", "k", "journalEntryId"},
		"no reason":             {`{"version":1,"outcome":"not-posted","reason":"  "}`, "ops", "k", "reason"},
		"no key":                {`{"version":1,"outcome":"not-posted","reason":"r"}`, "ops", "", "Idempotency-Key"},
		"bad reverses":          {`{"version":1,"outcome":"not-posted","reason":"r","reversesActivationId":"z"}`, "ops", "k", "reversesActivationId"},
	} {
		f := &fakeStore{}
		rec := attemptCall(storedRouter(f), http.MethodPost, path, c.body, resolveDuty, c.user, c.key)
		_, fields := problemCode(t, rec)
		if rec.Code != http.StatusBadRequest || fields[c.field] == "" || f.resolve != nil {
			t.Errorf("%s: %d %v (store called: %v)", name, rec.Code, fields, f.resolve != nil)
		}
	}
	if rec := attemptCall(storedRouter(&fakeStore{}), http.MethodPost, path, `{"version":1,"outcome":"not-posted","reason":"r"}`, resolveDuty, "", "k"); rec.Code != http.StatusUnauthorized {
		t.Errorf("no user: %d, want 401", rec.Code)
	}
}

func TestAttempts_ResolvePassesTheDecisionToTheStore(t *testing.T) {
	f := &fakeStore{}
	entry := strings.ToUpper(ids.New())
	rec := attemptCall(storedRouter(f), http.MethodPost, "/v1/product-costing/versions/"+someID+"/activation-attempt/resolve",
		`{"version":3,"outcome":"posted","journalEntryId":"`+entry+`","reason":" ledger shows it "}`, resolveDuty, "ops", "k-1")
	if rec.Code != http.StatusOK || f.resolve == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := *f.resolve
	if got.VersionID != someID || got.Expected != 3 || got.Outcome != store.OutcomePosted || got.JournalEntryID != strings.ToLower(entry) || got.Reason != "ledger shows it" || got.Key != "k-1" {
		t.Fatalf("store input %+v", got)
	}
}
