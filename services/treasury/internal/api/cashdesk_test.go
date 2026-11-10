package api

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

type fakeDesks struct {
	desk    store.CashDesk
	entries []store.NewCashDeskEntry
	closed  decimal.Decimal
	reason  string
}

func (f *fakeDesks) CreateCashDesk(_ context.Context, in store.NewCashDesk, _ string) (store.CashDesk, error) {
	return store.CashDesk{ID: ids.New(), Code: in.Code, Currency: in.Currency, MaxBalance: in.MaxBalance, Status: "active", Version: 1}, nil
}
func (f *fakeDesks) ListCashDesks(context.Context, string) ([]store.CashDesk, error) {
	return []store.CashDesk{f.desk}, nil
}
func (f *fakeDesks) GetCashDesk(context.Context, string) (store.CashDesk, error) { return f.desk, nil }
func (f *fakeDesks) OpenCashDeskDay(_ context.Context, id, date string) (store.CashDeskDay, error) {
	return store.CashDeskDay{DeskID: id, BusinessDate: date, Status: "open"}, nil
}
func (f *fakeDesks) AddCashDeskEntry(_ context.Context, in store.NewCashDeskEntry, _ string) (store.CashDeskEntry, error) {
	f.entries = append(f.entries, in)
	return store.CashDeskEntry{ID: ids.New(), Kind: in.Kind, Amount: in.Amount}, nil
}
func (f *fakeDesks) ListCashDeskDays(context.Context, string) ([]store.CashDeskDay, error) {
	return []store.CashDeskDay{}, nil
}
func (f *fakeDesks) ListCashDeskEntries(context.Context, string, string) ([]store.CashDeskEntry, error) {
	return []store.CashDeskEntry{}, nil
}
func (f *fakeDesks) CloseCashDeskDay(_ context.Context, id, date string, counted decimal.Decimal, reason string) (store.CashDeskDay, error) {
	f.closed, f.reason = counted, reason
	return store.CashDeskDay{DeskID: id, BusinessDate: date, Status: "closed"}, nil
}

func deskServer(f *fakeDesks) http.Handler {
	r := httpx.NewRouter()
	New(newFakeStore()).WithCashDesks(f).Register(r)
	return r
}

func rawBody(s string) *bytes.Buffer { return bytes.NewBufferString(s) }

func TestCashDesk_RoutesValidateAndPassTheRulesToTheStore(t *testing.T) {
	f := &fakeDesks{desk: store.CashDesk{ID: ids.New(), Currency: "USD", Status: "active"}}
	h := deskServer(f)
	key := map[string]string{"Idempotency-Key": ids.New()}
	good := `{"legalEntityId":"` + testLE + `","code":"FRONT-1","name":"Front desk","currency":"USD","mainAccountId":"` + ids.New() + `","maxBalance":"1000.00"}`
	if rec := call(h, http.MethodPost, "/v1/treasury/cash-desks", rawBody(good), "application/json", key); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{
		"bad code":      strings.Replace(good, "FRONT-1", "front 1", 1),
		"too precise":   strings.Replace(good, "1000.00", "1000.001", 1),
		"no account":    strings.Replace(good, `"mainAccountId":"`, `"mainAccountId":"x`, 1),
		"zero maximum":  strings.Replace(good, "1000.00", "0", 1),
		"unknown field": strings.Replace(good, `"name"`, `"owner":"x","name"`, 1),
	} {
		if rec := call(h, http.MethodPost, "/v1/treasury/cash-desks", rawBody(body), "application/json", map[string]string{"Idempotency-Key": ids.New()}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if rec := call(h, http.MethodPost, "/v1/treasury/cash-desks", rawBody(good), "application/json", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("without Idempotency-Key: %d, want 400", rec.Code)
	}
	path := "/v1/treasury/cash-desks/" + f.desk.ID + "/entries"
	if rec := call(h, http.MethodPost, path, rawBody(`{"kind":"payment","amount":"12.50","description":" taxi "}`), "application/json", key); rec.Code != http.StatusCreated || len(f.entries) != 1 || f.entries[0].Description != "taxi" || f.entries[0].Amount != "12.5" {
		t.Fatalf("entry: %d %s %+v", rec.Code, rec.Body, f.entries)
	}
	for name, body := range map[string]string{"kind": `{"kind":"transfer","amount":"1","description":"x"}`, "cents": `{"kind":"receipt","amount":"1.005","description":"x"}`, "negative": `{"kind":"receipt","amount":"-1","description":"x"}`, "no description": `{"kind":"receipt","amount":"1","description":" "}`} {
		if rec := call(h, http.MethodPost, path, rawBody(body), "application/json", map[string]string{"Idempotency-Key": ids.New()}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	closePath := "/v1/treasury/cash-desks/" + f.desk.ID + "/days/2026-11-02:close"
	if rec := call(h, http.MethodPost, closePath, rawBody(`{"countedBalance":"379.00","reason":"a coin missing"}`), "application/json", nil); rec.Code != http.StatusOK || !f.closed.Equal(decimal.RequireFromString("379")) || f.reason != "a coin missing" {
		t.Fatalf("close: %d %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{"negative": `{"countedBalance":"-1"}`, "text": `{"countedBalance":"lots"}`, "cents": `{"countedBalance":"1.005"}`} {
		if rec := call(h, http.MethodPost, closePath, rawBody(body), "application/json", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("close %s: %d, want 400", name, rec.Code)
		}
	}
	if rec := call(h, http.MethodPost, "/v1/treasury/cash-desks/"+f.desk.ID+"/days/soon:close", rawBody(`{"countedBalance":"1"}`), "application/json", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad date: %d, want 400", rec.Code)
	}
}

func TestCashDesk_UnwiredAnswers503(t *testing.T) {
	r := httpx.NewRouter()
	New(newFakeStore()).Register(r)
	if rec := call(r, http.MethodGet, "/v1/treasury/cash-desks?legalEntityId="+testLE, nil, "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired: %d, want 503", rec.Code)
	}
}
