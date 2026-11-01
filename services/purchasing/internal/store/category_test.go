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

func dayOf(t *testing.T, s string) domain.Day {
	t.Helper()
	d, err := domain.ParseDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCategoryVendor_CreateChangeListAndEvents(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	user := ids.New()
	ctx := asUser(1, user)
	vendor := ids.New()
	in := domain.CategoryVendor{LegalEntityID: legalEntity, Category: "IT hardware", VendorID: vendor, Status: domain.AssignmentProvisional,
		ValidFrom: dayOf(t, "2026-03-01")}
	made, err := s.CreateCategoryVendor(ctx, in, store.Idem{Key: "cv1", Hash: "h1"})
	if err != nil || made.Version != 1 || made.Status != domain.AssignmentProvisional || made.CreatedBy != user || made.ValidTo != nil {
		t.Fatalf("create = %+v, %v", made, err)
	}
	replay, err := s.CreateCategoryVendor(ctx, in, store.Idem{Key: "cv1", Hash: "h1"})
	if err != nil || replay.ID != made.ID {
		t.Fatalf("a replayed key returns the first row: %+v, %v", replay, err)
	}
	if _, err := s.CreateCategoryVendor(ctx, in, store.Idem{Key: "cv1", Hash: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Errorf("same key, other body: %v", err)
	}
	if _, err := s.CreateCategoryVendor(ctx, in, store.Idem{Key: "cv2", Hash: "h2"}); problemCode(err) != store.CodeCategoryVendorExists {
		t.Errorf("a second assignment of the vendor to the category: %v", err)
	}
	// The failed create left no claim behind.
	other := in
	other.Category = "MRO"
	if _, err := s.CreateCategoryVendor(ctx, other, store.Idem{Key: "cv2", Hash: "h2"}); err != nil {
		t.Errorf("the same vendor in another category, and the key of the failed create: %v", err)
	}

	to := dayOf(t, "2026-12-31")
	if _, err := s.UpdateCategoryVendor(ctx, made.ID, 7, domain.AssignmentApproved, made.ValidFrom, &to, ""); problemCode(err) != store.CodeCategoryVendorStale {
		t.Errorf("stale If-Match: %v", err)
	}
	next, err := s.UpdateCategoryVendor(ctx, made.ID, 1, domain.AssignmentApproved, made.ValidFrom, &to, "audited")
	if err != nil || next.Version != 2 || next.Status != domain.AssignmentApproved || next.ValidTo == nil || next.ValidTo.String() != "2026-12-31" || next.Notes != "audited" {
		t.Fatalf("update = %+v, %v", next, err)
	}
	if _, err := s.GetCategoryVendor(ctx, ids.New()); problemCode(err) != store.CodeCategoryVendorNotFound {
		t.Errorf("unknown id: %v", err)
	}

	page, err := s.ListCategoryVendors(ctx, store.CategoryVendorFilter{LegalEntityID: legalEntity}, "", 1)
	if err != nil || len(page.Items) != 1 || page.Next == "" || page.Items[0].Category != "MRO" {
		t.Fatalf("first page newest first = %+v, %v", page, err)
	}
	rest, err := s.ListCategoryVendors(ctx, store.CategoryVendorFilter{LegalEntityID: legalEntity}, page.Next, 5)
	if err != nil || len(rest.Items) != 1 || rest.Items[0].ID != made.ID {
		t.Fatalf("second page = %+v, %v", rest, err)
	}
	if byStatus, _ := s.ListCategoryVendors(ctx, store.CategoryVendorFilter{Status: domain.AssignmentBlocked}, "", 5); len(byStatus.Items) != 0 {
		t.Errorf("status filter: %+v", byStatus.Items)
	}
	if byCat, _ := s.ListCategoryVendors(ctx, store.CategoryVendorFilter{Category: "IT hardware", VendorID: vendor}, "", 5); len(byCat.Items) != 1 {
		t.Errorf("category and vendor filter: %+v", byCat.Items)
	}
	assigned, err := s.CategoryAssignments(ctx, legalEntity, []string{"IT hardware", "Unknown"})
	if err != nil || len(assigned) != 1 || assigned[0].VendorID != vendor {
		t.Fatalf("assignments for the rule = %+v, %v", assigned, err)
	}
	if none, _ := s.CategoryAssignments(ctx, legalEntity, nil); len(none) != 0 {
		t.Errorf("no categories, no rows: %+v", none)
	}

	counts := map[string]int{}
	for _, e := range outbox(t, ctx, pool) {
		counts[e.Type]++
	}
	if counts[purchasingevents.CategoryVendorAssigned] != 2 || counts[purchasingevents.CategoryVendorChanged] != 1 {
		t.Errorf("events = %v; want 2 assigned (the replay and the failures write none) and 1 changed", counts)
	}
	if other, _ := s.ListCategoryVendors(asUser(2, user), store.CategoryVendorFilter{}, "", 5); len(other.Items) != 0 {
		t.Errorf("another tenant sees %d assignments", len(other.Items))
	}
}

func TestCategoryVendor_LegalEntityScopeHidesOtherEntities(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	entityA, entityB := ids.New(), ids.New()
	ctxA := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(92), LegalEntityID: entityA, UserID: ids.New()})
	ctxB := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(92), LegalEntityID: entityB, UserID: ids.New()})
	made, err := s.CreateCategoryVendor(ctxA, domain.CategoryVendor{LegalEntityID: entityA, Category: "IT", VendorID: ids.New(),
		Status: domain.AssignmentApproved, ValidFrom: dayOf(t, "2026-03-01")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCategoryVendor(ctxB, made.ID); problemCode(err) != store.CodeCategoryVendorNotFound {
		t.Errorf("entity B reads entity A's assignment: %v", err)
	}
	if page, _ := s.ListCategoryVendors(ctxB, store.CategoryVendorFilter{}, "", 5); len(page.Items) != 0 {
		t.Errorf("entity B lists %d", len(page.Items))
	}
	if rows, _ := s.CategoryAssignments(ctxB, entityA, []string{"IT"}); len(rows) != 0 {
		t.Errorf("entity B's rule reads %d of entity A", len(rows))
	}
	if _, err := s.CreateCategoryVendor(ctxB, domain.CategoryVendor{LegalEntityID: entityA, Category: "IT", VendorID: ids.New(),
		Status: domain.AssignmentApproved, ValidFrom: dayOf(t, "2026-03-01")}, store.Idem{}); err == nil {
		t.Error("entity B wrote a row for entity A")
	}
}

func TestCategoryManager_CreateListDeleteAndEvents(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	user := ids.New()
	ctx := asUser(1, user)
	worker := ids.New()
	in := domain.CategoryManager{LegalEntityID: legalEntity, Category: "IT hardware", WorkerID: worker}
	made, err := s.CreateCategoryManager(ctx, in, store.Idem{Key: "m1", Hash: "h"})
	if err != nil || made.CreatedBy != user {
		t.Fatalf("create = %+v, %v", made, err)
	}
	if again, err := s.CreateCategoryManager(ctx, in, store.Idem{Key: "m1", Hash: "h"}); err != nil || again.ID != made.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.CreateCategoryManager(ctx, in, store.Idem{Key: "m2", Hash: "h2"}); problemCode(err) != store.CodeCategoryManagerExists {
		t.Errorf("the worker twice: %v", err)
	}
	if _, err := s.CreateCategoryManager(ctx, domain.CategoryManager{LegalEntityID: legalEntity, Category: "IT hardware", WorkerID: ids.New()}, store.Idem{}); err != nil {
		t.Fatalf("a second manager: %v", err)
	}
	page, err := s.ListCategoryManagers(ctx, store.CategoryManagerFilter{Category: "IT hardware"}, "", 1)
	if err != nil || len(page.Items) != 1 || page.Next == "" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if byWorker, _ := s.ListCategoryManagers(ctx, store.CategoryManagerFilter{WorkerID: worker}, "", 5); len(byWorker.Items) != 1 {
		t.Errorf("worker filter: %+v", byWorker.Items)
	}
	if err := s.DeleteCategoryManager(ctx, made.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCategoryManager(ctx, made.ID); problemCode(err) != store.CodeCategoryManagerNotFound {
		t.Errorf("deleting twice: %v", err)
	}
	if left, _ := s.ListCategoryManagers(ctx, store.CategoryManagerFilter{}, "", 5); len(left.Items) != 1 {
		t.Errorf("left = %d, want 1", len(left.Items))
	}
	counts := map[string]int{}
	for _, e := range outbox(t, ctx, pool) {
		counts[e.Type]++
	}
	if counts[purchasingevents.CategoryManagerAssigned] != 2 || counts[purchasingevents.CategoryManagerRemoved] != 1 {
		t.Errorf("events = %v", counts)
	}
}

func TestPolicy_CategoryAccessModeDefaultsToSoftAndIsSaved(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := asUser(1, ids.New())
	if got, err := s.GetPolicy(ctx, legalEntity); err != nil || got.CategoryAccessMode != domain.AccessSoft {
		t.Fatalf("default = %+v, %v", got, err)
	}
	saved, err := s.PutPolicy(ctx, domain.Policy{LegalEntityID: legalEntity, ChangeManagementDefault: true, CategoryAccessMode: domain.AccessHard}, 0)
	if err != nil || saved.CategoryAccessMode != domain.AccessHard {
		t.Fatalf("create = %+v, %v", saved, err)
	}
	next, err := s.PutPolicy(ctx, domain.Policy{LegalEntityID: legalEntity, ChangeManagementDefault: true, CategoryAccessMode: domain.AccessOff}, 1)
	if err != nil || next.CategoryAccessMode != domain.AccessOff {
		t.Fatalf("update = %+v, %v", next, err)
	}
	list, _ := s.ListPolicies(ctx)
	if len(list) != 1 || list[0].CategoryAccessMode != domain.AccessOff {
		t.Errorf("list = %+v", list)
	}
}

// The PO line category (FR-17.20) is written on create and on a line an edit adds, read
// back with the PO, and kept on an existing line by an edit.
func TestPurchaseOrderLine_CategoryIsStoredAndKept(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(61)
	in := lifecyclePO(t, ids.New(), "5", "2027-01-05")
	in.Lines[0].Category = "IT hardware"
	po, err := s.CreatePurchaseOrder(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetPurchaseOrder(ctx, po.ID); err != nil || got.Lines[0].Category != "IT hardware" {
		t.Fatalf("read back = %+v, %v", got.Lines, err)
	}
	next := edited(po, lifecyclePO(t, "", "6", "2027-01-05"))
	next.Lines[0].Category = "IT hardware"
	added := next.Lines[0]
	added.LineNo, added.Category = 2, "MRO"
	next.Lines = append(next.Lines, added)
	next.Subtotal = next.Subtotal.Add(added.LineAmount)
	next.Total = next.Subtotal
	out, err := s.EditPurchaseOrder(ctx, po.ID, po.RowVersion, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 2 || out.Lines[0].Category != "IT hardware" || out.Lines[1].Category != "MRO" {
		t.Fatalf("after edit = %+v", out.Lines)
	}
}

// Two changes made from the same version: the second is refused even if it read the row
// before the first committed (the update carries the version predicate).
func TestCategoryVendor_ConcurrentChangesFromOneVersion(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := asUser(1, ids.New())
	made, err := s.CreateCategoryVendor(ctx, domain.CategoryVendor{LegalEntityID: legalEntity, Category: "IT", VendorID: ids.New(),
		Status: domain.AssignmentApproved, ValidFrom: dayOf(t, "2026-01-01")}, store.Idem{Key: "cc1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	for _, st := range []string{domain.AssignmentBlocked, domain.AssignmentProvisional} {
		go func(st string) {
			_, err := s.UpdateCategoryVendor(ctx, made.ID, 1, st, made.ValidFrom, nil, "")
			errs <- err
		}(st)
	}
	ok, stale := 0, 0
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case problemCode(err) == store.CodeCategoryVendorStale:
			stale++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || stale != 1 {
		t.Fatalf("ok %d stale %d", ok, stale)
	}
	if got, _ := s.GetCategoryVendor(ctx, made.ID); got.Version != 2 {
		t.Errorf("version = %d", got.Version)
	}
}
