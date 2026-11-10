package tenant_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const vendorID = "0b1c2d3e-0000-4000-8000-0000000000aa"

func vendorSigner(t *testing.T, name string) (*tenant.ServiceSigner, *tenant.ServiceVerifier) {
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
	return signer.WithClock(clock), tenant.NewServiceVerifier("purchasing", map[string]ed25519.PublicKey{name: pub}).WithClock(clock)
}

func vendorUser(vendor string) tenant.Principal {
	return tenant.Principal{TenantID: validTenant, LegalEntityID: validLE, UserID: "vendor-user", VendorID: vendor, Duties: map[string]struct{}{}}
}

func TestVendorTokenCarriesTheVendorToThePrincipal(t *testing.T) {
	signer, verifier := vendorSigner(t, "supplier-portal")
	token, err := signer.Mint("purchasing", vendorUser(vendorID), "")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifier.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if p := claims.Principal(); p.VendorID != vendorID || p.Service != "supplier-portal" || !p.HasDuty("procurement.vendor-portal.read") {
		t.Fatalf("principal %+v", p)
	}
}

func TestOnlyTheSupplierPortalSignsAVendor(t *testing.T) {
	portal, _ := vendorSigner(t, "supplier-portal")
	if _, err := portal.Mint("purchasing", vendorUser(""), ""); err == nil {
		t.Error("the supplier portal signed a token without a vendor")
	}
	other, otherVerifier := vendorSigner(t, "receiving")
	token, err := other.Mint("purchasing", vendorUser(vendorID), "")
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := otherVerifier.Verify(token); err != nil || claims.Vnd != "" || claims.Principal().VendorID != "" {
		t.Errorf("another service signed a vendor into a token: %+v %v", claims, err)
	}
}
