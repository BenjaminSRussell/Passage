package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// realignSetup posts expense on DEPT D100 in January, February and March 2026, one posting on D200
// in February and one with no department, and returns the environment with a reason code.
func realignSetup(t *testing.T) env {
	t.Helper()
	e, _ := setup(t)
	if _, _, err := e.store.PutReasonCode(e.ctx, "REORG", ReasonCode{Name: "Reorganisation", AppliesTo: []string{"ledger"}, AlternativePostingDateAllowed: true, Active: true}); err != nil {
		t.Fatalf("reason: %v", err)
	}
	post := func(key, date, amount, dept string) {
		in := vendorInvoice(e.ledger.ID, key, amount)
		d, _ := time.Parse(time.DateOnly, date)
		in.PostingDate = d
		in.Lines[0].Dimensions = map[string]string{"DEPT": dept}
		if dept == "" {
			in.Lines[0].Dimensions = nil
		}
		if _, err := e.store.PostEntry(e.ctx, in); err != nil {
			t.Fatalf("post %s: %v", key, err)
		}
	}
	post("RA-1", "2026-01-10", "100.00", "D100")
	post("RA-2", "2026-01-20", "50.00", "D100")
	post("RA-3", "2026-02-10", "200.00", "D100")
	post("RA-4", "2026-02-12", "30.00", "D200")
	post("RA-5", "2026-03-10", "400.00", "D100")
	post("RA-6", "2026-03-11", "7.00", "")
	return e
}

func realignRequest(e env, key string) RealignmentInput {
	f := domain.RealignmentFilter{AccountFrom: "6000", AccountTo: "6999", PeriodFrom: "2026-01", PeriodTo: "2026-03"}
	m := domain.RealignmentMapping{"DEPT": {"D100": "D150"}}
	h, _ := domain.HashPayload(map[string]any{"f": f, "m": m})
	return RealignmentInput{LedgerID: e.ledger.ID, Filter: f, Mapping: m, Key: key, Hash: h, Actor: "controller"}
}

// netByDept is the account 6010 net per DEPT value and period from the balance projection.
func netByDept(t *testing.T, e env) map[string]string {
	t.Helper()
	rows, err := e.admin.Query(e.ctx, `select b.period_key || '/' || coalesce(nullif(c.dimensions ->> 'DEPT', ''), '-'), sum(b.net)::text
		from balance b join dimension_combination c on c.tenant_id = b.tenant_id and c.id = b.dimension_combination_id
		join main_account a on a.tenant_id = b.tenant_id and a.id = b.main_account_id
		where b.ledger_id = $1 and a.account_number = '6010' group by 1`, e.ledger.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		if !decimal.RequireFromString(v).IsZero() {
			out[k] = decimal.RequireFromString(v).StringFixed(2)
		}
	}
	return out
}

func realignAction(e env, run RealignmentRun, reason string) RealignmentAction {
	return RealignmentAction{LedgerID: e.ledger.ID, RunID: run.ID, ReasonCode: reason, ReasonComment: "cost centre merge", Key: "k-" + run.ID,
		Actor: "controller", TraceID: testTrace, Today: testToday}
}

func mustProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != status || p.Code != code {
		t.Fatalf("want %d %s, got %v", status, code, err)
	}
}

