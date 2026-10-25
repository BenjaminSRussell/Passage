package store_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const addressChangedType = "dev.passage.org.address.changed.v1"

func problemErrors(err error) map[string]string {
	var problem *httpx.Problem
	if errors.As(err, &problem) {
		return problem.Errors
	}
	return nil
}

func ptr(s string) *string { return &s }

func newAddress(ownerType, ownerID string, purposes ...store.AddressPurpose) store.Address {
	return store.Address{OwnerType: ownerType, OwnerID: ownerID, CountryCode: "US", RegionCode: ptr("CA"), City: "San Francisco", PostalCode: ptr("94105"),
		Street1: "1 Main St", GeoStatus: "unverified", Purposes: purposes}
}

func business(primary bool) store.AddressPurpose {
	return store.AddressPurpose{Purpose: "business", Primary: primary}
}

func (w warehouseWorld) rows(t *testing.T, table string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(w.ctx, w.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `select count(*) from `+table).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgres_AddressCountryRulesEndToEnd(t *testing.T) {
	w := newWarehouseWorld(t)
	noRegion := newAddress("site", w.s1.ID, business(true))
	noRegion.RegionCode = nil
	badPostal := newAddress("site", w.s1.ID, business(true))
	badPostal.PostalCode = ptr("9410")
	badRegion := newAddress("site", w.s1.ID, business(true))
	badRegion.RegionCode = ptr("ZZ")
	badCountry := newAddress("site", w.s1.ID, business(true))
	badCountry.CountryCode = "XX"
	for name, tc := range map[string]struct {
		in    store.Address
		code  string
		field string
	}{
		"region required": {noRegion, "ORG-ADDR-REGION-REQUIRED", "regionCode"},
		"postal format":   {badPostal, "ORG-ADDR-POSTAL-FORMAT", "postalCode"},
		"region unknown":  {badRegion, "ORG-ADDR-REGION-UNKNOWN", "regionCode"},
		"country invalid": {badCountry, "ORG-ADDR-COUNTRY-INVALID", "countryCode"},
	} {
		_, err := w.s.CreateAddress(w.ctx, tc.in, "")
		if problemCode(t, err) != tc.code {
			t.Fatalf("%s error = %v; want %s", name, err, tc.code)
		}
		if _, ok := problemErrors(err)[tc.field]; !ok {
			t.Fatalf("%s errors = %v; want field %s", name, problemErrors(err), tc.field)
		}
	}
	if w.rows(t, "postal_address") != 0 || w.rows(t, "address_purpose") != 0 || len(w.unitEvents(addressChangedType)) != 0 {
		t.Fatal("a rejected create left rows or an outbox event behind")
	}
}

func TestPostgres_AddressCreateWritesOneEventAndReplayWritesNone(t *testing.T) {
	w := newWarehouseWorld(t)
	in := newAddress("warehouse", w.wh1.ID, business(true), store.AddressPurpose{Purpose: "delivery"})
	in.PostalCode = ptr(" 94105 ")
	first, err := w.s.CreateAddress(w.ctx, in, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != first.VersionID || first.LegalEntityID == nil || *first.LegalEntityID != w.usmf.ID || first.Formatted == "" || *first.PostalCode != "94105" || first.GeoStatus != "unverified" {
		t.Fatalf("created = %+v; want id == versionId, USMF legal entity, normalized postal code", first)
	}
	if w.rows(t, "postal_address") != 1 || w.rows(t, "address_purpose") != 2 {
		t.Fatalf("rows = %d postal_address, %d address_purpose; want 1 and 2", w.rows(t, "postal_address"), w.rows(t, "address_purpose"))
	}
	replay, err := w.s.CreateAddress(w.ctx, in, "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the original address", replay, err)
	}
	changed := in
	changed.City = "Oakland"
	if _, err := w.s.CreateAddress(w.ctx, changed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay with a different city error = %v", err)
	}
	events := w.unitEvents(addressChangedType)
	if len(events) != 1 || w.rows(t, "postal_address") != 1 {
		t.Fatalf("events = %v, rows = %d; want exactly one of each", events, w.rows(t, "postal_address"))
	}
	e := events[0]
	if e["address_id"] != first.ID || e["version_id"] != first.VersionID || e["owner_type"] != "warehouse" || e["owner_id"] != w.wh1.ID ||
		!reflect.DeepEqual(e["purposes"], []any{"business", "delivery"}) || e["valid_from"] != first.ValidFrom {
		t.Fatalf("event data = %v", e)
	}
}

func TestPostgres_AddressPrimaryPurposeIsUniquePerOwner(t *testing.T) {
	w := newWarehouseWorld(t)
	if _, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1.ID, business(true)), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1.ID, business(true)), ""); problemCode(t, err) != store.CodeAddressPrimaryDuplicate {
		t.Fatalf("second primary error = %v", err)
	}
	if w.rows(t, "postal_address") != 1 || w.rows(t, "address_purpose") != 1 || len(w.unitEvents(addressChangedType)) != 1 {
		t.Fatal("the rejected second primary left rows or an event behind")
	}
	if _, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1.ID, business(false)), ""); err != nil {
		t.Fatalf("non-primary second business address error = %v", err)
	}
	if _, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1De.ID, business(true)), ""); err != nil {
		t.Fatalf("primary for another owner error = %v", err)
	}
}

