package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

func auditKinds(items []domain.AuditEntry) []string {
	out := make([]string, len(items))
	for i, e := range items {
		out[i] = e.Kind
	}
	return out
}

// AC-15.9 (FR-15.10): a reopened period with a posting and a re-close lists
// the reopen with both approvers, the posting and the re-close in the order
// they happened, with their trace ids, whatever order the events arrived in.
// Redelivered events are replays, and a batch with one bad entry writes none.
func TestAuditTrail_ReopenPostingAndReCloseInOrder(t *testing.T) {
	st, ctx := migrated(t)
	ledger, other := ids.New(), ids.New()
	t0 := time.Date(2026, 4, 3, 9, 0, 0, 0, time.UTC)
	entry := func(min int, kind, actor, object, trace, event string, details map[string]any) domain.AuditEntry {
		return domain.AuditEntry{OccurredAt: t0.Add(time.Duration(min) * time.Minute), LedgerID: ledger, Period: "2026-03", Kind: kind,
			ActorID: actor, ObjectRef: object, TraceID: trace, SourceEventID: event, Details: details}
	}
	reopen := []domain.AuditEntry{
		entry(50, domain.AuditPeriodChanged, "u-controller", "period:2026-03", "t-close", "e6", map[string]any{"from": "open", "to": "closed"}),
		entry(30, domain.AuditPosted, "u-accountant", "journal:JV-0042", "t-post", "e5",
			map[string]any{"voucher": "JV-0042", "amount": json.Number("1250.000000"), "replayed": false}),
		entry(0, domain.AuditPeriodChanged, "u-controller", "period:2026-03", "t-request", "e1", map[string]any{"from": "closed", "to": "reopen_requested"}),
		entry(5, domain.AuditReopenApproved, "u-cfo", "period:2026-03", "t-ap1", "e2", map[string]any{"approver": "u-cfo", "step": 1}),
		entry(6, domain.AuditReopenApproved, "u-auditor", "period:2026-03", "t-ap2", "e3", map[string]any{"approver": "u-auditor", "step": 2}),
		entry(7, domain.AuditPeriodChanged, "u-controller", "period:2026-03", "t-open", "e4", map[string]any{"from": "reopen_requested", "to": "open"}),
	}
	noise := entry(10, domain.AuditPosted, "u-accountant", "journal:JV-9", "t-x", "e7", nil)
	noise.LedgerID = other
	feb := entry(11, domain.AuditPosted, "u-accountant", "journal:JV-8", "t-y", "e8", nil)
	feb.Period = "2026-02"
	if n, err := st.PutAuditEntries(ctx, append(append([]domain.AuditEntry{}, reopen...), noise, feb)); err != nil || n != 8 {
		t.Fatalf("PutAuditEntries = %d, %v", n, err)
	}

	f := domain.AuditFilter{LedgerID: ledger, Period: "2026-03"}
	page, err := st.AuditTrail(ctx, f, nil, domain.AuditMaxPage)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"period_changed", "reopen_approved", "reopen_approved", "period_changed", "posted", "period_changed"}
	if got := auditKinds(page.Items); strings.Join(got, ",") != strings.Join(want, ",") || page.Next != nil {
		t.Fatalf("kinds = %v (next %v), want %v", got, page.Next, want)
	}
	var traces, approvers []string
	for _, e := range page.Items {
		traces = append(traces, e.TraceID)
		if e.Kind == domain.AuditReopenApproved {
			approvers = append(approvers, e.Details["approver"].(string))
		}
	}
	if strings.Join(traces, ",") != "t-request,t-ap1,t-ap2,t-open,t-post,t-close" || strings.Join(approvers, ",") != "u-cfo,u-auditor" {
		t.Fatalf("traces %v approvers %v", traces, approvers)
	}
	posted := page.Items[4]
	if posted.Details["amount"] != json.Number("1250.000000") || posted.Details["replayed"] != false || !posted.OccurredAt.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("posting details are not kept exactly: %+v", posted)
	}

	// Keyset pages of two give the same entries in the same order.
	var paged []string
	var after *domain.AuditCursor
	for i := 0; i < 5; i++ {
		p, err := st.AuditTrail(ctx, f, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Items {
			paged = append(paged, e.TraceID)
		}
		if after = p.Next; after == nil {
			break
		}
	}
	if strings.Join(paged, ",") != strings.Join(traces, ",") {
		t.Fatalf("paged traces %v, want %v", paged, traces)
	}

	// Kind and actor filters; a date window across ledgers and periods.
	if p, _ := st.AuditTrail(ctx, domain.AuditFilter{Period: "2026-03", Kind: domain.AuditReopenApproved, ActorID: "u-auditor"}, nil, 10); len(p.Items) != 1 || p.Items[0].TraceID != "t-ap2" {
		t.Fatalf("kind and actor filter = %+v", p.Items)
	}
	if p, _ := st.AuditTrail(ctx, domain.AuditFilter{From: t0.Add(10 * time.Minute), To: t0.Add(12 * time.Minute)}, nil, 10); strings.Join(auditKinds(p.Items), ",") != "posted,posted" {
		t.Fatalf("window = %+v", p.Items)
	}

	// Redelivered events are replays; a batch with a bad entry writes nothing.
	if n, err := st.PutAuditEntries(ctx, reopen); err != nil || n != 0 {
		t.Fatalf("redelivery wrote %d, %v", n, err)
	}
	fresh := entry(60, domain.AuditPosted, "u-accountant", "journal:JV-0043", "t-z", "e9", nil)
	var de *domain.Error
	for name, bad := range map[string]domain.AuditEntry{
		"reporting kind":     entry(61, domain.AuditStatementRun, "u", "run:x", "", "e10", nil),
		"no source event id": entry(61, domain.AuditPosted, "u", "journal:J", "", "", nil),
		"no actor":           entry(61, domain.AuditPosted, "", "journal:J", "", "e11", nil),
	} {
		if _, err := st.PutAuditEntries(ctx, []domain.AuditEntry{fresh, bad}); !errors.As(err, &de) || de.Code != domain.CodeAuditInvalid {
			t.Fatalf("%s: err = %v, want %s", name, err, domain.CodeAuditInvalid)
		}
	}
	if p, _ := st.AuditTrail(ctx, f, nil, 100); len(p.Items) != len(reopen) {
		t.Fatalf("a refused batch wrote entries: %d", len(p.Items))
	}

	// An unbounded filter never reaches the database.
	if _, err := st.AuditTrail(ctx, domain.AuditFilter{LedgerID: ledger}, nil, 10); !errors.Is(err, domain.ErrAuditFilter) {
		t.Fatalf("unbounded filter = %v", err)
	}
}

