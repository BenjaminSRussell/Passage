package store_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

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

func revisionOf(current store.Address, patch store.AddressPatch, validFrom string) store.Address {
	next, _ := patch.Apply(current)
	next.ValidFrom = validFrom
	return next
}

func (w warehouseWorld) versionDates(t *testing.T, versionID string) (validFrom string, validTo *string) {
	t.Helper()
	err := dbx.WithTenantTx(w.ctx, w.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `select to_char(valid_from, 'YYYY-MM-DD'), to_char(valid_to, 'YYYY-MM-DD') from postal_address where id = $1`, versionID).Scan(&validFrom, &validTo)
	})
	if err != nil {
		t.Fatal(err)
	}
	return validFrom, validTo
}

func TestPostgres_ReviseAddressCopyOnWrite(t *testing.T) {
	w := newWarehouseWorld(t)
	created, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1.ID, business(true), store.AddressPurpose{Purpose: "delivery"}), "")
	if err != nil {
		t.Fatal(err)
	}
	patch := store.AddressPatch{City: ptr("Oakland"), Purposes: []store.AddressPurpose{business(true), {Purpose: "invoice"}}}
	revised, err := w.s.ReviseAddress(w.ctx, created.ID, created.VersionID, revisionOf(created, patch, ""))
	if err != nil {
		t.Fatal(err)
	}
	if revised.ID != created.ID || revised.VersionID == created.VersionID || revised.City != "Oakland" || revised.ValidTo != nil || *revised.LegalEntityID != w.usmf.ID {
		t.Fatalf("revised = %+v; want the same address id, a new version id and the new city", revised)
	}
	var versions int
	if err := dbx.WithTenantTx(w.ctx, w.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `select count(*) from postal_address where address_id = $1`, created.ID).Scan(&versions)
	}); err != nil || versions != 2 {
		t.Fatalf("versions = %d, %v; want two rows for one address_id after a same-day revision", versions, err)
	}
	oldFrom, oldTo := w.versionDates(t, created.VersionID)
	newFrom, newTo := w.versionDates(t, revised.VersionID)
	if oldTo == nil || *oldTo != newFrom || oldFrom != created.ValidFrom || newTo != nil {
		t.Fatalf("old version %s..%v, new version %s..%v; want the old valid_to to equal the new valid_from", oldFrom, oldTo, newFrom, newTo)
	}
	current, err := w.s.GetAddress(w.ctx, created.ID)
	if err != nil || current.VersionID != revised.VersionID || current.City != "Oakland" || !reflect.DeepEqual(current.Purposes, []store.AddressPurpose{business(true), {Purpose: "invoice"}}) {
		t.Fatalf("current = %+v, %v; want the new version with purposes business, invoice", current, err)
	}
	old, err := w.s.GetAddressVersion(w.ctx, created.VersionID)
	if err != nil || old.City != "San Francisco" || old.VersionID != created.VersionID || old.ValidTo == nil {
		t.Fatalf("old version = %+v, %v; want the original content, closed", old, err)
	}
	events := w.unitEvents(addressChangedType)
	if len(events) != 2 || events[1]["version_id"] != revised.VersionID || events[1]["address_id"] != created.ID || !reflect.DeepEqual(events[1]["purposes"], []any{"business", "invoice"}) || events[1]["valid_from"] != newFrom {
		t.Fatalf("events = %v; want exactly two, the second for the new version", events)
	}
	again, err := w.s.ReviseAddress(w.ctx, created.ID, revised.VersionID, revisionOf(revised, store.AddressPatch{City: ptr("Berkeley")}, ""))
	if err != nil || again.City != "Berkeley" || w.rows(t, "postal_address") != 3 {
		t.Fatalf("second same-day revision = %+v, %v, rows %d", again, err, w.rows(t, "postal_address"))
	}
}

