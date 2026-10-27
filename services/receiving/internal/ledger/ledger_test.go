package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const tenantID = "00000000-0000-0000-0000-000000000001"

func testEntry() domain.JournalEntry {
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return domain.JournalEntry{
		IdempotencyKey: "post:receiving:productreceipt:r-1:receipt",
		LegalEntityID:  "le-1",
		PostingDate:    day,
		DocumentDate:   day,
		Source:         domain.DocumentSource{Context: "receiving", DocumentType: "productreceipt", DocumentID: "r-1", Purpose: "receipt"},
		RateType:       "spot",
		Lines: []domain.JournalLine{
			{Account: "1400", Amount: money.Must("12.5", "USD")},
			{Account: "2100", Amount: money.Must("-12.5", "USD")},
		},
	}
}

func testCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
}

func serve(t *testing.T, status int, body string) (*Client, *http.Request, *[]byte) {
	t.Helper()
	var got http.Request
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(context.Background())
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, nil), &got, &raw
}

func TestPost_SendsHeadersAndBodyAndReturnsID(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		client, got, raw := serve(t, status, `{"journalEntryId":"je-9"}`)
		id, err := client.Post(testCtx(), testEntry())
		if err != nil || id != "je-9" {
			t.Fatalf("status %d: id %q err %v", status, id, err)
		}
		if got.URL.Path != domain.LedgerPostPath || got.Method != http.MethodPost {
			t.Fatalf("request %s %s", got.Method, got.URL.Path)
		}
		if got.Header.Get("Idempotency-Key") != "post:receiving:productreceipt:r-1:receipt" || got.Header.Get("X-Passage-Tenant") != tenantID {
			t.Fatalf("headers %v", got.Header)
		}
		var body requestBody
		if err := json.Unmarshal(*raw, &body); err != nil {
			t.Fatal(err)
		}
		if body.Currency != "USD" || body.PostingDate != "2026-09-01" || len(body.Lines) != 2 {
			t.Fatalf("body %+v", body)
		}
		if body.Lines[0].Debit == nil || body.Lines[0].Debit.Amount != "12.50" || body.Lines[0].Credit != nil {
			t.Fatalf("line 1 %+v", body.Lines[0])
		}
		if body.Lines[1].Credit == nil || body.Lines[1].Credit.Amount != "12.50" || body.Lines[1].Debit != nil {
			t.Fatalf("line 2 %+v", body.Lines[1])
		}
		if strings.Contains(string(*raw), "post:receiving") {
			t.Fatal("idempotency key must travel in the header only")
		}
	}
}

