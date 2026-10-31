package party_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/party"
)

const (
	entity = "00000000-0000-0000-0000-0000000000a1"
	vendor = "00000000-0000-0000-0000-0000000000c1"
)

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entity, UserID: "u1", Duties: map[string]struct{}{"payment-run.execute": {}}})
}

func signed(t *testing.T) (*tenant.ServiceSigner, *tenant.ServiceVerifier) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("payables", priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, tenant.NewServiceVerifier("party", map[string]ed25519.PublicKey{"payables": pub})
}

func TestBankAccounts_RevealsWithOnlyTheRevealDutyAndNeverForwardsTheUser(t *testing.T) {
	signer, verifier := signed(t)
	var got http.Header
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotPath = r.Header.Clone(), r.URL.Path
		_, _ = w.Write([]byte(`{"bankAccounts":[{"id":"a1","accountHolder":" Acme ","bankCountry":"de","currencyCode":"EUR","accountNumber":"DE89370400440532013000","routingNumber":""},{"id":"a2","accountHolder":"Acme","bankCountry":"US","currencyCode":"USD","accountNumber":"000111222333","routingNumber":"021000021"}]}`))
	}))
	defer srv.Close()

	accounts, err := party.New(srv.URL+"/", nil).WithSigner(signer).BankAccounts(ctx(), entity, vendor)

	if err != nil || len(accounts) != 2 || gotPath != "/v1/party/vendors/"+vendor+"/bank-accounts:reveal" {
		t.Fatalf("accounts %+v err %v path %s", accounts, err, gotPath)
	}
	for _, h := range []string{tenant.HeaderTenant, tenant.HeaderLegalEntity, tenant.HeaderUser, tenant.HeaderDuties} {
		if got.Get(h) != "" {
			t.Errorf("%s must not be sent with a service token", h)
		}
	}
	claims, err := verifier.Verify(got.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Aud != "party" || claims.Act != "u1" || claims.Le != entity || len(claims.Duties) != 1 || claims.Duties[0] != "party.vendor_bank.reveal" {
		t.Fatalf("claims %+v err %v", claims, err)
	}
	iban, us := accounts[0].Creditor(), accounts[1].Creditor()
	if iban.IBAN != "DE89370400440532013000" || iban.OtherID != "" || iban.Name != "Acme" || iban.Address.Country != "DE" {
		t.Errorf("iban creditor = %+v", iban)
	}
	if us.IBAN != "" || us.OtherID != "000111222333" || us.RoutingNumber != "021000021" {
		t.Errorf("us creditor = %+v", us)
	}
}

func TestBankAccounts_Failures(t *testing.T) {
	signer, _ := signed(t)
	tests := map[string]struct {
		status int
		body   string
		check  func(error) bool
	}{
		"not found":    {http.StatusNotFound, "", func(err error) bool { return errors.Is(err, party.ErrVendorNotFound) }},
		"forbidden":    {http.StatusForbidden, "", func(err error) bool { var se *party.StatusError; return errors.As(err, &se) && se.Status == 403 }},
		"server error": {http.StatusInternalServerError, "", func(err error) bool { var se *party.StatusError; return errors.As(err, &se) && se.Status == 500 }},
		"bad body":     {http.StatusOK, "not json", func(err error) bool { return err != nil }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			if _, err := party.New(srv.URL, nil).WithSigner(signer).BankAccounts(ctx(), entity, vendor); !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestBankAccounts_NeedsASignerAndNeverFollowsRedirects(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirector.Close()
	if _, err := party.New(redirector.URL, nil).BankAccounts(ctx(), entity, vendor); !errors.Is(err, party.ErrUnsigned) || leaked {
		t.Fatalf("unsigned err = %v leaked=%v: user headers are never forwarded for a reveal", err, leaked)
	}
	signer, _ := signed(t)
	_, err := party.New(redirector.URL, nil).WithSigner(signer).BankAccounts(ctx(), entity, vendor)
	var se *party.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusFound || leaked {
		t.Fatalf("err=%v leaked=%v, want the 302 returned without following it", err, leaked)
	}
}

func TestBankAccounts_RefusesToSignForAnEntityOutsideTheUsersScope(t *testing.T) {
	signer, _ := signed(t)
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	if _, err := party.New(srv.URL, nil).WithSigner(signer).BankAccounts(ctx(), "00000000-0000-0000-0000-0000000000b2", vendor); !errors.Is(err, party.ErrConfig) || called {
		t.Fatalf("err %v called %v: a token for an entity the user cannot act in must never be sent", err, called)
	}
}

func TestBankAccounts_NoPrincipalIsAConfigFault(t *testing.T) {
	signer, _ := signed(t)
	if _, err := party.New("http://unused", nil).WithSigner(signer).BankAccounts(context.Background(), entity, vendor); !errors.Is(err, party.ErrConfig) {
		t.Fatalf("err = %v", err)
	}
}
