package store_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func TestEscapeLike(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "Springfield", "Springfield"},
		{"percent", "50%", `50\%`},
		{"underscore", "a_b", `a\_b`},
		{"backslash", `a\b`, `a\\b`},
		{"mixed", `100%_\x`, `100\%\_\\x`},
		{"empty", "", ""},
	} {
		if got := store.EscapeLike(tc.in); got != tc.want {
			t.Errorf("%s: EscapeLike(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestPostgres_AddressSearchIndexMigration(t *testing.T) {
	admin := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	body, err := fs.ReadFile(migrations.FS, "0026_address_search.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, string(body)); err != nil {
		t.Fatalf("re-applying 0026 = %v", err)
	}
	// The extension must not land in the migrating schema: a throwaway or service schema dropped
	// later would take it, and every index on gin_trgm_ops, with it.
	var extensionSchema string
	if err := admin.QueryRow(ctx, `select extnamespace::regnamespace::text from pg_extension where extname = 'pg_trgm'`).Scan(&extensionSchema); err != nil || extensionSchema != "public" {
		t.Fatalf("pg_trgm schema = %q, %v; want public", extensionSchema, err)
	}
	var indexes int
	if err := admin.QueryRow(ctx, `select count(*) from pg_indexes where indexname = 'postal_address_formatted_trgm' and schemaname = current_schema()`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("indexes = %d, %v; want exactly one", indexes, err)
	}
	var plan []string
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `explain select 1 from postal_address where formatted ilike '%' || 'springfield' || '%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "postal_address_formatted_trgm") {
		t.Fatalf("plan = %v; want the trigram index used by the search predicate", plan)
	}
}

func TestPostgres_ListAddressesSearchesLiterallyAcrossOwnersWithCursor(t *testing.T) {
	w := newWarehouseWorld(t)
	create := func(ownerType, ownerID, city, street string, purposes ...store.AddressPurpose) store.Address {
		in := newAddress(ownerType, ownerID, purposes...)
		in.City, in.Street1 = city, street
		created, err := w.s.CreateAddress(w.ctx, in, "")
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	springfield := create("warehouse", w.wh1.ID, "Springfield", "1 Main St", business(true))
	create("site", w.s1.ID, "Shelbyville", "2 Elm St", store.AddressPurpose{Purpose: "delivery"})
	discount := create("party", "00000000-0000-0000-0004-0000000000aa", "Discountville", "50% Off Plaza", business(true))
	underscore := create("party", "00000000-0000-0000-0004-0000000000bb", "Snake", "a_b Road", business(true))
	revisedFrom := create("warehouse", w.wh1.ID, "Oldtown", "3 Old Rd", business(false))
	if _, err := w.s.ReviseAddress(w.ctx, revisedFrom.ID, revisedFrom.VersionID, revisionOf(revisedFrom, store.AddressPatch{City: ptr("Newtown")}, "")); err != nil {
		t.Fatal(err)
	}
	search := func(f store.AddressFilter, after string, limit int) store.AddressPage {
		t.Helper()
		page, err := w.s.ListAddresses(w.ctx, f, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	if page := search(store.AddressFilter{Query: "SPRINGFIELD"}, "", 10); len(page.Items) != 1 || page.Items[0].ID != springfield.ID || len(page.Items[0].Purposes) != 1 {
		t.Fatalf("case-insensitive search = %+v; want the Springfield address with its purposes", page)
	}
	if page := search(store.AddressFilter{Query: "50%"}, "", 10); len(page.Items) != 1 || page.Items[0].ID != discount.ID {
		t.Fatalf("q=50%% = %+v; want only the literal match", page)
	}
	if page := search(store.AddressFilter{Query: "a_b"}, "", 10); len(page.Items) != 1 || page.Items[0].ID != underscore.ID {
		t.Fatalf("q=a_b = %+v; want only the literal match", page)
	}
	if page := search(store.AddressFilter{Query: "Oldtown"}, "", 10); len(page.Items) != 0 {
		t.Fatalf("superseded version found: %+v", page)
	}
	if page := search(store.AddressFilter{Query: "Newtown"}, "", 10); len(page.Items) != 1 {
		t.Fatalf("revised version not found: %+v", page)
	}
	if page := search(store.AddressFilter{Query: "St", CountryCode: "CA"}, "", 10); len(page.Items) != 0 {
		t.Fatalf("country filter CA = %+v; want none", page)
	}
	if page := search(store.AddressFilter{Query: "Main", CountryCode: "US", OwnerType: "warehouse", OwnerID: w.wh1.ID, Purpose: "business"}, "", 10); len(page.Items) != 1 {
		t.Fatalf("combined filters = %+v; want the one match", page)
	}
	first := search(store.AddressFilter{Query: "US"}, "", 2)
	second := search(store.AddressFilter{Query: "US"}, first.NextID, 2)
	third := search(store.AddressFilter{Query: "US"}, second.NextID, 2)
	seen := map[string]bool{}
	for _, item := range append(append(first.Items, second.Items...), third.Items...) {
		seen[item.ID] = true
	}
	if first.NextID == "" || len(seen) != 5 {
		t.Fatalf("paged search saw %d distinct addresses, first page %+v; want all 5 current ones across pages", len(seen), first)
	}
}

func TestPostgres_AddressSearchIsolationAndScope(t *testing.T) {
	w := newWarehouseWorld(t)
	for _, in := range []store.Address{
		newAddress("warehouse", w.wh1.ID, business(true)),
		newAddress("warehouse", w.wh1De.ID, business(true)),
		newAddress("operating_unit", mustCreateUnit(t, w.s, w.ctx, unit("department", "D100")).ID, business(true)),
	} {
		if _, err := w.s.CreateAddress(w.ctx, in, ""); err != nil {
			t.Fatal(err)
		}
	}
	filter := store.AddressFilter{Query: "Main"}
	if page, err := w.s.ListAddresses(w.ctx, filter, "", 10); err != nil || len(page.Items) != 3 {
		t.Fatalf("tenant 1 search = %+v, %v; want three", page, err)
	}
	if page, err := w.s.ListAddresses(principalContext(2), filter, "", 10); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 search = %+v, %v; want none", page, err)
	}
	page, err := w.s.ListAddresses(scopedTo(w.demf.ID), filter, "", 10)
	owners := map[string]bool{}
	for _, item := range page.Items {
		owners[item.OwnerType+":"+item.OwnerID] = true
	}
	if err != nil || len(owners) != 2 || !owners["warehouse:"+w.wh1De.ID] || owners["warehouse:"+w.wh1.ID] {
		t.Fatalf("DEMF-scoped search owners = %v, %v; want the DEMF warehouse and the operating unit only", owners, err)
	}
}

func TestPostgres_AddressAsOfAndVersionHistory(t *testing.T) {
	w := newWarehouseWorld(t)
	first := newAddress("warehouse", w.wh1.ID, business(true))
	first.ValidFrom = "2020-01-01"
	created, err := w.s.CreateAddress(w.ctx, first, "")
	if err != nil {
		t.Fatal(err)
	}
	revised, err := w.s.ReviseAddress(w.ctx, created.ID, created.VersionID, revisionOf(created, store.AddressPatch{Street1: ptr("9 New St")}, "2021-06-01"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ asOf, wantVersion string }{
		{"2020-01-01", created.VersionID},
		{"2021-03-15", created.VersionID},
		{"2021-05-31", created.VersionID},
		{"2021-06-01", revised.VersionID},
		{"2999-01-01", revised.VersionID},
	} {
		got, err := w.s.GetAddressAsOf(w.ctx, created.ID, tc.asOf)
		if err != nil || got.VersionID != tc.wantVersion || len(got.Purposes) != 1 {
			t.Fatalf("as of %s = %+v, %v; want version %s with purposes", tc.asOf, got, err, tc.wantVersion)
		}
	}
	if got, _ := w.s.GetAddressAsOf(w.ctx, created.ID, "2021-03-15"); got.Street1 != "1 Main St" {
		t.Fatalf("as-of street = %q; want the original", got.Street1)
	}
	if _, err := w.s.GetAddressAsOf(w.ctx, created.ID, "2019-12-31"); problemCode(t, err) != store.CodeAddressNotFound {
		t.Fatalf("as of before the first version error = %v", err)
	}
	if _, err := w.s.GetAddressAsOf(principalContext(2), created.ID, "2021-03-15"); problemCode(t, err) != store.CodeAddressNotFound {
		t.Fatalf("tenant 2 as-of error = %v", err)
	}
	versions, err := w.s.ListAddressVersions(w.ctx, created.ID)
	if err != nil || len(versions) != 2 || versions[0].VersionID != revised.VersionID || versions[0].ValidTo != nil ||
		versions[1].VersionID != created.VersionID || versions[1].ValidTo == nil || *versions[1].ValidTo != versions[0].ValidFrom || versions[1].ValidFrom != "2020-01-01" {
		t.Fatalf("versions = %+v, %v; want newest first with the closed validTo equal to the newer validFrom", versions, err)
	}
	single, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1.ID, store.AddressPurpose{Purpose: "delivery"}), "")
	if err != nil {
		t.Fatal(err)
	}
	if one, err := w.s.ListAddressVersions(w.ctx, single.ID); err != nil || len(one) != 1 || one[0].ValidTo != nil {
		t.Fatalf("single-version history = %+v, %v", one, err)
	}
	for name, ctx := range map[string]func() error{
		"tenant 2": func() error { _, err := w.s.ListAddressVersions(principalContext(2), created.ID); return err },
		"unknown": func() error {
			_, err := w.s.ListAddressVersions(w.ctx, "00000000-0000-0000-0003-0000000000ff")
			return err
		},
	} {
		if problemCode(t, ctx()) != store.CodeAddressNotFound {
			t.Fatalf("%s versions did not return not-found", name)
		}
	}
	owned, err := w.s.CreateAddress(w.ctx, newAddress("warehouse", w.wh1De.ID, business(true)), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.ListAddressVersions(scopedTo(w.usmf.ID), owned.ID); problemCode(t, err) != store.CodeAddressNotFound {
		t.Fatalf("scoped versions of a DEMF address error = %v", err)
	}
}
