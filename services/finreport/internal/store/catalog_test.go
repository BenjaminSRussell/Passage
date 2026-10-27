package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

func countType(types []string, typ string) int {
	n := 0
	for _, t := range types {
		if t == typ {
			n++
		}
	}
	return n
}

func accountTree(strict bool) domain.Hierarchy {
	return domain.Hierarchy{Code: "HIER-BS", Name: "Balance sheet accounts", Subject: domain.SubjectAccount, Strict: strict,
		Nodes: []domain.HierarchyNode{
			{Code: "TOTAL", Label: "Total"},
			{Code: "ASSETS", Label: "Assets", Parent: "TOTAL", Rules: []domain.Rule{{From: "100000", To: "199999"}}},
			{Code: "LIAB", Label: "Liabilities", Parent: "TOTAL", Rules: []domain.Rule{{Values: []string{"2*"}}}},
		}}
}

// FR-15.13 and BR-15.13: drafts are revision-checked, publish materializes the
// membership per version, a non-strict publish raises hierarchy.unassigned per
// unassigned member in the same transaction, and a strict or overlapping
// publish is refused without creating a version.
func TestHierarchy_DraftPublishAndMembership(t *testing.T) {
	st, ctx := migrated(t)
	h, rev, err := st.SaveHierarchy(ctx, accountTree(false), nil, "owner-1")
	if err != nil || rev != 1 || h.OwnerUserID != "owner-1" {
		t.Fatalf("first save = %+v rev %d err %v", h, rev, err)
	}
	if _, _, err := st.SaveHierarchy(ctx, accountTree(false), nil, "u2"); problemCode(err) != "precondition_required" {
		t.Fatalf("save without If-Match = %v", err)
	}
	stale := 7
	if _, _, err := st.SaveHierarchy(ctx, accountTree(false), &stale, "u2"); problemCode(err) != CodeRevisionConflict {
		t.Fatalf("stale save = %v", err)
	}
	if _, rev, err = st.SaveHierarchy(ctx, accountTree(false), &rev, "u2"); err != nil || rev != 2 {
		t.Fatalf("second save rev %d err %v", rev, err)
	}
	if got, _, err := st.GetHierarchy(ctx, "HIER-BS", Draft); err != nil || got.OwnerUserID != "owner-1" {
		t.Fatalf("the owner set on the first save is kept: %+v %v", got, err)
	}
	if _, _, err := st.HierarchyMembership(ctx, "HIER-BS", Latest); problemCode(err) != CodeHierarchyNotFound {
		t.Fatalf("membership before publish = %v", err)
	}

	members := []domain.Member{{ID: "a-cash", Key: "100100"}, {ID: "a-ap", Key: "200100"}, {ID: "a-rev", Key: "400100"}, {ID: "a-exp", Key: "500100"}}
	pub, m, err := st.PublishHierarchy(ctx, "HIER-BS", "publisher", members)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Version != 1 || pub.Status != "published" || m.Assigned["a-cash"] != "ASSETS" || m.Assigned["a-ap"] != "LIAB" ||
		fmt.Sprint(m.Unassigned) != "[a-exp a-rev]" {
		t.Fatalf("published %+v membership %+v", pub, m)
	}
	if n := countType(outboxTypes(t, ctx, st.pool), events.HierarchyUnassigned); n != 2 {
		t.Fatalf("hierarchy.unassigned events = %d, want 2", n)
	}
	if _, _, err := st.GetHierarchy(ctx, "HIER-BS", Draft); problemCode(err) != CodeHierarchyNotFound {
		t.Fatalf("draft after publish = %v", err)
	}
	stored, version, err := st.HierarchyMembership(ctx, "HIER-BS", Latest)
	if err != nil || version != 1 || len(stored.Assigned) != 2 || len(stored.Unassigned) != 2 {
		t.Fatalf("stored membership v%d %+v %v", version, stored, err)
	}

	// Version 2 is strict: the same members are refused and nothing is published.
	if _, _, err := st.SaveHierarchy(ctx, accountTree(true), nil, "u2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PublishHierarchy(ctx, "HIER-BS", "publisher", members); problemCode(err) != domain.CodeHierUnassigned {
		t.Fatalf("strict publish with unassigned members = %v", err)
	}
	duplicate := append(members[:2:2], domain.Member{ID: "a-cash", Key: "100200"})
	if _, _, err := st.PublishHierarchy(ctx, "HIER-BS", "publisher", duplicate); problemCode(err) != domain.ShapeCode {
		t.Fatalf("duplicate member ids = %v", err)
	}
	if _, _, err := st.GetHierarchy(ctx, "HIER-BS", 2); problemCode(err) != CodeHierarchyNotFound {
		t.Fatalf("a refused publish must not create version 2: %v", err)
	}
	if n := countType(outboxTypes(t, ctx, st.pool), events.HierarchyUnassigned); n != 2 {
		t.Fatalf("a refused publish wrote events: %d", n)
	}
	pub, _, err = st.PublishHierarchy(ctx, "HIER-BS", "publisher", members[:2])
	if err != nil || pub.Version != 2 || !pub.Strict {
		t.Fatalf("strict publish of assigned members = %+v %v", pub, err)
	}
	if _, v, err := st.HierarchyMembership(ctx, "HIER-BS", 1); err != nil || v != 1 {
		t.Fatalf("version 1 membership stays readable: v%d %v", v, err)
	}

	list, err := st.ListHierarchies(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if got := list[0]; got.LatestVersion != 2 || got.HasDraft || !got.Strict || got.AssignedCount != 2 || got.UnassignedCount != 0 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestHierarchy_OverlapRefusesPublish(t *testing.T) {
	st, ctx := migrated(t)
	h := accountTree(true)
	h.Nodes = append(h.Nodes, domain.HierarchyNode{Code: "BANK", Label: "Bank", Parent: "TOTAL", Rules: []domain.Rule{{Values: []string{"1001*"}}}})
	if _, _, err := st.SaveHierarchy(ctx, h, nil, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PublishHierarchy(ctx, "HIER-BS", "u1", []domain.Member{{ID: "a1", Key: "100100"}}); problemCode(err) != domain.CodeHierOverlap {
		t.Fatalf("overlapping publish = %v", err)
	}
	if _, _, err := st.GetHierarchy(ctx, "HIER-BS", Draft); err != nil {
		t.Fatalf("the draft stays after a refused publish: %v", err)
	}
}

// BR-15.8: retiring keeps history readable and blocks new runs; publishing the
// code again takes the next version number instead of colliding with history.
func TestRetireDefinition(t *testing.T) {
	st, ctx := migrated(t)
	if _, err := st.RetireDefinition(ctx, "NEVER"); problemCode(err) != CodeDefinitionNotFound {
		t.Fatalf("retire of an unknown code = %v", err)
	}
	if _, _, err := st.SaveDraft(ctx, storeFixtureDefinition("RETIRE-ME"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RetireDefinition(ctx, "RETIRE-ME"); problemCode(err) != CodeDefinitionNotFound {
		t.Fatalf("retire of a draft-only code = %v", err)
	}
	if _, err := st.Publish(ctx, "RETIRE-ME", "u1"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	seedBalance(t, ctx, st.pool, ledger, 2026, 3, "1000", "acct", "10", 1)
	spec := RunSpec{Code: "RETIRE-ME", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), IdempotencyKey: "before", RequestHash: "h"}
	run, err := st.CreateRun(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}

	if n, err := st.RetireDefinition(ctx, "RETIRE-ME"); err != nil || n != 1 {
		t.Fatalf("retire = %d, %v", n, err)
	}
	if n, err := st.RetireDefinition(ctx, "RETIRE-ME"); err != nil || n != 0 {
		t.Fatalf("retiring again = %d, %v; want 0 and no error", n, err)
	}
	spec.IdempotencyKey = "after"
	if _, err := st.CreateRun(ctx, spec); problemCode(err) != CodeNotPublished {
		t.Fatalf("run of a retired definition = %v", err)
	}
	if _, err := st.GetRun(ctx, run.ID); err != nil {
		t.Fatalf("the earlier run stays readable: %v", err)
	}
	def, _, err := st.GetDefinition(ctx, "RETIRE-ME", 1)
	if err != nil || def.Status != "retired" {
		t.Fatalf("retired version 1 = %+v, %v", def, err)
	}
	list, err := st.ListDefinitions(ctx)
	if err != nil || len(list) != 1 || list[0].Status != "retired" || list[0].LatestVersion != 0 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	if _, _, err := st.SaveDraft(ctx, storeFixtureDefinition("RETIRE-ME"), nil); err != nil {
		t.Fatal(err)
	}
	pub, err := st.Publish(ctx, "RETIRE-ME", "u1")
	if err != nil || pub.Version != 2 {
		t.Fatalf("publish after retire = v%d, %v; want version 2", pub.Version, err)
	}
	if list, _ := st.ListDefinitions(ctx); list[0].Status != "published" || list[0].LatestVersion != 2 {
		t.Fatalf("list after republish = %+v", list)
	}
}

// FR-15.5: install publishes the standard set once, in one transaction, skips
// codes the tenant already uses (its own draft is left alone), and the
// installed balance sheet ties out (check row 0.00) on a balanced ledger.
func TestInstallStandard(t *testing.T) {
	st, ctx := migrated(t)
	own := storeFixtureDefinition(domain.CodeBSStd)
	own.Name = "Our own balance sheet"
	if _, _, err := st.SaveDraft(ctx, own, nil); err != nil {
		t.Fatal(err)
	}
	res, err := st.InstallStandard(ctx, domain.StandardDefinitions(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range res {
		got[r.Code] = r.Action
	}
	if got[domain.CodeTBStd] != "installed" || got[domain.CodePLStd] != "installed" || got[domain.CodePLFunc] != "installed" ||
		got[domain.CodeCFStd] != "installed" || got[domain.CodeBSStd] != "skipped" {
		t.Fatalf("install = %+v", res)
	}
	if draft, _, err := st.GetDefinition(ctx, domain.CodeBSStd, Draft); err != nil || draft.Name != "Our own balance sheet" {
		t.Fatalf("install touched the tenant's draft: %+v %v", draft, err)
	}
	if n := countType(outboxTypes(t, ctx, st.pool), events.DefinitionPublished); n != 4 {
		t.Fatalf("definition.published events = %d, want 4", n)
	}
	again, err := st.InstallStandard(ctx, domain.StandardDefinitions(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Action != "skipped" || r.Reason == "" {
			t.Fatalf("second install = %+v, want every code skipped with a reason", again)
		}
	}

	// The installed trial balance on a balanced ledger: opening period 0 plus
	// period 1, with the run's currency type (the standard columns leave it empty).
	ledger := ids.New()
	for _, b := range []struct {
		no      int
		account string
		net     string
	}{{0, "100100", "1000"}, {0, "300100", "-1000"}, {1, "100100", "180"}, {1, "400100", "-300"}, {1, "500100", "200"}, {1, "200100", "-80"}} {
		seedBalance(t, ctx, st.pool, ledger, 2026, b.no, b.account, "rpt", b.net, 9)
	}
	run, err := st.CreateRun(ctx, RunSpec{Code: domain.CodeTBStd, LedgerID: ledger, CurrencyType: "rpt", PeriodYear: 2026, PeriodNo: 1,
		Calendar: []domain.Period{{Year: 2026, No: 0, Quarter: 1}, {Year: 2026, No: 1, Quarter: 1}}, IdempotencyKey: "tb", RequestHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	cells, err := st.ListCells(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, c := range cells {
		values[c.Row+"/"+c.Column] = c.Value.StringFixed(2)
	}
	if values["TOTAL/CLOSE"] != "0.00" || values["ASSETS/OPEN"] != "1000.00" || values["ASSETS/CLOSE"] != "1180.00" || values["ASSETS/MOVE"] != "180.00" {
		t.Fatalf("trial balance cells = %v", values)
	}
	if len(run.Checks) != 0 {
		t.Fatalf("trial balance checks = %+v, want none", run.Checks)
	}

	// AC-15.2: the installed CF-STD reconciles; the opening period is not a
	// cash flow, so the year to date equals the period.
	cal := []domain.Period{{Year: 2026, No: 0, Quarter: 1}, {Year: 2026, No: 1, Quarter: 1}}
	cf, err := st.CreateRun(ctx, RunSpec{Code: domain.CodeCFStd, LedgerID: ledger, CurrencyType: "rpt", PeriodYear: 2026, PeriodNo: 1,
		Calendar: cal, IdempotencyKey: "cf", RequestHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	cells, err = st.ListCells(ctx, cf.ID)
	if err != nil {
		t.Fatal(err)
	}
	values = map[string]string{}
	for _, c := range cells {
		values[c.Row+"/"+c.Column] = c.Value.StringFixed(2)
	}
	if values["NI/P"] != "100.00" || values["CASH/P"] != "180.00" || values["CASH/YTD"] != "180.00" || values["CF_CHK/P"] != "0.00" || values["CF_CHK/YTD"] != "0.00" {
		t.Fatalf("cash flow cells = %v", values)
	}

	// PL-FUNC: the expense line carries no FUNC_AREA, so the run completes with
	// a REPORT-0015 warning that GetRun and ListRuns return too (FR-15.14).
	fn, err := st.CreateRun(ctx, RunSpec{Code: domain.CodePLFunc, LedgerID: ledger, CurrencyType: "rpt", PeriodYear: 2026, PeriodNo: 1,
		Calendar: cal, IdempotencyKey: "fn", RequestHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fn.Checks) != 1 || fn.Checks[0].Code != domain.CodePLFuncMissing || fn.Checks[0].Severity != domain.SeverityWarning {
		t.Fatalf("PL-FUNC checks = %+v, want one REPORT-0015 warning", fn.Checks)
	}
	if again, err := st.GetRun(ctx, fn.ID); err != nil || len(again.Checks) != 1 || again.Checks[0].Detail != fn.Checks[0].Detail {
		t.Fatalf("stored checks = %+v, %v", again.Checks, err)
	}
	if list, err := st.ListRuns(ctx, domain.CodePLFunc, 10); err != nil || len(list) != 1 || len(list[0].Checks) != 1 {
		t.Fatalf("listed PL-FUNC runs = %+v, %v", list, err)
	}

	// BR-15.5: cash that moves with no other entry fails the cash flow run, and
	// nothing is stored.
	seedBalance(t, ctx, st.pool, ledger, 2026, 1, "100200", "rpt", "50", 10)
	_, err = st.CreateRun(ctx, RunSpec{Code: domain.CodeCFStd, LedgerID: ledger, CurrencyType: "rpt", PeriodYear: 2026, PeriodNo: 1,
		Calendar: cal, IdempotencyKey: "cf-2", RequestHash: "h"})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeCashFlow {
		t.Fatalf("unreconciled cash flow = %v, want %s", err, domain.CodeCashFlow)
	}
	if list, _ := st.ListRuns(ctx, domain.CodeCFStd, 10); len(list) != 1 {
		t.Fatalf("cash flow runs = %d, want only the reconciled one", len(list))
	}
}
