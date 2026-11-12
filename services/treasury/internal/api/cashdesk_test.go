package api

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
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
	closeIn store.CloseDay
	// posted holds the journal lines in the posting tests: PostPending lines wait here until the ledger takes them.
	mu       sync.Mutex
	journal  []store.CashDeskEntry
	day      store.CashDeskDay
	diffPost bool
	updates  []store.UpdateCashDesk
	status   string
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, in)
	e := store.CashDeskEntry{ID: ids.New(), DeskID: in.DeskID, BusinessDate: "2026-11-02", LineNo: len(f.entries), Kind: in.Kind, Amount: in.Amount, CounterAccount: in.CounterAccount, PostingStatus: "not_posted"}
	if in.BankAccountID != "" {
		e.BankAccountID = &in.BankAccountID
	}
	if in.PostPending {
		e.PostingStatus = "pending"
	}
	f.journal = append(f.journal, e)
	return e, nil
}
func (f *fakeDesks) ListCashDeskDays(context.Context, string) ([]store.CashDeskDay, error) {
	return []store.CashDeskDay{}, nil
}
func (f *fakeDesks) ListCashDeskEntries(context.Context, string, string) ([]store.CashDeskEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.CashDeskEntry{}, f.journal...), nil
}
func (f *fakeDesks) CloseCashDeskDay(_ context.Context, id, date string, in store.CloseDay) (store.CashDeskDay, error) {
	f.closed, f.reason, f.closeIn = in.Counted, in.Reason, in
	f.day = store.CashDeskDay{DeskID: id, BusinessDate: date, Status: "closed", DifferencePosting: "not_posted"}
	if f.desk.DualControl {
		f.day.Status = "closing"
	} else if in.PostDifference {
		f.day.DifferencePosting = "pending"
		diff := "-0.50"
		f.day.Difference = &diff
	}
	return f.day, nil
}
func (f *fakeDesks) ApproveCashDeskDay(_ context.Context, id, date string, post bool) (store.CashDeskDay, error) {
	f.day.Status = "closed"
	return f.day, nil
}
func (f *fakeDesks) RejectCashDeskDay(_ context.Context, id, date string) (store.CashDeskDay, error) {
	f.day.Status = "open"
	return f.day, nil
}
func (f *fakeDesks) GetCashDeskDay(context.Context, string, string) (store.CashDeskDay, error) {
	return f.day, nil
}
func (f *fakeDesks) UpdateCashDesk(_ context.Context, id string, version int, in store.UpdateCashDesk) (store.CashDesk, error) {
	f.updates = append(f.updates, in)
	return f.desk, nil
}
func (f *fakeDesks) SetCashDeskStatus(_ context.Context, id string, version int, status string) (store.CashDesk, error) {
	f.status = status
	d := f.desk
	d.Status = status
	return d, nil
}
func (f *fakeDesks) PendingDeskPostings(context.Context, string, string) (store.DeskPostings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := store.DeskPostings{Desk: f.desk, Day: f.day}
	for _, e := range f.journal {
		if e.PostingStatus == "pending" {
			out.Entries = append(out.Entries, e)
		}
	}
	if f.day.DifferencePosting == "pending" {
		out.Difference = f.day.Difference
	}
	return out, nil
}
func (f *fakeDesks) MarkDeskEntryPosted(_ context.Context, _, entryID, ledgerEntryID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.journal {
		if f.journal[i].ID == entryID {
			f.journal[i].PostingStatus, f.journal[i].LedgerEntryID = "posted", ledgerEntryID
		}
	}
	return nil
}
func (f *fakeDesks) MarkDeskDifferencePosted(_ context.Context, _, _, ledgerEntryID string) error {
	f.day.DifferencePosting, f.day.DifferenceLedgerEntryID = "posted", ledgerEntryID
	return nil
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
