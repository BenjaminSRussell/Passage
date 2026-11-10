package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/search/migrations"
)

var (
	t0      = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	entityA = "00000000-0000-0000-0000-0000000000a1"
	entityB = "00000000-0000-0000-0000-0000000000b1"
)

func setup(t *testing.T) *Store {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	testkit.AssertTenantRLS(t, admin)
	return New(app)
}

func asScope(tenantID, scopes string, duties ...string) context.Context {
	p := tenant.Principal{TenantID: tenantID, LegalEntityID: scopes, UserID: "user-test", Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

func po(le, number string, at time.Time) Record {
	return Record{Type: TypePurchaseOrder, ID: ids.New(), LegalEntityID: le, Number: number, Title: number, Subtitle: "USD 10.00", Status: "CONFIRMED", At: at}
}

func numbers(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Number
	}
	return out
}

func TestEveryTableIsIsolatedByTenantAndLegalEntity(t *testing.T) {
	st := setup(t)
	testkit.CrossTenant(t, st.pool, []testkit.RLSCase{
		{Table: "search_record", LegalEntity: true,
			Insert: `insert into search_record (tenant_id, legal_entity_id, record_type, record_id, number, title, source_at) values ($1, $2, 'purchase_order', $3, 'PO-1', 'PO-1', now())`,
			Args:   func(tenantID, le string) []any { return []any{tenantID, le, ids.New()} }},
		{Table: "search_record_tenant",
			Insert: `insert into search_record_tenant (tenant_id, record_type, record_id, number, title, source_at) values ($1, 'party', $2, 'P-1', 'Contoso', now())`,
			Args:   func(tenantID, _ string) []any { return []any{tenantID, ids.New()} }},
		{Table: "consumer_inbox", NoUpdate: true,
			Insert: `insert into consumer_inbox (tenant_id, consumer, event_id) values ($1, 'search-index', $2)`,
			Args:   func(tenantID, _ string) []any { return []any{tenantID, ids.New()} }},
	})
}

func TestApplyIgnoresOlderEventsAndTombstonesStayRemoved(t *testing.T) {
	st := setup(t)
	tid := testkit.TenantID(1)
	ctx := asScope(tid, entityA)
	search := func() []Hit {
		hits, err := st.Search(ctx, Query{Text: "contoso", Types: []string{TypeParty}, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return hits
	}
	id := ids.New()
	party := func(name string, at time.Time) Record {
		return Record{Type: TypeParty, ID: id, Number: "P-7", Title: name, Subtitle: "organization", Status: "active", At: at}
	}
	for i, tc := range []struct {
		rec       Record
		applied   bool
		wantTitle string
	}{
		{party("Contoso Ltd", t0.Add(2*time.Second)), true, "Contoso Ltd"},
		{party("Contoso Old", t0.Add(time.Second)), false, "Contoso Ltd"},
		{party("Contoso Ltd", t0.Add(2*time.Second)), true, "Contoso Ltd"},
		{party("Contoso Newer", t0.Add(3*time.Second)), true, "Contoso Newer"},
		{Record{Type: TypeParty, ID: id, Status: "MERGED", Removed: true, At: t0.Add(4 * time.Second)}, true, ""},
		{party("Contoso Resurrected", t0.Add(3500*time.Millisecond)), false, ""},
	} {
		applied, err := st.Apply(ctx, tc.rec)
		if err != nil || applied != tc.applied {
			t.Fatalf("step %d: applied=%v err=%v, want applied=%v", i, applied, err, tc.applied)
		}
		hits := search()
		if tc.wantTitle == "" && len(hits) != 0 {
			t.Fatalf("step %d: a removed record is still found: %+v", i, hits)
		}
		if tc.wantTitle != "" && (len(hits) != 1 || hits[0].Title != tc.wantTitle) {
			t.Fatalf("step %d: hits %+v, want title %q", i, hits, tc.wantTitle)
		}
	}
}

func TestStatusOnlyEventKeepsTheTitle(t *testing.T) {
	st := setup(t)
	ctx := asScope(testkit.TenantID(1), entityA)
	r := po(entityA, "PO-1042", t0)
	if _, err := st.Apply(ctx, r); err != nil {
		t.Fatal(err)
	}
	cancel := Record{Type: TypePurchaseOrder, ID: r.ID, LegalEntityID: entityA, Number: "PO-1042", Status: "CANCELLED", At: t0.Add(time.Minute)}
	if applied, err := st.Apply(ctx, cancel); err != nil || !applied {
		t.Fatalf("cancel: applied=%v err=%v", applied, err)
	}
	hits, err := st.Search(ctx, Query{Text: "po-1042", Types: []string{TypePurchaseOrder}, Limit: 5})
	if err != nil || len(hits) != 1 || hits[0].Status != "CANCELLED" || hits[0].Subtitle != "USD 10.00" {
		t.Fatalf("hits %+v err %v, want the cancelled order with its subtitle", hits, err)
	}
}

func TestSearchRanksExactNumberThenPrefixThenSimilarity(t *testing.T) {
	st := setup(t)
	ctx := asScope(testkit.TenantID(1), entityA)
	for _, r := range []Record{
		po(entityA, "PO-1042", t0),
		po(entityA, "PO-10420", t0),
		po(entityA, "PO-2001", t0),
		{Type: TypeParty, ID: ids.New(), Number: "P-9", Title: "Order PO-1042 supplies", Status: "active", At: t0},
	} {
		if _, err := st.Apply(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := st.Search(ctx, Query{Text: "PO-1042", Types: []string{TypePurchaseOrder, TypeParty}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := numbers(hits), []string{"PO-1042", "PO-10420", "P-9"}; !slices.Equal(got, want) {
		t.Fatalf("ranking %v, want %v", got, want)
	}
	if hits[0].LegalEntityID != entityA || hits[2].LegalEntityID != "" {
		t.Fatalf("legal entities %q / %q, want entity A for the order and none for the party", hits[0].LegalEntityID, hits[2].LegalEntityID)
	}
}

func TestSearchFindsNamesBySubstringAndTypo(t *testing.T) {
	st := setup(t)
	ctx := asScope(testkit.TenantID(1), entityA)
	for _, name := range []string{"Contoso Industries Ltd", "Fabrikam Inc", "Northwind Traders"} {
		if _, err := st.Apply(ctx, Record{Type: TypeParty, ID: ids.New(), Number: "P-" + name[:3], Title: name, At: t0}); err != nil {
			t.Fatal(err)
		}
	}
	for q, want := range map[string]string{"dustries": "Contoso Industries Ltd", "contsoso": "Contoso Industries Ltd", "northwnd": "Northwind Traders"} {
		hits, err := st.Search(ctx, Query{Text: q, Types: []string{TypeParty}, Limit: 5})
		if err != nil || len(hits) == 0 || hits[0].Title != want {
			t.Errorf("search %q: %+v err %v, want %q first", q, hits, err, want)
		}
	}
}

func TestSearchTreatsLikeCharactersAsText(t *testing.T) {
	st := setup(t)
	ctx := asScope(testkit.TenantID(1), entityA)
	for _, n := range []string{"PO-1", "PO-2", "100%-DONE", `A_B\C`} {
		if _, err := st.Apply(ctx, Record{Type: TypeProduct, ID: ids.New(), Number: n, Title: n, At: t0}); err != nil {
			t.Fatal(err)
		}
	}
	for q, want := range map[string][]string{"%%": nil, "__": nil, `\\`: nil, "0%-": {"100%-DONE"}, "a_b": {`A_B\C`}} {
		hits, err := st.Search(ctx, Query{Text: q, Types: []string{TypeProduct}, Limit: 10})
		if err != nil || !slices.Equal(numbers(hits), want) {
			t.Errorf("search %q: %v err %v, want %v", q, numbers(hits), err, want)
		}
	}
}

func TestSearchNeverCrossesTenantsOrLegalEntities(t *testing.T) {
	st := setup(t)
	tenantA, tenantB := testkit.TenantID(1), testkit.TenantID(2)
	for _, w := range []struct{ tenant, entity, number string }{
		{tenantA, entityA, "PO-A"}, {tenantA, entityB, "PO-B"}, {tenantB, entityA, "PO-OTHER-TENANT"},
	} {
		if _, err := st.Apply(asScope(w.tenant, w.entity), po(w.entity, w.number, t0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Apply(asScope(tenantB, ""), Record{Type: TypeProduct, ID: ids.New(), Number: "PO-PRODUCT-B", Title: "PO", At: t0}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		expect []string
	}{
		{"entity A", asScope(tenantA, entityA), []string{"PO-A"}},
		{"entity B", asScope(tenantA, entityB), []string{"PO-B"}},
		{"both entities", asScope(tenantA, entityA+","+entityB), []string{"PO-A", "PO-B"}},
		{"no entity", asScope(tenantA, ""), nil},
		{"other tenant", asScope(tenantB, entityA), []string{"PO-OTHER-TENANT", "PO-PRODUCT-B"}},
	} {
		hits, err := st.Search(tc.ctx, Query{Text: "po-", Types: []string{TypePurchaseOrder, TypeProduct}, Limit: 10})
		if err != nil || !slices.Equal(numbers(hits), tc.expect) {
			t.Errorf("%s sees %v (err %v), want %v", tc.name, numbers(hits), err, tc.expect)
		}
	}
}

func TestApplyAndSearchRefuseAContextWithoutAPrincipal(t *testing.T) {
	st := setup(t)
	if _, err := st.Apply(context.Background(), po(entityA, "PO-1", t0)); err == nil {
		t.Error("Apply without a principal must fail")
	}
	if _, err := st.Search(context.Background(), Query{Text: "po", Limit: 1}); err == nil {
		t.Error("Search without a principal must fail")
	}
}

func TestSearchHonoursTheLimit(t *testing.T) {
	st := setup(t)
	ctx := asScope(testkit.TenantID(1), entityA)
	for _, n := range []string{"PO-1", "PO-2", "PO-3"} {
		if _, err := st.Apply(ctx, po(entityA, n, t0)); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := st.Search(ctx, Query{Text: "po-", Types: []string{TypePurchaseOrder}, Limit: 2})
	if err != nil || !slices.Equal(numbers(hits), []string{"PO-1", "PO-2"}) {
		t.Fatalf("hits %v err %v, want the first two by number", numbers(hits), err)
	}
}
