package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

const reconPath = "/v1/inventory/reconciliations"

// Reconcile records the input and answers a run whose inventory value is 200
// and whose GL-derived ledger balance is 150, or the override when one is sent.
func (f *fakeStore) Reconcile(_ context.Context, in store.ReconcileInput) (domain.Reconciliation, error) {
	f.reconIn = in
	led, source := decimal.NewFromInt(150), domain.LedgerSourceJournals
	if in.LedgerBalance != nil {
		led, source = *in.LedgerBalance, domain.LedgerSourceOverride
	}
	cmp, err := domain.CompareBalances(decimal.NewFromInt(200), led, in.Currency)
	if err != nil {
		return domain.Reconciliation{}, err
	}
	return domain.Reconciliation{ID: ids.New(), LegalEntityID: in.LegalEntityID, Account: in.Account, Currency: in.Currency,
		RunDate: in.RunDate, InventoryValue: cmp.InventoryValue, LedgerBalance: cmp.LedgerBalance, Diff: cmp.Diff,
		Status: cmp.Status, LedgerSource: source, NegativeRows: 2, CreatedAt: time.Now()}, nil
}

func (f *fakeStore) GetReconciliation(_ context.Context, id string) (domain.Reconciliation, error) {
	for _, r := range f.reconRuns {
		if r.ID == id {
			return r, nil
		}
	}
	return domain.Reconciliation{}, httpx.NewProblem(http.StatusNotFound, domain.CodeReconciliationNotFound, "reconciliation run not found")
}

func (f *fakeStore) ListReconciliations(_ context.Context, filter store.ReconciliationFilter) ([]domain.Reconciliation, error) {
	f.reconFilter = filter
	return f.reconRuns, nil
}

type reconBody struct {
	ID             string `json:"id"`
	Account        string `json:"account"`
	RunDate        string `json:"runDate"`
	InventoryValue string `json:"inventoryValue"`
	LedgerBalance  string `json:"ledgerBalance"`
	Diff           string `json:"diff"`
	Status         string `json:"status"`
	LedgerSource   string `json:"ledgerSource"`
	NegativeRows   int    `json:"negativeRows"`
}

