package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/testdb"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

const outboundDefJSON = `{"eventType":"dev.passage.sales.order.*.v1","subject":{"entity":"salesOrder","idPath":"data.order_id"},"xrefs":[{"name":"customer","entity":"customer","idPath":"data.customer_id"}],"mapping":{"entity":"SalesOrder","version":1,"fields":[{"source":"xref.customer","target":"CustAccount","type":"string","required":true}]}}`

func outboundIn(name string) store.OutboundMapping {
	return store.OutboundMapping{Name: name, SourceSystem: "custom_acme", EventType: "dev.passage.sales.order.*.v1", Definition: json.RawMessage(outboundDefJSON), CreatedBy: "ben"}
}

// adminContext is a tenant administrator without a legal-entity claim holding both human duties of the outbound
// routes (the all-scope duty is for the xref seeding through the inbound store).
func adminContext(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: "admin-" + strconv.Itoa(n), Duties: map[string]struct{}{
		tenant.DutyAllScope: {}, tenant.DutyTenantAdmin: {}, store.DutyRun: {}, store.DutyProjectWrite: {}}})
}

// hubContext is the principal of a verified integration-hub service token: one legal entity, its two duties.
func hubContext(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: leA, UserID: "service:integration-hub", Service: store.HubService,
		Duties: map[string]struct{}{store.DutyOutboundRender: {}, store.DutyXrefRecord: {}}})
}

