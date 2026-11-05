package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/coexist"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/migrations"
)

const (
	le1 = "00000000-0000-7000-8000-0000000000e1"
	le2 = "00000000-0000-7000-8000-0000000000e2"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// ledgerServer is dbServer with a fake general ledger and a settable clock; it also returns the
// store for direct reads.
func ledgerServer(t *testing.T) (http.Handler, *pgxpool.Pool, *store.Postgres, *ledgerclient.Fake, *clock) {
	t.Helper()
	var admin, app *pgxpool.Pool
	if os.Getenv(testkit.EnvAppURL) != "" {
		admin, app = testkit.DBWithApp(t)
	} else {
		admin = testkit.DB(t)
		app = admin
	}
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(app)
	fake := ledgerclient.NewFake()
	fake.AddLedger("L-US", le1)
	fake.AddLedger("L-OTHER", le2)
	c := &clock{t: time.Now().UTC()}
	return New(st).WithLedger(fake).WithClock(c.now).Routes(), admin, st, fake, c
}

// padAssets returns n clean straight-line assets in book GAAP, so one quarantined line stays within
// the 10 % quarantine share.
func padAssets(n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf(`{"legacyId":"FA-P%d","asset":"TOOL-%d","book":"GAAP","currency":"USD","method":"SL","usefulLifeMonths":12,"remainingLifeMonths":12,"acquisitionCost":"1200","nextDepreciation":"100"}`, i, i))
	}
	return out
}

func padPayroll(n int) ([]string, string) {
	var out, workers []string
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf(`{"legacyId":"PY-P%d","worker":"W%d","taxYear":"2026","currency":"USD","gross":"100","net":"100"}`, i, i))
		workers = append(workers, fmt.Sprintf(`"W%d"`, i))
	}
	return out, strings.Join(workers, ",")
}

func stagedBody(company, master string, records ...string) string {
	return fmt.Sprintf(`{"sourceSystem":"AX2012","sourceCompany":%q,"cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z",
		"quarantineMaxPct":"10","master":[%s],"records":[%s]}`, company, master, strings.Join(records, ","))
}