// AC-11.17: a realignment D100 to D150 over three periods is posted, then reversed, and the balances per
// combination return to the values before the run.
func TestPostgres_AC11_17_RealignmentPostAndReverse(t *testing.T) {
	e := realignSetup(t)
	before := netByDept(t, e)
	if before["2026-01/D100"] != "150.00" || before["2026-02/D100"] != "200.00" || before["2026-03/D100"] != "400.00" || before["2026-03/-"] != "7.00" {
		t.Fatalf("setup balances: %v", before)
	}

	run, replayed, err := e.store.CreateRealignment(e.ctx, realignRequest(e, "RA-RUN-1"))
	if err != nil || replayed {
		t.Fatalf("simulate: %v replayed=%v", err, replayed)
	}
	if run.Status != "proposed" || run.CombinationsAffected != 3 || run.LineCount != 4 || run.AmountMoved != "750.000000" && run.AmountMoved != "750.00" || len(run.Preview) != 3 {
		t.Fatalf("simulation: %+v", run)
	}
	if p := run.Preview[0]; p.Period != "2026-01" || p.OldName != "6010-D100" || p.NewName != "6010-D150" || p.Net != "150" && p.Net != "150.00" {
		t.Fatalf("first preview row: %+v", p)
	}
	if got := netByDept(t, e); len(got) != len(before) || got["2026-01/D100"] != "150.00" {
		t.Fatalf("simulation moved balances: %v", got)
	}
	again, replayed, err := e.store.CreateRealignment(e.ctx, realignRequest(e, "RA-RUN-1"))
	if err != nil || !replayed || again.ID != run.ID {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	other := realignRequest(e, "RA-RUN-1")
	other.Hash, _ = domain.HashPayload("another body")
	_, _, err = e.store.CreateRealignment(e.ctx, other)
	mustProblem(t, err, http.StatusConflict, "LEDGER_IDEMPOTENCY_CONFLICT")

	_, err = e.store.PostRealignment(e.ctx, realignAction(e, run, ""))
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_REASON_REQUIRED" {
		t.Fatalf("post without a reason: %v", err)
	}
	posted, err := e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if posted.Status != "posted" || len(posted.EntryIDs) != 3 || posted.ReasonCode == nil || *posted.ReasonCode != "REORG" || posted.PostedBy == nil {
		t.Fatalf("posted run: %+v", posted)
	}
	after := netByDept(t, e)
	for _, k := range []string{"2026-01", "2026-02", "2026-03"} {
		if _, left := after[k+"/D100"]; left {
			t.Errorf("%s/D100 still holds %s", k, after[k+"/D100"])
		}
	}
	if after["2026-01/D150"] != "150.00" || after["2026-02/D150"] != "200.00" || after["2026-03/D150"] != "400.00" ||
		after["2026-02/D200"] != "30.00" || after["2026-03/-"] != "7.00" {
		t.Fatalf("balances after the run: %v", after)
	}
	var flagged int
	if err := e.admin.QueryRow(e.ctx, `select count(*) from journal_entry where realignment_run_id = $1 and is_realignment and source_type = 'realignment'`, run.ID).Scan(&flagged); err != nil || flagged != 3 {
		t.Fatalf("flagged entries = %d err=%v", flagged, err)
	}
	if got, err := e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG")); err != nil || got.Status != "posted" || len(got.EntryIDs) != 3 {
		t.Fatalf("a repeated post must change nothing: %+v %v", got, err)
	}
	_, err = e.store.CancelRealignment(e.ctx, realignAction(e, run, ""))
	mustProblem(t, err, http.StatusConflict, CodeRealignmentState)

	reversed, err := e.store.ReverseRealignment(e.ctx, realignAction(e, run, "REORG"))
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if reversed.Status != "reversed" || len(reversed.ReversalEntryIDs) != 3 {
		t.Fatalf("reversed run: %+v", reversed)
	}
	final := netByDept(t, e)
	if len(final) != len(before) {
		t.Fatalf("balances after the reversal: %v, want %v", final, before)
	}
	for k, v := range before {
		if final[k] != v {
			t.Errorf("%s = %s after the reversal, want %s", k, final[k], v)
		}
	}
	if err := e.admin.QueryRow(e.ctx, `select count(*) from journal_entry where realignment_run_id = $1 and reversal_of_entry_id is not null`, run.ID).Scan(&flagged); err != nil || flagged != 3 {
		t.Fatalf("reversing entries flagged = %d err=%v", flagged, err)
	}
	if got, err := e.store.ReverseRealignment(e.ctx, realignAction(e, run, "REORG")); err != nil || got.Status != "reversed" {
		t.Fatalf("a repeated reverse must change nothing: %+v %v", got, err)
	}
	_, err = e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG"))
	mustProblem(t, err, http.StatusConflict, CodeRealignmentState)

	list, err := e.store.ListRealignments(e.ctx, e.ledger.ID)
	if err != nil || len(list) != 1 || len(list[0].Preview) != 0 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if got, err := e.store.GetRealignment(e.ctx, e.ledger.ID, run.ID); err != nil || len(got.Preview) != 3 {
		t.Fatalf("get: %+v %v", got, err)
	}
	_, err = e.store.GetRealignment(e.ctx, e.ledger.ID, "00000000-0000-4000-8000-000000000999")
	mustProblem(t, err, http.StatusNotFound, CodeRealignmentNotFound)
}

