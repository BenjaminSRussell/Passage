package tenant_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const customerID = "0c1d2e3f-0000-4000-8000-0000000000bb"

func customerSigner(t *testing.T, name, aud string) (*tenant.ServiceSigner, *tenant.ServiceVerifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner(name, priv)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return epoch }
	return signer.WithClock(clock), tenant.NewServiceVerifier(aud, map[string]ed25519.PublicKey{name: pub}).WithClock(clock)
}

func customerUser(customer, vendor string) tenant.Principal {
	return tenant.Principal{TenantID: validTenant, LegalEntityID: validLE, UserID: "customer-contact", CustomerID: customer, VendorID: vendor, Duties: map[string]struct{}{}}
}

func TestCustomerTokenCarriesTheCustomerToThePrincipal(t *testing.T) {
	signer, verifier := customerSigner(t, "customer-portal", "receivables")
	token, err := signer.Mint("receivables", customerUser(customerID, ""), "")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifier.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	p := claims.Principal()
	if p.CustomerID != customerID || p.Service != "customer-portal" || p.VendorID != "" || !p.HasDuty("receivables.customer-portal.read") {
		t.Fatalf("principal %+v", p)
	}
	if p.HasDuty("receivables.view") || p.HasDuty("receivables.payment.post") {
		t.Fatalf("the customer portal holds a staff duty: %+v", p.Duties)
	}
	got, ok := tenant.PortalCustomer(tenant.With(context.Background(), p))
	if !ok || got != customerID {
		t.Fatalf("PortalCustomer = %q %v", got, ok)
	}
}

func TestOnlyTheCustomerPortalSignsACustomer(t *testing.T) {
	portal, _ := customerSigner(t, "customer-portal", "billing")
	if _, err := portal.Mint("billing", customerUser("", ""), ""); err == nil {
		t.Error("the customer portal signed a token without a customer")
	}
	// Another service never signs a customer, even when the principal it calls onward for carries one.
	other, otherVerifier := customerSigner(t, "sales", "billing")
	token, err := other.Mint("billing", customerUser(customerID, ""), "")
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := otherVerifier.Verify(token); err != nil || claims.Cus != "" || claims.Principal().CustomerID != "" {
		t.Errorf("another service signed a customer into a token: %+v %v", claims, err)
	}
	// The supplier portal never signs a customer either.
	supplier, supplierVerifier := customerSigner(t, "supplier-portal", "purchasing")
	token, err = supplier.Mint("purchasing", customerUser(customerID, vendorID), "")
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := supplierVerifier.Verify(token); err != nil || claims.Cus != "" || claims.Vnd != vendorID {
		t.Errorf("the supplier portal signed a customer: %+v %v", claims, err)
	}
	// And the customer portal never signs a vendor.
	portal2, verifier2 := customerSigner(t, "customer-portal", "sales")
	token, err = portal2.Mint("sales", customerUser(customerID, vendorID), "")
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := verifier2.Verify(token); err != nil || claims.Vnd != "" || claims.Cus != customerID {
		t.Errorf("the customer portal signed a vendor: %+v %v", claims, err)
	}
}

func TestPortalCustomerTrustsOnlyTheCustomerPortalToken(t *testing.T) {
	cases := map[string]struct {
		p      tenant.Principal
		wantID string
		wantOK bool
	}{
		"customer portal token":       {tenant.Principal{Service: tenant.CustomerPortalIssuer, CustomerID: customerID}, customerID, true},
		"portal token without id":     {tenant.Principal{Service: tenant.CustomerPortalIssuer}, "", false},
		"user with a customer field":  {tenant.Principal{UserID: "u", CustomerID: customerID}, "", false},
		"another service":             {tenant.Principal{Service: "sales", CustomerID: customerID}, "", false},
		"supplier portal with vendor": {tenant.Principal{Service: tenant.VendorPortalIssuer, VendorID: vendorID, CustomerID: customerID}, "", false},
	}
	for name, c := range cases {
		id, ok := tenant.PortalCustomer(tenant.With(context.Background(), c.p))
		if id != c.wantID || ok != c.wantOK {
			t.Errorf("%s: got %q %v, want %q %v", name, id, ok, c.wantID, c.wantOK)
		}
	}
	if _, ok := tenant.PortalCustomer(context.Background()); ok {
		t.Error("no principal gave a customer")
	}
}

func TestCustomerPortalGrantIsReadOnlyAndCustomerScoped(t *testing.T) {
	grants := tenant.ServiceGrantTable()["customer-portal"]
	want := map[string]string{"receivables": "receivables.customer-portal.read", "billing": "billing.customer-portal.read", "sales": "sales.customer-portal.read"}
	if len(grants) != len(want) {
		t.Fatalf("grants %v", grants)
	}
	for aud, duty := range want {
		if len(grants[aud]) != 1 || grants[aud][0] != duty {
			t.Errorf("%s grant %v, want only %s", aud, grants[aud], duty)
		}
	}
}
