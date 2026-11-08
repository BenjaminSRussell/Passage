package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

const (
	chfCash       = "/v1/org/currencies/CHF/rounding-rules/cash"
	entityA       = "0190c9d2-7a4b-7c3e-8f10-000000000001"
	entityB       = "0190c9d2-7a4b-7c3e-8f10-000000000002"
	scopeHeader   = "X-Passage-Legal-Entity"
	validRuleBody = `{"unit":"0.05","method":"half_up"}`
)

func TestPutCurrencyRounding_PassesTheParsedRuleToTheStore(t *testing.T) {
	f := &fakeStore{roundingChanged: true}
	r, _ := newRouter(f)
	rec := do(r, http.MethodPut, chfCash, `{"unit":"0.050","method":"half_up","legalEntityId":"`+entityA+`"}`, referenceDuties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"unit":"0.05"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	got := f.roundingPut
	if got.CurrencyCode != "CHF" || got.Purpose != "cash" || got.Method != "half_up" || !got.Unit.Equal(decimal.New(5, -2)) || got.LegalEntityID == nil || *got.LegalEntityID != entityA {
		t.Fatalf("store received %+v", got)
	}
	do(r, http.MethodPut, chfCash, validRuleBody, referenceDuties, true)
	if f.roundingPut.LegalEntityID != nil {
		t.Fatalf("a missing legalEntityId must store a tenant row, got %v", *f.roundingPut.LegalEntityID)
	}
}

func TestPutCurrencyRounding_RefusesInvalidInput(t *testing.T) {
	tests := []struct {
		name, path, body, field string
	}{
		{"unknown purpose", "/v1/org/currencies/CHF/rounding-rules/retail", validRuleBody, "purpose"},
		{"purpose is case sensitive", "/v1/org/currencies/CHF/rounding-rules/General", validRuleBody, "purpose"},
		{"unknown method", chfCash, `{"unit":"0.05","method":"bankers"}`, "method"},
		{"missing method", chfCash, `{"unit":"0.05"}`, "method"},
		{"unit not a number", chfCash, `{"unit":"abc","method":"up"}`, "unit"},
		{"unit missing", chfCash, `{"method":"up"}`, "unit"},
		{"unit NaN", chfCash, `{"unit":"NaN","method":"up"}`, "unit"},
		{"unit zero", chfCash, `{"unit":"0","method":"up"}`, "unit"},
		{"unit negative", chfCash, `{"unit":"-1","method":"up"}`, "unit"},
		{"unit seven places", chfCash, `{"unit":"0.0000001","method":"up"}`, "unit"},
		{"unit out of range", chfCash, `{"unit":"1e400","method":"up"}`, "unit"},
		{"legal entity not a uuid", chfCash, `{"unit":"0.05","method":"up","legalEntityId":"bogus"}`, "legalEntityId"},
		{"legal entity empty", chfCash, `{"unit":"0.05","method":"up","legalEntityId":""}`, "legalEntityId"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			r, _ := newRouter(f)
			rec := do(r, http.MethodPut, tt.path, tt.body, referenceDuties, true)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"COM-0001"`) || !strings.Contains(rec.Body.String(), `"`+tt.field+`"`) || f.roundingPutCalls != 0 {
				t.Fatalf("status %d body %s calls %d", rec.Code, rec.Body, f.roundingPutCalls)
			}
		})
	}
	r, _ := newRouter(&fakeStore{})
	for _, body := range []string{`{`, `{"unit":0.05,"method":"up"}`} {
		if rec := do(r, http.MethodPut, chfCash, body, referenceDuties, true); rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status %d", body, rec.Code)
		}
	}
}

func TestPutCurrencyRounding_AuthorizationAndStoreFailure(t *testing.T) {
	f := &fakeStore{}
	r, a := newRouter(f)
	if rec := do(r, http.MethodPut, chfCash, validRuleBody, "org.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without org.reference.write: %d", rec.Code)
	}
	if rec := do(r, http.MethodPut, chfCash, validRuleBody, referenceDuties, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a tenant: %d", rec.Code)
	}
	rec := do(r, http.MethodPut, chfCash, validRuleBody, referenceDuties, true, scopeHeader, entityA)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") || f.roundingPutCalls != 0 {
		t.Fatalf("scoped caller: %d %s", rec.Code, rec.Body)
	}
	f.err = errors.New("boom")
	if rec := do(r, http.MethodPut, chfCash, validRuleBody, referenceDuties, true); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "COM-0500") {
		t.Fatalf("store failure: %d %s", rec.Code, rec.Body)
	}
	if got := metric(a, "org_currency_rounding_rules_changed_total"); got != "0" {
		t.Fatalf("counter after refusals and failure = %s; want 0", got)
	}
}

func metric(a *api.API, name string) string {
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if value, ok := strings.CutPrefix(line, name+" "); ok {
			return value
		}
	}
	return "missing"
}

func TestPutCurrencyRounding_CounterCountsOnlyChangedRows(t *testing.T) {
	f := &fakeStore{roundingChanged: true}
	r, a := newRouter(f)
	do(r, http.MethodPut, chfCash, validRuleBody, referenceDuties, true)
	f.roundingChanged = false
	do(r, http.MethodPut, chfCash, validRuleBody, referenceDuties, true)
	if got := metric(a, "org_currency_rounding_rules_changed_total"); got != "1" {
		t.Fatalf("counter = %s; want 1", got)
	}
}

func TestListCurrencyRoundingRules(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	rec := do(r, http.MethodGet, "/v1/org/currencies/CHF/rounding-rules", "", "org.read", true)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, "/v1/org/currencies/CHF/rounding-rules", "", "", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without org.read: %d", rec.Code)
	}
	f.err = httpx.NewProblem(http.StatusNotFound, store.CodeReferenceNotFound, "no currency")
	if rec := do(r, http.MethodGet, "/v1/org/currencies/XXQ/rounding-rules", "", "org.read", true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown currency: %d", rec.Code)
	}
}

// An empty currency segment names no currency: it must not reach the store, where an empty
// currency lists every rule of the tenant (the package export's read).
func TestListCurrencyRoundingRules_EmptyCurrencyIsNotFound(t *testing.T) {
	f := &fakeStore{roundings: []store.CurrencyRounding{{CurrencyCode: "CHF", Purpose: "cash"}}}
	r, _ := newRouter(f)
	rec := do(r, http.MethodGet, "/v1/org/currencies//rounding-rules", "", "org.read", true)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeReferenceNotFound) {
		t.Fatalf("empty currency: %d %s", rec.Code, rec.Body)
	}
}

func TestRoundCurrency_RefusesInvalidInput(t *testing.T) {
	tests := []struct {
		name, query, field string
	}{
		{"missing amount", "purpose=cash", "amount"},
		{"empty amount", "amount=", "amount"},
		{"not a number", "amount=abc", "amount"},
		{"too large", "amount=1e40", "amount"},
		{"too fine", "amount=1e-30", "amount"},
		{"unknown purpose", "amount=1&purpose=retail", "purpose"},
		{"legal entity not a uuid", "amount=1&legalEntityId=bogus", "legalEntityId"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			r, _ := newRouter(f)
			rec := do(r, http.MethodGet, "/v1/org/currencies/CHF/round?"+tt.query, "", "org.read", true)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"COM-0001"`) || !strings.Contains(rec.Body.String(), `"`+tt.field+`"`) || f.resolveArgs != [3]string{} {
				t.Fatalf("status %d body %s resolved %v", rec.Code, rec.Body, f.resolveArgs)
			}
		})
	}
	r, _ := newRouter(&fakeStore{})
	if rec := do(r, http.MethodGet, "/v1/org/currencies/CHF/round?amount=1", "", "", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without org.read: %d", rec.Code)
	}
}