func TestPostgres_RealignmentRefusesBadRequestsAndClosedPeriods(t *testing.T) {
	e := realignSetup(t)
	bad := realignRequest(e, "BAD-1")
	bad.Mapping = domain.RealignmentMapping{"COLOR": {"A": "B"}}
	_, _, err := e.store.CreateRealignment(e.ctx, bad)
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != domain.CodeRealignmentInvalid {
		t.Fatalf("unknown attribute: %v", err)
	}

	run, _, err := e.store.CreateRealignment(e.ctx, realignRequest(e, "CLOSED-1"))
	if err != nil {
		t.Fatal(err)
	}
	gate := &fakeGate{status: "closed"}
	_, err = e.store.PostRealignment(WithPeriodGate(e.ctx, gate), realignAction(e, run, "REORG"))
	if !errors.As(err, &rej) || rej.Violations[0].Code != domain.CodeRealignmentPeriodClosed || len(rej.Violations) != 3 {
		t.Fatalf("closed periods: %v", err)
	}
	if got := netByDept(t, e); got["2026-01/D100"] != "150.00" {
		t.Fatalf("a refused run moved balances: %v", got)
	}
	_, err = e.store.PostRealignment(e.ctx, realignAction(e, run, "NOPE"))
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_REASON_REQUIRED" {
		t.Fatalf("unknown reason: %v", err)
	}

	cancelled, err := e.store.CancelRealignment(e.ctx, realignAction(e, run, ""))
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	_, err = e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG"))
	mustProblem(t, err, http.StatusConflict, CodeRealignmentState)
	_, err = e.store.ReverseRealignment(e.ctx, realignAction(e, run, "REORG"))
	mustProblem(t, err, http.StatusConflict, CodeRealignmentState)
}

