package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/ledger"
)

const (
	entityA   = "00000000-0000-0000-0000-0000000000a1"
	entityB   = "00000000-0000-0000-0000-0000000000a2"
	ledgerID  = "0a1b2c3d-0000-4000-8000-000000000001"
	agingURL  = "/v1/payables/aging?legal_entity=" + entityA + "&as_of=2026-01-31"
	reconcile = "/v1/payables/aging:reconcile?legal_entity=" + entityA + "&as_of=2026-01-31&ledger=" + ledgerID
)

type fakeAgingStore struct {
	rows []domain.AgingRow
	err  error
	le   string
	asOf string
}

func (f *fakeAgingStore) AgingRows(_ context.Context, le, asOf string) ([]domain.AgingRow, error) {
	f.le, f.asOf = le, asOf
	return f.rows, f.err
}

type fakeLedger struct {
	bal     ledger.Balance
	err     error
	account string
	period  string
	owner   string
	ownErr  error
}

func (f *fakeLedger) LegalEntity(context.Context, string, string) (string, error) {
	if f.ownErr != nil {
		return "", f.ownErr
	}
	if f.owner == "" {
		return entityA, nil
	}
	return f.owner, nil
}

func (f *fakeLedger) AccountBalance(_ context.Context, _, _, account, period string) (ledger.Balance, error) {
	f.account, f.period = account, period
	return f.bal, f.err
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func agingRows() []domain.AgingRow {
	due := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	return []domain.AgingRow{
		{VendorID: "v1", Currency: "USD", DueDate: due, OpenAmount: dec("600.00")},
		{VendorID: "v2", Currency: "USD", DueDate: due.AddDate(0, 0, 30), OpenAmount: dec("400.00")},
	}
}

func agingRouter(store *fakeAgingStore, l api.LedgerBalances, account string) *chi.Mux {
	a := api.New(fakeDB{}, fakeValidator{}, fixedNow).WithAging(store, l, account)
	r := httpx.NewRouter()
	a.Register(r)
	return r
}

func get(r http.Handler, path, duties, legalEntity string) *httptest.ResponseRecorder {
	if legalEntity == "" {
		duties += "," + tenant.DutyAllScope
	}
	return getScoped(r, path, duties, legalEntity)
}

func getScoped(r http.Handler, path, duties, legalEntity string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set(tenant.HeaderLegalEntityScopeAll, tenant.LegalEntityScopeAll)
	if legalEntity != "" {
		req.Header.Set("X-Passage-Legal-Entity", legalEntity)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAging_Report(t *testing.T) {
	store := &fakeAgingStore{rows: agingRows()}
	rec := get(agingRouter(store, nil, ""), agingURL+"&group_by=vendor", "aging.read", "")
	if rec.Code != http.StatusOK || store.le != entityA || store.asOf != "2026-01-31" {
		t.Fatalf("status %d le=%s asOf=%s body=%s", rec.Code, store.le, store.asOf, rec.Body)
	}
	for _, want := range []string{`"groupBy":"vendor"`, `"vendorId":"v1"`, `"bucket":"1-30"`, `"amount":"600"`, `"bucket":"CURRENT"`, `"amount":"400"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body lacks %s: %s", want, rec.Body)
		}
	}
	rolled := get(agingRouter(store, nil, ""), agingURL, "aging.read", "")
	if strings.Contains(rolled.Body.String(), "vendorId") || !strings.Contains(rolled.Body.String(), `"groupBy":"none"`) {
		t.Fatalf("ungrouped body = %s", rolled.Body)
	}
}

func TestAging_ReportRejections(t *testing.T) {
	r := agingRouter(&fakeAgingStore{}, nil, "")
	tests := []struct {
		name, path, duties, entity string
		want                       int
		wantBody                   string
	}{
		{"missing duty", agingURL, "", "", http.StatusForbidden, "AUTH-0003"},
		{"reconcile duty does not open the report", agingURL, "aging.reconcile", "", http.StatusForbidden, "AUTH-0003"},
		{"bad as_of", "/v1/payables/aging?legal_entity=" + entityA + "&as_of=31-01-2026", "aging.read", "", http.StatusBadRequest, "as_of"},
		{"bad group_by", agingURL + "&group_by=bank", "aging.read", "", http.StatusBadRequest, "group_by"},
		{"no legal entity", "/v1/payables/aging", "aging.read", "", http.StatusBadRequest, "legal_entity"},
		{"entity outside scope", agingURL, "aging.read", entityB, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(r, tc.path, tc.duties, tc.entity)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body, tc.want, tc.wantBody)
			}
		})
	}
}

func TestAging_CallerWithoutEntityScopeCannotNameAnEntity(t *testing.T) {
	rec := getScoped(agingRouter(&fakeAgingStore{rows: agingRows()}, nil, ""), agingURL, "aging.read", "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestAging_ScopedPrincipalDefaultsItsLegalEntity(t *testing.T) {
	store := &fakeAgingStore{rows: agingRows()}
	rec := get(agingRouter(store, nil, ""), "/v1/payables/aging?as_of=2026-01-31", "aging.read", entityA)
	if rec.Code != http.StatusOK || store.le != entityA {
		t.Fatalf("status %d le=%s", rec.Code, store.le)
	}
}

func TestAging_StoreFailureIs500(t *testing.T) {
	rec := get(agingRouter(&fakeAgingStore{err: errors.New("db down")}, nil, ""), agingURL, "aging.read", "")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "COM-0500") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestAging_Reconcile(t *testing.T) {
	tests := []struct {
		name       string
		ledger     ledger.Balance
		account    string
		wantAcct   string
		reconciled bool
		wantBody   []string
	}{
		{"match", ledger.Balance{Currency: "USD", Credit: dec("1500.00"), Debit: dec("500.00")}, "", api.DefaultControlAcct, true,
			[]string{`"reconciled":true`, `"difference":"0"`, `"ledgerBalance":"1000"`, `"subledgerTotal":"1000"`}},
		{"mismatch", ledger.Balance{Currency: "USD", Credit: dec("1000.00"), Debit: dec("0.50")}, "2100", "2100", false,
			[]string{`"reconciled":false`, `"difference":"0.5"`, `"controlAccount":"2100"`, `"ledgerPeriod":"2026-01"`}},
		{"debit balance is not a liability", ledger.Balance{Currency: "USD", Debit: dec("1000.00")}, "", api.DefaultControlAcct, false,
			[]string{`"reconciled":false`, `"difference":"2000"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := &fakeLedger{bal: tc.ledger}
			rec := get(agingRouter(&fakeAgingStore{rows: agingRows()}, l, tc.account), reconcile, "aging.reconcile", "")
			if rec.Code != http.StatusOK || l.account != tc.wantAcct || l.period != "2026-01" {
				t.Fatalf("status %d account=%s period=%s body=%s", rec.Code, l.account, l.period, rec.Body)
			}
			for _, want := range tc.wantBody {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("body lacks %s: %s", want, rec.Body)
				}
			}
		})
	}
}