func TestRoundCurrency_ScopedCaller(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	rec := do(r, http.MethodGet, "/v1/org/currencies/CHF/round?amount=1&legalEntityId="+entityB, "", "org.read", true, scopeHeader, entityA)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") || f.resolveArgs != [3]string{} {
		t.Fatalf("another entity: %d %s", rec.Code, rec.Body)
	}
	rec = do(r, http.MethodGet, "/v1/org/currencies/CHF/round?amount=1", "", "org.read", true, scopeHeader, entityA)
	if rec.Code != http.StatusOK || f.resolveArgs != [3]string{"CHF", "general", entityA} || !strings.Contains(rec.Body.String(), `"legalEntityId":"`+entityA+`"`) {
		t.Fatalf("own scope: %d %s resolved %v", rec.Code, rec.Body, f.resolveArgs)
	}
	rec = do(r, http.MethodGet, "/v1/org/currencies/CHF/round?amount=1&legalEntityId="+entityA, "", "org.read", true, scopeHeader, entityA)
	if rec.Code != http.StatusOK {
		t.Fatalf("own id: %d %s", rec.Code, rec.Body)
	}
}

func TestRoundCurrency_RefusesMultiEntityClaim(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	claim := entityA + "," + entityB
	rec := do(r, http.MethodGet, "/v1/org/currencies/CHF/round?amount=1", "", "org.read", true, scopeHeader, claim)
	if rec.Code != http.StatusBadRequest || f.resolveArgs != [3]string{} {
		t.Fatalf("several entities, no query: %d %s resolved %v", rec.Code, rec.Body, f.resolveArgs)
	}
	rec = do(r, http.MethodGet, "/v1/org/currencies/CHF/round?amount=1&legalEntityId="+entityB, "", "org.read", true, scopeHeader, claim)
	if rec.Code != http.StatusBadRequest || f.resolveArgs != [3]string{} {
		t.Fatalf("several entities, named: %d %s resolved %v", rec.Code, rec.Body, f.resolveArgs)
	}
}

