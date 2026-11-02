package store_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

const leB2 = "00000000-0000-7000-8000-0000000000b2"

// hubFor is the integration hub's token for legal entity le of tenant 1.
func hubFor(le string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "service:integration-hub", Service: store.HubService,
		Duties: map[string]struct{}{store.DutyOutboundRender: {}, store.DutyXrefRecord: {}}})
}

func xrefLE(t *testing.T, s store.Store, admin context.Context, entity, key string) string {
	t.Helper()
	rows, err := s.ListXref(admin, store.XrefFilter{Entity: entity, SourceKey: key})
	if err != nil || len(rows) != 1 {
		t.Fatalf("xref %s/%s = %+v %v", entity, key, rows, err)
	}
	return rows[0].LegalEntityID
}

// exerciseXrefLegalEntity is the contract of the legal entity on the cross-reference, run against Postgres (app
// role) and Mem: a lookup for one legal entity resolves its own rows and the tenant-wide ones, never another legal
// entity's, and xref:record tags the rows of a legal-entity-scoped subject with the token's legal entity.
func exerciseXrefLegalEntity(t *testing.T, s store.Store) {
	admin := adminContext(1)
	hubA, hubB := hubFor(leA), hubFor(leB2)
	saved, _, err := s.SaveOutboundMapping(admin, outboundIn("Orders"), "")
	if err != nil {
		t.Fatal(err)
	}
	partyDef := strings.Replace(outboundDefJSON, `"subject":{"entity":"salesOrder"`, `"subject":{"entity":"party"`, 1)
	partyMap := outboundIn("Parties")
	partyMap.Definition = []byte(partyDef)
	partyMap.EventType = "dev.passage.sales.order.*.v1"
	pm, _, err := s.SaveOutboundMapping(admin, partyMap, "")
	if err != nil {
		t.Fatal(err)
	}

	// What the loader wrote: a customer role per legal entity, a tenant-wide party.
	ca, cb, party := ids.New(), ids.New(), ids.New()
	put := func(entity, key, target, le string) {
		t.Helper()
		if err := s.Xref().Put(admin, domain.XrefKey{SourceSystem: "custom_acme", Entity: entity, SourceKey: key}, domain.XrefEntry{TargetID: target, ContentHash: "h", LegalEntityID: le}); err != nil {
			t.Fatal(err)
		}
	}
	put("customer", "C-A", ca, leA)
	put("customer", "C-B", cb, leB2)
	put("party", "P-1", party, "")
	look := func(ctx context.Context, entity string, id string) string {
		t.Helper()
		got, err := s.LegacyKeys(ctx, "custom_acme", entity, []string{id})
		if err != nil {
			t.Fatal(err)
		}
		return got[id]
	}
	if look(hubA, "customer", ca) != "C-A" || look(hubB, "customer", cb) != "C-B" {
		t.Fatal("a legal entity must resolve its own rows")
	}
	if k := look(hubA, "customer", cb); k != "" {
		t.Fatalf("legal entity A resolved legal entity B's customer: %q", k)
	}
	if k := look(hubB, "customer", ca); k != "" {
		t.Fatalf("legal entity B resolved legal entity A's customer: %q", k)
	}
	if look(hubA, "party", party) != "P-1" || look(hubB, "party", party) != "P-1" {
		t.Fatal("a tenant-wide row must resolve in every legal entity")
	}
	if look(admin, "customer", ca) != "C-A" || look(admin, "customer", cb) != "C-B" {
		t.Fatal("a tenant administrator without a legal-entity claim sees every row")
	}
	// A record with a key in each legal entity answers each caller with its own.
	both := ids.New()
	put("customer", "X-A", both, leA)
	put("customer", "X-B", both, leB2)
	if look(hubA, "customer", both) != "X-A" || look(hubB, "customer", both) != "X-B" {
		t.Fatal("each legal entity must get its own key for a record that has one in each")
	}

	// A tagged row is never moved to another legal entity by a later load.
	put("customer", "C-A", ca, leB2)
	if xrefLE(t, s, admin, "customer", "C-A") != leA {
		t.Fatal("a later put must not retag a row")
	}

	// A rerun tags an untagged row once (an unchanged row that names its legal entity); a tagged row keeps its tag.
	put("customer", "C-OLD", ids.New(), "")
	if err := s.Xref().TagLegalEntities(admin, "custom_acme", "customer", map[string]string{"C-OLD": leB2, "C-A": leB2, "C-NONE": leB2}); err != nil {
		t.Fatal(err)
	}
	if xrefLE(t, s, admin, "customer", "C-OLD") != leB2 || xrefLE(t, s, admin, "customer", "C-A") != leA {
		t.Fatal("tagging must set an untagged row and leave a tagged one")
	}
	if rows, _ := s.ListXref(admin, store.XrefFilter{Entity: "customer", SourceKey: "C-NONE"}); len(rows) != 0 {
		t.Fatalf("tagging created a row: %+v", rows)
	}

	// xref:record tags a legal-entity-scoped subject with the token's legal entity and leaves a tenant-wide one untagged.
	order := ids.New()
	rec := store.OutboundXrefRecord{MappingID: saved.ID, EventID: "e1", LegacyKey: "SO-1", Refs: []domain.OutboundRef{{Entity: "salesOrder", ID: order}}}
	if _, err := s.RecordOutboundXref(hubA, rec); err != nil {
		t.Fatal(err)
	}
	if xrefLE(t, s, admin, "salesOrder", "SO-1") != leA {
		t.Fatal("xref:record must tag the row with the token's legal entity")
	}
	if look(hubA, "salesOrder", order) != "SO-1" || look(hubB, "salesOrder", order) != "" {
		t.Fatal("the recorded order must resolve in its own legal entity only")
	}
	// Another legal entity cannot record against the same key, nor against the same record.
	other := rec
	other.EventID = "e2"
	_, err = s.RecordOutboundXref(hubB, other)
	problemStatus(t, err, 409, store.CodeXrefConflict)
	if xrefLE(t, s, admin, "salesOrder", "SO-1") != leA {
		t.Fatal("a refused record must not retag the row")
	}
	pRec := store.OutboundXrefRecord{MappingID: pm.ID, EventID: "p1", LegacyKey: "CUST-9", Refs: []domain.OutboundRef{{Entity: "party", ID: ids.New()}}}
	if _, err := s.RecordOutboundXref(hubA, pRec); err != nil {
		t.Fatal(err)
	}
	if xrefLE(t, s, admin, "party", "CUST-9") != "" {
		t.Fatal("a tenant-wide subject entity must stay untagged")
	}
	if look(hubB, "party", pRec.Refs[0].ID) != "CUST-9" {
		t.Fatal("a tenant-wide subject must resolve in every legal entity")
	}

	// A row without a tag (loaded before the column) is accepted but never claimed: one legal entity's token cannot
	// prove the record is its own, so tagging it would take it away from the legal entity it belongs to.
	old := ids.New()
	put("salesOrder", "SO-OLD", old, "")
	if _, err := s.RecordOutboundXref(hubA, store.OutboundXrefRecord{MappingID: saved.ID, EventID: "e3", LegacyKey: "SO-OLD", Refs: []domain.OutboundRef{{Entity: "salesOrder", ID: old}}}); err != nil {
		t.Fatal(err)
	}
	if xrefLE(t, s, admin, "salesOrder", "SO-OLD") != "" {
		t.Fatal("xref:record must not tag a row it did not create")
	}
	if _, err := s.RecordOutboundXref(hubB, store.OutboundXrefRecord{MappingID: saved.ID, EventID: "e4", LegacyKey: "SO-OLD", Refs: []domain.OutboundRef{{Entity: "salesOrder", ID: old}}}); err != nil {
		t.Fatal(err)
	}
	if look(hubB, "salesOrder", old) != "SO-OLD" {
		t.Fatal("an untagged row must keep resolving in the other legal entity after a record")
	}
	// Nor an inbound load's row tagged with another legal entity: the hub of legal entity B cannot record against A's
	// customer, whatever the mapping's subject entity.
	custMap := outboundIn("Customers")
	custMap.Definition = []byte(strings.Replace(outboundDefJSON, `"subject":{"entity":"salesOrder"`, `"subject":{"entity":"customer"`, 1))
	cm, _, err := s.SaveOutboundMapping(admin, custMap, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RecordOutboundXref(hubB, store.OutboundXrefRecord{MappingID: cm.ID, EventID: "c1", LegacyKey: "C-A", Refs: []domain.OutboundRef{{Entity: "customer", ID: ca}}})
	problemStatus(t, err, 409, store.CodeXrefConflict)
	if xrefLE(t, s, admin, "customer", "C-A") != leA {
		t.Fatal("a refused record must leave the inbound row as it was")
	}
}

func TestMem_XrefLegalEntity(t *testing.T) { exerciseXrefLegalEntity(t, store.NewMem()) }

func TestPostgres_XrefLegalEntityUnderRLS(t *testing.T) {
	_, pool := outboundDB(t)
	s := store.New(pool)
	exerciseXrefLegalEntity(t, s)

	// The database itself keeps a scoped session to its own legal entity's rows: the tenant-wide rows are visible
	// to the all-entities scope only (the outbound store filters them in by hand).
	count := func(le string, where string) (n int, err error) {
		ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le})
		err = dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `select count(*) from migration.xref where `+where).Scan(&n)
		})
		return n, err
	}
	if n, err := count(leA, `source_entity = 'customer'`); err != nil || n != 2 { // C-A and X-A (C-OLD is B's)
		t.Fatalf("legal entity A sees %d customer rows: %v", n, err)
	}
	if n, _ := count(leA, `legal_entity_id = '`+leB2+`'`); n != 0 {
		t.Fatalf("legal entity A sees %d rows of legal entity B", n)
	}
	if n, _ := count(leA, `legal_entity_id is null`); n != 0 {
		t.Fatalf("a scoped session sees %d tenant-wide rows", n)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leA})
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `insert into migration.xref (tenant_id, source_system, source_entity, source_key, target_id, legal_entity_id)
			values ($1, 'custom_acme', 'customer', 'C-EVIL', $2, $3)`, testkit.TenantID(1), ids.New(), leB2)
		return err
	})
	if err == nil {
		t.Fatal("a session scoped to legal entity A wrote a row for legal entity B")
	}
	if n, _ := count(leB2, `source_key = 'C-A'`); n != 0 {
		t.Fatal("legal entity B sees A's row")
	}
}