// FR-15.10: publishing, running, scheduling, retiring and hierarchy publishing
// each write their audit entry in the same transaction as the change; a
// refused or replayed change writes none. An export is recorded with its
// digest, and the digest matches the file.
func TestAuditTrail_RecordsReportingChangesAndExports(t *testing.T) {
	st, ctx := migrated(t)
	from := time.Now().Add(-time.Minute)
	if _, _, err := st.SaveDraft(ctx, storeFixtureDefinition("SCHED-DEF"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "SCHED-DEF", "publisher"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "SCHED-DEF", "publisher"); problemCode(err) != CodeDefinitionNotFound {
		t.Fatalf("second publish without a draft = %v", err)
	}
	ledger := ids.New()
	seedBalance(t, ctx, st.pool, ledger, 2026, 3, "1000", "acct", "10", 7)
	spec := RunSpec{Code: "SCHED-DEF", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), RequestedBy: "runner", IdempotencyKey: "audit-run", RequestHash: "h"}
	run, err := st.CreateRun(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := st.CreateRun(ctx, spec); err != nil || !replay.Replayed {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	sc := nightly()
	if _, _, err := st.SaveSchedule(ctx, sc, nil); err != nil {
		t.Fatal(err)
	}
	stale := 9
	if _, _, err := st.SaveSchedule(ctx, sc, &stale); problemCode(err) != CodeRevisionConflict {
		t.Fatalf("stale save = %v", err)
	}
	if _, _, err := st.SaveHierarchy(ctx, accountTree(false), nil, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PublishHierarchy(ctx, "HIER-BS", "publisher", []domain.Member{{ID: "a-cash", Key: "100100"}, {ID: "a-rev", Key: "400100"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RetireDefinition(ctx, "SCHED-DEF"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RetireDefinition(ctx, "SCHED-DEF"); err != nil {
		t.Fatal(err)
	}
	f := domain.AuditFilter{From: from, To: time.Now().Add(time.Minute)}
	page, err := st.AuditTrail(ctx, f, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := "definition_published,statement_run,schedule_saved,hierarchy_published,definition_retired"
	if got := strings.Join(auditKinds(page.Items), ","); got != want {
		t.Fatalf("kinds = %s, want %s", got, want)
	}
	byKind := map[string]domain.AuditEntry{}
	for _, e := range page.Items {
		byKind[e.Kind] = e
	}
	if e := byKind[domain.AuditStatementRun]; e.ActorID != "runner" || e.LedgerID != ledger || e.Period != "2026-03" ||
		e.ObjectRef != "run:"+run.ID || e.Details["resultHash"] != run.ResultHash || e.Details["hwm"] != json.Number("7") {
		t.Fatalf("statement_run entry = %+v", e)
	}
	if e := byKind[domain.AuditDefinitionPublished]; e.ActorID != "publisher" || e.ObjectRef != "definition:SCHED-DEF@1" {
		t.Fatalf("definition_published entry = %+v", e)
	}
	if e := byKind[domain.AuditScheduleSaved]; e.ActorID != "u1" || e.Details["revision"] != json.Number("1") {
		t.Fatalf("schedule_saved entry = %+v", e)
	}
	if e := byKind[domain.AuditDefinitionRetired]; e.ActorID != "user-test" || e.Details["retiredVersions"] != json.Number("1") {
		t.Fatalf("definition_retired entry (actor from the principal) = %+v", e)
	}
	if e := byKind[domain.AuditHierarchyPublished]; e.ObjectRef != "hierarchy:HIER-BS@1" || e.Details["unassigned"] != json.Number("1") || e.Details["members"] != json.Number("2") {
		t.Fatalf("hierarchy_published entry = %+v", e)
	}

	exp, err := st.ExportAuditTrail(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if exp.Entries != 5 || exp.SHA256 != domain.AuditDigest(exp.Body) || !ids.Valid(exp.ID) {
		t.Fatalf("export = %d entries, digest %s, id %s", exp.Entries, exp.SHA256, exp.ID)
	}
	if lines := strings.Count(string(exp.Body), "\n"); lines != 6 || !strings.Contains(string(exp.Body), "run:"+run.ID) {
		t.Fatalf("export body:\n%s", exp.Body)
	}
	page, err = st.AuditTrail(ctx, domain.AuditFilter{From: from, To: time.Now().Add(time.Minute), Kind: domain.AuditExported}, nil, 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("export entries = %+v, %v", page.Items, err)
	}
	if e := page.Items[0]; e.ObjectRef != "audit-export:"+exp.ID || e.Details["sha256"] != exp.SHA256 || e.Details["entries"] != json.Number("5") || e.ActorID != "user-test" {
		t.Fatalf("audit_exported entry = %+v", e)
	}
	// Exporting the same entries again gives the same file and digest.
	again, err := st.ExportAuditTrail(ctx, domain.AuditFilter{From: from, To: time.Now().Add(time.Minute), Kind: domain.AuditStatementRun})
	if err != nil {
		t.Fatal(err)
	}
	twice, err := st.ExportAuditTrail(ctx, domain.AuditFilter{From: from, To: time.Now().Add(time.Minute), Kind: domain.AuditStatementRun})
	if err != nil || twice.SHA256 != again.SHA256 || twice.ID == again.ID {
		t.Fatalf("repeat export digest %s vs %s, ids %s %s, %v", twice.SHA256, again.SHA256, twice.ID, again.ID, err)
	}
}