// D1a: the three staged packs validate, stage with legacy references and reconcile; a fixed asset
// off its depreciation projection is quarantined (MIG-1507); a re-run stages nothing new and a
// natural key under a new legacy id is refused.
func TestStagedPacks_StageReconcileAndRerun(t *testing.T) {
	h, admin, st, _, _ := ledgerServer(t)
	assets := []string{
		`{"legacyId":"FA-1","asset":"PRESS-1","book":"GAAP","currency":"USD","method":"SL","usefulLifeMonths":120,"remainingLifeMonths":60,"acquisitionCost":"120000","accumulatedDepreciation":"60000","nextDepreciation":"1000"}`,
		`{"legacyId":"FA-2","asset":"PRESS-1","book":"TAX","currency":"USD","method":"DB","usefulLifeMonths":60,"remainingLifeMonths":30,"acquisitionCost":"50000","accumulatedDepreciation":"20000","nextDepreciation":"1000.01"}`,
		`{"legacyId":"FA-3","asset":"VAN-9","book":"GAAP","currency":"USD","method":"SL","usefulLifeMonths":60,"remainingLifeMonths":10,"acquisitionCost":"6000","accumulatedDepreciation":"5000","nextDepreciation":"90"}`,
	}
	assets = append(assets, padAssets(7)...)
	path := "/api/v1/migration/packs/fixed_assets/runs"
	rec := do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: stagedBody("USMF", `"GAAP","TAX"`, assets...), user: "engineer"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("fixed_assets: %d %s", rec.Code, rec.Body)
	}
	run := decode[runDetailDTO](t, rec)
	if run.Status != "LOADED" || run.RecordsLoaded != 9 || run.RecordsQuarantined != 1 || !strings.HasPrefix(run.Quarantined[0].Reason, "MIG-1507") {
		t.Fatalf("fixed_assets run %+v %+v", run.runDTO, run.Quarantined)
	}
	if n := count(t, admin, `select count(*) from staged_opening_line s join legacy_xref x on x.xref_id = s.legacy_xref_id and x.passage_id = s.line_id
		where s.pack = 'fixed_assets' and x.source_table = 'fixed_assets' and (s.measures->>'nbv')::numeric > 0`); n != 9 {
		t.Fatalf("staged assets tied to their xref = %d", n)
	}
	// A clean extract reconciles; a re-run stages nothing new.
	clean := append(assets[:2:2], assets[3:]...)
	again := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: stagedBody("USMF", `"GAAP","TAX"`, clean...)}))
	if again.Status != "RECONCILED" || again.RecordsLoaded != 0 || again.AlreadyPresent != 9 {
		t.Fatalf("re-run %+v", again.runDTO)
	}
	dup := `{"legacyId":"FA-77","asset":"PRESS-1","book":"GAAP","currency":"USD","method":"SL","usefulLifeMonths":120,"remainingLifeMonths":60,"acquisitionCost":"120000","accumulatedDepreciation":"60000","nextDepreciation":"1000"}`
	if r := do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: stagedBody("USMF", `"GAAP"`, dup)}); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "already staged") {
		t.Fatalf("natural key under a new legacy id: %d %s", r.Code, r.Body)
	}

	wip := do(t, h, request{method: "POST", path: "/api/v1/migration/packs/wip_prod/runs", tenant: tenantA, duties: allDuties, body: stagedBody("USMF", `"FG-1"`,
		`{"legacyId":"W-1","productionOrder":"PRD-1","item":"FG-1","currency":"USD","material":"100.50","labor":"20","overhead":"5"}`,
		`{"legacyId":"W-2","productionOrder":"PRD-2","item":"FG-1","currency":"USD","material":"7"}`)})
	if w := decode[runDetailDTO](t, wip); wip.Code != http.StatusCreated || w.Status != "RECONCILED" || w.Pack != "wip_prod" {
		t.Fatalf("wip_prod: %d %s", wip.Code, wip.Body)
	}
	padP, workers := padPayroll(8)
	payLines := append([]string{`{"legacyId":"PY-1","worker":"E1","taxYear":"2026","currency":"USD","gross":"90000","tax":"20000","deductions":"5000","net":"65000"}`,
		`{"legacyId":"PY-2","worker":"E2","taxYear":"2026","currency":"USD","gross":"1000","tax":"100","net":"901"}`}, padP...)
	pay := do(t, h, request{method: "POST", path: "/api/v1/migration/packs/payroll_ytd/runs", tenant: tenantA, duties: allDuties, body: stagedBody("USMF", `"E1","E2",`+workers, payLines...)})
	p := decode[runDetailDTO](t, pay)
	if pay.Code != http.StatusCreated || p.Status != "LOADED" || p.RecordsQuarantined != 1 {
		t.Fatalf("payroll_ytd: %d %s", pay.Code, pay.Body)
	}
	// The coexistence router matches a staged legacy id to its Passage line.
	known, err := st.KnownXrefs(testkit.Ctx(tenantA), domain.SourceAX2012, "USMF", "payroll_ytd", []string{"PY-1", "PY-2"})
	if err != nil || len(known) != 1 || known["payroll_ytd|PY-1"] == "" {
		t.Fatalf("known xrefs %v %v", known, err)
	}
}

func compensationPostings() string {
	return `{"postings":[` + strings.Join([]string{
		posting("JOURNAL", "USMF", "JE-1", "2027-01-02T10:00:00Z", "1100", "250"),
		posting("JOURNAL", "USMF", "JE-1", "2027-01-02T10:00:00Z", "4000", "-250"),
		posting("JOURNAL", "USMF", "JE-2", "2027-01-03T10:00:00Z", "6000", "80.40"),
		posting("JOURNAL", "USMF", "JE-2", "2027-01-03T10:00:00Z", "2000", "-80.40"),
		posting("DOCUMENT", "USMF", "INV-1", "2027-01-02T10:00:00Z", "1200", "99"),
	}, ",") + `]}`
}

func rolledBackPlan(t *testing.T, h http.Handler) string {
	t.Helper()
	plan := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage",
		body: generateBody(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 36), user: "manager"}))
	if r := rollbackBoth(t, h, "/api/v1/cutover/plans/"+plan.PlanID+"/rollback", compensationPostings(), ""); r.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", r.Code, r.Body)
	}
	return plan.PlanID
}

func postComp(t *testing.T, h http.Handler, planID, body string) (compensationReportDTO, int, string) {
	t.Helper()
	r := do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/" + planID + "/rollback/compensations", tenant: tenantA, duties: "migration.cutover.manage",
		body: body, user: "manager", le: le1})
	if r.Code != http.StatusOK {
		return compensationReportDTO{}, r.Code, r.Body.String()
	}
	return decode[compensationReportDTO](t, r), r.Code, ""
}