// A value that was deactivated since the old postings is still realigned away from, and a new value
// that is not registered or is suspended is refused like any posting.
func TestPostgres_RealignmentValidatesTheNewValue(t *testing.T) {
	e := realignSetup(t)
	if _, _, err := e.store.PutDimensionValue(e.ctx, e.ledger.ID, 0, domain.DimensionValue{Attribute: "DEPT", Value: "D150", Name: "Merged"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, _, err := e.store.PutDimensionValue(e.ctx, e.ledger.ID, 0, domain.DimensionValue{Attribute: "DEPT", Value: "D100", Name: "Old", Suspended: true}); err != nil {
		t.Fatalf("suspend old: %v", err)
	}
	run, _, err := e.store.CreateRealignment(e.ctx, realignRequest(e, "VAL-1"))
	if err != nil {
		t.Fatal(err)
	}
	if posted, err := e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG")); err != nil || posted.Status != "posted" {
		t.Fatalf("realigning away from a suspended value must work: %+v %v", posted, err)
	}

	e2 := realignSetup(t)
	if _, _, err := e2.store.PutDimensionValue(e2.ctx, e2.ledger.ID, 0, domain.DimensionValue{Attribute: "DEPT", Value: "D150", Name: "Merged", Suspended: true}); err != nil {
		t.Fatalf("register inactive: %v", err)
	}
	run2, _, err := e2.store.CreateRealignment(e2.ctx, realignRequest(e2, "VAL-2"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e2.store.PostRealignment(e2.ctx, realignAction(e2, run2, "REORG"))
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_DIMENSION_INACTIVE" {
		t.Fatalf("inactive new value: %v", err)
	}
}

// closedBefore is a close-period gate that reports every date before from as closed.
type closedBefore struct{ from time.Time }

func (g closedBefore) Status(_ context.Context, _, _ string, postingDate time.Time, _ string) (string, error) {
	if postingDate.Before(g.from) {
		return "closed", nil
	}
	return "open", nil
}

func postedRun(t *testing.T, e env, key string) RealignmentRun {
	t.Helper()
	run, _, err := e.store.CreateRealignment(e.ctx, realignRequest(e, key))
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	posted, err := e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return posted
}

// The entries of a run are flagged on the entry and line reads, count in the trial balance like any
// entry, and are reversed only through their run.
func TestPostgres_RealignmentEntriesAreFlaggedAndReversedOnlyAsARun(t *testing.T) {
	e := realignSetup(t)
	tbBefore, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID, PeriodFrom: "2026-01", PeriodTo: "2026-03"})
	if err != nil {
		t.Fatal(err)
	}
	run := postedRun(t, e, "FLAG-1")

	entry, err := e.store.GetEntry(e.ctx, run.EntryIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !entry.IsRealignment || entry.RealignmentRunID == nil || *entry.RealignmentRunID != run.ID || entry.Source.Type != "realignment" {
		t.Fatalf("entry read: realignment=%v run=%v source=%+v", entry.IsRealignment, entry.RealignmentRunID, entry.Source)
	}
	page, err := e.store.ListLines(e.ctx, LineFilter{LedgerID: e.ledger.ID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	flagged, plain := 0, 0
	for _, l := range page.Items {
		if l.IsRealignment {
			flagged++
		} else {
			plain++
		}
	}
	if flagged != 6 || plain == 0 {
		t.Fatalf("line reads: %d realignment lines, %d others", flagged, plain)
	}
	// A reclassification moves balances between combinations of the same account, so the trial
	// balance per account is unchanged.
	tbAfter, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID, PeriodFrom: "2026-01", PeriodTo: "2026-03"})
	if err != nil {
		t.Fatal(err)
	}
	// Rows carry turnover too, so they differ; the net per account must not.
	netOf := func(tb TrialBalance) map[string]string {
		out := map[string]string{}
		for _, r := range tb.Rows {
			out[r.Account] = r.Net
		}
		return out
	}
	if b, a := netOf(tbBefore), netOf(tbAfter); fmt.Sprint(b) != fmt.Sprint(a) {
		t.Fatalf("trial balance net per account changed: before %v after %v", b, a)
	}

	_, err = e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: run.EntryIDs[0], ReasonCode: "REORG", Key: "single-rev", Hash: keyHash("single-rev"),
		Actor: "controller", TraceID: testTrace, Today: testToday})
	mustProblem(t, err, http.StatusConflict, CodeRealignmentEntry)

	reversed, err := e.store.ReverseRealignment(e.ctx, realignAction(e, run, "REORG"))
	if err != nil {
		t.Fatalf("reverse the run: %v", err)
	}
	rev, err := e.store.GetEntry(e.ctx, reversed.ReversalEntryIDs[0])
	if err != nil || !rev.IsRealignment || rev.RealignmentRunID == nil || *rev.RealignmentRunID != run.ID {
		t.Fatalf("reversing entry read: %+v %v", rev, err)
	}
	_, err = e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: rev.ID, ReasonCode: "REORG", Key: "single-rev-2", Hash: keyHash("single-rev-2"),
		Actor: "controller", TraceID: testTrace, Today: testToday})
	mustProblem(t, err, http.StatusConflict, CodeRealignmentEntry)
}