func TestRoundCurrency_FormatsTheResolvedRule(t *testing.T) {
	f := &fakeStore{resolved: store.RoundingRule{Unit: decimal.RequireFromString("0.050000"), Method: domain.HalfUp, Source: store.RoundingSourceTenant}}
	r, _ := newRouter(f)
	rec := do(r, http.MethodGet, "/v1/org/currencies/CHF/round?amount=10.024&purpose=cash", "", "org.read", true)
	want := `{"currency":"CHF","purpose":"cash","legalEntityId":null,"amount":"10.024","rounded":"10.00","unit":"0.05","method":"half_up","source":"tenant"}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("status %d body %s; want %s", rec.Code, rec.Body, want)
	}
	f.resolved = store.RoundingRule{Unit: decimal.RequireFromString("100.000000"), Method: domain.Down, Source: store.RoundingSourceLegalEntity}
	rec = do(r, http.MethodGet, "/v1/org/currencies/JPY/round?amount=1234", "", "org.read", true)
	if !strings.Contains(rec.Body.String(), `"rounded":"1200"`) || !strings.Contains(rec.Body.String(), `"unit":"100"`) {
		t.Fatalf("unit 100: %s", rec.Body)
	}
}

func TestRoundCurrency_StoreBacked(t *testing.T) {
	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, pool, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouter()
	api.New(store.New(pool)).Register(r)

	for path, body := range map[string]string{
		"/v1/org/currencies/CHF/rounding-rules/cash":    `{"unit":"0.05","method":"half_up"}`,
		"/v1/org/currencies/EUR/rounding-rules/general": `{"unit":"0.01","method":"half_even"}`,
	} {
		if rec := do(r, http.MethodPut, path, body, referenceDuties, true); rec.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	tests := []struct{ path, rounded, source string }{
		{"/v1/org/currencies/CHF/round?amount=10.024&purpose=cash", "10.00", "tenant"},
		{"/v1/org/currencies/CHF/round?amount=10.025&purpose=cash", "10.05", "tenant"},
		{"/v1/org/currencies/EUR/round?amount=2.345", "2.34", "tenant"},
		{"/v1/org/currencies/EUR/round?amount=2.355", "2.36", "tenant"},
		{"/v1/org/currencies/JPY/round?amount=1234.5", "1234", "default"},
		{"/v1/org/currencies/CHF/round?amount=10.025", "10.02", "default"},
	}
	for _, tt := range tests {
		rec := do(r, http.MethodGet, tt.path, "", "org.read", true)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rounded":"`+tt.rounded+`"`) || !strings.Contains(rec.Body.String(), `"source":"`+tt.source+`"`) {
			t.Errorf("GET %s: %d %s; want rounded %s source %s", tt.path, rec.Code, rec.Body, tt.rounded, tt.source)
		}
	}
	list := do(r, http.MethodGet, "/v1/org/currencies/CHF/rounding-rules", "", "org.read", true)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"purpose":"cash"`) || !strings.Contains(list.Body.String(), `"unit":"0.05"`) {
		t.Errorf("list: %d %s", list.Code, list.Body)
	}
}

func TestDeleteCurrencyRounding_PassesTheScopeToTheStore(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	if rec := do(r, http.MethodDelete, chfCash, "", referenceDuties, true); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || f.roundingDeleteArgs != [3]string{"CHF", "cash", ""} {
		t.Fatalf("tenant delete = %d %q, store got %v", rec.Code, rec.Body, f.roundingDeleteArgs)
	}
	if rec := do(r, http.MethodDelete, chfCash+"?legalEntityId="+entityA, "", referenceDuties, true); rec.Code != http.StatusNoContent || f.roundingDeleteArgs != [3]string{"CHF", "cash", entityA} {
		t.Fatalf("legal-entity delete = %d, store got %v", rec.Code, f.roundingDeleteArgs)
	}
}

func TestDeleteCurrencyRounding_RefusesInvalidInput(t *testing.T) {
	tests := []struct{ name, path, field string }{
		{"unknown purpose", "/v1/org/currencies/CHF/rounding-rules/retail", "purpose"},
		{"purpose is case sensitive", "/v1/org/currencies/CHF/rounding-rules/Cash", "purpose"},
		{"legal entity not a uuid", chfCash + "?legalEntityId=bogus", "legalEntityId"},
		{"legal entity empty", chfCash + "?legalEntityId=", "legalEntityId"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			r, _ := newRouter(f)
			rec := do(r, http.MethodDelete, tt.path, "", referenceDuties, true)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+tt.field+`"`) || f.roundingDeleteCalls != 0 {
				t.Fatalf("got %d %s with %d store calls; want 400 naming %s and none", rec.Code, rec.Body, f.roundingDeleteCalls, tt.field)
			}
		})
	}
}