func TestPost_Failures(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantPeriod bool
	}{
		{"period closed", http.StatusUnprocessableEntity, `{"code":"period_closed"}`, true},
		{"other 422", http.StatusUnprocessableEntity, `{"code":"unbalanced_entry"}`, false},
		{"422 not json", http.StatusUnprocessableEntity, `nope`, false},
		{"server error", http.StatusInternalServerError, `{"code":"period_closed"}`, false},
		{"unavailable", http.StatusServiceUnavailable, ``, false},
		{"empty 2xx", http.StatusCreated, ``, false},
		{"non-json 2xx", http.StatusCreated, `<html>`, false},
		{"2xx without id", http.StatusCreated, `{"journalEntryId":""}`, false},
		{"2xx wrong type", http.StatusCreated, `{"journalEntryId":{"a":1}}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := serve(t, tc.status, tc.body)
			id, err := client.Post(testCtx(), testEntry())
			if err == nil || id != "" {
				t.Fatalf("want error and no id, got %q %v", id, err)
			}
			if got := errors.Is(err, domain.ErrPeriodClosed); got != tc.wantPeriod {
				t.Fatalf("period error = %v, want %v (%v)", got, tc.wantPeriod, err)
			}
		})
	}
}

func TestPost_TimeoutIsNotPeriodClosed(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	client := New(srv.URL, &http.Client{Timeout: 50 * time.Millisecond})
	id, err := client.Post(testCtx(), testEntry())
	if err == nil || id != "" || errors.Is(err, domain.ErrPeriodClosed) {
		t.Fatalf("got %q %v", id, err)
	}
}

func TestNew_DefaultsToThreeSecondTimeout(t *testing.T) {
	if got := New("http://ledger", nil).http.Timeout; got != 3*time.Second {
		t.Fatalf("timeout %v", got)
	}
}

func TestPost_RejectsBeforeSending(t *testing.T) {
	mixed := testEntry()
	mixed.Lines[1].Amount = money.Must("-12.5", "EUR")
	noAccount := testEntry()
	noAccount.Lines[0].Account = ""
	tests := []struct {
		name  string
		ctx   context.Context
		entry domain.JournalEntry
	}{
		{"no principal", context.Background(), testEntry()},
		{"no lines", testCtx(), domain.JournalEntry{IdempotencyKey: "k"}},
		{"mixed currency", testCtx(), mixed},
		{"empty account", testCtx(), noAccount},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer srv.Close()
			if _, err := New(srv.URL, nil).Post(tc.ctx, tc.entry); err == nil || called {
				t.Fatalf("err %v called %v", err, called)
			}
		})
	}
}

func TestPathsMatchTheLedgerServiceRouteTable(t *testing.T) {
	src, err := os.ReadFile("../../../ledger/internal/api/api.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`r.Route("/api/v1/ledger"`, `Post("/journal-entries", a.postEntry)`, `Post("/journal-entries/{id}/reverse", a.reverseEntry)`} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("ledger route table no longer contains %s; update domain.LedgerPostPath and LedgerReversePathTmpl", want)
		}
	}
	if domain.LedgerPostPath != "/api/v1/ledger/journal-entries" || fmt.Sprintf(domain.LedgerReversePathTmpl, "E1") != "/api/v1/ledger/journal-entries/E1/reverse" {
		t.Fatalf("paths %s %s do not match the ledger routes", domain.LedgerPostPath, domain.LedgerReversePathTmpl)
	}
}

func TestPostReadsTheLedgerEntriesResponseAndClassifiesRejections(t *testing.T) {
	entry := domain.JournalEntry{IdempotencyKey: "k", LegalEntityID: "le", Lines: []domain.JournalLine{{Account: "1400", Amount: money.Must("5.00", "USD")}, {Account: "2100", Amount: money.Must("-5.00", "USD")}}}
	tests := []struct {
		name     string
		status   int
		body     string
		wantID   string
		rejected bool
		conflict bool
	}{
		{"entries array", http.StatusCreated, `{"entries":[{"id":"E7"}]}`, "E7", false, false},
		{"validation rejection is definite", http.StatusBadRequest, `{"code":"COM-0001"}`, "", true, false},
		{"forbidden is definite", http.StatusForbidden, `{}`, "", true, false},
		{"server error is unknown", http.StatusInternalServerError, `{}`, "", false, false},
		{"gateway timeout is unknown", http.StatusGatewayTimeout, `{}`, "", false, false},
		{"request timeout is unknown", http.StatusRequestTimeout, `{}`, "", false, false},
		{"idempotency conflict", http.StatusConflict, `{"code":"LEDGER_IDEMPOTENCY_CONFLICT"}`, "", false, true},
		{"in progress is unknown", http.StatusConflict, `{"code":"LEDGER_IDEMPOTENCY_IN_PROGRESS"}`, "", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := serve(t, tc.status, tc.body)
			id, err := client.Post(testCtx(), entry)
			if errors.Is(err, domain.ErrLedgerRejected) != tc.rejected || errors.Is(err, domain.ErrLedgerConflict) != tc.conflict || id != tc.wantID {
				t.Fatalf("id=%q err=%v, want id %q rejected=%v conflict=%v", id, err, tc.wantID, tc.rejected, tc.conflict)
			}
		})
	}
}

func TestReverseCallsTheReverseRouteWithItsOwnKey(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	if err := New(srv.URL, nil).Reverse(testCtx(), "E1", "k:reverse"); err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/api/v1/ledger/journal-entries/E1/reverse" || got.Header.Get("Idempotency-Key") != "k:reverse" {
		t.Fatalf("reverse sent %s with key %q", got.URL.Path, got.Header.Get("Idempotency-Key"))
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(failing.Close)
	if err := New(failing.URL, nil).Reverse(testCtx(), "E1", "k"); err == nil {
		t.Fatal("a failed reversal must surface")
	}
}