// A period closed after the post blocks the reversal on the original dates, all or nothing; a
// posting date in an open period reverses every entry there and the cumulative balances return.
func TestPostgres_RealignmentReversalIntoAClosedPeriod(t *testing.T) {
	e := realignSetup(t)
	before := netByDept(t, e)
	run := postedRun(t, e, "CLOSE-REV-1")

	gated := WithPeriodGate(e.ctx, closedBefore{from: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)})
	_, err := e.store.ReverseRealignment(gated, realignAction(e, run, "REORG"))
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != domain.CodeRealignmentPeriodClosed || !strings.Contains(rej.Violations[0].Message, "postingDate") {
		t.Fatalf("reversal into a closed period: %v", err)
	}
	if got, _ := e.store.GetRealignment(e.ctx, e.ledger.ID, run.ID); got.Status != "posted" || len(got.ReversalEntryIDs) != 0 {
		t.Fatalf("a refused reversal changed the run: %+v", got)
	}
	var reversing int
	if err := e.admin.QueryRow(e.ctx, `select count(*) from journal_entry where realignment_run_id = $1 and reversal_of_entry_id is not null`, run.ID).Scan(&reversing); err != nil || reversing != 0 {
		t.Fatalf("a refused reversal posted %d entries (err %v)", reversing, err)
	}

	in := realignAction(e, run, "REORG")
	date := testToday
	in.PostingDate = &date
	reversed, err := e.store.ReverseRealignment(gated, in)
	if err != nil || reversed.Status != "reversed" {
		t.Fatalf("reverse into an open period: %+v %v", reversed, err)
	}
	var dates int
	if err := e.admin.QueryRow(e.ctx, `select count(*) from journal_entry where realignment_run_id = $1 and reversal_of_entry_id is not null and posting_date = $2::text::date`,
		run.ID, testToday.Format(time.DateOnly)).Scan(&dates); err != nil || dates != 3 {
		t.Fatalf("reversing entries on %s = %d (err %v)", testToday.Format(time.DateOnly), dates, err)
	}
	cumulative := func(m map[string]string) map[string]decimal.Decimal {
		out := map[string]decimal.Decimal{}
		for k, v := range m {
			dept := k[strings.Index(k, "/")+1:]
			out[dept] = out[dept].Add(decimal.RequireFromString(v))
		}
		for k, v := range out {
			if v.IsZero() {
				delete(out, k)
			}
		}
		return out
	}
	if b, a := cumulative(before), cumulative(netByDept(t, e)); fmt.Sprint(b) != fmt.Sprint(a) {
		t.Fatalf("cumulative balances per department: before %v after %v", b, a)
	}
}

