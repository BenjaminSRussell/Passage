package ledger_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
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
	if err != nil || claims.Act != "u1" || !claims.Principal().HasDuty("ledger.balance.read") || claims.Principal().HasDuty("ledger.journal.post") {
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