func TestAging_ReconcileCurrencyWithoutLedgerIsNotReconciled(t *testing.T) {
	rows := append(agingRows(), domain.AgingRow{VendorID: "v1", Currency: "EUR", DueDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), OpenAmount: dec("5.00")})
	l := &fakeLedger{bal: ledger.Balance{Currency: "USD", Credit: dec("1000.00")}}
	rec := get(agingRouter(&fakeAgingStore{rows: rows}, l, ""), reconcile, "aging.reconcile", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ledgerAvailable":false`) || !strings.HasSuffix(strings.TrimSpace(rec.Body.String()), `"reconciled":false}`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"comparable":false,"reason":"foreign-currency items require translation"`) || strings.Contains(rec.Body.String(), `"difference":"0"`) {
		t.Fatalf("ledger-currency line must be not comparable beside foreign items: %s", rec.Body)
	}
}

func TestAging_ReconcileFailures(t *testing.T) {
	tests := []struct {
		name     string
		l        api.LedgerBalances
		path     string
		duties   string
		want     int
		wantBody string
	}{
		{"ledger error", &fakeLedger{err: errors.New("boom")}, reconcile, "aging.reconcile", http.StatusBadGateway, "COM-0502"},
		{"ledger forbids caller", &fakeLedger{err: &ledger.StatusError{Status: http.StatusForbidden}}, reconcile, "aging.reconcile", http.StatusForbidden, "AUTH-0003"},
		{"ledger lookup forbids caller", &fakeLedger{ownErr: &ledger.StatusError{Status: http.StatusForbidden}}, reconcile, "aging.reconcile", http.StatusForbidden, "AUTH-0003"},
		{"ledger lookup error", &fakeLedger{ownErr: errors.New("boom")}, reconcile, "aging.reconcile", http.StatusBadGateway, "COM-0502"},
		{"ledger of another legal entity", &fakeLedger{owner: entityB}, reconcile, "aging.reconcile", http.StatusBadRequest, "ledger does not belong"},
		{"no ledger configured", nil, reconcile, "aging.reconcile", http.StatusServiceUnavailable, "COM-0503"},
		{"missing ledger param", &fakeLedger{}, "/v1/payables/aging:reconcile?legal_entity=" + entityA, "aging.reconcile", http.StatusBadRequest, "ledger"},
		{"report duty does not open reconcile", &fakeLedger{}, reconcile, "aging.read", http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(agingRouter(&fakeAgingStore{rows: agingRows()}, tc.l, ""), tc.path, tc.duties, "")
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body, tc.want, tc.wantBody)
			}
		})
	}
}

