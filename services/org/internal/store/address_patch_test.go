package store_test

import (
	"reflect"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

func TestAddressPatchApply(t *testing.T) {
	lat := decimal.RequireFromString("37.789")
	base := store.Address{ID: "a", VersionID: "v", OwnerType: "warehouse", OwnerID: "o", CountryCode: "US", RegionCode: ptr("CA"), City: "San Francisco", PostalCode: ptr("94105"),
		Street1: "1 Main St", Street2: ptr("Floor 2"), Latitude: &lat, GeoStatus: "geocoded", ValidFrom: "2026-01-01",
		Purposes: []store.AddressPurpose{business(true), {Purpose: "delivery"}}}
	tests := []struct {
		name        string
		patch       store.AddressPatch
		wantChanged bool
		check       func(store.Address) bool
	}{
		{"sets city", store.AddressPatch{City: ptr("Oakland")}, true, func(a store.Address) bool { return a.City == "Oakland" }},
		{"empty string clears street2 and postalCode", store.AddressPatch{Street2: ptr(""), PostalCode: ptr("")}, true, func(a store.Address) bool { return a.Street2 == nil && a.PostalCode == nil }},
		{"nil fields keep values", store.AddressPatch{}, false, func(a store.Address) bool { return a.City == "San Francisco" && *a.Street2 == "Floor 2" }},
		{"same values are no change", store.AddressPatch{City: ptr("San Francisco"), Street2: ptr("Floor 2")}, false, func(store.Address) bool { return true }},
		{"clearing an absent field is no change", store.AddressPatch{Building: ptr("")}, false, func(a store.Address) bool { return a.Building == nil }},
		{"reordered purposes are no change", store.AddressPatch{Purposes: []store.AddressPurpose{{Purpose: "delivery"}, business(true)}}, false, func(store.Address) bool { return true }},
		{"primary flag flip is a change", store.AddressPatch{Purposes: []store.AddressPurpose{business(false), {Purpose: "delivery"}}}, true, func(a store.Address) bool { return !a.Purposes[0].Primary }},
		{"different purpose set is a change", store.AddressPatch{Purposes: []store.AddressPurpose{business(true), {Purpose: "invoice"}}}, true, func(a store.Address) bool { return a.Purposes[1].Purpose == "invoice" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := tc.patch.Apply(base)
			if changed != tc.wantChanged || !tc.check(got) {
				t.Fatalf("Apply = %+v, %v; want changed %v", got, changed, tc.wantChanged)
			}
			if got.ID != base.ID || got.VersionID != base.VersionID || got.OwnerType != base.OwnerType || got.OwnerID != base.OwnerID || got.ValidFrom != base.ValidFrom ||
				got.GeoStatus != base.GeoStatus || !reflect.DeepEqual(got.Latitude, base.Latitude) {
				t.Fatalf("Apply touched identity, owner, geo or date fields: %+v", got)
			}
		})
	}
}
