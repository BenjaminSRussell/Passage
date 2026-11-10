package api

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestPortalVendorPrincipalTrustsOnlyTheSupplierPortalToken(t *testing.T) {
	const vendor = "0b1c2d3e-0000-4000-8000-0000000000aa"
	cases := map[string]struct {
		p      tenant.Principal
		wantID string
		wantOK bool
	}{
		"supplier portal token":       {tenant.Principal{Service: tenant.VendorPortalIssuer, VendorID: vendor}, vendor, true},
		"portal token without vendor": {tenant.Principal{Service: tenant.VendorPortalIssuer}, "", false},
		"user with a vendor field":    {tenant.Principal{UserID: "u", VendorID: vendor}, "", false},
		"another service":             {tenant.Principal{Service: "receiving", VendorID: vendor}, "", false},
	}
	for name, c := range cases {
		id, ok := PortalVendorPrincipal{}.VendorID(tenant.With(context.Background(), c.p))
		if id != c.wantID || ok != c.wantOK {
			t.Errorf("%s: got %q %v, want %q %v", name, id, ok, c.wantID, c.wantOK)
		}
	}
	if _, ok := (PortalVendorPrincipal{}).VendorID(context.Background()); ok {
		t.Error("no principal gave a vendor")
	}
}