func TestAging_RoutesAbsentWithoutWiring(t *testing.T) {
	r, _ := newRouter(fakeDB{})
	if rec := get(r, agingURL, "aging.read", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestAging_MetricsCountOutcomes(t *testing.T) {
	a := api.New(fakeDB{}, fakeValidator{}, fixedNow).WithAging(&fakeAgingStore{rows: agingRows()}, &fakeLedger{bal: ledger.Balance{Currency: "USD", Credit: dec("1000.00")}}, "")
	r := httpx.NewRouter()
	a.Register(r)
	get(r, reconcile, "aging.reconcile", "")
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `payables_aging_reconciliations_total{outcome="reconciled"} 1`) {
		t.Fatalf("metrics = %s", rec.Body)
	}
}

func groupedRows() []domain.AgingRow {
	due := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	return []domain.AgingRow{
		{VendorID: "v1", VendorGroupID: "g-b", Currency: "USD", DueDate: due, OpenAmount: dec("600.00")},
		{VendorID: "v2", VendorGroupID: "g-a", Currency: "USD", DueDate: due, OpenAmount: dec("250.25")},
		{VendorID: "v3", VendorGroupID: "g-a", Currency: "USD", DueDate: due.AddDate(0, 0, 40), OpenAmount: dec("149.75")},
		{VendorID: "v4", Currency: "USD", DueDate: due, OpenAmount: dec("0.50")},
	}
}

func TestAging_ReconcileByVendorGroup(t *testing.T) {
	l := &fakeLedger{bal: ledger.Balance{Currency: "USD", Credit: dec("1000.50")}}
	rec := get(agingRouter(&fakeAgingStore{rows: groupedRows()}, l, ""), reconcile+"&group_by=vendorGroup", "aging.reconcile", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	order := []string{`"vendorGroupId":"g-a","currency":"USD","subledgerTotal":"400"`, `"vendorGroupId":"g-b","currency":"USD","subledgerTotal":"600"`, `"vendorGroupId":"UNASSIGNED","currency":"USD","subledgerTotal":"0.5"`}
	last := -1
	for _, want := range order {
		i := strings.Index(body, want)
		if i <= last {
			t.Fatalf("vendorGroups out of order or missing %s: %s", want, body)
		}
		last = i
	}
	for _, want := range []string{`"reconciled":true`, `"difference":"0"`, `"comparable":false,"reason":"the ledger exposes one AP control balance`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s: %s", want, body)
		}
	}
}

func TestAging_ReconcileWithoutGroupByOmitsVendorGroups(t *testing.T) {
	l := &fakeLedger{bal: ledger.Balance{Currency: "USD", Credit: dec("1000.00")}}
	rec := get(agingRouter(&fakeAgingStore{rows: groupedRows()}, l, ""), reconcile, "aging.reconcile", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "vendorGroups") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestAging_ReportRejectsVendorGroupGrouping(t *testing.T) {
	rec := get(agingRouter(&fakeAgingStore{rows: groupedRows()}, nil, ""), agingURL+"&group_by=vendorGroup", "aging.read", "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "aging:reconcile") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestAging_ReconcileByVendorGroupNeedsExplicitEntityScope(t *testing.T) {
	r := agingRouter(&fakeAgingStore{rows: groupedRows()}, &fakeLedger{}, "")
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"entity outside scope":    get(r, reconcile+"&group_by=vendorGroup", "aging.reconcile", entityB),
		"unscoped names a entity": getScoped(r, reconcile+"&group_by=vendorGroup", "aging.reconcile", ""),
		"no entity at all":        get(r, "/v1/payables/aging:reconcile?ledger="+ledgerID+"&group_by=vendorGroup", "aging.reconcile", ""),
	} {
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body)
		}
	}
}
