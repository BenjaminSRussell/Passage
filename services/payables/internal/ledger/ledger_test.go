package ledger_test

import (
	"context"
	"crypto/ed25519"
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
	bal, err := ledger.New(srv.URL+"/", nil).AccountBalance(ctx(), "", "led-1", "2000", "2027-06")
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
	bal, err := ledger.New(srv.URL, nil).AccountBalance(ctx(), "", "l", "2000", "2027-06")
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
			if _, err := ledger.New(srv.URL, nil).AccountBalance(tc.ctx, "", "l", "2000", "2027-06"); err == nil {
				t.Fatal("want error")
			}
		})
	}
	if _, err := ledger.New("http://127.0.0.1:1", nil).AccountBalance(ctx(), "", "l", "2000", "2027-06"); err == nil {
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
	got, err := ledger.New(srv.URL, nil).LegalEntity(ctx(), "", "led-1")
	if err != nil || got != "le-9" || gotPath != "/v1/ledger/ledgers/led-1" {
		t.Fatalf("got %q err=%v path=%s", got, err, gotPath)
	}
}

func TestStatusErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	c := ledger.New(srv.URL, nil)
	var se *ledger.StatusError
	if _, err := c.AccountBalance(ctx(), "", "l", "2000", "2027-06"); !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("balance err = %v", err)
	}
	se = nil
	if _, err := c.LegalEntity(ctx(), "", "l"); !errors.As(err, &se) || se.Status != http.StatusForbidden {
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
		_, err := ledger.New(redirector.URL, client).LegalEntity(ctx(), "", "ledger-1")
		var se *ledger.StatusError
		if !errors.As(err, &se) || se.Status != http.StatusFound || leaked {
			t.Fatalf("%s client: err=%v leaked=%v, want the 302 returned without following it", name, err, leaked)
		}
	}
}

func postClient(url string) *ledger.Client {
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("payables", priv)
	return ledger.New(url, nil).WithSigner(signer)
}

func postCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: "u1"})
}

func postEntry() ledger.Entry {
	return ledger.Entry{LegalEntityID: testkit.TenantID(9), LedgerID: "led-1", DocumentID: "run-1", PostingDate: "2026-01-10", Currency: "USD", IdempotencyKey: "payables:payment-run:run-1", SourceType: "payment_run",
		Lines: []domain.RunJournalLine{{Account: "2000", Amount: decimal.RequireFromString("150"), Text: "vendor v1"}, {Account: "1010", Credit: true, Amount: decimal.RequireFromString("150")}}}
}

func TestPost_SendsABalancedEntryWithTheIdempotencyKeyAndAServiceToken(t *testing.T) {
	var key, tenantHeader, path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, tenantHeader, path = r.Header.Get("Idempotency-Key"), r.Header.Get(tenant.HeaderServiceToken), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"entries":[{"id":"entry-9"}]}`))
	}))
	defer srv.Close()
	id, err := postClient(srv.URL).Post(postCtx(), postEntry())
	if err != nil || id != "entry-9" {
		t.Fatalf("id=%s err=%v", id, err)
	}
	if key != "payables:payment-run:run-1" || tenantHeader == "" || path != "/v1/ledger/journal-entries" {
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
		_, err := postClient(srv.URL).Post(postCtx(), postEntry())
		srv.Close()
		var se *ledger.StatusError
		if err == nil || (tc.status >= 400) != errors.As(err, &se) || (se != nil && se.Status != tc.status) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := postClient("http://127.0.0.1:1").Post(postCtx(), postEntry()); err == nil {
		t.Error("an unreachable ledger must fail")
	}
	if _, err := postClient("http://ledger").Post(context.Background(), postEntry()); err == nil {
		t.Error("a call without a principal must fail")
	}
}

func TestAccountBalance_SignedSendsPayablesServiceTokenWithoutUserDuties(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("payables", priv)
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"currency":"USD","rows":[]}`))
	}))
	defer srv.Close()
	principal := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: "u1"}
	if _, err := ledger.New(srv.URL, nil).WithSigner(signer).AccountBalance(tenant.With(context.Background(), principal), testkit.TenantID(9), "led-1", "2000", "2027-06"); err != nil {
		t.Fatal(err)
	}
	if got.Get(tenant.HeaderDuties) != "" {
		t.Fatalf("user duties sent alongside the token: %v", got)
	}
	claims, err := tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"payables": pub}).Verify(got.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Act != "u1" || !claims.Principal().HasDuty("ledger.balance.read") || claims.Principal().HasDuty("ledger.entry.post") || claims.Principal().HasDuty("ledger.entry.lookup") || len(claims.Duties) != 1 {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

func TestAccountBalance_SignedWithoutALegalEntityFailsBeforeSending(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("payables", priv)
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sent = true }))
	defer srv.Close()
	if _, err := ledger.New(srv.URL, nil).WithSigner(signer).AccountBalance(ctx(), "", "led-1", "2000", "2027-06"); err == nil || sent {
		t.Fatalf("err %v sent %v", err, sent)
	}
}

