package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func catalogEntry(name, category, unit, price string) domain.CatalogEntry {
	return domain.CatalogEntry{VendorID: ids.New(), VendorPartNo: "P-" + name, Name: name, Category: category, Unit: unit,
		UnitPrice: dec(price), Currency: "USD", LeadTimeDays: 3}
}

func draftCatalog(t *testing.T, units []string, entries ...domain.CatalogEntry) domain.Catalog {
	to := day(t, "2027-12-31")
	return domain.Catalog{LegalEntityID: legalEntity, Name: "Plant MRO", ValidFrom: day(t, "2027-01-01"), ValidTo: &to, OrgUnitIDs: units, Entries: entries}
}

func TestCatalog_LifecycleEntriesAndEvents(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(1)
	created, err := s.CreateCatalog(ctx, draftCatalog(t, nil, catalogEntry("Gloves", "MRO", "PR", "4.25")), store.Idem{Key: "c1", Hash: "h"})
	if err != nil || created.Status != domain.CatalogDraft || created.Version != 1 || len(created.Entries) != 1 || created.Number == "" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if again, err := s.CreateCatalog(ctx, draftCatalog(t, nil), store.Idem{Key: "c1", Hash: "h"}); err != nil || again.ID != created.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.CreateCatalog(ctx, draftCatalog(t, nil), store.Idem{Key: "c1", Hash: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Errorf("key reuse: %v", err)
	}
	// Entries: add under If-Match, stale version, removal.
	added, err := s.AddCatalogEntries(ctx, created.ID, 1, []domain.CatalogEntry{catalogEntry("Tape", "MRO", "RL", "1.10"), catalogEntry("Bolts", "FASTENER", "BX", "9")}, store.Idem{Key: "a1", Hash: "h"})
	if err != nil || added.Version != 2 || len(added.Entries) != 3 || added.Entries[2].EntryNo != 3 {
		t.Fatalf("add = %+v, %v", added, err)
	}
	if _, err := s.AddCatalogEntries(ctx, created.ID, 1, []domain.CatalogEntry{catalogEntry("Late", "MRO", "EA", "1")}, store.Idem{Key: "a2", Hash: "h"}); problemCode(err) != store.CodeVersionConflict {
		t.Errorf("stale add: %v", err)
	}
	removed, err := s.RemoveCatalogEntry(ctx, created.ID, added.Entries[1].ID, 2)
	if err != nil || removed.Version != 3 || len(removed.Entries) != 2 {
		t.Fatalf("remove = %+v, %v", removed, err)
	}
	if _, err := s.RemoveCatalogEntry(ctx, created.ID, ids.New(), 3); problemCode(err) != store.CodeCatalogEntryNotFound {
		t.Errorf("remove unknown: %v", err)
	}
	// Publish: an empty catalog is refused, a real one publishes and writes the event once.
	empty, _ := s.CreateCatalog(ctx, draftCatalog(t, nil), store.Idem{})
	if _, err := s.ActOnCatalog(ctx, empty.ID, 1, domain.CatalogPublish, store.Idem{}); problemCode(err) != domain.CodeCatalogEmpty {
		t.Errorf("publish empty: %v", err)
	}
	if _, err := s.ActOnCatalog(ctx, created.ID, 2, domain.CatalogPublish, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Errorf("publish stale: %v", err)
	}
	pub, err := s.ActOnCatalog(ctx, created.ID, 3, domain.CatalogPublish, store.Idem{Key: "p1", Hash: "h"})
	if err != nil || pub.Status != domain.CatalogPublished || pub.Version != 4 {
		t.Fatalf("publish = %+v, %v", pub, err)
	}
	if replay, err := s.ActOnCatalog(ctx, created.ID, 3, domain.CatalogPublish, store.Idem{Key: "p1", Hash: "h"}); err != nil || replay.Version != 4 {
		t.Fatalf("publish replay = %+v, %v", replay, err)
	}
	if _, err := s.AddCatalogEntries(ctx, created.ID, 4, []domain.CatalogEntry{catalogEntry("X", "MRO", "EA", "1")}, store.Idem{}); problemCode(err) != domain.CodeCatalogState {
		t.Errorf("add to published: %v", err)
	}
	if _, err := s.RemoveCatalogEntry(ctx, created.ID, pub.Entries[0].ID, 4); problemCode(err) != domain.CodeCatalogState {
		t.Errorf("remove from published: %v", err)
	}
	ret, err := s.ActOnCatalog(ctx, created.ID, 4, domain.CatalogRetire, store.Idem{})
	if err != nil || ret.Status != domain.CatalogRetired {
		t.Fatalf("retire = %+v, %v", ret, err)
	}
	if _, err := s.ActOnCatalog(ctx, created.ID, 5, domain.CatalogRetire, store.Idem{}); problemCode(err) != domain.CodeCatalogState {
		t.Errorf("retire twice: %v", err)
	}
	var published, retired int
	for _, e := range outbox(t, ctx, pool) {
		switch e.Type {
		case purchasingevents.CatalogPublished:
			published++
			if e.Data["catalog_id"] != created.ID || e.Data["entry_count"] != float64(2) || e.Data["status"] != "PUBLISHED" {
				t.Errorf("published data = %v", e.Data)
			}
		case purchasingevents.CatalogRetired:
			retired++
		}
	}
	if published != 1 || retired != 1 {
		t.Errorf("events published %d retired %d, want 1 and 1 (refusals and replays write none)", published, retired)
	}
	list, err := s.ListCatalogs(ctx, store.CatalogFilter{Status: domain.CatalogRetired}, "", 10)
	if err != nil || len(list.Items) != 1 || list.Items[0].EntryCount != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := s.GetCatalog(ctx, ids.New()); problemCode(err) != store.CodeCatalogNotFound {
		t.Errorf("get unknown: %v", err)
	}
}

func TestCatalog_SearchHonoursStatusValidityUnitAndFilters(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(2)
	unitA, unitB := ids.New(), ids.New()
	publish := func(c domain.Catalog) domain.Catalog {
		made, err := s.CreateCatalog(ctx, c, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		out, err := s.ActOnCatalog(ctx, made.ID, 1, domain.CatalogPublish, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	open := publish(draftCatalog(t, nil, catalogEntry("Safety gloves", "MRO", "PR", "4.25"), catalogEntry("Pallet wrap", "PACKING", "RL", "7")))
	unitOnly := publish(draftCatalog(t, []string{unitA}, catalogEntry("Cutting oil", "MRO", "L", "12")))
	expired := draftCatalog(t, nil, catalogEntry("Old stock", "MRO", "EA", "1"))
	past := day(t, "2026-06-30")
	expired.ValidFrom, expired.ValidTo = day(t, "2026-01-01"), &past
	publish(expired)
	_, _ = s.CreateCatalog(ctx, draftCatalog(t, nil, catalogEntry("Draft only", "MRO", "EA", "1")), store.Idem{})
	retired := publish(draftCatalog(t, nil, catalogEntry("Retired item", "MRO", "EA", "1")))
	if _, err := s.ActOnCatalog(ctx, retired.ID, 2, domain.CatalogRetire, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	other := draftCatalog(t, nil, catalogEntry("Other entity", "MRO", "EA", "1"))
	other.LegalEntityID = ids.New()
	publish(other)

	names := func(f store.CatalogSearch) []string {
		t.Helper()
		f.LegalEntityID, f.Date = legalEntity, day(t, "2027-03-01")
		page, err := s.SearchCatalogEntries(ctx, f, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range page.Items {
			out = append(out, h.Name)
			if h.CatalogID == "" || h.CatalogNumber == "" {
				t.Errorf("hit without its catalog: %+v", h)
			}
		}
		return out
	}
	has := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		seen := map[string]bool{}
		for _, g := range got {
			seen[g] = true
		}
		for _, w := range want {
			if !seen[w] {
				return false
			}
		}
		return true
	}
	if got := names(store.CatalogSearch{}); !has(got, "Safety gloves", "Pallet wrap") {
		t.Errorf("no unit: %v (a unit-limited catalog needs the unit; draft, retired, expired and other-entity entries never show)", got)
	}
	if got := names(store.CatalogSearch{OrgUnitID: unitA}); !has(got, "Safety gloves", "Pallet wrap", "Cutting oil") {
		t.Errorf("unit A: %v", got)
	}
	if got := names(store.CatalogSearch{OrgUnitID: unitB}); !has(got, "Safety gloves", "Pallet wrap") {
		t.Errorf("unit B: %v", got)
	}
	if got := names(store.CatalogSearch{Text: "GLOVE"}); !has(got, "Safety gloves") {
		t.Errorf("text: %v", got)
	}
	if got := names(store.CatalogSearch{Text: "100%"}); len(got) != 0 {
		t.Errorf("a percent sign matches literally: %v", got)
	}
	if got := names(store.CatalogSearch{Category: "PACKING"}); !has(got, "Pallet wrap") {
		t.Errorf("category: %v", got)
	}
	if got := names(store.CatalogSearch{VendorID: open.Entries[0].VendorID}); len(got) != 1 {
		t.Errorf("vendor: %v", got)
	}
	f := store.CatalogSearch{LegalEntityID: legalEntity, Date: day(t, "2026-06-01")}
	if page, _ := s.SearchCatalogEntries(ctx, f, "", 50); len(page.Items) != 1 || page.Items[0].Name != "Old stock" {
		t.Errorf("an earlier date sees only what was valid then: %+v", page.Items)
	}
	// Paging by entry id.
	f = store.CatalogSearch{LegalEntityID: legalEntity, Date: day(t, "2027-03-01"), OrgUnitID: unitA}
	first, _ := s.SearchCatalogEntries(ctx, f, "", 2)
	if len(first.Items) != 2 || first.Next == "" {
		t.Fatalf("page 1 = %+v", first)
	}
	second, _ := s.SearchCatalogEntries(ctx, f, first.Next, 2)
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID || second.Items[0].ID == first.Items[1].ID {
		t.Errorf("page 2 = %+v", second)
	}
	// GetCatalogEntry returns the entry with its catalog header.
	cat, e, err := s.GetCatalogEntry(ctx, unitOnly.Entries[0].ID)
	if err != nil || e.Name != "Cutting oil" || cat.ID != unitOnly.ID || len(cat.OrgUnitIDs) != 1 || cat.OrgUnitIDs[0] != unitA || cat.Status != domain.CatalogPublished {
		t.Fatalf("entry = %+v catalog %+v, %v", e, cat, err)
	}
	if _, _, err := s.GetCatalogEntry(ctx, ids.New()); problemCode(err) != store.CodeCatalogEntryNotFound {
		t.Errorf("unknown entry: %v", err)
	}
}

func TestCatalog_RequisitionLineKeepsTheEntryLink(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(3)
	cat, err := s.CreateCatalog(ctx, draftCatalog(t, nil, catalogEntry("Gloves", "MRO", "PR", "4.25")), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	req := requisition(t, ids.New(), "4.25", "10")
	req.Lines[0].CatalogEntryID = cat.Entries[0].ID
	made, err := s.CreateRequisition(ctx, req, store.Idem{})
	if err != nil || made.Lines[0].CatalogEntryID != cat.Entries[0].ID || made.Lines[1].CatalogEntryID != "" {
		t.Fatalf("created = %+v, %v", made.Lines, err)
	}
	got, err := s.GetRequisition(ctx, made.ID)
	if err != nil || got.Lines[0].CatalogEntryID != cat.Entries[0].ID {
		t.Fatalf("read back = %+v, %v", got.Lines, err)
	}
}

func TestCatalog_TenantAndLegalEntityIsolation(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	entityA, entityB := ids.New(), ids.New()
	as := func(n int, le string) context.Context {
		return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: le, UserID: ids.New()})
	}
	in := draftCatalog(t, nil, catalogEntry("Gloves", "MRO", "PR", "4.25"))
	in.LegalEntityID = entityA
	made, err := s.CreateCatalog(as(4, entityA), in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCatalog(as(4, entityB), made.ID); problemCode(err) != store.CodeCatalogNotFound {
		t.Errorf("another legal entity reads the catalog: %v", err)
	}
	if _, err := s.GetCatalog(as(5, entityA), made.ID); problemCode(err) != store.CodeCatalogNotFound {
		t.Errorf("another tenant reads the catalog: %v", err)
	}
	if _, _, err := s.GetCatalogEntry(as(4, entityB), made.Entries[0].ID); problemCode(err) != store.CodeCatalogEntryNotFound {
		t.Errorf("another legal entity reads the entry: %v", err)
	}
	if page, _ := s.ListCatalogs(as(4, entityB), store.CatalogFilter{}, "", 10); len(page.Items) != 0 {
		t.Errorf("another legal entity lists %d catalogs", len(page.Items))
	}
	if _, err := s.AddCatalogEntries(as(4, entityB), made.ID, 1, []domain.CatalogEntry{catalogEntry("X", "MRO", "EA", "1")}, store.Idem{}); problemCode(err) != store.CodeCatalogNotFound {
		t.Errorf("another legal entity adds an entry: %v", err)
	}
}
