package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/coexist"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// postGoLive books the journals of postings (the rollback request shape) in the fake ledger as
// Passage did after go-live, so the compensations can be checked to take them off again.
func postGoLive(t *testing.T, fake *ledgerclient.Fake, legalEntityID, ledgerID string, postings []map[string]string) {
	t.Helper()
	type doc struct {
		date, currency string
		lines          []ledgerclient.Line
	}
	docs, order := map[string]*doc{}, []string{}
	for _, p := range postings {
		if p["kind"] != "JOURNAL" {
			continue
		}
		k := p["entity"] + "/" + p["documentRef"]
		if docs[k] == nil {
			docs[k] = &doc{date: p["postedAt"][:10], currency: p["currency"]}
			order = append(order, k)
		}
		a := decimal.RequireFromString(p["amount"])
		docs[k].lines = append(docs[k].lines, ledgerclient.Line{Account: p["account"], Credit: a.IsNegative(), Amount: a.Abs()})
	}
	for _, k := range order {
		d := docs[k]
		if _, err := fake.Post(context.Background(), ledgerclient.Entry{LegalEntityID: legalEntityID, LedgerID: ledgerID, DocumentID: k, PostingDate: d.date,
			Currency: d.currency, IdempotencyKey: "golive-" + k, SourceType: "gl_journal", Lines: d.lines}); err != nil {
			t.Fatalf("go-live posting %s: %v", k, err)
		}
	}
}

// ledgerNet sums every entry the fake ledger holds per (ledger, account), debit positive.
func ledgerNet(fake *ledgerclient.Fake) map[string]decimal.Decimal {
	net := map[string]decimal.Decimal{}
	for _, e := range fake.Entries() {
		for _, l := range e.Lines {
			k := e.LedgerID + "|" + l.Account
			if l.Credit {
				net[k] = net[k].Sub(l.Amount)
			} else {
				net[k] = net[k].Add(l.Amount)
			}
		}
	}
	return net
}

type rolledBackEvent struct {
	Data struct {
		PlanID             string `json:"plan_id"`
		PackRunsRolledBack int    `json:"pack_runs_rolled_back"`
		CoexistenceResumed []struct {
			SourceSystem  string `json:"source_system"`
			SourceCompany string `json:"source_company"`
			Entity        string `json:"entity"`
			Mode          string `json:"mode"`
			Version       int    `json:"version"`
		} `json:"coexistence_resumed"`
	} `json:"data"`
}