func TestReconcileRunsAgainstTheInventoryAccountToday(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	entity := ids.New()
	rec := do(t, h, http.MethodPost, reconPath, `{"legalEntityId":"`+entity+`","currency":"USD"}`, []string{"inventory.admin"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out reconBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.ReconciliationDifference || out.Diff != "50" || out.LedgerSource != domain.LedgerSourceJournals || out.NegativeRows != 2 {
		t.Fatalf("unexpected run %+v", out)
	}
	if fs.reconIn.LedgerBalance != nil || fs.reconIn.LegalEntityID != entity || fs.reconIn.Currency != "USD" {
		t.Fatalf("store input %+v", fs.reconIn)
	}
	if fs.reconIn.Account != "1300" || out.Account != "1300" {
		t.Fatalf("account %q / %q, want the configured inventory account 1300", fs.reconIn.Account, out.Account)
	}
	if today := time.Now().UTC().Format("2006-01-02"); out.RunDate != today {
		t.Fatalf("run date %s, want today %s", out.RunDate, today)
	}
}

func TestReconcilePassesTheLedgerOverride(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodPost, reconPath, `{"legalEntityId":"`+ids.New()+`","currency":"USD","ledgerBalance":"200.00"}`, []string{"inventory.admin"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.reconIn.LedgerBalance == nil || !fs.reconIn.LedgerBalance.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("ledger override not passed: %v", fs.reconIn.LedgerBalance)
	}
	var out reconBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.ReconciliationMatched || out.Diff != "0" || out.LedgerSource != domain.LedgerSourceOverride {
		t.Fatalf("unexpected run %+v", out)
	}
}

func TestReconcileValidation(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodPost, reconPath, `{"legalEntityId":"nope","currency":"usd","ledgerBalance":"1.0000001"}`, []string{"inventory.admin"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	p := decodeProblem(t, rec)
	for _, f := range []string{"legalEntityId", "currency", "ledgerBalance"} {
		if _, ok := p.Errors[f]; !ok {
			t.Errorf("missing field error for %s: %v", f, p.Errors)
		}
	}
	if rec := do(t, h, http.MethodPost, reconPath, `{"legalEntityId":"`+ids.New()+`","currency":"USD","extra":1}`, []string{"inventory.admin"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: status %d, want 400", rec.Code)
	}
	if fs.reconIn.LegalEntityID != "" {
		t.Fatal("an invalid request reached the store")
	}
}

func TestReconcileNeedsTheInventoryAccount(t *testing.T) {
	fs := &fakeStore{}
	r := chi.NewRouter()
	api.New(fs, store.PostParams{}).Register(r)
	rec := do(t, r, http.MethodPost, reconPath, `{"legalEntityId":"`+ids.New()+`","currency":"USD"}`, []string{"inventory.admin"}, nil)
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != store.CodeAccountMissing {
		t.Fatalf("status %d body %s, want 422 %s", rec.Code, rec.Body.String(), store.CodeAccountMissing)
	}
	if fs.reconIn.LegalEntityID != "" {
		t.Fatal("the store was called without an account")
	}
}

func TestReconciliationDuties(t *testing.T) {
	h := newServer(t, &fakeStore{})
	body := `{"legalEntityId":"` + ids.New() + `","currency":"USD"}`
	if rec := do(t, h, http.MethodPost, reconPath, body, []string{"inventory.read"}, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("run with inventory.read: status %d, want 403", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, reconPath, "", []string{"inventory.journal.write"}, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("list without inventory.read: status %d, want 403", rec.Code)
	}
}

func TestListAndGetReconciliations(t *testing.T) {
	run := domain.Reconciliation{ID: ids.New(), LegalEntityID: ids.New(), Account: "1300", Currency: "USD",
		RunDate: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), InventoryValue: decimal.RequireFromString("350.00"),
		LedgerBalance: decimal.RequireFromString("300.00"), Diff: decimal.RequireFromString("50.00"),
		Status: domain.ReconciliationDifference, LedgerSource: domain.LedgerSourceOverride}
	fs := &fakeStore{reconRuns: []domain.Reconciliation{run}}
	h := newServer(t, fs)

	rec := do(t, h, http.MethodGet, reconPath+"?legalEntityId="+run.LegalEntityID+"&status=DIFFERENCE", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d body %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []reconBody `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Diff != "50" || list.Items[0].RunDate != "2026-10-26" {
		t.Fatalf("list items %+v", list.Items)
	}
	if fs.reconFilter.LegalEntityID != run.LegalEntityID || fs.reconFilter.Status != domain.ReconciliationDifference {
		t.Fatalf("filter %+v", fs.reconFilter)
	}

	rec = do(t, h, http.MethodGet, reconPath+"?legalEntityId=x&status=OPEN", "", []string{"inventory.read"}, nil)
	if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors["legalEntityId"] == "" || p.Errors["status"] == "" {
		t.Fatalf("bad filters: status %d errors %v", rec.Code, p.Errors)
	}

	rec = do(t, h, http.MethodGet, reconPath+"/"+run.ID, "", []string{"inventory.read"}, nil)
	var one reconBody
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || rec.Code != http.StatusOK || one.ID != run.ID || one.LedgerBalance != "300" {
		t.Fatalf("get: status %d body %s", rec.Code, rec.Body.String())
	}
	for _, id := range []string{"nope", ids.New()} {
		rec = do(t, h, http.MethodGet, reconPath+"/"+id, "", []string{"inventory.read"}, nil)
		if rec.Code != http.StatusNotFound || decodeProblem(t, rec).Code != domain.CodeReconciliationNotFound {
			t.Fatalf("get %s: status %d body %s", id, rec.Code, rec.Body.String())
		}
	}
}
