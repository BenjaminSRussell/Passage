package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/tax/internal/api"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
	"github.com/BenjaminSRussell/passage/services/tax/migrations"
)

type fakeStore struct {
	queued     []string
	queueErr   error
	items      []store.Recommit
	processed  []json.RawMessage
	processErr []error
	totals     []store.Total
	recorded   []domain.Txn
	config     *store.LedgerConfig
}

func (f *fakeStore) RecordTransactions(_ context.Context, txns []domain.Txn) (int, error) {
	f.recorded = txns
	return len(txns), nil
}

func (f *fakeStore) SetLedgerConfig(_ context.Context, cfg store.LedgerConfig) error {
	f.config = &cfg
	return nil
}

func (f *fakeStore) SettleRun(context.Context, domain.Period, func(store.LedgerConfig) domain.GLMovement) (store.Run, error) {
	return store.Run{}, nil
}

func (f *fakeStore) Totals(context.Context, domain.Period) ([]store.Total, error) {
	return f.totals, nil
}

func (f *fakeStore) EnqueueRecommit(_ context.Context, provider, _ string, request json.RawMessage) (string, error) {
	if f.queueErr != nil {
		return "", f.queueErr
	}
	f.queued = append(f.queued, provider)
	f.items = append(f.items, store.Recommit{ID: ids.New(), Provider: provider, Request: request})
	return ids.New(), nil
}

func (f *fakeStore) Recommit(ctx context.Context, _ int, process func(context.Context, store.Recommit) (json.RawMessage, error)) (store.RecommitSummary, error) {
	var sum store.RecommitSummary
	for _, item := range f.items {
		sum.Processed++
		out, err := process(ctx, item)
		f.processed = append(f.processed, out)
		f.processErr = append(f.processErr, err)
		if err != nil {
			sum.Failed++
		} else {
			sum.Completed++
		}
	}
	return sum, nil
}

type recoveringProvider struct{ downProvider }

func (recoveringProvider) Calculate(_ context.Context, doc domain.Document) (domain.Result, error) {
	return domain.Result{Engine: "avalara", Lines: []domain.LineResult{{LineRef: doc.Lines[0].LineRef}}}, nil
}

func routerWith(s api.Store, providers map[string]domain.TaxProvider, l ledgerclient.Ledger) (http.Handler, *api.API) {
	a := api.New(fakeDB{}, providers, 100*time.Millisecond).WithStore(s)
	if l != nil {
		a.WithLedger(l)
	}
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func TestFallbackQueuesTheDocumentForRecommit(t *testing.T) {
	withProvider := mutated(t, `"party"`, `"provider":"avalara","party"`)
	fs := &fakeStore{}
	r, a := routerWith(fs, map[string]domain.TaxProvider{"avalara": downProvider{}}, nil)
	if rec := do(r, http.MethodPost, calculatePath, withProvider, testkit.TenantID(1), duty); rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if len(fs.queued) != 1 || fs.queued[0] != "avalara" {
		t.Fatalf("queued %v, want one avalara entry", fs.queued)
	}

	t.Run("recommit succeeds once the provider answers", func(t *testing.T) {
		healthy, _ := routerWith(fs, map[string]domain.TaxProvider{"avalara": recoveringProvider{}}, nil)
		rec := do(healthy, http.MethodPost, "/v1/tax/recommit:run", ``, testkit.TenantID(1), "tax.recommit.run")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"completed":1`) {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(string(fs.processed[len(fs.processed)-1]), `"engine":"avalara"`) {
			t.Fatalf("recommit result %s", fs.processed[len(fs.processed)-1])
		}
	})

	t.Run("recommit keeps the entry while the provider is still down", func(t *testing.T) {
		before := len(fs.processErr)
		if rec := do(r, http.MethodPost, "/v1/tax/recommit:run", ``, testkit.TenantID(1), "tax.recommit.run"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"failed":1`) {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		if err := fs.processErr[before]; err == nil || !strings.Contains(err.Error(), "still unavailable") {
			t.Fatalf("process error %v", err)
		}
	})

	t.Run("recommit without the provider configured fails the entry", func(t *testing.T) {
		before := len(fs.processErr)
		bare, _ := routerWith(fs, nil, nil)
		do(bare, http.MethodPost, "/v1/tax/recommit:run", ``, testkit.TenantID(1), "tax.recommit.run")
		if err := fs.processErr[before]; err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("process error %v", err)
		}
	})
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "tax_provider_fallbacks_total 1") {
		t.Errorf("metrics %s", metrics.Body.String())
	}
}