// 0014 backfills the legal entity from the project of the runs that loaded the key, only where one legal entity is
// proven, and leaves everything else untagged.
func TestPostgres_XrefLegalEntityBackfill(t *testing.T) {
	admin, _ := testkit.DBWithApp(t)
	ctx := context.Background()
	_, _ = admin.Exec(ctx, "drop schema if exists migration cascade")
	_, _ = admin.Exec(ctx, "drop table if exists schema_migrations")
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade")
	})
	before := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") && e.Name() < "0013" {
			b, _ := fs.ReadFile(migrations.FS, e.Name())
			before[e.Name()] = &fstest.MapFile{Data: b}
		}
	}
	if err := dbx.Migrate(ctx, admin, before); err != nil {
		t.Fatal(err)
	}
	tn := testkit.TenantID(1)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	pa, pb, pplain := ids.New(), ids.New(), ids.New()
	for _, p := range []struct{ id, name, le string }{{pa, "pa", leA}, {pb, "pb", leB2}} {
		exec(`insert into migration.project (tenant_id, id, name, project_type, source_system, target_legal_entity_id) values ($1, $2, $3, 'migrate', 'ax2012', $4)`, tn, p.id, p.name, p.le)
	}
	exec(`insert into migration.project (tenant_id, id, name, project_type, source_system) values ($1, $2, 'plain', 'migrate', 'ax2012')`, tn, pplain)
	run := func(project, entity string, dry bool) string {
		id := ids.New()
		exec(`insert into migration.run (tenant_id, id, source_system, mode, entity, status, dry_run, mapping_version, mapping, report, project_id)
			values ($1, $2, 'ax2012', 'cutover', $3, 'succeeded', $4, 1, '{}', '{}', nullif($5, '')::uuid)`, tn, id, entity, dry, project)
		return id
	}
	row := func(entity, key, runID string) {
		target := ids.New()
		exec(`insert into migration.xref (tenant_id, source_system, source_entity, source_key, target_id) values ($1, 'ax2012', $2, $3, $4)`, tn, entity, key, target)
		if runID != "" {
			exec(`insert into migration.run_ledger (tenant_id, run_id, source_key, target_id, idempotency_key, content_hash, action) values ($1, $2, $3, $4, 'i-'||$3, 'h', 'created')`, tn, runID, key, target)
		}
	}
	rA, rB, rPlain, rDry := run(pa, "customer", false), run(pb, "customer", false), run(pplain, "customer", false), run(pa, "customer", true)
	rItem := run(pa, "ar_open_item", false)
	row("customer", "ONLY-A", rA)
	row("customer", "ONLY-B", rB)
	row("customer", "NO-PROJECT-LE", rPlain)
	row("customer", "DRY-ONLY", rDry)
	row("customer", "NO-LEDGER", "")
	row("ar_open_item", "INV-1", rItem)
	row("party", "PARTY-1", "") // tenant-wide entity: never tagged
	// A key whose ledger lines sit under projects of two legal entities is ambiguous and stays untagged.
	row("customer", "BOTH", rA)
	var target string
	if err := admin.QueryRow(ctx, `select target_id::text from migration.xref where source_key = 'BOTH'`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	exec(`insert into migration.run_ledger (tenant_id, run_id, source_key, target_id, idempotency_key, content_hash, action) values ($1, $2, 'BOTH', $3, 'i2', 'h', 'updated')`, tn, rB, target)
	// A run under legal entity B's project that skipped the key as unchanged did not load it: still legal entity A.
	row("customer", "SKIPPED-IN-B", rA)
	exec(`insert into migration.run_ledger (tenant_id, run_id, source_key, target_id, idempotency_key, content_hash, action)
		select tenant_id, $1::uuid, source_key, target_id, 'is', 'h', 'unchanged' from migration.xref where source_key = 'SKIPPED-IN-B'`, rB)
	// A party loaded under a project of legal entity A stays tenant-wide.
	rParty := run(pa, "party", false)
	exec(`insert into migration.run_ledger (tenant_id, run_id, source_key, target_id, idempotency_key, content_hash, action)
		select tenant_id, $1::uuid, source_key, target_id, 'ip', 'h', 'created' from migration.xref where source_key = 'PARTY-1'`, rParty)

	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"ONLY-A": leA, "ONLY-B": leB2, "NO-PROJECT-LE": "", "DRY-ONLY": "", "NO-LEDGER": "", "INV-1": leA, "PARTY-1": "", "BOTH": "", "SKIPPED-IN-B": leA}
	rows, err := admin.Query(ctx, `select source_key, coalesce(legal_entity_id::text, '') from migration.xref`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var k, le string
		if err := rows.Scan(&k, &le); err != nil {
			t.Fatal(err)
		}
		if w, ok := want[k]; !ok || w != le {
			t.Errorf("%s: legal entity %q, want %q", k, le, w)
		}
		seen++
	}
	if seen != len(want) {
		t.Fatalf("saw %d rows, want %d", seen, len(want))
	}
}