func TestAccountBalance_SignedForMultiEntityAndAllScopeUsersUsesTheExplicitEntity(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("payables", priv)
	verifier := tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"payables": pub})
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get(tenant.HeaderServiceToken)
		_, _ = w.Write([]byte(`{"currency":"USD","rows":[]}`))
	}))
	defer srv.Close()
	entityA, entityB := testkit.TenantID(8), testkit.TenantID(9)
	multi := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityA + "," + entityB, UserID: "u1"}
	allScope := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u1", Duties: map[string]struct{}{tenant.DutyAllScope: {}}}
	client := ledger.New(srv.URL, nil).WithSigner(signer)
	for name, p := range map[string]tenant.Principal{"multi-entity": multi, "all-scope": allScope} {
		token = ""
		if _, err := client.AccountBalance(tenant.With(context.Background(), p), entityB, "led-1", "2000", "2027-06"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if claims, err := verifier.Verify(token); err != nil || claims.Le != entityB {
			t.Fatalf("%s: claims %+v err %v", name, claims, err)
		}
	}
	token = ""
	outside := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityA, UserID: "u1"})
	if _, err := client.LegalEntity(outside, entityB, "led-1"); err == nil || token != "" {
		t.Fatalf("an entity outside the user's scope must not be signed: err %v token %q", err, token)
	}
}

func TestPost_SignedSendsOnlyThePostingDutyForTheRunsLegalEntity(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("payables", priv)
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"entries":[{"id":"entry-1"}]}`))
	}))
	defer srv.Close()
	le := testkit.TenantID(9)
	entry := postEntry()
	entry.LegalEntityID = le
	principal := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "erin"}
	if _, err := ledger.New(srv.URL, nil).WithSigner(signer).Post(tenant.With(context.Background(), principal), entry); err != nil {
		t.Fatal(err)
	}
	if got.Get(tenant.HeaderDuties) != "" || got.Get("Idempotency-Key") != "payables:payment-run:run-1" {
		t.Fatalf("headers %v", got)
	}
	claims, err := tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"payables": pub}).Verify(got.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Act != "erin" || claims.Le != le || len(claims.Duties) != 1 || claims.Duties[0] != "ledger.entry.post" {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

func TestPost_SignedRefusesAnAllScopeUserAndAnUnnamedEntity(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("payables", priv)
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sent = true }))
	defer srv.Close()
	client := ledger.New(srv.URL, nil).WithSigner(signer)
	entry := postEntry()
	entry.LegalEntityID = testkit.TenantID(9)
	allScope := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "erin", Duties: map[string]struct{}{tenant.DutyAllScope: {}}}
	if _, err := client.Post(tenant.With(context.Background(), allScope), entry); err == nil || sent {
		t.Fatalf("an all-scope user must not get a posting token: err %v sent %v", err, sent)
	}
	scoped := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(8) + "," + testkit.TenantID(9), UserID: "erin"}
	unnamed := postEntry()
	unnamed.LegalEntityID = ""
	if _, err := client.Post(tenant.With(context.Background(), scoped), unnamed); err == nil || sent {
		t.Fatalf("a multi-entity user needs the run's entity named: err %v sent %v", err, sent)
	}
}

func TestPost_InProgressConflictCarriesTheLedgerCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"LEDGER_IDEMPOTENCY_IN_PROGRESS"}`))
	}))
	defer srv.Close()
	_, err := postClient(srv.URL).Post(postCtx(), postEntry())
	var se *ledger.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusConflict || se.Code != ledger.CodeIdempotencyInProgress {
		t.Fatalf("err = %v", err)
	}
}

func TestPost_RefusesToSendUnsigned(t *testing.T) {
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sent = true }))
	defer srv.Close()
	client := ledger.New(srv.URL, nil)
	if client.Signed() {
		t.Fatal("a client without a signer is not signed")
	}
	if _, err := client.Post(postCtx(), postEntry()); err == nil || sent {
		t.Fatalf("a journal must never go out with forwarded user headers: err %v sent %v", err, sent)
	}
}

func TestEntryForDocument_AsksTheLedgerForThatDocumentAndSignsOnlyTheLookupDuty(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("payables", priv)
	var got *http.Request
	body := `{"items":[{"entryId":"entry-7"},{"entryId":"entry-8"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	le := testkit.TenantID(9)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "erin"})
	client := ledger.New(srv.URL, nil).WithSigner(signer)
	id, err := client.EntryForDocument(ctx, le, "led-1", "doc-1")
	if err != nil || id != "entry-7" {
		t.Fatalf("id %q err %v", id, err)
	}
	if got.URL.Path != "/v1/ledger/journal-lines" || got.URL.Query().Get("ledger") != "led-1" || got.URL.Query().Get("documentId") != "doc-1" || got.URL.Query().Get("limit") != "1" {
		t.Fatalf("request %s", got.URL)
	}
	claims, err := tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"payables": pub}).Verify(got.Header.Get(tenant.HeaderServiceToken))
	if err != nil || len(claims.Duties) != 1 || claims.Duties[0] != "ledger.entry.lookup" {
		t.Fatalf("claims %+v err %v", claims, err)
	}
	body = `{"items":[]}`
	if id, err := client.EntryForDocument(ctx, le, "led-1", "doc-2"); err != nil || id != "" {
		t.Fatalf("no entry: id %q err %v", id, err)
	}
	body = `nope`
	if _, err := client.EntryForDocument(ctx, le, "led-1", "doc-3"); err == nil {
		t.Fatal("an unreadable answer must be an error")
	}
}