// The reason must apply to ledger and carry a comment when the code says so (FR-11.15).
func TestPostgres_RealignmentReasonRules(t *testing.T) {
	e := realignSetup(t)
	if _, _, err := e.store.PutReasonCode(e.ctx, "BANKONLY", ReasonCode{Name: "Bank", AppliesTo: []string{"bank"}, Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.PutReasonCode(e.ctx, "EXPLAIN", ReasonCode{Name: "Explain", AppliesTo: []string{"ledger"}, RequiresComment: true, Active: true}); err != nil {
		t.Fatal(err)
	}
	run, _, err := e.store.CreateRealignment(e.ctx, realignRequest(e, "REASON-1"))
	if err != nil {
		t.Fatal(err)
	}
	var rej *domain.RejectedError
	_, err = e.store.PostRealignment(e.ctx, realignAction(e, run, "BANKONLY"))
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_REASON_INVALID_MODULE" {
		t.Fatalf("reason of another module: %v", err)
	}
	noComment := realignAction(e, run, "EXPLAIN")
	noComment.ReasonComment = ""
	_, err = e.store.PostRealignment(e.ctx, noComment)
	if !errors.As(err, &rej) || rej.Violations[0].Field != "reasonComment" {
		t.Fatalf("reason without its comment: %v", err)
	}
	if posted, err := e.store.PostRealignment(e.ctx, realignAction(e, run, "EXPLAIN")); err != nil || posted.Status != "posted" {
		t.Fatalf("post with comment: %+v %v", posted, err)
	}
}

// Line validation rules (FR-11.21) check the new side only: a rule against the old value does not
// block moving away from it, a rule against the new value refuses the post.
func TestPostgres_RealignmentRunsValidationRulesOnTheNewSide(t *testing.T) {
	rule := func(value string) domain.ValidationRule {
		return domain.ValidationRule{Sequence: 10, Name: "No " + value, Level: domain.RuleLevelLine, Severity: domain.SeverityError, Active: true,
			Message:      "Department " + value + " is closed",
			Prerequisite: []domain.RuleCondition{{Field: domain.FieldAccount, Op: domain.CondBetween, Values: []string{"6000", "6999"}}},
			Checks:       []domain.RuleCondition{{Field: "DEPT", Op: domain.CondNe, Values: []string{value}}}}
	}
	e := realignSetup(t)
	if _, _, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, ids.New(), 0, rule("D100")); err != nil {
		t.Fatal(err)
	}
	if run := postedRun(t, e, "RULE-1"); run.Status != "posted" {
		t.Fatalf("moving away from a refused value: %+v", run)
	}

	e2 := realignSetup(t)
	if _, _, err := e2.store.PutValidationRule(e2.ctx, e2.ledger.ID, ids.New(), 0, rule("D150")); err != nil {
		t.Fatal(err)
	}
	run, _, err := e2.store.CreateRealignment(e2.ctx, realignRequest(e2, "RULE-2"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e2.store.PostRealignment(e2.ctx, realignAction(e2, run, "REORG"))
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != domain.CodeValidationRuleFail || rej.Violations[0].LineIndex == nil || *rej.Violations[0].LineIndex != 1 {
		t.Fatalf("new value refused by a rule: %v", err)
	}
	if got := netByDept(t, e2); got["2026-01/D100"] != "150.00" {
		t.Fatalf("a refused run moved balances: %v", got)
	}
}

// A period with more balances than one entry holds posts several entries, all reversed with the run.
func TestPostgres_RealignmentSplitsALargePeriodIntoSeveralEntries(t *testing.T) {
	saved := realignmentRowsPerEntry
	realignmentRowsPerEntry = 1
	t.Cleanup(func() { realignmentRowsPerEntry = saved })
	e := realignSetup(t)
	before := netByDept(t, e)
	in := realignRequest(e, "CHUNK-1")
	in.Mapping = domain.RealignmentMapping{"DEPT": {"D100": "D150", "D200": "D250"}}
	in.Hash, _ = domain.HashPayload(in.Mapping)
	run, _, err := e.store.CreateRealignment(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	posted, err := e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG"))
	if err != nil || len(posted.EntryIDs) != 4 || posted.CombinationsAffected != 4 {
		t.Fatalf("post: %d entries, %+v, %v", len(posted.EntryIDs), posted, err)
	}
	if got := netByDept(t, e); got["2026-02/D250"] != "30.00" || got["2026-02/D150"] != "200.00" {
		t.Fatalf("balances after the run: %v", got)
	}
	if _, err := e.store.ReverseRealignment(e.ctx, realignAction(e, posted, "REORG")); err != nil {
		t.Fatal(err)
	}
	if got := netByDept(t, e); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("after the reversal %v, want %v", got, before)
	}
}

// FR-11.14 with FR-11.24: a trial balance grouped by department shows the realignment's move
// from D100 to D150 as turnover on both combinations, and the reversal puts the net back.
func TestPostgres_TrialBalanceByDimensionAfterRealignment(t *testing.T) {
	e := realignSetup(t)
	netOf := func() map[string]string {
		t.Helper()
		tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID, PeriodFrom: "2026-01", PeriodTo: "2026-03",
			AccountFrom: "6010", AccountTo: "6010", GroupBy: []string{"DEPT"}})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, r := range tb.Rows {
			out[r.Dimensions["DEPT"]] = r.Debit + "|" + r.Credit + "|" + r.Net
		}
		return out
	}
	if got := fmt.Sprint(netOf()); got != fmt.Sprint(map[string]string{"": "7.00|0.00|7.00", "D100": "750.00|0.00|750.00", "D200": "30.00|0.00|30.00"}) {
		t.Fatalf("before the run: %s", got)
	}
	run, _, err := e.store.CreateRealignment(e.ctx, realignRequest(e, "RA-TB-1"))
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	if _, err := e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG")); err != nil {
		t.Fatalf("post: %v", err)
	}
	want := map[string]string{"": "7.00|0.00|7.00", "D100": "750.00|750.00|0.00", "D150": "750.00|0.00|750.00", "D200": "30.00|0.00|30.00"}
	if got := fmt.Sprint(netOf()); got != fmt.Sprint(want) {
		t.Fatalf("after the run: %s, want %v", got, want)
	}
	tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID, GroupBy: []string{"DEPT"}})
	if err != nil || !tb.Balanced {
		t.Fatalf("the grouped ledger must still balance after the run: %+v %v", tb, err)
	}
	if _, err := e.store.ReverseRealignment(e.ctx, realignAction(e, run, "REORG")); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	got := netOf()
	if !strings.HasSuffix(got["D100"], "|750.00") || !strings.HasSuffix(got["D150"], "|0.00") {
		t.Fatalf("after the reversal: %v", got)
	}
}

