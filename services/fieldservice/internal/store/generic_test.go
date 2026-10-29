package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

// userCtx is a caller of tenant n with a user id and duties and no legal-entity scope.
func userCtx(n int, user string, duties ...string) context.Context {
	d := map[string]struct{}{}
	for _, x := range duties {
		d[x] = struct{}{}
	}
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: user, Duties: d})
}

// hrSetup stores the AC-45.10 process (INTAKE -> INVESTIGATION -> RESOLUTION with an
// "Interview" required in INVESTIGATION) and an HR category restricted to HrCaseAccess.
func hrSetup(t *testing.T, s *Postgres, ctx context.Context, window int) (Process, Category) {
	t.Helper()
	proc, _, err := s.CreateProcess(ctx, Process{Code: "HR-PROC", Name: "HR process", Stages: []domain.Stage{
		{Code: "INTAKE", Sequence: 1, RequiredActivities: []string{}},
		{Code: "INVESTIGATION", Sequence: 2, RequiredActivities: []string{"Interview"}},
		{Code: "RESOLUTION", Sequence: 3, RequiredActivities: []string{}},
	}}, "proc-hr", "hash-proc-hr")
	if err != nil {
		t.Fatal(err)
	}
	cat, _, err := s.CreateCategory(ctx, Category{Code: "HR-CASE", Name: "HR case", CategoryType: domain.CategoryHR,
		ProcessID: &proc.ID, DefaultPriority: 3, ReopenWindowDays: window, AccessDuty: "HrCaseAccess",
		ResolutionCodes: []string{"FOUND", "NO_ACTION"}}, "cat-hr", "hash-cat-hr")
	if err != nil {
		t.Fatal(err)
	}
	return proc, cat
}