// exerciseOutbound runs against Postgres (under the app role) and Mem so the fake keeps matching the contract. one and
// two are tenant administrators of tenants 1 and 2, hub and hub2 the integration hub's tokens for them.
func exerciseOutbound(t *testing.T, s store.Store, one, two, hub, hub2 context.Context, tenantAware bool) {
	saved, created, err := s.SaveOutboundMapping(one, outboundIn("Orders to Acme"), "k1")
	if err != nil || !created || saved.ID == "" || saved.Version != 1 || saved.Status != "active" || saved.Checksum == "" {
		t.Fatalf("save = %+v %v %v", saved, created, err)
	}
	if again, created, err := s.SaveOutboundMapping(one, outboundIn("Orders to Acme"), "k1"); err != nil || created || again.ID != saved.ID {
		t.Fatalf("same key must replay: %+v %v %v", again, created, err)
	}
	_, _, err = s.SaveOutboundMapping(one, outboundIn("Other"), "k1")
	problemStatus(t, err, 422, "idempotency_key_reuse")
	_, _, err = s.SaveOutboundMapping(one, outboundIn("Orders to Acme"), "k2")
	problemStatus(t, err, 409, store.CodeOutboundNameExists)

	// Version pinning: an edit writes version 2 and version 1 stays readable, byte for byte.
	v2def := strings.Replace(outboundDefJSON, `"required":true`, `"required":false`, 1)
	edited, err := s.UpdateOutboundMapping(one, saved.ID, 1, func(m *store.OutboundMapping) error {
		m.Definition = json.RawMessage(v2def)
		return nil
	})
	if err != nil || edited.Version != 2 || edited.Checksum == saved.Checksum {
		t.Fatalf("update = %+v %v", edited, err)
	}
	_, err = s.UpdateOutboundMapping(one, saved.ID, 1, func(*store.OutboundMapping) error { return nil })
	problemStatus(t, err, 409, store.CodeVersionConflict)
	cur, err := s.GetOutboundMapping(one, saved.ID, 0)
	if err != nil || cur.Version != 2 || !hasRequired(cur.Definition, false) {
		t.Fatalf("current = %+v %v", cur, err)
	}
	old, err := s.GetOutboundMapping(one, saved.ID, 1)
	if err != nil || old.Version != 1 || old.Checksum != saved.Checksum || !hasRequired(old.Definition, true) {
		t.Fatalf("pinned v1 = %+v %v", old, err)
	}
	_, err = s.GetOutboundMapping(one, saved.ID, 3)
	problemStatus(t, err, 404, store.CodeOutboundVersionGone)
	// A rename carries the definition into a new version.
	renamed, err := s.UpdateOutboundMapping(one, saved.ID, 2, func(m *store.OutboundMapping) error { m.Name = "Orders to Acme ERP"; return nil })
	if err != nil || renamed.Version != 3 || renamed.Name != "Orders to Acme ERP" || renamed.Checksum != edited.Checksum {
		t.Fatalf("rename = %+v %v", renamed, err)
	}
	if versions, err := s.ListOutboundVersions(one, saved.ID); err != nil || len(versions) != 3 || versions[0].Version != 3 || versions[2].Version != 1 {
		t.Fatalf("versions = %+v %v", versions, err)
	}
	if list, err := s.ListOutboundMappings(one, store.OutboundFilter{}); err != nil || len(list) != 1 || list[0].Definition != nil || list[0].Version != 3 {
		t.Fatalf("list = %+v %v", list, err)
	}

	// Tenant isolation: tenant 2 sees nothing of tenant 1 and may reuse the name.
	if tenantAware {
		if _, err := s.GetOutboundMapping(two, saved.ID, 0); err == nil {
			t.Fatal("tenant 2 read tenant 1's mapping")
		}
		if list, _ := s.ListOutboundMappings(two, store.OutboundFilter{}); len(list) != 0 {
			t.Fatalf("tenant 2 lists %d mappings", len(list))
		}
		if _, err := s.UpdateOutboundMapping(two, saved.ID, 3, func(*store.OutboundMapping) error { return nil }); err == nil {
			t.Fatal("tenant 2 updated tenant 1's mapping")
		}
		if _, err := s.RecordOutboundXref(hub2, store.OutboundXrefRecord{MappingID: saved.ID, EventID: "e1", LegacyKey: "SO-1", Refs: []domain.OutboundRef{{Entity: "salesOrder", ID: ids.New()}}}); err == nil {
			t.Fatal("tenant 2 recorded against tenant 1's mapping")
		}
		if _, _, err := s.SaveOutboundMapping(two, outboundIn("Orders to Acme ERP"), ""); err != nil {
			t.Fatalf("tenant 2 may reuse the name: %v", err)
		}
	}

	// Legacy keys: reverse lookup of an inbound load, lowest key wins, merged records answer to the survivor.
	cust, other := ids.New(), ids.New()
	put := func(ctx context.Context, key, target string) {
		t.Helper()
		if err := s.Xref().Put(ctx, domain.XrefKey{SourceSystem: "custom_acme", Entity: "customer", SourceKey: key}, domain.XrefEntry{TargetID: target}); err != nil {
			t.Fatal(err)
		}
	}
	put(one, "C-2000", cust)
	put(one, "C-1001", cust)
	put(one, "C-3000", other)
	got, err := s.LegacyKeys(one, "custom_acme", "customer", []string{cust, other, ids.New(), "not-a-uuid", strings.ToUpper(cust)})
	if err != nil || got[cust] != "C-1001" || got[other] != "C-3000" || got["not-a-uuid"] != "" || got[strings.ToUpper(cust)] != "C-1001" || len(got) != 3 {
		t.Fatalf("legacy keys = %+v %v", got, err)
	}
	if none, _ := s.LegacyKeys(one, "custom_acme", "vendor", []string{cust}); len(none) != 0 {
		t.Fatalf("another entity must not resolve: %+v", none)
	}
	if none, _ := s.LegacyKeys(one, "custom_other", "customer", []string{cust}); len(none) != 0 {
		t.Fatalf("another system must not resolve: %+v", none)
	}
	if none, _ := s.LegacyKeys(hub2, "custom_acme", "customer", []string{cust}); tenantAware && len(none) != 0 {
		t.Fatalf("tenant 2 resolved tenant 1's key: %+v", none)
	}

	// xref:record: idempotent on the event, refuses a different key for the same event and a key owned by another record.
	order := ids.New()
	rec := store.OutboundXrefRecord{MappingID: saved.ID, EventID: "evt-1", LegacyKey: "SO-77", Refs: []domain.OutboundRef{{Entity: "salesOrder", ID: order}}, Actor: "service:integration-hub"}
	if replayed, err := s.RecordOutboundXref(hub, rec); err != nil || replayed {
		t.Fatalf("record = %v %v", replayed, err)
	}
	if replayed, err := s.RecordOutboundXref(hub, rec); err != nil || !replayed {
		t.Fatalf("a repeat of the same event must be a no-op: %v %v", replayed, err)
	}
	if keys, _ := s.LegacyKeys(hub, "custom_acme", "salesOrder", []string{order}); keys[order] != "SO-77" {
		t.Fatalf("recorded key must resolve: %+v", keys)
	}
	diff := rec
	diff.LegacyKey = "SO-78"
	_, err = s.RecordOutboundXref(hub, diff)
	problemStatus(t, err, 409, store.CodeXrefEventConflict)
	stolen := rec
	stolen.EventID, stolen.Refs = "evt-2", []domain.OutboundRef{{Entity: "salesOrder", ID: ids.New()}}
	_, err = s.RecordOutboundXref(hub, stolen)
	problemStatus(t, err, 409, store.CodeXrefConflict)
	if keys, _ := s.LegacyKeys(hub, "custom_acme", "salesOrder", []string{order}); keys[order] != "SO-77" {
		t.Fatalf("a refused record must not change the cross-reference: %+v", keys)
	}
	// Another event for the same record and key (a retry under a new event id) is accepted: the key already points here.
	again := rec
	again.EventID = "evt-3"
	if replayed, err := s.RecordOutboundXref(hub, again); err != nil || replayed {
		t.Fatalf("same record and key under a new event: %v %v", replayed, err)
	}
	_, err = s.RecordOutboundXref(hub, store.OutboundXrefRecord{MappingID: ids.New(), EventID: "e", LegacyKey: "K", Refs: rec.Refs})
	problemStatus(t, err, 404, store.CodeOutboundNotFound)
	// Only the mapping's subject entity may be recorded: the hub cannot write an inbound entity's cross-reference.
	inbound := rec
	inbound.EventID, inbound.Refs = "evt-4", []domain.OutboundRef{{Entity: "customer", ID: ids.New()}}
	_, err = s.RecordOutboundXref(hub, inbound)
	problemStatus(t, err, 422, store.CodeXrefRefMismatch)
	// A mapping without a subject records the event and nothing in the cross-reference; the retry replays.
	none := rec
	none.EventID, none.Refs, none.LegacyKey = "evt-5", nil, "J-1"
	if replayed, err := s.RecordOutboundXref(hub, none); err != nil || replayed {
		t.Fatalf("record without refs = %v %v", replayed, err)
	}
	if replayed, err := s.RecordOutboundXref(hub, none); err != nil || !replayed {
		t.Fatalf("repeat without refs = %v %v", replayed, err)
	}
	if keys, _ := s.LegacyKeys(hub, "custom_acme", "salesOrder", []string{order}); keys[order] != "SO-77" {
		t.Fatalf("a record without refs must not touch the cross-reference: %+v", keys)
	}
}

