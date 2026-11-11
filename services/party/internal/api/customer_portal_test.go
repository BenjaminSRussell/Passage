package api_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const portalCustomer = "0195f0a0-0000-7000-8000-0000000000c1"

type accountsCustomers struct {
	fakeCustomers
	asked []string
}

func (f *accountsCustomers) PortalAccounts(_ context.Context, customerID string) ([]store.PortalAccount, error) {
	f.asked = append(f.asked, customerID)
	return []store.PortalAccount{{ID: customerID, AccountNumber: "C-1", Name: "Head office", Relation: "self"}}, nil
}

func TestCustomerPortalAccounts_OnlyThePortalTokenForItsOwnCustomer(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner(tenant.CustomerPortalIssuer, key)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("party", map[string]ed25519.PublicKey{tenant.CustomerPortalIssuer: pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	f := &accountsCustomers{}
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithCustomers(f).Register(r)

	token, err := signer.Mint("party", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: "contact", CustomerID: portalCustomer, Duties: map[string]struct{}{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/party/customer-portal/accounts?customerId="+knownID, nil)
	req.Header.Set(tenant.HeaderServiceToken, token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Head office") || len(f.asked) != 1 || f.asked[0] != portalCustomer {
		t.Fatalf("portal accounts: %d %s asked %v, want the token's customer only", rec.Code, rec.Body, f.asked)
	}
	// A staff user holding every duty is not a customer, and the read duty alone does not name one.
	for _, duties := range []string{"party.read", "party.customer-portal.read", "party.read,party.write"} {
		f.asked = nil
		if rec := do(r, http.MethodGet, "/v1/party/customer-portal/accounts", "", duties, true); rec.Code != http.StatusForbidden || len(f.asked) != 0 {
			t.Errorf("%s as a user: %d %s asked %v, want 403 and no read", duties, rec.Code, rec.Body, f.asked)
		}
	}
}