// AC-45.10 against Postgres as passage_app: the HR case starts at INTAKE, moves to
// INVESTIGATION, is refused RESOLUTION with CASE-STG-001 while the Interview is OPEN
// and advances after it is DONE. Every move is logged, emits
// dev.passage.case.stage.changed.v1 and replays under its Idempotency-Key.
func TestStore_GenericCaseStageGate(t *testing.T) {
	s, _ := newSliceStore(t, 41)
	hr := ids.New()
	ctx := userCtx(41, hr, "HrCaseAccess")
	_, cat := hrSetup(t, s, ctx, 14)
	worker := ids.New()
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

	c, _, err := s.OpenGenericCase(ctx, OpenGenericInput{CategoryID: cat.ID, LegalEntityID: ids.New(), Subject: "Grievance",
		Associations: []CaseAssociation{{EntityType: "worker", EntityID: worker, Role: "SUBJECT"}}, OpenedAt: at}, "case-hr", "hash-case-hr")
	if err != nil {
		t.Fatal(err)
	}
	if c.StageCode != "INTAKE" || c.Status != domain.GenericOpen || c.Version != 1 || c.Priority != 3 || c.OwnerID != hr ||
		len(c.Associations) != 1 || len(c.Log) != 1 || c.SLAResolutionDue != nil {
		t.Fatalf("opened case %+v", c)
	}
	again, replayed, err := s.OpenGenericCase(ctx, OpenGenericInput{CategoryID: cat.ID, LegalEntityID: c.LegalEntityID, Subject: "Grievance",
		OpenedAt: at}, "case-hr", "hash-case-hr")
	if err != nil || !replayed || again.ID != c.ID {
		t.Fatalf("open replay = %s replayed=%v err=%v", again.ID, replayed, err)
	}

	moved, replayed, err := s.AdvanceStage(ctx, CaseMove{ID: c.ID, Version: 1, ToStage: "INVESTIGATION", At: at}, "stg-1", "hash-stg-1")
	if err != nil || replayed || moved.StageCode != "INVESTIGATION" || moved.Version != 2 {
		t.Fatalf("advance = %+v replayed=%v err=%v", moved, replayed, err)
	}
	// The replay answers with the stored case even though the version moved on.
	if again, replayed, err := s.AdvanceStage(ctx, CaseMove{ID: c.ID, Version: 1, ToStage: "INVESTIGATION", At: at}, "stg-1", "hash-stg-1"); err != nil ||
		!replayed || again.Version != 2 {
		t.Fatalf("advance replay = %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := s.AdvanceStage(ctx, CaseMove{ID: c.ID, Version: 1, ToStage: "RESOLUTION", At: at}, "stg-stale", "hash-stg-stale"); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if _, _, err := s.AdvanceStage(ctx, CaseMove{ID: c.ID, Version: 2, ToStage: "RESOLUTION", At: at}, "stg-1", "hash-other"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("key reused with another request: %v", err)
	}

	interview, _, err := s.CreateActivity(ctx, Activity{Kind: domain.ActivityTask, Subject: "Interview", OwnerWorkerID: ids.New(),
		Associations: []Association{{EntityType: "case", EntityID: c.ID}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// A DONE "Interview" on another record does not open the gate.
	other, _, err := s.CreateActivity(ctx, Activity{Kind: domain.ActivityTask, Subject: "Interview", OwnerWorkerID: ids.New(),
		Associations: []Association{{EntityType: "case", EntityID: ids.New()}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteActivity(ctx, other.ID, other.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AdvanceStage(ctx, CaseMove{ID: c.ID, Version: 2, ToStage: "RESOLUTION", At: at}, "stg-gate", "hash-stg-gate"); problemCode(err) != domain.CodeStageGate {
		t.Fatalf("gate with the Interview OPEN: %v", err)
	}
	if got, err := s.GetGenericCase(ctx, c.ID); err != nil || got.StageCode != "INVESTIGATION" || got.Version != 2 {
		t.Fatalf("after the refused gate: %+v %v", got, err)
	}
	if _, err := s.CompleteActivity(ctx, interview.ID, interview.Version); err != nil {
		t.Fatal(err)
	}
	passed, _, err := s.AdvanceStage(ctx, CaseMove{ID: c.ID, Version: 2, ToStage: "RESOLUTION", At: at}, "stg-2", "hash-stg-2")
	if err != nil || passed.StageCode != "RESOLUTION" || passed.Version != 3 {
		t.Fatalf("advance after the Interview is DONE = %+v %v", passed, err)
	}
	if _, _, err := s.AdvanceStage(ctx, CaseMove{ID: c.ID, Version: 3, ToStage: "INVESTIGATION", At: at}, "stg-back-0", "hash-stg-back-0"); problemCode(err) != domain.CodeStageInvalid {
		t.Fatalf("back without a reason: %v", err)
	}
	back, _, err := s.AdvanceStage(ctx, CaseMove{ID: c.ID, Version: 3, ToStage: "INVESTIGATION", Reason: "new evidence", At: at}, "stg-back", "hash-stg-back")
	if err != nil || back.StageCode != "INVESTIGATION" || back.Log[len(back.Log)-1].Text != "stage RESOLUTION to INVESTIGATION: new evidence" {
		t.Fatalf("back with a reason = %+v %v", back, err)
	}
	stageEvents := 0
	for _, typ := range outboxTypes(t, s, ctx, c.ID) {
		if typ == svcevents.CaseStageChanged {
			stageEvents++
		}
	}
	if stageEvents != 3 {
		t.Fatalf("%d stage events, want 3 (forward, forward, back)", stageEvents)
	}
}

// BR-45.9 resolution, close and reopen: RESOLVED needs a code from the category list;
// a stage move on a CLOSED case is refused; a reopen inside the window reopens the
// case itself, and one after it creates a new case linked by reopenedFrom.
func TestStore_GenericCaseResolveCloseAndReopen(t *testing.T) {
	s, _ := newSliceStore(t, 42)
	ctx := userCtx(42, ids.New(), "HrCaseAccess")
	_, cat := hrSetup(t, s, ctx, 14)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	c, _, err := s.OpenGenericCase(ctx, OpenGenericInput{CategoryID: cat.ID, LegalEntityID: ids.New(), Subject: "Pay query", Priority: 2,
		OpenedAt: at}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	move := func(v int, to, code, key string) (GenericCase, error) {
		got, _, err := s.TransitionGenericCase(ctx, CaseMove{ID: c.ID, Version: v, ToStatus: to, ResolutionCode: code, At: at}, key, "hash-"+key)
		return got, err
	}
	if _, err := move(1, domain.GenericResolved, "", "res-0"); problemCode(err) != domain.CodeResolutionRequired {
		t.Fatalf("resolve without a code: %v", err)
	}
	if _, err := move(1, domain.GenericResolved, "OTHER", "res-1"); problemCode(err) != domain.CodeResolutionUnknown {
		t.Fatalf("resolve with a code outside the list: %v", err)
	}
	if _, err := move(1, domain.GenericClosed, "", "res-2"); problemCode(err) != domain.CodeCaseState {
		t.Fatalf("close an OPEN case: %v", err)
	}
	resolved, err := move(1, domain.GenericResolved, "FOUND", "res-3")
	if err != nil || resolved.ResolutionCode == nil || *resolved.ResolutionCode != "FOUND" || resolved.ResolvedAt == nil {
		t.Fatalf("resolve = %+v %v", resolved, err)
	}
	closed, err := move(resolved.Version, domain.GenericClosed, "", "res-4")
	if err != nil || closed.Status != domain.GenericClosed || closed.ClosedAt == nil {
		t.Fatalf("close = %+v %v", closed, err)
	}
	if _, _, err := s.AdvanceStage(ctx, CaseMove{ID: c.ID, Version: closed.Version, ToStage: "INVESTIGATION", At: at}, "", ""); problemCode(err) != domain.CodeCaseState {
		t.Fatalf("stage move on a CLOSED case: %v", err)
	}
	if _, _, err := s.AddAssociation(ctx, c.ID, CaseAssociation{EntityType: "item", EntityID: ids.New(), Role: "RELATED"}, at, "", ""); problemCode(err) != domain.CodeCaseState {
		t.Fatalf("association on a CLOSED case: %v", err)
	}

	inside, _, err := s.ReopenGenericCase(ctx, CaseMove{ID: c.ID, Version: closed.Version, Reason: "employee follow-up",
		At: at.AddDate(0, 0, 10)}, "reopen-1", "hash-reopen-1")
	if err != nil || inside.Created || inside.Case.ID != c.ID || inside.Case.Status != domain.GenericOpen ||
		inside.Case.ResolutionCode != nil || inside.Case.StageCode != "INTAKE" {
		t.Fatalf("reopen inside the window = %+v %v", inside, err)
	}
	resolved, err = move(inside.Case.Version, domain.GenericResolved, "NO_ACTION", "res-5")
	if err != nil {
		t.Fatal(err)
	}
	later := at.AddDate(0, 0, 40)
	outside, _, err := s.ReopenGenericCase(ctx, CaseMove{ID: c.ID, Version: resolved.Version, Reason: "raised again", At: later},
		"reopen-2", "hash-reopen-2")
	if err != nil || !outside.Created || outside.Case.ID == c.ID || outside.Case.ReopenedFrom == nil || *outside.Case.ReopenedFrom != c.ID ||
		outside.Case.Status != domain.GenericOpen || !outside.Case.OpenedAt.Equal(later) || outside.Case.Priority != 2 {
		t.Fatalf("reopen after the window = %+v %v", outside, err)
	}
	if replay, replayed, err := s.ReopenGenericCase(ctx, CaseMove{ID: c.ID, Version: resolved.Version, Reason: "raised again", At: later},
		"reopen-2", "hash-reopen-2"); err != nil || !replayed || replay.Case.ID != outside.Case.ID {
		t.Fatalf("reopen replay = %+v replayed=%v err=%v", replay, replayed, err)
	}
	original, err := s.GetGenericCase(ctx, c.ID)
	if err != nil || original.Status != domain.GenericResolved || original.Log[len(original.Log)-1].Kind != "REOPENED" {
		t.Fatalf("the original after a reopen as a new case = %+v %v", original, err)
	}
	canceled, _, err := s.TransitionGenericCase(ctx, CaseMove{ID: outside.Case.ID, Version: outside.Case.Version, ToStatus: domain.GenericCanceled,
		At: later}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReopenGenericCase(ctx, CaseMove{ID: canceled.ID, Version: canceled.Version, Reason: "x", At: later}, "", ""); problemCode(err) != domain.CodeCaseState {
		t.Fatalf("reopen a CANCELED case: %v", err)
	}
}

// AC-45.11 and BR-45.10: an HR case is hidden from a caller without the category's
// duty (CASE-SEC-001 on read and write, absent from the list) and open to its owner
// and duty holders; an unrestricted category is open to every CaseMaintain holder.
func TestStore_GenericCaseCategorySecurity(t *testing.T) {
	s, _ := newSliceStore(t, 43)
	partner := userCtx(43, ids.New(), "HrCaseAccess", "CaseCreate")
	collector := userCtx(43, ids.New(), "CollectionsAccess", "CaseCreate")
	_, hrCat := hrSetup(t, s, partner, 14)
	open, _, err := s.CreateCategory(partner, Category{Code: "INTERNAL", Name: "Internal", CategoryType: domain.CategoryInternal,
		DefaultPriority: 4, ResolutionCodes: []string{}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	le := ids.New()
	hrCase, _, err := s.OpenGenericCase(partner, OpenGenericInput{CategoryID: hrCat.ID, LegalEntityID: le, Subject: "HR matter", OpenedAt: at}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OpenGenericCase(collector, OpenGenericInput{CategoryID: hrCat.ID, LegalEntityID: le, Subject: "x", OpenedAt: at}, "", ""); problemCode(err) != domain.CodeCaseSecurity {
		t.Fatalf("collector opened an HR case: %v", err)
	}
	internal, _, err := s.OpenGenericCase(collector, OpenGenericInput{CategoryID: open.ID, LegalEntityID: le, Subject: "Laptop", OpenedAt: at}, "", "")
	if err != nil || internal.Priority != 4 {
		t.Fatalf("collector opens an internal case = %+v %v", internal, err)
	}

	if _, err := s.GetGenericCase(collector, hrCase.ID); problemCode(err) != domain.CodeCaseSecurity {
		t.Fatalf("collector read the HR case: %v", err)
	}
	if _, _, err := s.AdvanceStage(collector, CaseMove{ID: hrCase.ID, Version: 1, ToStage: "INVESTIGATION", At: at}, "", ""); problemCode(err) != domain.CodeCaseSecurity {
		t.Fatalf("collector moved the HR case: %v", err)
	}
	if _, _, err := s.AddAssociation(collector, hrCase.ID, CaseAssociation{EntityType: "invoice", EntityID: ids.New(), Role: "RELATED"}, at, "", ""); problemCode(err) != domain.CodeCaseSecurity {
		t.Fatalf("collector associated a record with the HR case: %v", err)
	}
	idsOf := func(p Page[GenericCase]) []string {
		var out []string
		for _, c := range p.Items {
			out = append(out, c.ID)
		}
		return out
	}
	seen, err := s.ListGenericCases(collector, GenericFilter{Limit: 10})
	if err != nil || slices.Contains(idsOf(seen), hrCase.ID) || !slices.Contains(idsOf(seen), internal.ID) {
		t.Fatalf("collector lists %v, %v; want the internal case only", idsOf(seen), err)
	}
	// The owner keeps access without the duty (a partner who later lost it).
	owner := userCtx(43, hrCase.OwnerID)
	if _, err := s.GetGenericCase(owner, hrCase.ID); err != nil {
		t.Fatalf("owner without the duty: %v", err)
	}
	other := userCtx(43, "another-partner", "HrCaseAccess")
	if got, err := s.ListGenericCases(other, GenericFilter{CategoryID: hrCat.ID, Limit: 10}); err != nil || len(got.Items) != 1 {
		t.Fatalf("another duty holder lists %v, %v", idsOf(got), err)
	}
}

// FR-45.12 associations, list filters, the category SLA profile and the customer
// rules: COLLECTIONS needs a customer, SERVICE categories are refused, the list
// filters by stage, status and associated record and pages by cursor.
func TestStore_GenericCaseAssociationsListAndSLA(t *testing.T) {
	s, _ := newSliceStore(t, 44)
	ctx := userCtx(44, ids.New())
	prof, _, err := s.CreateSLAProfile(ctx, SLAProfile{Code: "COLL", Name: "Collections", TimeZone: "UTC", AllDay: true,
		Targets: []domain.Target{{Priority: 2, ResponseMinutes: 60, ResolutionMinutes: 1440}}}, "", "hash-coll")
	if err != nil {
		t.Fatal(err)
	}
	coll, _, err := s.CreateCategory(ctx, Category{Code: "COLL", Name: "Collections", CategoryType: domain.CategoryCollections,
		SLAProfileID: &prof.ID, DefaultPriority: 2, ResolutionCodes: []string{}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	svc, _, err := s.CreateCategory(ctx, Category{Code: "SVC", Name: "Service", CategoryType: domain.CategoryService,
		DefaultPriority: 3, ResolutionCodes: []string{}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateCategory(ctx, Category{Code: "SVC", Name: "Again", CategoryType: domain.CategoryAudit, DefaultPriority: 3,
		ResolutionCodes: []string{}}, "", ""); problemCode(err) != CodeCategoryDuplicate {
		t.Fatalf("duplicate category code: %v", err)
	}
	missing := ids.New()
	if _, _, err := s.CreateCategory(ctx, Category{Code: "CHILD", Name: "Child", CategoryType: domain.CategoryAudit, ParentID: &missing,
		DefaultPriority: 3, ResolutionCodes: []string{}}, "", ""); problemCode(err) != CodeCategoryUnknown {
		t.Fatalf("unknown parent: %v", err)
	}
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	le, customer := ids.New(), ids.New()
	if _, _, err := s.OpenGenericCase(ctx, OpenGenericInput{CategoryID: svc.ID, LegalEntityID: le, CustomerPartyID: &customer, Subject: "x",
		OpenedAt: at}, "", ""); problemCode(err) != domain.CodeCategoryRouted {
		t.Fatalf("SERVICE category on the generic endpoint: %v", err)
	}
	if _, _, err := s.OpenGenericCase(ctx, OpenGenericInput{CategoryID: coll.ID, LegalEntityID: le, Subject: "x", OpenedAt: at}, "", ""); problemCode(err) != domain.CodeCustomerRequired {
		t.Fatalf("COLLECTIONS without a customer: %v", err)
	}
	if _, _, err := s.OpenGenericCase(ctx, OpenGenericInput{CategoryID: coll.ID, LegalEntityID: le, CustomerPartyID: &customer, Subject: "x",
		Priority: 1, OpenedAt: at}, "", ""); problemCode(err) != CodeNoTarget {
		t.Fatalf("priority with no target in the category profile: %v", err)
	}
	invoice := ids.New()
	first, _, err := s.OpenGenericCase(ctx, OpenGenericInput{CategoryID: coll.ID, LegalEntityID: le, CustomerPartyID: &customer,
		Subject: "Disputed invoice", OpenedAt: at}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.SLAResponseDue == nil || !first.SLAResponseDue.Equal(at.Add(time.Hour)) || !first.SLAResolutionDue.Equal(at.Add(24*time.Hour)) {
		t.Fatalf("category SLA dues %v %v", first.SLAResponseDue, first.SLAResolutionDue)
	}
	second, _, err := s.OpenGenericCase(ctx, OpenGenericInput{CategoryID: coll.ID, LegalEntityID: le, CustomerPartyID: &customer,
		Subject: "Second", OpenedAt: at}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	linked, replayed, err := s.AddAssociation(ctx, first.ID, CaseAssociation{EntityType: "invoice", EntityID: invoice, Role: "SUBJECT"}, at, "assoc-1", "hash-assoc-1")
	if err != nil || replayed || len(linked.Associations) != 1 || linked.Version != 2 {
		t.Fatalf("associate = %+v replayed=%v err=%v", linked, replayed, err)
	}
	if again, replayed, err := s.AddAssociation(ctx, first.ID, CaseAssociation{EntityType: "invoice", EntityID: invoice, Role: "SUBJECT"}, at,
		"assoc-1", "hash-assoc-1"); err != nil || !replayed || again.Version != 2 {
		t.Fatalf("association replay = %+v replayed=%v err=%v", again, replayed, err)
	}
	// The same record again with another role replaces the role.
	relinked, _, err := s.AddAssociation(ctx, first.ID, CaseAssociation{EntityType: "invoice", EntityID: invoice, Role: "RELATED"}, at, "", "")
	if err != nil || len(relinked.Associations) != 1 || relinked.Associations[0].Role != "RELATED" {
		t.Fatalf("re-association = %+v %v", relinked.Associations, err)
	}

	byInvoice, err := s.ListGenericCases(ctx, GenericFilter{EntityType: "invoice", EntityID: invoice, Limit: 10})
	if err != nil || len(byInvoice.Items) != 1 || byInvoice.Items[0].ID != first.ID {
		t.Fatalf("list by invoice = %+v %v", byInvoice.Items, err)
	}
	page1, err := s.ListGenericCases(ctx, GenericFilter{CategoryID: coll.ID, Status: domain.GenericOpen, Limit: 1})
	if err != nil || len(page1.Items) != 1 || page1.NextCursor == "" {
		t.Fatalf("first page = %+v %v", page1, err)
	}
	page2, err := s.ListGenericCases(ctx, GenericFilter{CategoryID: coll.ID, Status: domain.GenericOpen, Cursor: page1.NextCursor, Limit: 1})
	if err != nil || len(page2.Items) != 1 || page2.NextCursor != "" || page2.Items[0].ID == page1.Items[0].ID {
		t.Fatalf("second page = %+v %v", page2, err)
	}
	if got := []string{page1.Items[0].ID, page2.Items[0].ID}; !slices.Contains(got, first.ID) || !slices.Contains(got, second.ID) {
		t.Fatalf("pages hold %v", got)
	}
	if none, err := s.ListGenericCases(ctx, GenericFilter{Status: domain.GenericResolved, Limit: 10}); err != nil || len(none.Items) != 0 {
		t.Fatalf("no RESOLVED case expected: %+v %v", none.Items, err)
	}
}

// Generic cases under RLS as passage_app: another tenant sees no process, category
// or case, and a caller scoped to another legal entity sees none of the entity's cases.
func TestStore_GenericCaseTenantAndLegalEntityIsolation(t *testing.T) {
	s, _ := newSliceStore(t, 45)
	t1 := userCtx(45, ids.New())
	t2 := userCtx(46, ids.New())
	cat, _, err := s.CreateCategory(t1, Category{Code: "AUDIT", Name: "Audit", CategoryType: domain.CategoryAudit, DefaultPriority: 3,
		ResolutionCodes: []string{}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := s.OpenGenericCase(t1, OpenGenericInput{CategoryID: cat.ID, LegalEntityID: ids.New(), Subject: "Finding 7",
		OpenedAt: time.Now().UTC()}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetGenericCase(t2, c.ID); problemCode(err) != CodeGenericUnknown {
		t.Fatalf("tenant 2 read tenant 1's case: %v", err)
	}
	if page, err := s.ListGenericCases(t2, GenericFilter{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 lists %d cases, %v", len(page.Items), err)
	}
	if cats, err := s.ListCategories(t2); err != nil || len(cats) != 0 {
		t.Fatalf("tenant 2 lists %d categories, %v", len(cats), err)
	}
	if _, _, err := s.OpenGenericCase(t2, OpenGenericInput{CategoryID: cat.ID, LegalEntityID: ids.New(), Subject: "x",
		OpenedAt: time.Now().UTC()}, "", ""); problemCode(err) != CodeCategoryUnknown {
		t.Fatalf("tenant 2 used tenant 1's category: %v", err)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(45), LegalEntityID: ids.New(), Duties: map[string]struct{}{}})
	if page, err := s.ListGenericCases(scoped, GenericFilter{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a caller scoped to another legal entity lists %d cases, %v", len(page.Items), err)
	}
}
