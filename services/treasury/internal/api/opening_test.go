package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

type fakeOpenings struct{ got store.OpeningImport }

func (f *fakeOpenings) ImportOpeningBalances(_ context.Context, in store.OpeningImport) (store.OpeningBatch, bool, error) {
	f.got = in
	return store.OpeningBatch{LegalEntityID: in.LegalEntityID, BatchKey: in.BatchKey, AsOf: in.AsOf, ItemCount: len(in.Items), TotalsMinor: map[string]int64{"USD": 5}}, false, nil
}

func (f *fakeOpenings) ListOpeningBatches(context.Context, string, string, int) ([]store.OpeningBatch, error) {
	return []store.OpeningBatch{}, nil
}

func TestOpeningBalances_RoutesValidateRowsAndNeedTheStore(t *testing.T) {
	path := "/v1/treasury/opening-balances"
	off := httpx.NewRouter()
	New(newFakeStore()).Register(off)
	if rec := call(off, http.MethodGet, path+"?legal_entity="+testLE, nil, "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("not enabled: %d", rec.Code)
	}
	f := &fakeOpenings{}
	h := httpx.NewRouter()
	New(newFakeStore()).WithOpeningBalances(f).Register(h)
	item := `{"reference":"B-1","bankAccountId":"` + ids.New() + `","currency":"USD","balanceMinor":5}`
	good := `{"legalEntityId":"` + testLE + `","batchKey":"k","asOf":"2026-12-31","items":[` + item + `]}`
	if rec := call(h, http.MethodPost, path, rawBody(good), "application/json", nil); rec.Code != http.StatusCreated || f.got.BatchKey != "k" || len(f.got.Items) != 1 {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	bad := strings.Replace(good, `"currency":"USD"`, `"currency":"usd"`, 1)
	if rec := call(h, http.MethodPost, path, rawBody(bad), "application/json", nil); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "items[1].currency") {
		t.Fatalf("bad row: %d %s", rec.Code, rec.Body)
	}
	dup := strings.Replace(good, "]}", ","+item+"]}", 1)
	if rec := call(h, http.MethodPost, path, rawBody(dup), "application/json", nil); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "items[2].reference") {
		t.Fatalf("duplicate reference: %d %s", rec.Code, rec.Body)
	}
	same := strings.Replace(dup, `"reference":"B-1","bankAccountId"`, `"reference":"B-2","bankAccountId"`, -1)
	same = strings.Replace(same, `"reference":"B-2","bankAccountId"`, `"reference":"B-1","bankAccountId"`, 1)
	if rec := call(h, http.MethodPost, path, rawBody(same), "application/json", nil); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "items[2].bankAccountId") {
		t.Fatalf("one account twice: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, http.MethodPost, path, rawBody(strings.Replace(good, "2026-12-31", "31.12.2026", 1)), "application/json", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad date: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, path+"?legal_entity=x", nil, "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad entity: %d", rec.Code)
	}
}
