package tenant_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
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

func TestProspectRegistrationTokenNamesNoVendorAndCarriesOnlyTheRegisterDuty(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	portal, err := tenant.NewServiceSigner("supplier-portal", priv)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return epoch }
	portal = portal.WithClock(clock)
	verifier := tenant.NewServiceVerifier("party", map[string]ed25519.PublicKey{"supplier-portal": pub}).WithClock(clock)

	token, err := portal.MintDuties("party", vendorUser(""), "", []string{tenant.DutyPortalRegister})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifier.Verify(token)
	if err != nil {
		t.Fatalf("a registration token was refused: %v", err)
	}
	if p := claims.Principal(); p.VendorID != "" || !p.HasDuty(tenant.DutyPortalRegister) || p.HasDuty("party.vendor-portal.read") {
		t.Fatalf("principal %+v", p)
	}

	// A vendorless token for any other duty, or for the register duty plus another, is never signed.
	for _, only := range [][]string{nil, {"party.vendor-portal.read"}, {tenant.DutyPortalRegister, "party.vendor-portal.read"}} {
		if _, err := portal.MintDuties("party", vendorUser(""), "", only); err == nil {
			t.Errorf("signed a vendorless token for %v", only)
		}
	}
}

func TestVendorlessPortalTokenForAnotherDutyIsRefusedByTheVerifier(t *testing.T) {
	// Forge one with a signer that allows it: the register token, then check a copy with a different duty cannot verify.
	// The verifier rule is exercised through a token that names a vendor-portal duty but no vendor: Mint refuses to create it,
	// so the only way to get one is a leaked key, and the verifier must still refuse it.
	pub, priv, _ := ed25519.GenerateKey(nil)
	clock := func() time.Time { return epoch }
	verifier := tenant.NewServiceVerifier("party", map[string]ed25519.PublicKey{"supplier-portal": pub}).WithClock(clock)
	claims, _ := json.Marshal(tenant.ServiceClaims{
		Iss: "supplier-portal", Aud: "party", Tid: validTenant, Le: validLE, Act: "anon", Duties: []string{"party.vendor-portal.read"},
		Iat: epoch.Unix(), Exp: epoch.Unix() + 30, Jti: "j1",
	})
	body := base64.RawURLEncoding.EncodeToString(claims)
	forged := body + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte("passage.service-token.v1."+body)))
	if _, err := verifier.Verify(forged); err == nil {
		t.Error("a supplier portal token without a vendor was accepted for a read duty")
	}
}
