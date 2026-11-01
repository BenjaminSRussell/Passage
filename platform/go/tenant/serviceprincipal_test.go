package tenant_test

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
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
	if len(p.Duties) != 1 || !p.HasDuty("VendorInvoicePost") || p.HasDuty(tenant.DutyAllScope) || tenant.IsTenantAdmin(p) {
		t.Fatalf("duties = %v, want only VendorInvoicePost", p.Duties)
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
