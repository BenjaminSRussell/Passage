package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/ledger"
)

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u1", Duties: map[string]struct{}{"ledger.balance.read": {}}})
}

func TestAccountBalance(t *testing.T) {
	var gotPath, gotQuery, gotTenant, gotDuties string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotTenant, gotDuties = r.URL.Path, r.URL.RawQuery, r.Header.Get(tenant.HeaderTenant), r.Header.Get(tenant.HeaderDuties)
		_, _ = w.Write([]byte(`{"currency":"USD","rows":[{"account":"2000","debit":"10.00","credit":"110.50"},{"account":"2001","debit":"1.00","credit":"2.00"}]}`))
	}))
	defer srv.Close()
	bal, err := ledger.New(srv.URL+"/", nil).AccountBalance(ctx(), "led-1", "2000", "2027-06")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/ledger/balances" || gotTenant != testkit.TenantID(1) || gotDuties != "ledger.balance.read" {
		t.Fatalf("path=%s tenant=%s duties=%s", gotPath, gotTenant, gotDuties)
	}
	for _, want := range []string{"ledger=led-1", "period_to=2027-06", "account_from=2000", "account_to=2000"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %s lacks %s", gotQuery, want)
		}
	}
	if bal.Currency != "USD" || bal.Credit.String() != "110.5" || bal.Debit.String() != "10" {
		t.Fatalf("balance = %+v (only the requested account counts)", bal)
	}
}

func TestAccountBalance_AccountWithNoRowsIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"currency":"USD","rows":[]}`)) }))
	defer srv.Close()
	bal, err := ledger.New(srv.URL, nil).AccountBalance(ctx(), "l", "2000", "2027-06")
	if err != nil || bal.Currency != "USD" || !bal.Credit.IsZero() || !bal.Debit.IsZero() {
		t.Fatalf("bal=%+v err=%v", bal, err)
	}
}

func TestAccountBalance_Failures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		ctx    context.Context
	}{
		{"forbidden", http.StatusForbidden, `{}`, ctx()},
		{"server error", http.StatusInternalServerError, `{}`, ctx()},
		{"not json", http.StatusOK, `nope`, ctx()},
		{"no currency", http.StatusOK, `{"rows":[]}`, ctx()},
		{"bad amount", http.StatusOK, `{"currency":"USD","rows":[{"account":"2000","debit":"x","credit":"1"}]}`, ctx()},
		{"no principal", http.StatusOK, `{"currency":"USD"}`, context.Background()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			if _, err := ledger.New(srv.URL, nil).AccountBalance(tc.ctx, "l", "2000", "2027-06"); err == nil {
				t.Fatal("want error")
			}
		})
	}
	if _, err := ledger.New("http://127.0.0.1:1", nil).AccountBalance(ctx(), "l", "2000", "2027-06"); err == nil {
		t.Fatal("unreachable ledger must error")
	}
}

func TestLegalEntity(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"led-1","legalEntityId":"le-9"}`))
	}))
	defer srv.Close()
	got, err := ledger.New(srv.URL, nil).LegalEntity(ctx(), "led-1")
	if err != nil || got != "le-9" || gotPath != "/v1/ledger/ledgers/led-1" {
		t.Fatalf("got %q err=%v path=%s", got, err, gotPath)
	}
}

func TestStatusErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	c := ledger.New(srv.URL, nil)
	var se *ledger.StatusError
	if _, err := c.AccountBalance(ctx(), "l", "2000", "2027-06"); !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("balance err = %v", err)
	}
	se = nil
	if _, err := c.LegalEntity(ctx(), "l"); !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("legal entity err = %v", err)
	}
}

func TestLedgerClient_DoesNotFollowRedirectsWithTenantHeaders(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	for name, client := range map[string]*http.Client{"default": nil, "custom": {}} {
		_, err := ledger.New(redirector.URL, client).LegalEntity(ctx(), "ledger-1")
		var se *ledger.StatusError
		if !errors.As(err, &se) || se.Status != http.StatusFound || leaked {
			t.Fatalf("%s client: err=%v leaked=%v, want the 302 returned without following it", name, err, leaked)
		}
	}
}

func postEntry() ledger.Entry {
	return ledger.Entry{LedgerID: "led-1", DocumentID: "run-1", PostingDate: "2026-01-10", Currency: "USD", IdempotencyKey: "payables:payment-run:run-1", SourceType: "payment_run",
		Lines: []domain.RunJournalLine{{Account: "2000", Amount: decimal.RequireFromString("150"), Text: "vendor v1"}, {Account: "1010", Credit: true, Amount: decimal.RequireFromString("150")}}}
}

func TestPost_SendsABalancedEntryWithIdempotencyKeyAndForwardedIdentity(t *testing.T) {
	var key, tenantHeader, path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, tenantHeader, path = r.Header.Get("Idempotency-Key"), r.Header.Get(tenant.HeaderTenant), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"entries":[{"id":"entry-9"}]}`))
	}))
	defer srv.Close()
	id, err := ledger.New(srv.URL, nil).Post(ctx(), postEntry())
	if err != nil || id != "entry-9" {
		t.Fatalf("id=%s err=%v", id, err)
	}
	if key != "payables:payment-run:run-1" || tenantHeader != testkit.TenantID(1) || path != "/v1/ledger/journal-entries" {
		t.Fatalf("key=%s tenant=%s path=%s", key, tenantHeader, path)
	}
	lines := body["lines"].([]any)
	first, second := lines[0].(map[string]any), lines[1].(map[string]any)
	if body["ledgerId"] != "led-1" || body["documentId"] != "run-1" || first["debitCredit"] != "D" || second["debitCredit"] != "C" ||
		first["amount"].(map[string]any)["value"] != "150.00" || body["source"].(map[string]any)["system"] != "payables" {
		t.Fatalf("body = %v", body)
	}
}

func TestPost_Failures(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"rejected":         {http.StatusUnprocessableEntity, `{}`},
		"server error":     {http.StatusInternalServerError, `{}`},
		"no entry id":      {http.StatusCreated, `{"entries":[]}`},
		"unreadable reply": {http.StatusOK, `not json`},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := ledger.New(srv.URL, nil).Post(ctx(), postEntry())
		srv.Close()
		var se *ledger.StatusError
		if err == nil || (tc.status >= 400) != errors.As(err, &se) || (se != nil && se.Status != tc.status) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := ledger.New("http://127.0.0.1:1", nil).Post(ctx(), postEntry()); err == nil {
		t.Error("an unreachable ledger must fail")
	}
	if _, err := ledger.New("http://ledger", nil).Post(context.Background(), postEntry()); err == nil {
		t.Error("a call without a principal must fail")
	}
}