const usLedger = `{"ledgers":[{"entity":"USMF","legalEntityId":"` + le1 + `","ledgerId":"L-US"}]}`

// D9: a ROLLED_BACK plan's re-keying journals are compensated in the pinned ledger, each once, with
// the lines flipped; the reconciliation counts only these journals.
func TestRollbackCompensation_PostsEachJournalOnceAndReconciles(t *testing.T) {
	h, _, _, fake, _ := ledgerServer(t)
	notRolled := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage",
		body: generateBody(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 36), user: "manager"}))
	if _, code, body := postComp(t, h, notRolled.PlanID, usLedger); code != http.StatusConflict {
		t.Fatalf("a plan that is not rolled back: %d %s", code, body)
	}
	planID := rolledBackPlan(t, h)
	// Without a pinned ledger nothing is posted; the compensations wait.
	rep, code, body := postComp(t, h, planID, `{}`)
	if code != http.StatusOK || rep.Pending != 2 || rep.Posted != 0 || rep.Reconciled || len(fake.Entries()) != 0 {
		t.Fatalf("unpinned: %d %s %+v", code, body, rep)
	}
	if _, code, _ := postComp(t, h, planID, `{"ledgers":[{"entity":"USMF","legalEntityId":"`+le1+`","ledgerId":"L-OTHER"}]}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("a ledger of another legal entity must be refused: %d", code)
	}
	if _, code, _ := postComp(t, h, planID, `{"ledgers":[{"entity":"DEMF","legalEntityId":"`+le1+`","ledgerId":"L-US"}]}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("an entity outside the plan must be refused: %d", code)
	}
	rep, code, body = postComp(t, h, planID, usLedger)
	if code != http.StatusOK || rep.Posted != 2 || rep.Pending != 0 || !rep.Reconciled || len(rep.Totals) != 4 {
		t.Fatalf("posted: %d %s %+v", code, body, rep)
	}
	entries := fake.Entries()
	if len(entries) != 2 {
		t.Fatalf("ledger entries %d, want 2 (the document line is re-keyed only)", len(entries))
	}
	e := entries[0].Entry
	if e.LedgerID != "L-US" || e.LegalEntityID != le1 || e.SourceType != domain.CompensationSourceType || e.DocumentID != "rollback:JE-1" ||
		e.Lines[0].Account != "1100" || !e.Lines[0].Credit || e.Lines[1].Credit || e.PostingDate != time.Now().UTC().Format(time.DateOnly) {
		t.Fatalf("compensating entry %+v", e)
	}
	// Running again posts nothing new; a different ledger for the pinned entity is refused.
	if rep, _, _ = postComp(t, h, planID, usLedger); rep.Posted != 2 || len(fake.Entries()) != 2 {
		t.Fatalf("second run reposted: %+v, %d entries", rep, len(fake.Entries()))
	}
	if _, code, _ := postComp(t, h, planID, `{"ledgers":[{"entity":"USMF","legalEntityId":"`+le1+`","ledgerId":"L-OTHER"}]}`); code != http.StatusConflict {
		t.Fatalf("re-pinning must be 409, got %d", code)
	}
	got := do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + planID + "/rollback/compensations", tenant: tenantA, duties: "migration.cutover.manage"})
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"reconciled":true`) || !strings.Contains(got.Body.String(), entries[1].ID) {
		t.Fatalf("report: %d %s", got.Code, got.Body)
	}
}

// Sweep rules: a failing compensation backs off and is parked (at once on a rejection), the others
// are not held up, and a retry of parked rows posts them under the same key.
func TestRollbackCompensation_BacksOffParksAndRetries(t *testing.T) {
	h, _, _, fake, clk := ledgerServer(t)
	planID := rolledBackPlan(t, h)
	fake.FailPosts(fmt.Errorf("ledger timeout"))
	rep, _, _ := postComp(t, h, planID, usLedger)
	if rep.Pending != 2 || rep.Compensations[0].Attempts != 1 || rep.Compensations[0].LastCode != "MIG-COMPENSATION-FAILED" || rep.Compensations[0].NextAttemptAt == nil {
		t.Fatalf("after a transient failure %+v", rep)
	}
	// Not due yet: the next run does not try again.
	if rep, _, _ = postComp(t, h, planID, `{}`); rep.Compensations[0].Attempts != 1 {
		t.Fatalf("retried before its backoff: %+v", rep.Compensations[0])
	}
	clk.add(2 * time.Minute)
	fake.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	rep, _, _ = postComp(t, h, planID, `{}`)
	if rep.Parked != 2 || rep.Compensations[0].LastCode != ledgerclient.CodePeriodClosed {
		t.Fatalf("a rejection must park: %+v", rep)
	}
	fake.FailPosts(nil)
	clk.add(time.Hour)
	if rep, _, _ = postComp(t, h, planID, `{}`); rep.Parked != 2 || len(fake.Entries()) != 0 {
		t.Fatalf("parked rows ran without a retry: %+v", rep)
	}
	rep, _, _ = postComp(t, h, planID, `{"retryParked":true}`)
	if rep.Posted != 2 || !rep.Reconciled || len(fake.Entries()) != 2 {
		t.Fatalf("retry: %+v, %d entries", rep, len(fake.Entries()))
	}
}

func replayBody(period, sys string, docs ...string) string {
	return fmt.Sprintf(`{"period":%q,"sourceSystem":%q,"legalEntityId":%q,"ledgerId":"L-US","revenue":"100000","documents":[%s]}`,
		period, sys, le1, strings.Join(docs, ","))
}

func replayDoc(seq int, id, date, debit, credit, amount string) string {
	return fmt.Sprintf(`{"seq":%d,"documentId":%q,"postingDate":%q,"currency":"USD","lines":[{"account":%q,"amount":%q},{"account":%q,"amount":"-%s"}]}`,
		seq, id, date, debit, amount, credit, amount)
}

// D4: the legacy documents are replayed through the ledger in their original sequence and the
// trial balance read back is diffed with the legacy one; a document the ledger refuses shows as
// unexplained variances. Replaying again books nothing twice and returns the stored run.
func TestParallelReplay_DiffsTrialBalances(t *testing.T) {
	h, _, _, fake, _ := ledgerServer(t)
	path := "/api/v1/migration/parallel-runs/replay"
	clean := replayBody("2026-03", "AX2012", replayDoc(1, "GJ-1", "2026-03-05", "1100", "4000", "500"), replayDoc(2, "GJ-2", "2026-03-06", "6000", "2000", "75.25"))
	rec := do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: clean, user: "engineer", le: le1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	run := decode[replayDTO](t, rec)
	if run.Posted != 2 || len(run.Rejected) != 0 || run.UnexplainedCount != 0 || run.DocumentsReplayed != 2 || !run.CanSignOff {
		t.Fatalf("clean replay %+v", run)
	}
	again := decode[replayDTO](t, do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: clean, le: le1}))
	if again.ParallelRunID != run.ParallelRunID || len(fake.Entries()) != 2 {
		t.Fatalf("second replay: run %s vs %s, %d entries", again.ParallelRunID, run.ParallelRunID, len(fake.Entries()))
	}
	// April: the ledger has closed through 2026-04-02, so GJ-3 is refused and its accounts differ.
	fake.RefuseClosedThrough("2026-04-02")
	april := replayBody("2026-04", "AX2012", replayDoc(1, "GJ-3", "2026-04-01", "1100", "4000", "40"), replayDoc(2, "GJ-4", "2026-04-10", "1100", "4000", "60"))
	rec = do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: april, le: le1})
	bad := decode[replayDTO](t, rec)
	if rec.Code != http.StatusCreated || bad.Posted != 1 || len(bad.Rejected) != 1 || bad.Rejected[0].DocumentID != "GJ-3" || bad.Rejected[0].Code != ledgerclient.CodePeriodClosed {
		t.Fatalf("april replay: %d %s", rec.Code, rec.Body)
	}
	if bad.UnexplainedCount != 2 || bad.CanSignOff {
		t.Fatalf("refused document must leave two unexplained TB variances: %+v", bad.Variances)
	}
	for _, v := range bad.Variances {
		if v.Area != "TB" || (v.Key != "1100|USD|2026-04" && v.Key != "4000|USD|2026-04") {
			t.Fatalf("variance %+v", v)
		}
		if v.Key == "1100|USD|2026-04" && (v.Legacy != "100.000000" || v.Passage != "60.000000") {
			t.Fatalf("1100 variance %+v", v)
		}
	}
	// Another legal entity's ledger is refused before anything is posted.
	foreign := strings.Replace(clean, `"ledgerId":"L-US"`, `"ledgerId":"L-OTHER"`, 1)
	if r := do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: foreign, le: le1}); r.Code != http.StatusUnprocessableEntity || problemCode(t, r) != CodeLedgerForeign {
		t.Fatalf("foreign ledger: %d %s", r.Code, r.Body)
	}
}

type fakeAdapter struct {
	in        []domain.InboundRecord
	suspended map[string]bool
}

func (a *fakeAdapter) System() domain.SourceSystem { return domain.SourceAX2012 }
func (a *fakeAdapter) Pull(context.Context, string, string, time.Time) ([]domain.InboundRecord, error) {
	return a.in, nil
}
func (a *fakeAdapter) Push(context.Context, string, string, []domain.OutboundRecord) error {
	return nil
}
func (a *fakeAdapter) Suspend(_ context.Context, company, entity string) error {
	a.suspended[company+"/"+entity] = true
	return nil
}
func (a *fakeAdapter) Resume(_ context.Context, company, entity string) error {
	delete(a.suspended, company+"/"+entity)
	return nil
}

// D6, BR-49.10: the entity-by-entity cutover flag is versioned; once an entity is cut over the
// adapter's legacy changes made after the cutover are refused, and a flip back lets them flow.
func TestCoexistence_EntityFlagRoutesAdapterRecords(t *testing.T) {
	h, _, st, _, clk := ledgerServer(t)
	list := do(t, h, request{method: "GET", path: "/api/v1/migration/coexistence?sourceSystem=AX2012&sourceCompany=USMF", tenant: tenantA, duties: "migration.factory.view"})
	if list.Code != http.StatusOK || strings.Count(list.Body.String(), `"mode":"LEGACY"`) != len(domain.CoexistEntities) {
		t.Fatalf("defaults: %d %s", list.Code, list.Body)
	}
	put := func(mode string, version int) (coexistDTO, int) {
		r := do(t, h, request{method: "PUT", path: "/api/v1/migration/coexistence/AX2012/USMF/customer", tenant: tenantA, duties: "migration.cutover.manage",
			body: fmt.Sprintf(`{"mode":%q,"expectedVersion":%d}`, mode, version), user: "manager"})
		if r.Code != http.StatusOK {
			return coexistDTO{}, r.Code
		}
		return decode[coexistDTO](t, r), r.Code
	}
	f, code := put("PASSAGE", 0)
	if code != http.StatusOK || f.Version != 1 || f.CutoverAt == nil || f.ChangedBy != "manager" {
		t.Fatalf("cutover: %d %+v", code, f)
	}
	if _, code := put("LEGACY", 0); code != http.StatusConflict {
		t.Fatalf("stale version: %d", code)
	}
	if _, code := put("PASSAGE", 1); code != http.StatusConflict {
		t.Fatalf("same mode: %d", code)
	}
	cut := *f.CutoverAt
	a := &fakeAdapter{suspended: map[string]bool{}, in: []domain.InboundRecord{
		{Entity: "customer", SourceTable: "CustTable", LegacyID: "C-OLD", ChangedAt: cut.Add(-time.Minute)},
		{Entity: "customer", SourceTable: "CustTable", LegacyID: "C-NEW", ChangedAt: cut.Add(time.Minute)},
	}}
	ctx := testkit.Ctx(tenantA)
	flag, err := st.GetCoexistence(ctx, domain.SourceAX2012, "USMF", "customer")
	if err != nil {
		t.Fatal(err)
	}
	if err := coexist.Apply(ctx, a, flag); err != nil || !a.suspended["USMF/customer"] {
		t.Fatalf("cutover must suspend the adapter: %v %v", err, a.suspended)
	}
	routed, err := coexist.Pull(ctx, a, st, st, "USMF", "customer", cut.Add(-time.Hour))
	if err != nil || routed[0].Decision != domain.RouteCreate || routed[1].Decision != domain.RouteRefused {
		t.Fatalf("routed %+v %v", routed, err)
	}
	clk.add(time.Hour)
	if f, code = put("LEGACY", 1); code != http.StatusOK || f.Version != 2 || f.CutoverAt != nil {
		t.Fatalf("flip back: %d %+v", code, f)
	}
	flag, _ = st.GetCoexistence(ctx, domain.SourceAX2012, "USMF", "customer")
	if err := coexist.Apply(ctx, a, flag); err != nil || a.suspended["USMF/customer"] {
		t.Fatalf("rollback must resume the adapter: %v", err)
	}
	if routed, _ = coexist.Pull(ctx, a, st, st, "USMF", "customer", cut); routed[1].Decision != domain.RouteCreate {
		t.Fatalf("after the flip back: %+v", routed[1])
	}
}