func readRolledBackEvent(t *testing.T, admin *pgxpool.Pool, planID string) rolledBackEvent {
	t.Helper()
	var payload []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where type = 'dev.passage.cutover.plan.rolledback.v1' and aggregate_id = $1`, planID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt rolledBackEvent
	if err := json.Unmarshal(payload, &evt); err != nil {
		t.Fatal(err)
	}
	return evt
}

// rollbackWithLedgers approves a rollback as alice and executes it as bob, both in legal entity le.
func rollbackWithLedgers(t *testing.T, h http.Handler, planID, body, le string) planDTO {
	t.Helper()
	path := "/api/v1/cutover/plans/" + planID + "/rollback"
	if r := do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: "migration.cutover.manage", body: body, user: "alice", le: le}); r.Code != http.StatusAccepted {
		t.Fatalf("first approval: %d %s", r.Code, truncate(r.Body.String()))
	}
	r := do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: "migration.cutover.manage", body: body, user: "bob", le: le})
	if r.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", r.Code, truncate(r.Body.String()))
	}
	return decode[planDTO](t, r)
}

// AC-49.7, BR-49.1 (issue #49 proof 4a): rolling back a rehearsal cutover restores legacy
// coexistence. The rollback moves the plan's pack runs to ROLLED_BACK, hands every entity kind
// Passage mastered back to LEGACY so the adapters resume (and lists them on
// cutover.plan.rolledback for the integration hub), and posts the compensating journals, which net
// the go-live postings to zero in the ledger. Runs and flags outside the plan are untouched.
func TestWave49_RollbackRehearsalRestoresLegacyCoexistence(t *testing.T) {
	h, admin, st, fake, _ := ledgerServer(t)
	ctx := testkit.Ctx(tenantA)

	// The rehearsal: USMF's open items and GL balances are migrated; DEMF and a USMF pack outside
	// the plan are migrated too and must survive the rollback.
	run := func(path, body string) runDetailDTO {
		rec := do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: body, user: "engineer"})
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", path, rec.Code, truncate(rec.Body.String()))
		}
		out := decode[runDetailDTO](t, rec)
		if out.Status != "RECONCILED" {
			t.Fatalf("%s: %+v", path, out.runDTO)
		}
		return out
	}
	oi := run(runsPath, openItemsBody(3))
	gl := run(glPath, glBody("USMF", "", cleanGL...))
	demf := run(runsPath, strings.Replace(openItemsBody(2), `"sourceCompany":"USMF"`, `"sourceCompany":"DEMF"`, 1))
	wip := run("/api/v1/migration/packs/wip_prod/runs", stagedBody("USMF", `"FG-1"`, `{"legacyId":"W-1","productionOrder":"PRD-1","item":"FG-1","currency":"USD","material":"10"}`))

	plan := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage",
		body: generateBody(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 36), user: "manager"}))

	// Go-live: customers, vendors and open items of USMF are cut over to Passage, as is DEMF's
	// customer master (another plan). The adapter follows each flag.
	adapter := &fakeAdapter{suspended: map[string]bool{}}
	cut := func(company, entity string) {
		r := do(t, h, request{method: "PUT", path: "/api/v1/migration/coexistence/AX2012/" + company + "/" + entity, tenant: tenantA, duties: "migration.cutover.manage",
			body: `{"mode":"PASSAGE","expectedVersion":0}`, user: "manager"})
		if r.Code != http.StatusOK {
			t.Fatalf("cut over %s %s: %d %s", company, entity, r.Code, r.Body)
		}
		f, err := st.GetCoexistence(ctx, domain.SourceAX2012, company, entity)
		if err != nil {
			t.Fatal(err)
		}
		if err := coexist.Apply(ctx, adapter, f); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []string{"customer", "vendor", "open_items"} {
		cut("USMF", e)
	}
	cut("DEMF", "customer")
	if len(adapter.suspended) != 4 {
		t.Fatalf("suspended after go-live: %v", adapter.suspended)
	}

	// Passage posted two journals before the abort; they come back for re-keying and are compensated.
	var postings struct {
		Postings []map[string]string `json:"postings"`
	}
	if err := json.Unmarshal([]byte(compensationPostings()), &postings); err != nil {
		t.Fatal(err)
	}
	postGoLive(t, fake, le1, "L-US", postings.Postings)
	body := strings.Replace(compensationPostings(), `{"postings":`, `{"ledgers":[{"entity":"USMF","legalEntityId":"`+le1+`","ledgerId":"L-US"}],"postings":`, 1)
	got := rollbackWithLedgers(t, h, plan.PlanID, body, le1)

	out := got.RollbackOutcome
	if !got.RolledBack || out == nil || out.PackRunsRolledBack != 2 || out.CoexistenceResumed != 3 || len(out.Coexistence) != 3 || out.CompensationProblem != "" {
		t.Fatalf("rollback outcome %+v", out)
	}
	for _, f := range out.Coexistence {
		if f.SourceCompany != "USMF" || f.Mode != "LEGACY" || f.CutoverAt != nil || f.Version != 2 || f.ChangedBy != "bob" {
			t.Fatalf("flag handed back %+v", f)
		}
	}
	if c := out.Compensations; c == nil || c.Posted != 2 || c.Pending != 0 || c.Parked != 0 || !c.Reconciled {
		t.Fatalf("compensations posted with the rollback: %+v", out.Compensations)
	}

	// BR-49.1: the plan's runs are ROLLED_BACK, with the plan; the others are as they were.
	status := func(id string) string {
		return decode[runDetailDTO](t, do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + id, tenant: tenantA, duties: "migration.factory.view"})).Status
	}
	for id, want := range map[string]string{oi.RunID: "ROLLED_BACK", gl.RunID: "ROLLED_BACK", demf.RunID: "RECONCILED", wip.RunID: "RECONCILED"} {
		if s := status(id); s != want {
			t.Fatalf("run %s is %s, want %s", id, s, want)
		}
	}
	if n := count(t, admin, `select count(*) from pack_run where rolled_back_plan_id = $1 and rolled_back_at is not null`, plan.PlanID); n != 2 {
		t.Fatalf("runs rolled back with the plan: %d", n)
	}

	// FR-49.7: USMF is mastered by the legacy system again; DEMF's customer master stays cut over.
	list := do(t, h, request{method: "GET", path: "/api/v1/migration/coexistence?sourceSystem=AX2012&sourceCompany=USMF", tenant: tenantA, duties: "migration.factory.view"})
	if strings.Count(list.Body.String(), `"mode":"LEGACY"`) != len(domain.CoexistEntities) {
		t.Fatalf("USMF flags after the rollback: %s", list.Body)
	}
	if f, _ := st.GetCoexistence(ctx, domain.SourceAX2012, "DEMF", "customer"); f.Mode != domain.CoexistPassage {
		t.Fatalf("DEMF is not in the plan: %+v", f)
	}

	// The event the integration hub consumes lists exactly the flags handed back, and every flip
	// wrote its own coexistence.changed for the plan.
	evt := readRolledBackEvent(t, admin, plan.PlanID)
	if evt.Data.PackRunsRolledBack != 2 || len(evt.Data.CoexistenceResumed) != 3 {
		t.Fatalf("rolledback event %+v", evt.Data)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.migration.coexistence.changed.v1' and payload->'data'->>'plan_id' = $1
		and payload->'data'->>'mode' = 'LEGACY'`, plan.PlanID); n != 3 {
		t.Fatalf("coexistence.changed for the rollback: %d", n)
	}
	// The hub resumes each adapter the event names (services/integration-hub-ts does this on the
	// event); legacy changes of USMF flow again and a post-cutover change is created, not refused.
	for _, c := range evt.Data.CoexistenceResumed {
		f, err := st.GetCoexistence(ctx, domain.SourceSystem(c.SourceSystem), c.SourceCompany, c.Entity)
		if err != nil || f.Version != c.Version {
			t.Fatalf("flag %+v at version %d: %v", c, f.Version, err)
		}
		if err := coexist.Apply(ctx, adapter, f); err != nil {
			t.Fatal(err)
		}
	}
	if len(adapter.suspended) != 1 || !adapter.suspended["DEMF/customer"] {
		t.Fatalf("adapters still suspended after the rollback: %v", adapter.suspended)
	}
	adapter.in = []domain.InboundRecord{{Entity: "customer", SourceTable: "CustTable", LegacyID: "C-AFTER", ChangedAt: time.Now().Add(time.Minute)}}
	routed, err := coexist.Pull(ctx, adapter, st, st, "USMF", "customer", time.Now().Add(-time.Hour))
	if err != nil || routed[0].Decision != domain.RouteCreate {
		t.Fatalf("legacy change after the rollback: %+v %v", routed, err)
	}

	// The compensating journals took the go-live postings off the ledger: every account nets to zero,
	// and only this plan's journals were posted against it.
	for k, v := range ledgerNet(fake) {
		if !v.IsZero() {
			t.Fatalf("ledger %s nets to %s after the compensations", k, v)
		}
	}
	if n := len(fake.Entries()); n != 4 {
		t.Fatalf("ledger entries %d, want the 2 go-live journals and their 2 compensations", n)
	}

	// The rollback cannot run twice, and the stored plan keeps its outcome.
	if r := do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/" + plan.PlanID + "/rollback", tenant: tenantA, duties: "migration.cutover.manage",
		body: `{}`, user: "carol"}); r.Code != http.StatusConflict {
		t.Fatalf("second rollback: %d", r.Code)
	}
	again := decode[planDTO](t, do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID, tenant: tenantA, duties: "migration.cutover.manage"}))
	if again.RollbackOutcome == nil || again.RollbackOutcome.PackRunsRolledBack != 2 || again.RollbackOutcome.CoexistenceResumed != 3 {
		t.Fatalf("stored outcome %+v", again.RollbackOutcome)
	}
}

