package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// importRows builds n staged rows for the vendor: every row in noUnit lacks a unit
// (ERROR), the rest are ACCEPTABLE with part number V-<n>.
func importRows(vendor string, n int, noUnit ...int) []domain.ImportRow {
	missing := map[int]bool{}
	for _, r := range noUnit {
		missing[r] = true
	}
	rows := make([]domain.ImportRow, n)
	for i := range rows {
		no := i + 1
		raw := map[string]string{domain.ColVendorPartNo: fmt.Sprintf("V-%d", no), domain.ColName: fmt.Sprintf("Item %d", no),
			domain.ColCategory: "MRO", domain.ColUnit: "EA", domain.ColUnitPrice: "2.50", domain.ColCurrency: "USD"}
		if missing[no] {
			delete(raw, domain.ColUnit)
			rows[i] = domain.ImportRow{RowNo: no, Status: domain.RowError, Reason: "unit is required", Raw: raw}
			continue
		}
		rows[i] = domain.ImportRow{RowNo: no, Status: domain.RowAcceptable, Raw: raw, Entry: &domain.CatalogEntry{VendorID: vendor,
			VendorPartNo: raw[domain.ColVendorPartNo], Name: raw[domain.ColName], Category: "MRO", Unit: "EA", UnitPrice: dec("2.50"),
			Currency: "USD", LeadTimeDays: 2}}
	}
	return rows
}