func TestFallbackStillAnswersWhenTheQueueFails(t *testing.T) {
	fs := &fakeStore{queueErr: errors.New("db down")}
	r, a := routerWith(fs, map[string]domain.TaxProvider{"avalara": downProvider{}}, nil)
	rec := do(r, http.MethodPost, calculatePath, mutated(t, `"party"`, `"provider":"avalara","party"`), testkit.TenantID(1), duty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"fallback":true`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "tax_recommit_queue_failures_total 1") {
		t.Errorf("queue failure not counted: %s", metrics.Body.String())
	}
}

func TestRecommitRejectsABadLimit(t *testing.T) {
	r, _ := routerWith(&fakeStore{}, nil, nil)
	for _, limit := range []string{"0", "51", "x"} {
		if rec := do(r, http.MethodPost, "/v1/tax/recommit:run?limit="+limit, ``, testkit.TenantID(1), "tax.recommit.run"); rec.Code != http.StatusBadRequest {
			t.Errorf("limit %s: got %d", limit, rec.Code)
		}
	}
}

func TestRecordTransactionsValidation(t *testing.T) {
	good := `{"id":"` + ids.New() + `","code":"VAT19","direction":"output","tax_date":"2026-01-10","amount":"19.00"}`
	tests := []struct {
		name string
		body string
		want int
	}{
		{"valid", `{"transactions":[` + good + `]}`, http.StatusCreated},
		{"empty list", `{"transactions":[]}`, http.StatusBadRequest},
		{"bad id", `{"transactions":[{"id":"x","code":"VAT19","direction":"output","tax_date":"2026-01-10","amount":"1"}]}`, http.StatusBadRequest},
		{"bad direction", `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"sideways","tax_date":"2026-01-10","amount":"1"}]}`, http.StatusBadRequest},
		{"code with separator", `{"transactions":[{"id":"` + ids.New() + `","code":"A|B","direction":"output","tax_date":"2026-01-10","amount":"1"}]}`, http.StatusBadRequest},
		{"missing date", `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"output","amount":"1"}]}`, http.StatusBadRequest},
		{"unknown field", `{"transactions":[],"x":1}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := routerWith(&fakeStore{}, nil, nil)
			if rec := do(r, http.MethodPost, "/v1/tax/transactions", tc.body, testkit.TenantID(1), "tax.transaction.record"); rec.Code != tc.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestSettleRequestValidationAndUnconfiguredDependencies(t *testing.T) {
	valid := `{"period_from":"2026-01-01","period_to":"2026-01-31"}`
	tests := []struct {
		name   string
		body   string
		ledger ledgerclient.Ledger
		want   int
	}{
		{"ledger not configured", valid, nil, http.StatusServiceUnavailable},
		{"inverted period", strings.Replace(valid, "2026-01-31", "2025-12-31", 1), ledgerclient.NewFake(), http.StatusBadRequest},
		{"request-supplied ledger id", `{"period_from":"2026-01-01","period_to":"2026-01-31","ledger_id":"` + ids.New() + `"}`, ledgerclient.NewFake(), http.StatusBadRequest},
		{"request-supplied accounts", `{"period_from":"2026-01-01","period_to":"2026-01-31","accounts":{"VAT19|output":"2200"}}`, ledgerclient.NewFake(), http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := routerWith(&fakeStore{}, nil, tc.ledger)
			if rec := do(r, http.MethodPost, "/v1/tax/settlements:run", tc.body, testkit.TenantID(1), "tax.settlement.run"); rec.Code != tc.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
	t.Run("store not configured", func(t *testing.T) {
		r, _ := newRouter(fakeDB{}, nil)
		if rec := do(r, http.MethodPost, "/v1/tax/settlements:run", valid, testkit.TenantID(1), "tax.settlement.run"); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d", rec.Code)
		}
	})
}

func TestLedgerConfigValidationAndStorage(t *testing.T) {
	valid := `{"ledger_id":"` + ids.New() + `","accounts":{"VAT19|output":"2200"}}`
	tests := []struct {
		name string
		body string
		want int
	}{
		{"valid", valid, http.StatusOK},
		{"bad ledger id", strings.Replace(valid, `"ledger_id":"`, `"ledger_id":"x`, 1), http.StatusBadRequest},
		{"no accounts", `{"ledger_id":"` + ids.New() + `","accounts":{}}`, http.StatusBadRequest},
		{"account key without direction", strings.Replace(valid, "VAT19|output", "VAT19", 1), http.StatusBadRequest},
		{"account number injection", strings.Replace(valid, `"2200"`, `"22 00;"`, 1), http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			r, _ := routerWith(fs, nil, nil)
			rec := do(r, http.MethodPut, "/v1/tax/ledger-config", tc.body, testkit.TenantID(1), "tax.ledgerconfig.manage")
			if rec.Code != tc.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if stored := fs.config != nil; stored != (tc.want == http.StatusOK) {
				t.Fatalf("config stored %v for status %d", stored, tc.want)
			}
		})
	}
	r, _ := routerWith(&fakeStore{}, nil, nil)
	if rec := do(r, http.MethodPut, "/v1/tax/ledger-config", valid, testkit.TenantID(1), "tax.settlement.run"); rec.Code != http.StatusForbidden {
		t.Fatalf("settlement duty reached the ledger config: %d", rec.Code)
	}
}