func TestPostgres_ReviseAddressRejectionsWriteNothing(t *testing.T) {
	w := newWarehouseWorld(t)
	primary, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1.ID, business(true)), "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1.ID, store.AddressPurpose{Purpose: "invoice"}), "")
	if err != nil {
		t.Fatal(err)
	}
	tomorrow := time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")
	for _, tc := range []struct {
		name      string
		target    store.Address
		versionID string
		patch     store.AddressPatch
		validFrom string
		code      string
	}{
		{"stale versionId", primary, ids.New(), store.AddressPatch{City: ptr("Oakland")}, "", store.CodeVersionConflict},
		{"validFrom before the current version", primary, primary.VersionID, store.AddressPatch{City: ptr("Oakland")}, "2000-01-01", store.CodeAddressValidFrom},
		{"validFrom in the future", primary, primary.VersionID, store.AddressPatch{City: ptr("Oakland")}, tomorrow, store.CodeAddressValidFrom},
		{"US region cleared", primary, primary.VersionID, store.AddressPatch{RegionCode: ptr("")}, "", "ORG-ADDR-REGION-REQUIRED"},
		{"second primary business", other, other.VersionID, store.AddressPatch{Purposes: []store.AddressPurpose{business(true)}}, "", store.CodeAddressPrimaryDuplicate},
	} {
		rowsBefore, purposesBefore, eventsBefore := w.rows(t, "postal_address"), w.rows(t, "address_purpose"), len(w.unitEvents(addressChangedType))
		if _, err := w.s.ReviseAddress(w.ctx, tc.target.ID, tc.versionID, revisionOf(tc.target, tc.patch, tc.validFrom)); problemCode(t, err) != tc.code {
			t.Fatalf("%s error = %v; want %s", tc.name, err, tc.code)
		}
		current, err := w.s.GetAddress(w.ctx, tc.target.ID)
		if err != nil || current.VersionID != tc.target.VersionID || w.rows(t, "postal_address") != rowsBefore || w.rows(t, "address_purpose") != purposesBefore ||
			len(w.unitEvents(addressChangedType)) != eventsBefore || !reflect.DeepEqual(current.Purposes, tc.target.Purposes) {
			t.Fatalf("%s changed state: current %+v, %v", tc.name, current, err)
		}
	}
	if _, err := w.s.ReviseAddress(w.ctx, ids.New(), ids.New(), primary); problemCode(t, err) != store.CodeAddressNotFound {
		t.Fatalf("unknown address error = %v", err)
	}
}

func TestPostgres_ListAddressesPagesFiltersAndShowsCurrentVersionsOnce(t *testing.T) {
	w := newWarehouseWorld(t)
	var created []store.Address
	for _, purposes := range [][]store.AddressPurpose{{business(true)}, {{Purpose: "delivery"}}, {{Purpose: "invoice"}, {Purpose: "service"}}} {
		address, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1.ID, purposes...), "")
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, address)
	}
	filter := store.AddressFilter{OwnerType: "warehouse", OwnerID: w.wh1.ID}
	first, err := w.s.ListAddresses(w.ctx, filter, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextID != first.Items[1].ID {
		t.Fatalf("first page = %+v, %v; want 2 items and NextID", first, err)
	}
	second, err := w.s.ListAddresses(w.ctx, filter, first.NextID, 2)
	if err != nil || len(second.Items) != 1 || second.NextID != "" || second.Items[0].ID <= first.Items[1].ID {
		t.Fatalf("second page = %+v, %v; want the last item and no NextID", second, err)
	}
	for _, item := range append(first.Items, second.Items...) {
		if len(item.Purposes) == 0 {
			t.Fatalf("listed address %s has no purposes", item.ID)
		}
	}
	filter.Purpose = "invoice"
	invoice, err := w.s.ListAddresses(w.ctx, filter, "", 10)
	if err != nil || len(invoice.Items) != 1 || invoice.Items[0].ID != created[2].ID || len(invoice.Items[0].Purposes) != 2 {
		t.Fatalf("invoice filter = %+v, %v; want only the invoice address", invoice, err)
	}
	if _, err := w.s.ReviseAddress(w.ctx, created[0].ID, created[0].VersionID, revisionOf(created[0], store.AddressPatch{City: ptr("Oakland")}, "")); err != nil {
		t.Fatal(err)
	}
	filter.Purpose = ""
	all, err := w.s.ListAddresses(w.ctx, filter, "", 10)
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("list after revision = %+v, %v; want three current versions", all, err)
	}
	for _, item := range all.Items {
		if item.ValidTo != nil || (item.ID == created[0].ID) != (item.City == "Oakland") {
			t.Fatalf("listed %+v; want only current versions with the revised city on the revised address", item)
		}
	}
}

func TestPostgres_AddressVersionsIsolationAndScope(t *testing.T) {
	w := newWarehouseWorld(t)
	owned, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1De.ID, business(true)), "")
	if err != nil {
		t.Fatal(err)
	}
	scoped := scopedTo(w.usmf.ID)
	if _, err := w.s.ReviseAddress(scoped, owned.ID, owned.VersionID, revisionOf(owned, store.AddressPatch{City: ptr("Oakland")}, "")); problemCode(t, err) != store.CodeAddressNotFound {
		t.Fatalf("scoped revise of a DEMF address error = %v", err)
	}
	if _, err := w.s.GetAddressVersion(scoped, owned.VersionID); problemCode(t, err) != store.CodeAddressNotFound {
		t.Fatalf("scoped version read error = %v", err)
	}
	if page, err := w.s.ListAddresses(scoped, store.AddressFilter{OwnerType: "warehouse", OwnerID: w.wh1De.ID}, "", 10); err != nil || len(page.Items) != 0 {
		t.Fatalf("scoped list = %+v, %v; want an empty page", page, err)
	}
	if _, err := w.s.GetAddressVersion(principalContext(2), owned.VersionID); problemCode(t, err) != store.CodeAddressNotFound {
		t.Fatalf("tenant 2 version read error = %v", err)
	}
	if w.rows(t, "postal_address") != 1 || len(w.unitEvents(addressChangedType)) != 1 {
		t.Fatal("a rejected scoped revise left rows or an event behind")
	}
}