func TestDeleteCurrencyRounding_AuthorizationAndStoreFailures(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	if rec := do(r, http.MethodDelete, chfCash, "", "org.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without org.reference.write: %d", rec.Code)
	}
	if rec := do(r, http.MethodDelete, chfCash, "", referenceDuties, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a tenant: %d", rec.Code)
	}
	rec := do(r, http.MethodDelete, chfCash, "", referenceDuties, true, scopeHeader, entityA)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") || f.roundingDeleteCalls != 0 {
		t.Fatalf("scoped caller: %d %s", rec.Code, rec.Body)
	}
	f.err = httpx.NewProblem(http.StatusNotFound, store.CodeRoundingNotFound, "no rule")
	if rec := do(r, http.MethodDelete, chfCash, "", referenceDuties, true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "ORG-CUR-ROUNDING-NOT-FOUND") {
		t.Fatalf("missing rule: %d %s", rec.Code, rec.Body)
	}
	f.err = errors.New("boom")
	if rec := do(r, http.MethodDelete, chfCash, "", referenceDuties, true); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "COM-0500") {
		t.Fatalf("store failure: %d %s", rec.Code, rec.Body)
	}
}

func TestRoundCurrency_CallerWithNoLegalEntityScope(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	rec := do(r, http.MethodGet, "/v1/org/currencies/CHF/round?amount=1&legalEntityId="+entityA, "", "org.read", true)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") || f.resolveArgs != [3]string{} {
		t.Fatalf("named entity without scope: %d %s", rec.Code, rec.Body)
	}
	rec = do(r, http.MethodGet, "/v1/org/currencies/CHF/round?amount=1", "", "org.read", true)
	if rec.Code != http.StatusOK || f.resolveArgs != [3]string{"CHF", "general", ""} {
		t.Fatalf("tenant-level rule without scope: %d %s resolved %v", rec.Code, rec.Body, f.resolveArgs)
	}
}
