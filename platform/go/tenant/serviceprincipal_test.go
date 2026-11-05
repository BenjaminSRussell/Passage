package tenant_test

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	spTenant = "11111111-1111-4111-8111-111111111111"
	spEntity = "22222222-2222-4222-8222-222222222222"
)

func TestServicePrincipal(t *testing.T) {
	p, err := tenant.ServicePrincipal("receiving-ers", spTenant, spEntity)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "service:receiving-ers" || p.TenantID != spTenant || p.LegalEntityID != spEntity || p.Service != "" {
		t.Fatalf("principal = %+v", p)
	}
	if len(p.Duties) != 1 || !p.HasDuty("receiving.vendor-invoice.post") || p.HasDuty(tenant.DutyAllScope) || tenant.IsTenantAdmin(p) {
		t.Fatalf("duties = %v, want only receiving.vendor-invoice.post", p.Duties)
	}
	if tenantOnly, err := tenant.ServicePrincipal("receiving-ers", spTenant, ""); err != nil || tenantOnly.LegalEntityID != "" {
		t.Fatalf("tenant-only principal = %+v, %v", tenantOnly, err)
	}
}

func TestServicePrincipalRefusals(t *testing.T) {
	if _, err := tenant.ServicePrincipal("nobody", spTenant, ""); !errors.Is(err, tenant.ErrUnknownServicePrincipal) {
		t.Fatalf("unknown name err = %v", err)
	}
	for _, tid := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		if _, err := tenant.ServicePrincipal("receiving-ers", tid, ""); err == nil {
			t.Fatalf("tenant %q accepted", tid)
		}
	}
	if _, err := tenant.ServicePrincipal("receiving-ers", spTenant, "all"); err == nil {
		t.Fatal("non-UUID legal entity accepted")
	}
}

func TestServicePrincipalTableMatchesPolicyData(t *testing.T) {
	raw, err := os.ReadFile("../../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Duties            map[string]json.RawMessage `json:"duties"`
		ServicePrincipals map[string][]string        `json:"service_principals"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data.ServicePrincipals, tenant.ServicePrincipalTable()) {
		t.Fatalf("data.json service_principals %v != Go table %v", data.ServicePrincipals, tenant.ServicePrincipalTable())
	}
	for name, duties := range data.ServicePrincipals {
		for _, d := range duties {
			if _, ok := data.Duties[d]; !ok {
				t.Errorf("%s holds %q which is not a seeded duty", name, d)
			}
			if d == tenant.DutyAllScope || d == tenant.DutyTenantAdmin {
				t.Errorf("%s holds privileged duty %q", name, d)
			}
		}
	}
}

func TestHTTPUserHeaderCannotClaimAServiceActor(t *testing.T) {
	for user, want := range map[string]int{"service:receiving-ers": 401, "  Service:anything": 401, "warehouse-user": 200, "": 200} {
		reached := false
		h := tenant.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		req := httptest.NewRequest(http.MethodPost, "/v1/x", nil)
		req.Header.Set(tenant.HeaderTenant, spTenant)
		req.Header.Set(tenant.HeaderUser, user)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want || reached != (want == 200) {
			t.Errorf("user %q: status %d reached %v, want %d", user, rec.Code, reached, want)
		}
	}
}

func TestServiceTokenActMustBeAKnownServiceActor(t *testing.T) {
	f := newFixture(t)
	for act, ok := range map[string]bool{"service:receiving-ers": true, "service:evil": false, "SERVICE:receiving-ers": false, "warehouse-user": true} {
		f.principal.UserID = act
		_, err := f.verifier.Verify(f.mint(t, "ledger"))
		if (err == nil) != ok {
			t.Errorf("act %q: err = %v, want accepted=%v", act, err, ok)
		}
	}
}

// A party created by the migration loader takes its number from numbering: party signs that call with the loader as
// act, and the token carries party's grant only.
func TestPartyForwardsTheMigrationLoaderToNumberingWithItsOwnGrant(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("party", priv)
	if err != nil {
		t.Fatal(err)
	}
	loader, err := tenant.ServicePrincipal("migration-loader", validTenant, validLE)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.Mint("numbering", loader, "")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := tenant.NewServiceVerifier("numbering", map[string]ed25519.PublicKey{"party": pub}).Verify(token)
	if err != nil || claims.Act != "service:migration-loader" || strings.Join(claims.Duties, ",") != "numbering.sequence.allocate" {
		t.Fatalf("claims = %+v err = %v", claims, err)
	}
	if _, err := signer.Mint("product", loader, ""); err == nil {
		t.Fatal("party has no grant to product; it must not reach it as the loader")
	}
}

func TestServiceTokenActMustBeIssuedByItsOwnService(t *testing.T) {
	// The fixture signs as "receiving": it may act as its own principal, never as another service's.
	f := newFixture(t)
	for act, ok := range map[string]bool{
		"service:receiving-ers":       true,
		"service:migration-loader":    false,
		"service:integration-hub":     false,
		"service:scheduling-downtime": false,
	} {
		f.principal.UserID = act
		_, err := f.verifier.Verify(f.mint(t, "ledger"))
		if (err == nil) != ok {
			t.Errorf("act %q signed by receiving: err = %v, want accepted=%v", act, err, ok)
		}
	}
}
