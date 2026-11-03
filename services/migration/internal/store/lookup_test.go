package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const uomDefJSON = `{"kind":"uom_conversion","entries":{"KG|LB":"2.20462","LB|KG":"0.453592"}}`

func lookupIn(name string) store.LookupTable {
	return store.LookupTable{Name: name, Kind: "uom_conversion", Definition: json.RawMessage(uomDefJSON), CreatedBy: "ben"}
}

func exerciseLookup(t *testing.T, s store.Store, one, two, hub, hub2 context.Context, tenantAware bool) {
	saved, created, err := s.SaveLookupTable(one, lookupIn("uom"), "k1")
	if err != nil || !created || saved.ID == "" || saved.Version != 1 || saved.Status != "active" || saved.EntryCount != 2 {
		t.Fatalf("save = %+v %v %v", saved, created, err)
	}
	if again, created, err := s.SaveLookupTable(one, lookupIn("uom"), "k1"); err != nil || created || again.ID != saved.ID {
		t.Fatalf("same key must replay: %+v %v %v", again, created, err)
	}
	_, _, err = s.SaveLookupTable(one, lookupIn("other"), "k1")
	problemStatus(t, err, 422, "idempotency_key_reuse")
	_, _, err = s.SaveLookupTable(one, lookupIn("uom"), "k2")
	problemStatus(t, err, 409, store.CodeLookupNameExists)

	edited, err := s.UpdateLookupTable(one, saved.ID, 1, func(t *store.LookupTable) error {
		t.Definition = json.RawMessage(`{"kind":"uom_conversion","entries":{"KG|LB":"2.2046","LB|KG":"0.4536","KG|G":"1000"}}`)
		return nil
	})
	if err != nil || edited.Version != 2 || edited.EntryCount != 3 || edited.Checksum == saved.Checksum {
		t.Fatalf("update = %+v %v", edited, err)
	}
	_, err = s.UpdateLookupTable(one, saved.ID, 1, func(*store.LookupTable) error { return nil })
	problemStatus(t, err, 409, store.CodeVersionConflict)

	// Version 1 stays readable byte for byte; the current one is the default.
	v1, err := s.GetLookupTable(one, saved.ID, 1)
	if err != nil || v1.Version != 1 || v1.EntryCount != 2 || v1.Checksum != saved.Checksum {
		t.Fatalf("v1 = %+v %v", v1, err)
	}
	cur, err := s.GetLookupTable(one, saved.ID, 0)
	if err != nil || cur.Version != 2 || cur.EntryCount != 3 {
		t.Fatalf("current = %+v %v", cur, err)
	}
	_, err = s.GetLookupTable(one, saved.ID, 9)
	problemStatus(t, err, 404, store.CodeLookupVersionGone)
	vs, err := s.ListLookupVersions(one, saved.ID)
	if err != nil || len(vs) != 2 || vs[0].Version != 2 || vs[0].EntryCount != 3 || vs[1].EntryCount != 2 {
		t.Fatalf("versions = %+v %v", vs, err)
	}

	// The hub resolves entries of the current version; a missing key is absent, a missing table is not found.
	ans, err := s.LookupValues(hub, "uom", []string{"KG|LB", "KG|OZ"})
	if err != nil || !ans.Found || ans.Archived || ans.Kind != "uom_conversion" || ans.Version != 2 || len(ans.Values) != 1 || ans.Values["KG|LB"] != "2.2046" {
		t.Fatalf("lookup = %+v %v", ans, err)
	}
	if ans, err = s.LookupValues(hub, "nope", []string{"x"}); err != nil || ans.Found {
		t.Fatalf("missing table = %+v %v", ans, err)
	}

	// Another tenant sees nothing, and may reuse the name.
	if tenantAware {
		if ans, err = s.LookupValues(hub2, "uom", []string{"KG|LB"}); err != nil || ans.Found {
			t.Fatalf("tenant 2 resolves tenant 1's table: %+v %v", ans, err)
		}
		if _, err := s.GetLookupTable(two, saved.ID, 0); err == nil {
			t.Fatal("tenant 2 read tenant 1's table")
		}
		if _, created, err := s.SaveLookupTable(two, lookupIn("uom"), ""); err != nil || !created {
			t.Fatalf("tenant 2 same name: %v %v", created, err)
		}
	}

	// Archiving keeps the entries but stops them resolving.
	arch, err := s.UpdateLookupTable(one, saved.ID, 2, func(t *store.LookupTable) error { t.Status = store.OutboundArchivedStatus; return nil })
	if err != nil || arch.Status != "archived" || arch.Version != 3 {
		t.Fatalf("archive = %+v %v", arch, err)
	}
	if ans, err = s.LookupValues(hub, "uom", []string{"KG|LB"}); err != nil || !ans.Found || !ans.Archived || ans.Version != 3 || len(ans.Values) != 0 {
		t.Fatalf("archived lookup = %+v %v", ans, err)
	}

	// Listing is ordered by name, filterable and pageable.
	for _, n := range []string{"b-enum", "a-enum"} {
		in := store.LookupTable{Name: n, Kind: "enum", Definition: json.RawMessage(`{"kind":"enum","entries":{"kg":"KGM"}}`)}
		if _, _, err := s.SaveLookupTable(one, in, ""); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListLookupTables(one, store.LookupFilter{Limit: 10})
	if err != nil || len(all) != 3 || all[0].Name != "a-enum" || all[2].Name != "uom" || all[0].Definition != nil {
		t.Fatalf("list = %+v %v", all, err)
	}
	if enums, _ := s.ListLookupTables(one, store.LookupFilter{Kind: "enum", Limit: 10}); len(enums) != 2 {
		t.Fatalf("enum filter = %+v", enums)
	}
	if active, _ := s.ListLookupTables(one, store.LookupFilter{Status: "archived", Limit: 10}); len(active) != 1 || active[0].Name != "uom" {
		t.Fatalf("archived filter = %+v", active)
	}
	page, _ := s.ListLookupTables(one, store.LookupFilter{After: &store.ProjectCursor{Name: all[0].Name, ID: all[0].ID}, Limit: 10})
	if len(page) != 2 || page[0].Name != "b-enum" {
		t.Fatalf("after = %+v", page)
	}
}

func TestMem_LookupContract(t *testing.T) {
	exerciseLookup(t, store.NewMem(), adminContext(1), adminContext(2), hubContext(1), hubContext(2), false)
}

func TestPostgres_LookupTablesUnderRLS(t *testing.T) {
	_, pool := outboundDB(t) // the app role proves the 0017 grants are enough for the store
	exerciseLookup(t, store.New(pool), adminContext(1), adminContext(2), hubContext(1), hubContext(2), true)

	var visible int
	err := dbx.WithTenantTx(principalContext(3), pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select (select count(*) from migration.lookup_table) + (select count(*) from migration.lookup_table_version)`).Scan(&visible)
	})
	if err != nil || visible != 0 {
		t.Fatalf("tenant 3 sees %d lookup rows: %v", visible, err)
	}
}

// Lookup tables are tenant data like outbound mappings: a legal-entity scoped session and a caller without the duty
// are refused before the transaction opens.
func TestPostgres_LookupScopeRefusals(t *testing.T) {
	_, pool := outboundDB(t)
	s := store.New(pool)
	if _, _, err := s.SaveLookupTable(hubContext(1), lookupIn("x"), ""); err == nil {
		t.Fatal("the hub must not write lookup tables")
	}
	if _, err := s.LookupValues(principalContext(1), "x", []string{"k"}); err == nil {
		t.Fatal("a scoped session must not read lookup tables")
	}
}