// qtyByDept is the account 6010 net statistical quantity per period, DEPT value and unit from the
// quantity projection; zero rows are left out.
func qtyByDept(t *testing.T, e env) map[string]string {
	t.Helper()
	rows, err := e.admin.Query(e.ctx, `select b.period_key || '/' || coalesce(nullif(c.dimensions ->> 'DEPT', ''), '-') || '/' || b.unit, b.net_quantity::text
		from balance_quantity b join dimension_combination c on c.tenant_id = b.tenant_id and c.id = b.dimension_combination_id
		join main_account a on a.tenant_id = b.tenant_id and a.id = b.main_account_id
		where b.ledger_id = $1 and a.account_number = '6010' and b.net_quantity <> 0`, e.ledger.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = decimal.RequireFromString(v).StringFixed(3)
	}
	return out
}

// BR-11.2 with FR-11.24: a run moves the statistical quantities of a combination with its amounts,
// including a combination that holds quantities and no amount, and the reversal puts them back.
func TestPostgres_RealignmentMovesStatisticalQuantities(t *testing.T) {
	e := realignSetup(t)
	in := vendorInvoice(e.ledger.ID, "RQ-1", "10.00")
	in.PostingDate = time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	in.Lines = append(in.Lines,
		statLine(domain.SideDebit, "D100", "12.5", "KG"), statLine(domain.SideCredit, "D100", "2.5", "KG"),
		statLine(domain.SideCredit, "D100", "4", "PCS"),
		statLine(domain.SideDebit, "D300", "8", "HC"), statLine(domain.SideCredit, "D300", "3", "HC"))
	if _, err := e.store.PostEntry(e.ctx, in); err != nil {
		t.Fatalf("post: %v", err)
	}
	beforeQty, beforeNet := qtyByDept(t, e), netByDept(t, e)
	want := map[string]string{"2026-02/D100/KG": "10.000", "2026-02/D100/PCS": "-4.000", "2026-02/D300/HC": "5.000"}
	if fmt.Sprint(beforeQty) != fmt.Sprint(want) {
		t.Fatalf("setup quantities %v, want %v", beforeQty, want)
	}

	req := realignRequest(e, "RQ-RUN")
	req.Mapping = domain.RealignmentMapping{"DEPT": {"D100": "D150", "D300": "D350"}}
	req.Hash, _ = domain.HashPayload(req.Mapping)
	run, _, err := e.store.CreateRealignment(e.ctx, req)
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	var sawStatOnly bool
	for _, p := range run.Preview {
		if p.OldName == "6010-D300" {
			sawStatOnly = decimal.RequireFromString(p.Net).IsZero() && len(p.Quantities) == 1 && p.Quantities[0].Unit == "HC" && p.Quantities[0].Net == "5"
		}
		if p.OldName == "6010-D100" && p.Period == "2026-02" && len(p.Quantities) != 2 {
			t.Fatalf("the D100 row of February carries two units: %+v", p)
		}
	}
	if !sawStatOnly || run.CombinationsAffected != 4 {
		t.Fatalf("the quantity-only combination must be in the plan: %+v", run)
	}

	posted, err := e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	got := qtyByDept(t, e)
	want = map[string]string{"2026-02/D150/KG": "10.000", "2026-02/D150/PCS": "-4.000", "2026-02/D350/HC": "5.000"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("quantities after the run %v, want %v", got, want)
	}
	if n := netByDept(t, e); n["2026-02/D150"] != "210.00" {
		t.Fatalf("amounts must move with the quantities: %v", n)
	}
	// The quantity lines are statistical: they add no amount to the entry.
	var stat int
	if err := e.admin.QueryRow(e.ctx, `select count(*) from journal_line l join journal_entry j on j.tenant_id = l.tenant_id and j.id = l.entry_id
		where j.realignment_run_id = $1 and l.quantity is not null and l.amount_acct = 0`, run.ID).Scan(&stat); err != nil || stat != 6 {
		t.Fatalf("statistical reclassification lines = %d err=%v", stat, err)
	}

	if _, err := e.store.ReverseRealignment(e.ctx, realignAction(e, posted, "REORG")); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if got := qtyByDept(t, e); fmt.Sprint(got) != fmt.Sprint(beforeQty) {
		t.Fatalf("quantities after the reversal %v, want %v", got, beforeQty)
	}
	if got := netByDept(t, e); fmt.Sprint(got) != fmt.Sprint(beforeNet) {
		t.Fatalf("amounts after the reversal %v, want %v", got, beforeNet)
	}
}

