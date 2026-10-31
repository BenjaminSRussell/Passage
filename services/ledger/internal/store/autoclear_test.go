package store

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

func putRule(t *testing.T, e env, rule AutoClearingRule) AutoClearingRule {
	t.Helper()
	id := ids.New()
	out, created, err := e.store.PutAutoClearingRule(e.ctx, e.ledger.ID, id, 0, rule)
	if err != nil || !created {
		t.Fatalf("put rule: created=%v err=%v", created, err)
	}
	return out
}

func gl1999Rule(sequence int, tol string) AutoClearingRule {
	r := AutoClearingRule{Name: fmt.Sprintf("Rule %d", sequence), AccountFrom: "1900", AccountTo: "1999", Criteria: []string{"assignment"}, Sequence: sequence, Active: true}
	if tol != "" {
		r.AllowTolerance, r.ToleranceGroup = true, tol
	}
	return r
}

func runInput(e env, mode, key string) AutoClearingInput {
	hash, _ := domain.HashPayload(map[string]string{"mode": mode, "key": key})
	return AutoClearingInput{LedgerID: e.ledger.ID, Mode: mode, ClearingDate: testToday, Key: key, Hash: hash, TraceID: testTrace, Today: testToday}
}

func countRows(t *testing.T, e env, q string, args ...any) int {
	t.Helper()
	var n int
	if err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, q, append([]any{e.tenant}, args...)...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// postMany posts one entry with the given lines on account 1999 and their total
// against 2100 (debits) or 6010 (credits), under document id doc.
func postMany(t *testing.T, e env, key, doc string, lines []domain.LineInput) {
	t.Helper()
	debit, credit := decimal.Zero, decimal.Zero
	for i := range lines {
		lines[i].Account, lines[i].Currency = "1999", "USD"
		if lines[i].Side == domain.SideDebit {
			debit = debit.Add(lines[i].Amount)
		} else {
			credit = credit.Add(lines[i].Amount)
		}
	}
	if debit.IsPositive() {
		lines = append(lines, domain.LineInput{Account: "2100", Side: domain.SideCredit, Amount: debit, Currency: "USD"})
	}
	if credit.IsPositive() {
		lines = append(lines, domain.LineInput{Account: "6010", Side: domain.SideDebit, Amount: credit, Currency: "USD"})
	}
	hash, _ := domain.HashPayload(map[string]string{"key": key})
	if _, err := e.store.PostEntry(e.ctx, PostInput{LedgerID: e.ledger.ID, DocumentID: doc, PostingDate: testPostingDate, Description: key,
		Source: ledgerevents.Source{System: "ledger", Type: "test", ID: key}, Lines: lines,
		Key: key, Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday}); err != nil {
		t.Fatalf("post %s: %v", key, err)
	}
}

func line(side, amount, ref string) domain.LineInput {
	return domain.LineInput{Side: side, Amount: decimal.RequireFromString(amount), SourceLineRef: ref}
}

// AC-11.13: 1,000 open items whose assignments sum to zero in 400 groups (350
// pairs and 50 groups of five) plus 50 unmatched items; the run makes 400 clearing
// documents of mode auto, leaves the 50 items open and creates no residual item. A
// repeat of the key replays the stored run.
func TestPostgres_AC11_13_AutomaticClearing(t *testing.T) {
	e := oiSetup(t)
	putRule(t, e, gl1999Rule(10, ""))
	var debits, credits, odd []domain.LineInput
	for i := 0; i < 350; i++ {
		ref := fmt.Sprintf("PO-%03d", i)
		debits = append(debits, line(domain.SideDebit, "10.00", ref))
		credits = append(credits, line(domain.SideCredit, "10.00", ref))
	}
	for i := 0; i < 50; i++ {
		ref := fmt.Sprintf("GR-%03d", i)
		debits = append(debits, line(domain.SideDebit, "40.00", ref))
		for j := 0; j < 4; j++ {
			credits = append(credits, line(domain.SideCredit, "10.00", ref))
		}
	}
	for i := 0; i < 50; i++ {
		odd = append(odd, line(domain.SideDebit, "7.00", fmt.Sprintf("ODD-%02d", i)))
	}
	postMany(t, e, "debits", ids.New(), debits)
	postMany(t, e, "credits", ids.New(), credits)
	postMany(t, e, "odd", ids.New(), odd)
	if n := countRows(t, e, `select count(*)::int from open_item where tenant_id = $1 and status = 'open'`); n != 1000 {
		t.Fatalf("%d open items, want 1000", n)
	}

	sim, created, err := e.store.RunAutoClearing(e.ctx, runInput(e, "simulate", ""))
	if err != nil || created || sim.GroupsFound != 400 || sim.GroupsCleared != 400 || sim.UnmatchedItems != 50 || len(sim.Groups) != 400 || sim.Groups[0].Status != "proposed" || len(sim.Groups[0].ItemIDs) != 2 {
		t.Fatalf("simulate: created=%v err=%v found=%d cleared=%d unmatched=%d", created, err, sim.GroupsFound, sim.GroupsCleared, sim.UnmatchedItems)
	}
	if countRows(t, e, `select count(*)::int from clearing_document where tenant_id = $1`) != 0 ||
		countRows(t, e, `select count(*)::int from auto_clearing_run where tenant_id = $1`) != 0 {
		t.Fatal("a simulation must not write anything")
	}

	run, created, err := e.store.RunAutoClearing(e.ctx, runInput(e, "clear", "run-1"))
	if err != nil || !created || run.ID == "" || run.GroupsFound != 400 || run.GroupsCleared != 400 || run.GroupsSkipped != 0 || run.ItemsCleared != 950 || run.UnmatchedItems != 50 {
		t.Fatalf("run: created=%v err=%v found=%d cleared=%d skipped=%d items=%d unmatched=%d", created, err, run.GroupsFound, run.GroupsCleared, run.GroupsSkipped, run.ItemsCleared, run.UnmatchedItems)
	}
	if n := countRows(t, e, `select count(*)::int from open_item where tenant_id = $1 and status = 'open' and assignment like 'ODD-%'`); n != 50 ||
		countRows(t, e, `select count(*)::int from open_item where tenant_id = $1 and status = 'open'`) != 50 {
		t.Fatalf("%d unmatched items still open, want exactly the 50", n)
	}
	if n := countRows(t, e, `select count(*)::int from clearing_document where tenant_id = $1 and mode = 'auto' and status = 'cleared'`); n != 400 {
		t.Fatalf("%d auto clearing documents, want 400", n)
	}
	if n := countRows(t, e, `select count(*)::int from open_item where tenant_id = $1 and source_type = 'residual'`); n != 0 {
		t.Fatalf("%d residual items, want none", n)
	}
	if run.Groups[0].ClearingDocumentID == nil || run.Groups[0].Status != "cleared" {
		t.Fatalf("group log: %+v", run.Groups[0])
	}
	if ev := clearingEvents(t, e, *run.Groups[0].ClearingDocumentID); len(ev) != 1 || ev[0] != "cleared" {
		t.Fatalf("events %v", ev)
	}
	var mode string
	if err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select payload->'data'->>'mode' from outbox where type = $1 and aggregate_id = $2`, ledgerevents.OpenItemCleared, *run.Groups[0].ClearingDocumentID).Scan(&mode)
	}); err != nil || mode != "auto" {
		t.Fatalf("event mode %q err %v", mode, err)
	}

	again, created, err := e.store.RunAutoClearing(e.ctx, runInput(e, "clear", "run-1"))
	if err != nil || created || again.ID != run.ID || countRows(t, e, `select count(*)::int from clearing_document where tenant_id = $1`) != 400 {
		t.Fatalf("replay: created=%v err=%v id=%s", created, err, again.ID)
	}
	other := runInput(e, "clear", "run-1")
	other.Hash, _ = domain.HashPayload(map[string]string{"different": "body"})
	var prob *httpx.Problem
	if _, _, err := e.store.RunAutoClearing(e.ctx, other); !errors.As(err, &prob) || prob.Status != http.StatusConflict || prob.Code != CodeIdempotencyConflict {
		t.Fatalf("same key, other body: %v", err)
	}

	next, created, err := e.store.RunAutoClearing(e.ctx, runInput(e, "clear", "run-2"))
	if err != nil || !created || next.GroupsFound != 0 || next.UnmatchedItems != 50 {
		t.Fatalf("second run finds nothing: %v %+v", err, next)
	}
	got, err := e.store.GetAutoClearingRun(e.ctx, run.ID)
	if err != nil || len(got.Groups) != 400 || got.Groups[399].No != 400 {
		t.Fatalf("get run: %v %d groups", err, len(got.Groups))
	}
	runs, err := e.store.ListAutoClearingRuns(e.ctx, e.ledger.ID)
	if err != nil || len(runs) != 2 || runs[0].ID != next.ID {
		t.Fatalf("list runs: %v %+v", err, runs)
	}
}

// An automatic clearing that was reset clears again on the next run as a new
// document; the reset document is not replayed under the group's key.
func TestPostgres_AutoClearingAfterReset(t *testing.T) {
	e := oiSetup(t)
	putRule(t, e, gl1999Rule(10, ""))
	postOn(t, e, "d-1", domain.SideDebit, "10.00", "PO-1", testPostingDate)
	postOn(t, e, "c-1", domain.SideCredit, "10.00", "PO-1", testPostingDate)
	first, _, err := e.store.RunAutoClearing(e.ctx, runInput(e, "clear", "run-1"))
	if err != nil || first.GroupsCleared != 1 {
		t.Fatalf("first run: %v %+v", err, first)
	}
	docID := *first.Groups[0].ClearingDocumentID
	if _, err := e.store.ResetClearing(e.ctx, ResetInput{ID: docID, Key: "reset-1", ReasonCode: "CORR", Today: testToday}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n := len(openItems(t, e, "reset_open")); n != 2 {
		t.Fatalf("%d reset-open items, want 2", n)
	}
	second, _, err := e.store.RunAutoClearing(e.ctx, runInput(e, "clear", "run-2"))
	if err != nil || second.GroupsCleared != 1 || second.Groups[0].ClearingDocumentID == nil || *second.Groups[0].ClearingDocumentID == docID {
		t.Fatalf("second run must make a new document: %v %+v", err, second)
	}
	if n := len(openItems(t, e, "cleared")); n != 2 {
		t.Fatalf("%d cleared items after the second run, want 2", n)
	}
	doc, err := e.store.GetClearing(e.ctx, *second.Groups[0].ClearingDocumentID)
	if err != nil || doc.Status != "cleared" || doc.Mode != "auto" {
		t.Fatalf("new document: %v %+v", err, doc)
	}
}

// document_id groups the items of entries that share the posting request's
// documentId (BR-11.11 split documents), not of one journal entry.
func TestPostgres_AutoClearingByDocumentID(t *testing.T) {
	e := oiSetup(t)
	rule := gl1999Rule(10, "")
	rule.Criteria = []string{"document_id"}
	putRule(t, e, rule)
	doc := ids.New()
	postMany(t, e, "part-1", doc, []domain.LineInput{line(domain.SideDebit, "25.00", "")})
	postMany(t, e, "part-2", doc, []domain.LineInput{line(domain.SideCredit, "25.00", "")})
	postMany(t, e, "other", ids.New(), []domain.LineInput{line(domain.SideDebit, "25.00", "")})
	sim, _, err := e.store.RunAutoClearing(e.ctx, runInput(e, "simulate", ""))
	if err != nil || sim.GroupsFound != 1 || sim.GroupsCleared != 1 || sim.UnmatchedItems != 1 || sim.Groups[0].ItemCount != 2 {
		t.Fatalf("by document id: %v %+v", err, sim)
	}
}

// Rules and runs are visible only inside the caller's tenant and legal entities.
func TestPostgres_AutoClearingIsolation(t *testing.T) {
	e := oiSetup(t)
	rule := putRule(t, e, gl1999Rule(10, ""))
	postOn(t, e, "d-1", domain.SideDebit, "10.00", "PO-1", testPostingDate)
	postOn(t, e, "c-1", domain.SideCredit, "10.00", "PO-1", testPostingDate)
	run, _, err := e.store.RunAutoClearing(e.ctx, runInput(e, "clear", "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	otherLE := principalCtx(e.tenant, ids.New())
	if _, err := e.store.GetAutoClearingRun(otherLE, run.ID); problemCode(err) != CodeAutoRunNotFound {
		t.Fatalf("another legal entity sees the run: %v", err)
	}
	if _, err := e.store.ListAutoClearingRules(otherLE, e.ledger.ID); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("another legal entity sees the rules: %v", err)
	}
	in := runInput(e, "clear", "run-x")
	if _, _, err := e.store.RunAutoClearing(otherLE, in); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("another legal entity runs on the ledger: %v", err)
	}
	// A rule id held in another legal entity cannot be taken over through a ledger of the caller's.
	if _, _, err := e.store.PutAutoClearingRule(otherLE, e.ledger.ID, rule.ID, 0, gl1999Rule(10, "")); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("put through a foreign ledger: %v", err)
	}
	otherTenant := principalCtx("0190a000-0000-7000-8000-0000000000ee", e.le)
	for _, table := range []string{"auto_clearing_rule", "auto_clearing_run", "auto_clearing_run_group"} {
		var visible int
		if err := dbx.WithTenantTx(otherTenant, e.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(otherTenant, `select count(*) from `+table).Scan(&visible)
		}); err != nil || visible != 0 {
			t.Fatalf("row level security on %s: %d rows err=%v", table, visible, err)
		}
	}
}

// A rule with allowTolerance clears a 100.00 / 99.98 group through the difference
// account; a rule without it skips the group as unbalanced and logs why.
func TestPostgres_AutoClearingTolerance(t *testing.T) {
	e := oiSetup(t)
	tolerance(t, e, "STD", "0.05", "0")
	tolerance(t, e, "TIGHT", "0.01", "0")
	postOn(t, e, "inv-1", domain.SideDebit, "100.00", "PO-7", testPostingDate)
	postOn(t, e, "gr-1", domain.SideCredit, "99.98", "PO-7", testPostingDate)
	postOn(t, e, "inv-2", domain.SideDebit, "50.00", "PO-8", testPostingDate)
	postOn(t, e, "gr-2", domain.SideCredit, "49.00", "PO-8", testPostingDate)

	strict := putRule(t, e, gl1999Rule(10, ""))
	run, _, err := e.store.RunAutoClearing(e.ctx, runInput(e, "clear", "strict"))
	if err != nil || run.GroupsCleared != 0 || run.GroupsSkipped != 2 || run.Groups[0].Status != "skipped" || run.Groups[0].ReasonCode != "LEDGER_CLEARING_UNBALANCED" {
		t.Fatalf("strict: %v %+v", err, run)
	}
	if n := len(openItems(t, e, "open")); n != 4 {
		t.Fatalf("%d open, want 4", n)
	}

	if _, _, err := e.store.PutAutoClearingRule(e.ctx, e.ledger.ID, strict.ID, strict.Version, gl1999Rule(10, "STD")); err != nil {
		t.Fatal(err)
	}
	pr, _, err := e.store.RunAutoClearing(e.ctx, runInput(e, "clear", "tolerant"))
	if err != nil || pr.GroupsFound != 2 || pr.GroupsCleared != 1 || pr.GroupsSkipped != 1 || pr.ItemsCleared != 2 {
		t.Fatalf("tolerant: %v %+v", err, pr)
	}
	var doc *ClearingDocument
	for _, g := range pr.Groups {
		if g.Status == "cleared" {
			d, err := e.store.GetClearing(e.ctx, *g.ClearingDocumentID)
			if err != nil {
				t.Fatal(err)
			}
			doc = &d
		} else if g.ReasonCode != "LEDGER_CLEARING_UNBALANCED" || g.Net != "1.00" {
			t.Fatalf("skipped group: %+v", g)
		}
	}
	if doc == nil || doc.Mode != "auto" || doc.DifferenceAmount != "0.02" || doc.DifferenceEntryID == nil || doc.ToleranceGroup != "STD" {
		t.Fatalf("document: %+v", doc)
	}
	if got := trialNet(t, e, "6990"); got != "0.02" {
		t.Fatalf("difference account net %s, want 0.02", got)
	}
	// An inactive tolerance group skips the unbalanced groups with its own reason.
	if _, _, err := e.store.PutToleranceGroup(e.ctx, e.ledger.ID, "STD", 0, ToleranceGroup{Name: "STD", DebitLimit: "0.05", CreditLimit: "0.05", PercentLimit: "0", DifferenceAccount: "6990", Active: false}); err != nil {
		t.Fatal(err)
	}
	sim, _, err := e.store.RunAutoClearing(e.ctx, runInput(e, "simulate", ""))
	if err != nil || sim.GroupsSkipped != 1 || sim.Groups[0].ReasonCode != CodeToleranceInactive {
		t.Fatalf("inactive tolerance: %v %+v", err, sim)
	}
}

// Criteria combine (assignment and a dimension), rules run in sequence and a later
// rule over the same items does not clear them twice; a rule run alone must be active.
func TestPostgres_AutoClearingRulesOverlapAndSelect(t *testing.T) {
	e := oiSetup(t)
	postOn(t, e, "a-1", domain.SideDebit, "10.00", "PO-1", testPostingDate)
	postOn(t, e, "a-2", domain.SideCredit, "10.00", "PO-1", testPostingDate)
	first := putRule(t, e, gl1999Rule(10, ""))
	second := putRule(t, e, gl1999Rule(20, ""))
	byDim := gl1999Rule(30, "")
	byDim.Criteria = []string{"assignment", "dim.DEPT"}
	third := putRule(t, e, byDim)

	sim, _, err := e.store.RunAutoClearing(e.ctx, runInput(e, "simulate", ""))
	if err != nil || sim.GroupsFound != 1 || sim.Groups[0].RuleID != first.ID {
		t.Fatalf("overlap: %v %+v", err, sim)
	}
	one := runInput(e, "simulate", "")
	one.RuleID = third.ID
	sim, _, err = e.store.RunAutoClearing(e.ctx, one)
	// The postings carry no DEPT value, so a rule that groups by it matches nothing.
	if err != nil || sim.GroupsFound != 0 || sim.UnmatchedItems != 2 || sim.RuleID == nil || *sim.RuleID != third.ID {
		t.Fatalf("by dimension: %v %+v", err, sim)
	}
	if _, _, err := e.store.PutAutoClearingRule(e.ctx, e.ledger.ID, second.ID, second.Version, AutoClearingRule{Name: "Off", AccountFrom: "1900", AccountTo: "1999", Criteria: []string{"assignment"}, Sequence: 20, Active: false}); err != nil {
		t.Fatal(err)
	}
	two := runInput(e, "simulate", "")
	two.RuleID = second.ID
	if _, _, err := e.store.RunAutoClearing(e.ctx, two); err == nil {
		t.Fatal("an inactive rule must not run")
	}
	var rej *domain.RejectedError
	if _, _, err := e.store.RunAutoClearing(e.ctx, two); !errors.As(err, &rej) || rej.Violations[0].Code != CodeAutoRuleInactive {
		t.Fatalf("inactive: %v", err)
	}
	two.RuleID = ids.New()
	var prob *httpx.Problem
	if _, _, err := e.store.RunAutoClearing(e.ctx, two); !errors.As(err, &prob) || prob.Code != CodeAutoRuleNotFound {
		t.Fatalf("unknown rule: %v", err)
	}
}

func TestPostgres_AutoClearingRuleMaintenance(t *testing.T) {
	e := oiSetup(t)
	tolerance(t, e, "STD", "0.05", "0")
	rule := putRule(t, e, gl1999Rule(10, "STD"))
	if rule.Version != 1 || !rule.AllowTolerance || rule.ToleranceGroup != "STD" || rule.CreatedBy != "test-user" {
		t.Fatalf("created: %+v", rule)
	}
	var prob *httpx.Problem
	if _, _, err := e.store.PutAutoClearingRule(e.ctx, e.ledger.ID, rule.ID, 5, gl1999Rule(10, "STD")); !errors.As(err, &prob) || prob.Status != http.StatusPreconditionFailed {
		t.Fatalf("stale version: %v", err)
	}
	changed := gl1999Rule(15, "")
	changed.Criteria = []string{"assignment", "amount_txn"}
	out, created, err := e.store.PutAutoClearingRule(e.ctx, e.ledger.ID, rule.ID, 1, changed)
	if err != nil || created || out.Version != 2 || out.AllowTolerance || out.ToleranceGroup != "" || out.Sequence != 15 || len(out.Criteria) != 2 {
		t.Fatalf("replace: %v %+v", err, out)
	}
	var rej *domain.RejectedError
	if _, _, err := e.store.PutAutoClearingRule(e.ctx, e.ledger.ID, ids.New(), 0, gl1999Rule(10, "NOPE")); !errors.As(err, &rej) || rej.Violations[0].Code != CodeToleranceNotFound {
		t.Fatalf("unknown tolerance group: %v", err)
	}
	got, err := e.store.GetAutoClearingRule(e.ctx, e.ledger.ID, rule.ID)
	if err != nil || got.Version != 2 {
		t.Fatalf("get: %v %+v", err, got)
	}
	if _, err := e.store.GetAutoClearingRule(e.ctx, e.ledger.ID, ids.New()); !errors.As(err, &prob) || prob.Code != CodeAutoRuleNotFound {
		t.Fatalf("missing rule: %v", err)
	}
	list, err := e.store.ListAutoClearingRules(e.ctx, e.ledger.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
}