func TestPostgres_AddressOwnersIsolationAndScope(t *testing.T) {
	w := newWarehouseWorld(t)
	if _, err := w.s.CreateAddress(w.ctx, newAddress("site", ids.New(), business(true)), ""); problemCode(t, err) != store.CodeAddressOwnerInvalid {
		t.Fatalf("unknown site error = %v", err)
	}
	partyOwned, err := w.s.CreateAddress(w.ctx, newAddress("party", ids.New(), business(true)), "")
	if err != nil || partyOwned.LegalEntityID != nil {
		t.Fatalf("party owner = %+v, %v; want accepted with a null legal entity", partyOwned, err)
	}
	owned, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1De.ID, business(true)), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.GetAddress(principalContext(2), owned.ID); problemCode(t, err) != store.CodeAddressNotFound {
		t.Fatalf("tenant 2 get error = %v", err)
	}
	scoped := scopedTo(w.usmf.ID)
	if _, err := w.s.GetAddress(scoped, owned.ID); problemCode(t, err) != store.CodeAddressNotFound {
		t.Fatalf("scoped get of a DEMF address error = %v", err)
	}
	if _, err := w.s.CreateAddress(scoped, newAddress("warehouse", w.wh1De.ID, business(false)), ""); problemCode(t, err) != store.CodeAddressOwnerInvalid {
		t.Fatalf("scoped create for a DEMF warehouse error = %v", err)
	}
	if got, err := w.s.GetAddress(scoped, partyOwned.ID); err != nil || got.ID != partyOwned.ID {
		t.Fatalf("scoped get of a party address = %+v, %v", got, err)
	}
}

func TestPostgres_GetAddressReturnsPurposesAndNormalizedPostalCode(t *testing.T) {
	w := newWarehouseWorld(t)
	in := newAddress("location", mustLocation(t, w), store.AddressPurpose{Purpose: "service"}, business(true))
	in.CountryCode, in.RegionCode, in.PostalCode = "CA", ptr("ON"), ptr("k1a 0b1")
	created, err := w.s.CreateAddress(w.ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.s.GetAddress(w.ctx, created.ID)
	if err != nil || *got.PostalCode != "K1A 0B1" || !reflect.DeepEqual(got.Purposes, []store.AddressPurpose{business(true), {Purpose: "service"}}) {
		t.Fatalf("get = %+v, %v; want purposes ordered by name and the normalized postal code", got, err)
	}
	if _, err := w.s.GetAddress(w.ctx, ids.New()); problemCode(t, err) != store.CodeAddressNotFound {
		t.Fatalf("unknown id error = %v", err)
	}
}

func mustLocation(t *testing.T, w warehouseWorld) string {
	t.Helper()
	location, err := w.s.CreateLocation(w.ctx, store.Location{WarehouseID: w.wh1.ID, Code: "L1", Name: "Bin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return location.ID
}