// The rollback stands when its compensations cannot be posted: the outcome says why and the
// compensations endpoint takes over with its backoff.
func TestRollback_CompensationProblemLeavesTheRollbackInPlace(t *testing.T) {
	h, _, _, fake, clk := ledgerServer(t)
	plan := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage",
		body: generateBody(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 36), user: "manager"}))
	fake.FailPosts(fmt.Errorf("ledger timeout"))
	body := strings.Replace(compensationPostings(), `{"postings":`, `{"ledgers":[{"entity":"USMF","legalEntityId":"`+le1+`","ledgerId":"L-US"}],"postings":`, 1)
	got := rollbackWithLedgers(t, h, plan.PlanID, body, le1)
	c := got.RollbackOutcome.Compensations
	if !got.RolledBack || c == nil || c.Pending != 2 || c.Posted != 0 || c.Compensations[0].Attempts != 1 {
		t.Fatalf("a failing ledger backs the compensations off: %+v", got.RollbackOutcome)
	}
	fake.FailPosts(nil)
	clk.add(2 * time.Minute)
	rep, code, _ := postComp(t, h, plan.PlanID, `{}`)
	if code != http.StatusOK || rep.Posted != 2 || !rep.Reconciled {
		t.Fatalf("compensations after the backoff: %d %+v", code, rep)
	}
	// A foreign ledger is reported, not fatal.
	plan2 := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage",
		body: generateBody(time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), 36), user: "manager"}))
	foreign := strings.Replace(body, `"ledgerId":"L-US"`, `"ledgerId":"L-OTHER"`, 1)
	got = rollbackWithLedgers(t, h, plan2.PlanID, foreign, le1)
	if !got.RolledBack || got.RollbackOutcome.Compensations != nil || !strings.Contains(got.RollbackOutcome.CompensationProblem, "another legal entity") {
		t.Fatalf("foreign ledger: %+v", got.RollbackOutcome)
	}
}

// BR-49.1: a run its cutover rolled back is not loaded into receivables any more (MIG-0004).
func TestRollback_RolledBackRunIsNotLoaded(t *testing.T) {
	e := newLoadEnv(t, 1, nil)
	run := e.start(t, loadBody(nRecords(3), ""))
	plan := decode[planDTO](t, do(t, e.h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage",
		body: generateBody(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 36), user: "manager"}))
	if r := rollbackBoth(t, e.h, "/api/v1/cutover/plans/"+plan.PlanID+"/rollback", `{}`, ""); r.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", r.Code, r.Body)
	}
	rec := e.do(t, "POST", "/api/v1/migration/runs/"+run.RunID+"/load", "")
	if rec.Code != http.StatusConflict || problemCode(t, rec) != CodeTransition {
		t.Fatalf("load of a rolled-back run: %d %s", rec.Code, rec.Body)
	}
	if n := e.scalar(t, `select count(*) from target_row where status <> 'PENDING'`); n != 0 {
		t.Fatalf("rows sent for a rolled-back run: %d", n)
	}
}