func TestMem_OutboundContract(t *testing.T) {
	exerciseOutbound(t, store.NewMem(), adminContext(1), adminContext(2), hubContext(1), hubContext(2), false)
}

func TestPostgres_OutboundMappingsUnderRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t) // the app role proves the 0012 grants are enough for the store
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	exerciseOutbound(t, store.New(pool), adminContext(1), adminContext(2), hubContext(1), hubContext(2), true)

	// Rows are tenant-bound in the database itself, whatever the store does: as the app role with tenant 2's
	// setting, tenant 1's rows are invisible and cannot be written.
	var visible int
	err := dbx.WithTenantTx(principalContext(2), pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select (select count(*) from migration.outbound_mapping where created_by = 'admin-1')
			+ (select count(*) from migration.outbound_xref_event) + (select count(*) from migration.outbound_mapping_version where version = 3)`).Scan(&visible)
	})
	if err != nil || visible != 0 {
		t.Fatalf("tenant 2 sees %d of tenant 1's outbound rows: %v", visible, err)
	}
}

func outboundDB(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return admin, pool
}

// The hub's token names one legal entity, the tables admit only the all-entities scope. The outbound store opens it
// for the hub's token and for a tenant administrator only, each with its own duty; every other caller is refused
// before the transaction, and the rest of the service stays closed to the hub's scoped session.
func TestPostgres_OutboundScopeOnlyForTheHubAndTenantAdmins(t *testing.T) {
	_, pool := outboundDB(t)
	s := store.New(pool)
	saved, _, err := s.SaveOutboundMapping(adminContext(1), outboundIn("Orders"), "")
	if err != nil {
		t.Fatal(err)
	}
	cust := ids.New()
	if err := s.Xref().Put(adminContext(1), domain.XrefKey{SourceSystem: "custom_acme", Entity: "customer", SourceKey: "C-1"}, domain.XrefEntry{TargetID: cust}); err != nil {
		t.Fatal(err)
	}
	hub := hubContext(1)
	if got, err := s.GetOutboundMapping(hub, saved.ID, 0); err != nil || got.Name != "Orders" {
		t.Fatalf("the hub reads the mapping: %+v %v", got, err)
	}
	if keys, err := s.LegacyKeys(hub, "custom_acme", "customer", []string{cust}); err != nil || keys[cust] != "C-1" {
		t.Fatalf("the hub resolves the key: %+v %v", keys, err)
	}
	if rows, _ := s.ListXref(hub, store.XrefFilter{}); len(rows) != 0 {
		t.Fatalf("the general xref browse stays closed to a scoped session: %+v", rows)
	}
	with := func(p tenant.Principal) context.Context {
		p.TenantID = testkit.TenantID(1)
		return tenant.With(context.Background(), p)
	}
	duties := func(d ...string) map[string]struct{} {
		out := map[string]struct{}{}
		for _, x := range d {
			out[x] = struct{}{}
		}
		return out
	}
	record := store.OutboundXrefRecord{MappingID: saved.ID, EventID: "e", LegacyKey: "SO-1", Refs: []domain.OutboundRef{{Entity: "salesOrder", ID: ids.New()}}}
	for name, c := range map[string]struct {
		ctx context.Context
		op  func(context.Context) error
	}{
		// A person scoped to one legal entity holding the duties: the tables are not opened wide for them.
		"scoped person reads": {with(tenant.Principal{LegalEntityID: leA, Duties: duties(store.DutyRun, tenant.DutyTenantAdmin, store.DutyOutboundRender)}), func(ctx context.Context) error {
			_, err := s.GetOutboundMapping(ctx, saved.ID, 0)
			return err
		}},
		"admin without the write duty": {with(tenant.Principal{Duties: duties(store.DutyRun, tenant.DutyTenantAdmin)}), func(ctx context.Context) error {
			_, _, err := s.SaveOutboundMapping(ctx, outboundIn("X"), "")
			return err
		}},
		"reader without tenant.administer": {with(tenant.Principal{Duties: duties(store.DutyRun, tenant.DutyAllScope)}), func(ctx context.Context) error {
			_, err := s.ListOutboundMappings(ctx, store.OutboundFilter{})
			return err
		}},
		// The hub's render token cannot list, edit or record; another service holding the duty is not the hub.
		"hub lists": {hub, func(ctx context.Context) error {
			_, err := s.ListOutboundMappings(ctx, store.OutboundFilter{})
			return err
		}},
		"hub edits": {hub, func(ctx context.Context) error {
			_, err := s.UpdateOutboundMapping(ctx, saved.ID, 1, func(*store.OutboundMapping) error { return nil })
			return err
		}},
		"hub token without the record duty": {with(tenant.Principal{LegalEntityID: leA, Service: store.HubService, Duties: duties(store.DutyOutboundRender)}), func(ctx context.Context) error {
			_, err := s.RecordOutboundXref(ctx, record)
			return err
		}},
		"another service with the duties": {with(tenant.Principal{LegalEntityID: leA, Service: "workflow-ts", Duties: duties(store.DutyOutboundRender, store.DutyXrefRecord)}), func(ctx context.Context) error {
			_, err := s.RecordOutboundXref(ctx, record)
			return err
		}},
		"admin records": {adminContext(1), func(ctx context.Context) error { _, err := s.RecordOutboundXref(ctx, record); return err }},
	} {
		t.Run(name, func(t *testing.T) { problemStatus(t, c.op(c.ctx), 403, "AUTH-0003") })
	}
}

// xref:record never repoints an inbound row, accepts the key of a record merged into the subject, and two concurrent
// calls for one event (or one legacy key) leave exactly one winner.
func TestPostgres_OutboundRecordInboundRowsAndRaces(t *testing.T) {
	admin, pool := outboundDB(t)
	s := store.New(pool)
	saved, _, err := s.SaveOutboundMapping(adminContext(1), outboundIn("Orders"), "")
	if err != nil {
		t.Fatal(err)
	}
	hub := hubContext(1)
	survivor, loser := ids.New(), ids.New()
	put := func(key, target string) {
		t.Helper()
		if err := s.Xref().Put(adminContext(1), domain.XrefKey{SourceSystem: "custom_acme", Entity: "salesOrder", SourceKey: key}, domain.XrefEntry{TargetID: target, ContentHash: "h-" + key}); err != nil {
			t.Fatal(err)
		}
	}
	put("SO-1", loser)
	put("SO-2", ids.New())
	if _, err := admin.Exec(context.Background(), `update migration.xref set merged_into_target_id = $1 where source_key = 'SO-1'`, survivor); err != nil {
		t.Fatal(err)
	}
	// SO-1 resolves to the survivor (render gave it as xref.self), so the legacy ERP answering SO-1 is accepted.
	if _, err := s.RecordOutboundXref(hub, store.OutboundXrefRecord{MappingID: saved.ID, EventID: "e1", LegacyKey: "SO-1", Refs: []domain.OutboundRef{{Entity: "salesOrder", ID: survivor}}}); err != nil {
		t.Fatalf("the survivor's merged key: %v", err)
	}
	// SO-2 belongs to another record: refused, and the inbound row is unchanged.
	_, err = s.RecordOutboundXref(hub, store.OutboundXrefRecord{MappingID: saved.ID, EventID: "e2", LegacyKey: "SO-2", Refs: []domain.OutboundRef{{Entity: "salesOrder", ID: survivor}}})
	problemStatus(t, err, 409, store.CodeXrefConflict)
	var rows int
	if err := admin.QueryRow(context.Background(), `select count(*) from migration.xref where (source_key = 'SO-1' and target_id = $1 and merged_into_target_id = $2 and content_hash = 'h-SO-1')
		or (source_key = 'SO-2' and target_id <> $2 and content_hash = 'h-SO-2')`, loser, survivor).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("inbound rows changed: %d %v", rows, err)
	}
	var events int
	if err := admin.QueryRow(context.Background(), `select count(*) from migration.outbound_xref_event where event_id = 'e2'`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("a refused record must leave no event row: %d %v", events, err)
	}

	race := func(recs ...store.OutboundXrefRecord) (ok, conflicts int) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make([]error, len(recs))
		for i, r := range recs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = s.RecordOutboundXref(hub, r)
			}()
		}
		wg.Wait()
		for _, err := range errs {
			var p *httpx.Problem
			switch {
			case err == nil:
				ok++
			case errors.As(err, &p) && p.Status == 409:
				conflicts++
			default:
				t.Fatalf("race: %v", err)
			}
		}
		return ok, conflicts
	}
	for i := range 5 {
		order := ids.New()
		ref := []domain.OutboundRef{{Entity: "salesOrder", ID: order}}
		event := "race-" + strconv.Itoa(i)
		// One event, two different keys: one wins, the other is MIG-XREF-EVENT-CONFLICT.
		if ok, conflicts := race(store.OutboundXrefRecord{MappingID: saved.ID, EventID: event, LegacyKey: "A-" + event, Refs: ref},
			store.OutboundXrefRecord{MappingID: saved.ID, EventID: event, LegacyKey: "B-" + event, Refs: ref}); ok != 1 || conflicts != 1 {
			t.Fatalf("same event: %d ok, %d conflicts", ok, conflicts)
		}
		// One legacy key, two records under two events: one wins, the other is MIG-XREF-CONFLICT.
		if ok, conflicts := race(store.OutboundXrefRecord{MappingID: saved.ID, EventID: event + "-x", LegacyKey: "K-" + event, Refs: ref},
			store.OutboundXrefRecord{MappingID: saved.ID, EventID: event + "-y", LegacyKey: "K-" + event, Refs: []domain.OutboundRef{{Entity: "salesOrder", ID: ids.New()}}}); ok != 1 || conflicts != 1 {
			t.Fatalf("same key: %d ok, %d conflicts", ok, conflicts)
		}
	}
}

func hasRequired(def json.RawMessage, want bool) bool {
	var d struct {
		Mapping struct {
			Fields []struct {
				Required bool `json:"required"`
			} `json:"fields"`
		} `json:"mapping"`
	}
	return json.Unmarshal(def, &d) == nil && len(d.Mapping.Fields) == 1 && d.Mapping.Fields[0].Required == want
}