// A ledger whose quantity projection still misses lines posted before migration 0027
// (quantity_rebuild_pending) reads the quantities to move from the journal, so the run still
// empties the old combination; the rebuild then shows them all on the new one.
func TestPostgres_RealignmentReadsQuantitiesFromTheJournalUntilTheBackfill(t *testing.T) {
	e := realignSetup(t)
	in := vendorInvoice(e.ledger.ID, "RQP-1", "10.00")
	in.PostingDate = time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	in.Lines = append(in.Lines, statLine(domain.SideDebit, "D100", "7", "KG"), statLine(domain.SideDebit, "D300", "2", "HC"))
	if _, err := e.store.PostEntry(e.ctx, in); err != nil {
		t.Fatalf("post: %v", err)
	}
	// As before migration 0027: the lines are in the journal, not in the projection.
	if _, err := e.admin.Exec(e.ctx, `delete from balance_quantity where ledger_id = $1`, e.ledger.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.Exec(e.ctx, `update ledger set quantity_rebuild_pending = true where id = $1`, e.ledger.ID); err != nil {
		t.Fatal(err)
	}

	req := realignRequest(e, "RQP-RUN")
	req.Mapping = domain.RealignmentMapping{"DEPT": {"D100": "D150", "D300": "D350"}}
	req.Hash, _ = domain.HashPayload(req.Mapping)
	run, _, err := e.store.CreateRealignment(e.ctx, req)
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	moved := map[string]string{}
	for _, p := range run.Preview {
		for _, q := range p.Quantities {
			moved[p.Period+"/"+p.OldName+"/"+q.Unit] = q.Net
		}
	}
	if want := map[string]string{"2026-02/6010-D100/KG": "7", "2026-02/6010-D300/HC": "2"}; fmt.Sprint(moved) != fmt.Sprint(want) {
		t.Fatalf("quantities planned %v, want %v", moved, want)
	}
	if _, err := e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG")); err != nil {
		t.Fatalf("post: %v", err)
	}
	if _, err := e.store.RebuildBalances(e.ctx, e.ledger.ID); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got, want := qtyByDept(t, e), map[string]string{"2026-02/D150/KG": "7.000", "2026-02/D350/HC": "2.000"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("quantities after the run and the backfill %v, want %v", got, want)
	}
}
