package domain_test

import (
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
	"pgregory.net/rapid"
)

func TestCheckSiteAndWarehouse(t *testing.T) {
	site := &domain.OrgRef{ID: "S1", LegalEntityID: "LE1"}
	wh := &domain.OrgRef{ID: "W1", LegalEntityID: "LE1", SiteID: "S1"}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"valid site", domain.CheckSite("defaultSiteId", "LE1", site), ""},
		{"unknown site", domain.CheckSite("defaultSiteId", "LE1", nil), domain.CodeReleaseSiteInvalid},
		{"site of other legal entity", domain.CheckSite("defaultSiteId", "LE2", site), domain.CodeReleaseSiteInvalid},
		{"valid warehouse", domain.CheckWarehouse("defaultWarehouseId", "LE1", "S1", wh), ""},
		{"warehouse without site constraint", domain.CheckWarehouse("defaultWarehouseId", "LE1", "", wh), ""},
		{"unknown warehouse", domain.CheckWarehouse("defaultWarehouseId", "LE1", "S1", nil), domain.CodeReleaseWarehouseInvalid},
		{"warehouse of other legal entity", domain.CheckWarehouse("defaultWarehouseId", "LE2", "S1", wh), domain.CodeReleaseWarehouseInvalid},
		{"warehouse outside site", domain.CheckWarehouse("defaultWarehouseId", "LE1", "S2", wh), domain.CodeReleaseWarehouseInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == "" {
				if tc.err != nil {
					t.Fatalf("unexpected %v", tc.err)
				}
				return
			}
			var de *domain.Error
			if !errors.As(tc.err, &de) || de.Code != tc.want {
				t.Fatalf("got %v, want code %s", tc.err, tc.want)
			}
		})
	}
}

func TestCascadeHoldsWhenEveryPlacementIsAccepted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		sites := []string{"S1", "S2", "S3", "S4"}
		overrides := map[string]domain.SiteDefault{}
		for _, site := range sites[1:] {
			if rapid.Bool().Draw(t, "override "+site) {
				overrides[site] = domain.SiteDefault{SiteID: site, WarehouseID: "W-" + site}
			}
		}
		defaults := domain.ReleaseDefaults{LegalEntityID: "LE1", DefaultSiteID: "S1", DefaultWarehouseID: "W1", SiteDefaults: overrides}
		for _, site := range sites {
			got, err := domain.ResolveSiteDefault(defaults, site)
			if err != nil {
				t.Fatalf("resolve %s: %v", site, err)
			}
			if err := domain.CheckSite(site, "LE1", &domain.OrgRef{ID: got.SiteID, LegalEntityID: "LE1"}); err != nil {
				t.Fatalf("site %s: %v", site, err)
			}
			if err := domain.CheckWarehouse(site, "LE1", got.SiteID, &domain.OrgRef{ID: got.WarehouseID, LegalEntityID: "LE1", SiteID: got.SiteID}); err != nil {
				t.Fatalf("warehouse %s: %v", site, err)
			}
			want := "W1"
			if over, ok := overrides[site]; ok {
				want = over.WarehouseID
			}
			if got.WarehouseID != want {
				t.Fatalf("site %s: warehouse %s, want %s", site, got.WarehouseID, want)
			}
		}
	})
}