// AC-17.13: a published catalog with entries, a 50-row vendor file with 3 rows lacking a
// unit stages 47 ACCEPTABLE and 3 ERROR; after the buyer accepts, the 47 are in the
// catalog and in the picker for the catalog's unit only.
func TestVendorImport_AC1713StageAcceptCommit(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(1)
	finance, other := ids.New(), ids.New()
	vendor := ids.New()
	made, err := s.CreateCatalog(ctx, draftCatalog(t, []string{finance}, catalogEntry("Existing 1", "MRO", "EA", "1"), catalogEntry("Existing 2", "MRO", "EA", "1")), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := s.ActOnCatalog(ctx, made.ID, 1, domain.CatalogPublish, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	in := store.ImportInput{VendorID: vendor, Format: "CSV", FileName: "gloves.csv", Rows: importRows(vendor, 50, 7, 21, 40)}
	imp, err := s.StageVendorImport(ctx, cat.ID, in, store.Idem{Key: "s1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if imp.Status != domain.ImportStaged || imp.TotalRows != 50 || imp.AcceptableRows != 47 || imp.ErrorRows != 3 || imp.Version != 1 || imp.LegalEntityID != legalEntity {
		t.Fatalf("staged = %+v", imp)
	}
	if again, err := s.StageVendorImport(ctx, cat.ID, in, store.Idem{Key: "s1", Hash: "h"}); err != nil || again.ID != imp.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.StageVendorImport(ctx, cat.ID, in, store.Idem{Key: "s1", Hash: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Errorf("key reuse: %v", err)
	}
	// Staging touches nothing in the catalog or the picker.
	if got, _ := s.GetCatalog(ctx, cat.ID); len(got.Entries) != 2 || got.Version != cat.Version {
		t.Fatalf("catalog changed by staging: %d entries v%d", len(got.Entries), got.Version)
	}
	errs, err := s.ListImportRows(ctx, imp.ID, domain.RowError, 0, 10)
	if err != nil || len(errs.Items) != 3 || errs.Items[0].RowNo != 7 || errs.Items[0].Reason != "unit is required" || errs.Items[0].Entry != nil || errs.Items[0].Raw[domain.ColName] != "Item 7" {
		t.Fatalf("error rows = %+v, %v", errs, err)
	}
	first, err := s.ListImportRows(ctx, imp.ID, "", 0, 20)
	if err != nil || len(first.Items) != 20 || first.Next != 20 || first.Items[0].Entry == nil || first.Items[0].Entry.UnitPrice.String() != "2.5" || first.Items[0].Entry.LeadTimeDays != 2 {
		t.Fatalf("page 1 = %+v, %v", first, err)
	}
	if rest, _ := s.ListImportRows(ctx, imp.ID, "", first.Next, 200); len(rest.Items) != 30 || rest.Next != 0 {
		t.Errorf("page 2 = %d rows next %d", len(rest.Items), rest.Next)
	}

	// Decisions: an ERROR row cannot be accepted, an unknown row is a 404, a stale version conflicts.
	if _, err := s.DecideImportRows(ctx, imp.ID, 1, store.ImportDecisions{Accept: []int{7}}, store.Idem{}); problemCode(err) != domain.CodeImportRow {
		t.Errorf("accept an error row: %v", err)
	}
	if _, err := s.DecideImportRows(ctx, imp.ID, 1, store.ImportDecisions{Accept: []int{51}}, store.Idem{}); problemCode(err) != store.CodeImportRowNotFound {
		t.Errorf("unknown row: %v", err)
	}
	if _, err := s.DecideImportRows(ctx, imp.ID, 9, store.ImportDecisions{AcceptAll: true}, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Errorf("stale decisions: %v", err)
	}
	if _, err := s.ActOnCatalogImport(ctx, imp.ID, 1, domain.ImportCommit, store.Idem{}); problemCode(err) != domain.CodeImportNothing {
		t.Errorf("commit with nothing accepted: %v", err)
	}
	decided, err := s.DecideImportRows(ctx, imp.ID, 1, store.ImportDecisions{AcceptAll: true, Reject: []int{1, 2}}, store.Idem{Key: "d1", Hash: "h"})
	if err != nil || decided.Version != 2 || decided.AcceptedRows != 45 || decided.RejectedRows != 2 || decided.AcceptableRows != 0 || decided.ErrorRows != 3 {
		t.Fatalf("decided = %+v, %v", decided, err)
	}
	if replay, err := s.DecideImportRows(ctx, imp.ID, 1, store.ImportDecisions{AcceptAll: true, Reject: []int{1, 2}}, store.Idem{Key: "d1", Hash: "h"}); err != nil || replay.Version != 2 {
		t.Fatalf("decisions replay = %+v, %v", replay, err)
	}
	back, err := s.DecideImportRows(ctx, imp.ID, 2, store.ImportDecisions{Accept: []int{1}}, store.Idem{})
	if err != nil || back.AcceptedRows != 46 || back.RejectedRows != 1 {
		t.Fatalf("a rejected row accepted again = %+v, %v", back, err)
	}

	// Commit: 46 entries are added to the published catalog and the picker shows them to Finance only.
	done, err := s.ActOnCatalogImport(ctx, imp.ID, 3, domain.ImportCommit, store.Idem{Key: "c1", Hash: "h"})
	if err != nil || done.Status != domain.ImportCommitted || done.EntriesAdded != 46 || done.Version != 4 {
		t.Fatalf("commit = %+v, %v", done, err)
	}
	if replay, err := s.ActOnCatalogImport(ctx, imp.ID, 3, domain.ImportCommit, store.Idem{Key: "c1", Hash: "h"}); err != nil || replay.Version != 4 {
		t.Fatalf("commit replay = %+v, %v", replay, err)
	}
	if _, err := s.ActOnCatalogImport(ctx, imp.ID, 4, domain.ImportCommit, store.Idem{}); problemCode(err) != domain.CodeImportState {
		t.Errorf("second commit: %v", err)
	}
	if _, err := s.DecideImportRows(ctx, imp.ID, 4, store.ImportDecisions{AcceptAll: true}, store.Idem{}); problemCode(err) != domain.CodeImportState {
		t.Errorf("decide after commit: %v", err)
	}
	grown, err := s.GetCatalog(ctx, cat.ID)
	if err != nil || len(grown.Entries) != 48 || grown.Version != cat.Version+1 || grown.Entries[2].EntryNo != 3 || grown.Entries[2].VendorID != vendor || grown.Entries[2].Name != "Item 1" {
		t.Fatalf("catalog after commit: %d entries v%d %+v, %v", len(grown.Entries), grown.Version, grown.Entries[2], err)
	}
	f := store.CatalogSearch{LegalEntityID: legalEntity, Date: day(t, "2027-03-01"), OrgUnitID: finance, VendorID: vendor}
	if page, _ := s.SearchCatalogEntries(ctx, f, "", 200); len(page.Items) != 46 {
		t.Errorf("Finance sees %d imported entries, want 46", len(page.Items))
	}
	f.OrgUnitID = other
	if page, _ := s.SearchCatalogEntries(ctx, f, "", 200); len(page.Items) != 0 {
		t.Errorf("another unit sees %d imported entries, want 0", len(page.Items))
	}
	var committed int
	for _, e := range outbox(t, ctx, pool) {
		if e.Type == purchasingevents.CatalogImportCommitted {
			committed++
			d := e.Data
			if d["import_id"] != imp.ID || d["catalog_id"] != cat.ID || d["accepted_rows"] != float64(46) || d["rejected_rows"] != float64(1) ||
				d["error_rows"] != float64(3) || d["total_rows"] != float64(50) || d["catalog_version"] != float64(grown.Version) {
				t.Errorf("event data = %v", d)
			}
		}
	}
	if committed != 1 {
		t.Errorf("%d commit events, want 1 (refusals and replays write none)", committed)
	}
	list, err := s.ListCatalogImports(ctx, store.ImportFilter{CatalogID: cat.ID, Status: domain.ImportCommitted}, "", 10)
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != imp.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestVendorImport_DuplicatesCancelAndGuards(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(2)
	vendor := ids.New()
	draft := draftCatalog(t, nil)
	draft.Entries = []domain.CatalogEntry{{VendorID: vendor, VendorPartNo: "v-2", Name: "Held", Category: "MRO", Unit: "EA", UnitPrice: dec("1"), Currency: "USD"}}
	cat, err := s.CreateCatalog(ctx, draft, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	rows := importRows(vendor, 4)
	rows[2].Raw[domain.ColVendorPartNo] = "V-1" // repeats row 1
	imp, err := s.StageVendorImport(ctx, cat.ID, store.ImportInput{VendorID: vendor, Format: "JSON", Rows: rows}, store.Idem{})
	if err != nil || imp.AcceptableRows != 2 || imp.ErrorRows != 2 {
		t.Fatalf("staged = %+v, %v (row 2 is in the catalog already, row 3 repeats row 1)", imp, err)
	}
	errs, _ := s.ListImportRows(ctx, imp.ID, domain.RowError, 0, 10)
	if len(errs.Items) != 2 || errs.Items[0].RowNo != 2 || errs.Items[1].RowNo != 3 || errs.Items[0].Entry != nil {
		t.Fatalf("error rows = %+v", errs.Items)
	}
	// A different vendor may reuse the part number.
	if other, err := s.StageVendorImport(ctx, cat.ID, store.ImportInput{VendorID: ids.New(), Format: "JSON", Rows: importRows(ids.New(), 2)}, store.Idem{}); err != nil || other.ErrorRows != 0 {
		t.Errorf("another vendor's import = %+v, %v", other, err)
	}
	cancelled, err := s.ActOnCatalogImport(ctx, imp.ID, 1, domain.ImportCancel, store.Idem{})
	if err != nil || cancelled.Status != domain.ImportCancelled || cancelled.EntriesAdded != 0 {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	if _, err := s.ActOnCatalogImport(ctx, imp.ID, 2, domain.ImportCommit, store.Idem{}); problemCode(err) != domain.CodeImportState {
		t.Errorf("commit a cancelled import: %v", err)
	}
	if got, _ := s.GetCatalog(ctx, cat.ID); len(got.Entries) != 1 {
		t.Errorf("a cancelled import changed the catalog: %d entries", len(got.Entries))
	}
	// Guards: unknown catalog, size, retired catalog, commit into a retired catalog.
	if _, err := s.StageVendorImport(ctx, ids.New(), store.ImportInput{VendorID: vendor, Format: "CSV", Rows: importRows(vendor, 1)}, store.Idem{}); problemCode(err) != store.CodeCatalogNotFound {
		t.Errorf("unknown catalog: %v", err)
	}
	if _, err := s.StageVendorImport(ctx, cat.ID, store.ImportInput{VendorID: vendor, Format: "CSV"}, store.Idem{}); problemCode(err) != domain.CodeImportTooBig {
		t.Errorf("empty file: %v", err)
	}
	if _, err := s.GetCatalogImport(ctx, ids.New()); problemCode(err) != store.CodeImportNotFound {
		t.Errorf("unknown import: %v", err)
	}
	staged, err := s.StageVendorImport(ctx, cat.ID, store.ImportInput{VendorID: vendor, Format: "CSV", Rows: importRows(vendor, 3)}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideImportRows(ctx, staged.ID, 1, store.ImportDecisions{AcceptAll: true}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	pub, _ := s.ActOnCatalog(ctx, cat.ID, 1, domain.CatalogPublish, store.Idem{})
	if _, err := s.ActOnCatalog(ctx, cat.ID, pub.Version, domain.CatalogRetire, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnCatalogImport(ctx, staged.ID, 2, domain.ImportCommit, store.Idem{}); problemCode(err) != domain.CodeCatalogState {
		t.Errorf("commit into a retired catalog: %v", err)
	}
	if _, err := s.StageVendorImport(ctx, cat.ID, store.ImportInput{VendorID: vendor, Format: "CSV", Rows: importRows(vendor, 1)}, store.Idem{}); problemCode(err) != domain.CodeCatalogState {
		t.Errorf("stage into a retired catalog: %v", err)
	}
}

func TestVendorImport_TenantAndLegalEntityIsolation(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	entityA, entityB := ids.New(), ids.New()
	as := func(n int, le string) context.Context {
		return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: le, UserID: ids.New()})
	}
	in := draftCatalog(t, nil)
	in.LegalEntityID = entityA
	cat, err := s.CreateCatalog(as(4, entityA), in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	vendor := ids.New()
	imp, err := s.StageVendorImport(as(4, entityA), cat.ID, store.ImportInput{VendorID: vendor, Format: "CSV", Rows: importRows(vendor, 2)}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCatalogImport(as(4, entityB), imp.ID); problemCode(err) != store.CodeImportNotFound {
		t.Errorf("another legal entity reads the import: %v", err)
	}
	if _, err := s.GetCatalogImport(as(5, entityA), imp.ID); problemCode(err) != store.CodeImportNotFound {
		t.Errorf("another tenant reads the import: %v", err)
	}
	if _, err := s.ListImportRows(as(4, entityB), imp.ID, "", 0, 10); problemCode(err) != store.CodeImportNotFound {
		t.Errorf("another legal entity reads the rows: %v", err)
	}
	if page, _ := s.ListCatalogImports(as(4, entityB), store.ImportFilter{}, "", 10); len(page.Items) != 0 {
		t.Errorf("another legal entity lists %d imports", len(page.Items))
	}
	if _, err := s.StageVendorImport(as(4, entityB), cat.ID, store.ImportInput{VendorID: vendor, Format: "CSV", Rows: importRows(vendor, 1)}, store.Idem{}); problemCode(err) != store.CodeCatalogNotFound {
		t.Errorf("another legal entity stages into the catalog: %v", err)
	}
	if _, err := s.ActOnCatalogImport(as(4, entityB), imp.ID, 1, domain.ImportCancel, store.Idem{}); problemCode(err) != store.CodeImportNotFound {
		t.Errorf("another legal entity cancels the import: %v", err)
	}
}

// The same vendor file is staged once per catalog (PRD 3.17.16, key = file hash) until
// its import is cancelled; and a part number another import committed after this one
// was staged stops the commit instead of entering the catalog twice.
func TestVendorImport_SameFileOnceAndLateDuplicates(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(6)
	vendor := ids.New()
	cat, err := s.CreateCatalog(ctx, draftCatalog(t, nil), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	file := store.ImportInput{VendorID: vendor, Format: "CSV", Rows: importRows(vendor, 3)}
	first, err := s.StageVendorImport(ctx, cat.ID, file, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	asJSON := file
	asJSON.Format = "JSON"
	if _, err := s.StageVendorImport(ctx, cat.ID, asJSON, store.Idem{}); problemCode(err) != store.CodeImportDuplicate {
		t.Fatalf("the same rows staged again: %v", err)
	}
	if _, err := s.StageVendorImport(ctx, cat.ID, store.ImportInput{VendorID: ids.New(), Format: "CSV", Rows: importRows(vendor, 3)}, store.Idem{}); err != nil {
		t.Errorf("the same rows for another vendor: %v", err)
	}
	// A different file sharing part number V-2 stages while the first is still open.
	other := importRows(vendor, 2)
	other[0].Raw[domain.ColVendorPartNo], other[0].Entry.VendorPartNo = "V-9", "V-9"
	second, err := s.StageVendorImport(ctx, cat.ID, store.ImportInput{VendorID: vendor, Format: "CSV", Rows: other}, store.Idem{})
	if err != nil || second.AcceptableRows != 2 {
		t.Fatalf("second file = %+v, %v", second, err)
	}
	for _, imp := range []domain.CatalogImport{first, second} {
		if _, err := s.DecideImportRows(ctx, imp.ID, 1, store.ImportDecisions{AcceptAll: true}, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ActOnCatalogImport(ctx, first.ID, 2, domain.ImportCommit, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnCatalogImport(ctx, second.ID, 2, domain.ImportCommit, store.Idem{}); problemCode(err) != domain.CodeImportRow {
		t.Fatalf("commit of a part number already committed: %v", err)
	}
	if _, err := s.DecideImportRows(ctx, second.ID, 2, store.ImportDecisions{Reject: []int{2}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	done, err := s.ActOnCatalogImport(ctx, second.ID, 3, domain.ImportCommit, store.Idem{})
	if err != nil || done.EntriesAdded != 1 {
		t.Fatalf("commit after rejecting the duplicate = %+v, %v", done, err)
	}
	got, _ := s.GetCatalog(ctx, cat.ID)
	if got.EntryCount != 4 || got.Entries[3].VendorPartNo != "V-9" || got.Entries[3].EntryNo != 4 {
		t.Errorf("catalog = %d entries, last %+v", got.EntryCount, got.Entries[len(got.Entries)-1])
	}
	// Once cancelled, the file may be staged again (its rows are now duplicates).
	third, err := s.StageVendorImport(ctx, cat.ID, store.ImportInput{VendorID: vendor, Format: "CSV", Rows: importRows(vendor, 4)}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnCatalogImport(ctx, third.ID, 1, domain.ImportCancel, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if again, err := s.StageVendorImport(ctx, cat.ID, store.ImportInput{VendorID: vendor, Format: "CSV", Rows: importRows(vendor, 4)}, store.Idem{}); err != nil || again.ErrorRows != 3 || again.AcceptableRows != 1 {
		t.Errorf("re-staged after cancel = %+v, %v", again, err)
	}
}

// A full 50,000-row file stages, commits into a published catalog and is read back a
// page at a time; the catalog read never carries more than one page of entries.
func TestVendorImport_FullFileIntoPublishedCatalog(t *testing.T) {
	if testing.Short() {
		t.Skip("50,000-row import")
	}
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(7)
	vendor := ids.New()
	made, err := s.CreateCatalog(ctx, draftCatalog(t, nil, catalogEntry("Existing", "MRO", "EA", "1")), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := s.ActOnCatalog(ctx, made.ID, 1, domain.CatalogPublish, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	imp, err := s.StageVendorImport(ctx, cat.ID, store.ImportInput{VendorID: vendor, Format: "CSV", Rows: importRows(vendor, domain.MaxImportRows)}, store.Idem{})
	if err != nil || imp.AcceptableRows != domain.MaxImportRows {
		t.Fatalf("stage = %+v, %v", imp, err)
	}
	staged := time.Since(start)
	if _, err := s.DecideImportRows(ctx, imp.ID, 1, store.ImportDecisions{AcceptAll: true}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	// One entry is already there, so the full file would pass the 50,000 cap.
	if _, err := s.ActOnCatalogImport(ctx, imp.ID, 2, domain.ImportCommit, store.Idem{}); problemCode(err) != store.CodeCatalogTooManyEntry {
		t.Fatalf("commit over the cap: %v", err)
	}
	if _, err := s.DecideImportRows(ctx, imp.ID, 2, store.ImportDecisions{Reject: []int{domain.MaxImportRows}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	done, err := s.ActOnCatalogImport(ctx, imp.ID, 3, domain.ImportCommit, store.Idem{})
	if err != nil || done.EntriesAdded != domain.MaxImportRows-1 {
		t.Fatalf("commit = %+v, %v", done, err)
	}
	t.Logf("50,000 rows staged in %s, committed in %s", staged, time.Since(start))
	got, err := s.GetCatalog(ctx, cat.ID)
	if err != nil || got.EntryCount != domain.MaxCatalogEntries || len(got.Entries) != store.CatalogEntryPageSize || got.EntriesNextCursor != "200" {
		t.Fatalf("catalog read: count %d, %d entries, next %q, %v", got.EntryCount, len(got.Entries), got.EntriesNextCursor, err)
	}
	page, err := s.ListCatalogEntries(ctx, cat.ID, 49900, 200)
	if err != nil || len(page.Items) != 100 || page.Next != 0 || page.Items[99].EntryNo != domain.MaxCatalogEntries || page.Items[99].Name != fmt.Sprintf("Item %d", domain.MaxImportRows-1) {
		t.Fatalf("last page = %d items next %d, %v", len(page.Items), page.Next, err)
	}
	if _, err := s.ListCatalogEntries(ctx, ids.New(), 0, 10); problemCode(err) != store.CodeCatalogNotFound {
		t.Errorf("entries of an unknown catalog: %v", err)
	}
	f := store.CatalogSearch{LegalEntityID: legalEntity, Date: day(t, "2027-03-01"), VendorID: vendor}
	if hits, err := s.SearchCatalogEntries(ctx, f, "", 50); err != nil || len(hits.Items) != 50 || hits.Next == "" {
		t.Errorf("picker over a full catalog = %d hits, %v", len(hits.Items), err)
	}
}