func TestSAFTExport(t *testing.T) {
	fs := &fakeStore{totals: []store.Total{{Code: "VAT19", Direction: domain.DirOutput, Amount: decimal.RequireFromString("19"), Count: 1}}}
	r, _ := routerWith(fs, nil, nil)
	query := "?period_from=2026-01-01&period_to=2026-01-31&company_name=Acme&registration_number=DE1&country=DE&currency=EUR"
	rec := do(r, http.MethodGet, "/v1/tax/saft"+query, ``, testkit.TenantID(1), "tax.saft.export")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/xml") ||
		!strings.Contains(rec.Body.String(), "<TaxCode>VAT19</TaxCode>") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	for name, q := range map[string]string{
		"no period":       "?company_name=Acme&registration_number=DE1&country=DE&currency=EUR",
		"bad currency":    strings.Replace(query, "EUR", "eur", 1),
		"missing company": strings.Replace(query, "company_name=Acme", "company_name=", 1),
	} {
		if rec := do(r, http.MethodGet, "/v1/tax/saft"+q, ``, testkit.TenantID(1), "tax.saft.export"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", name, rec.Code)
		}
	}
}

func TestSettleThroughTheRouterReconcilesToTheLedger(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	entity, ledgerID := ids.New(), ids.New()
	fake := ledgerclient.NewFake()
	for key, lines := range map[string][]ledgerclient.Line{
		"sale":     {{Account: "1200", Amount: decimal.RequireFromString("19.00")}, {Account: "2200", Credit: true, Amount: decimal.RequireFromString("19.00")}},
		"purchase": {{Account: "1400", Amount: decimal.RequireFromString("5.00")}, {Account: "2100", Credit: true, Amount: decimal.RequireFromString("5.00")}},
	} {
		if _, err := fake.Post(context.Background(), ledgerclient.Entry{LegalEntityID: entity, LedgerID: ledgerID, DocumentID: key, PostingDate: "2026-01-15", Currency: "EUR", IdempotencyKey: key, Lines: lines}); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := routerWith(store.New(app), nil, fake)
	var callMethod func(method, path, body string) *httptest.ResponseRecorder
	call := func(path, body string) *httptest.ResponseRecorder { return callMethod(http.MethodPost, path, body) }
	callMethod = func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(tenant.HeaderTenant, testkit.TenantID(7))
		req.Header.Set(tenant.HeaderLegalEntity, entity)
		req.Header.Set(tenant.HeaderDuties, "tax.transaction.record,tax.settlement.run,tax.ledgerconfig.manage")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	record := `{"transactions":[` +
		`{"id":"` + ids.New() + `","code":"VAT19","direction":"output","tax_date":"2026-01-15","amount":"19.00"},` +
		`{"id":"` + ids.New() + `","code":"VAT19","direction":"input","tax_date":"2026-01-15","amount":"5.00"}]}`
	if rec := call("/v1/tax/transactions", record); rec.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", rec.Code, rec.Body.String())
	}
	settle := `{"period_from":"2026-01-01","period_to":"2026-01-31"}`

	if rec := call("/v1/tax/settlements:run", settle); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "COM-0503") {
		t.Fatalf("settle without stored config: %d %s", rec.Code, rec.Body.String())
	}
	config := `{"ledger_id":"` + ledgerID + `","accounts":{"VAT19|output":"2200","VAT19|input":"1400"}}`
	if rec := callMethod(http.MethodPut, "/v1/tax/ledger-config", config); rec.Code != http.StatusOK {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}

	rec := call("/v1/tax/settlements:run", settle)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"net_amount":"14"`) {
		t.Fatalf("settle: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("/v1/tax/settlements:run", settle); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "TAX-0009") {
		t.Fatalf("second run: %d %s", rec.Code, rec.Body.String())
	}

	extra := `{"transactions":[{"id":"` + ids.New() + `","code":"VAT19","direction":"output","tax_date":"2026-01-20","amount":"0.01"}]}`
	if rec := call("/v1/tax/transactions", extra); rec.Code != http.StatusCreated {
		t.Fatalf("record extra: %d", rec.Code)
	}
	if rec := call("/v1/tax/settlements:run", settle); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "TAX-0008") {
		t.Fatalf("one cent off the GL: %d %s", rec.Code, rec.Body.String())
	} else if strings.Contains(rec.Body.String(), "19.0") {
		t.Fatalf("mismatch response leaks GL amounts: %s", rec.Body.String())
	}
}
