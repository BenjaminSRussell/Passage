package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// --- quarantine release (FR-49.11) ---

// loadedWithQuarantine runs the open-items suite and reconciliation the way the API does, with
// every accepted record loaded, and returns the run and its quarantine.
func loadedWithQuarantine(t *testing.T, records []Record, parties ...string) (*PackRun, []Quarantined) {
	t.Helper()
	known := map[string]struct{}{}
	for _, p := range parties {
		known[p] = struct{}{}
	}
	accepted, quarantined := Validate(records, known)
	r := validatedRun(t)
	r.Pack, r.RecordsIn, r.RecordsQuarantined = PackOpenItems, len(records), len(quarantined)
	present := map[string]decimal.Decimal{}
	for _, rec := range accepted {
		present[OpenItemKey(rec.Side(), rec.LegacyID)] = rec.Amount
	}
	if err := ReconcileOpenItems(r, records, accepted, len(accepted), present); err != nil {
		t.Fatal(err)
	}
	return r, quarantined
}

func TestReleaseOpenItem_CorrectedPartyReconcilesTheRun(t *testing.T) {
	records := []Record{
		{LegacyID: "1", Party: "V1", Account: "2100", Currency: "USD", Amount: d("10.00")},
		{LegacyID: "2", Party: "V9", Account: "2100", Currency: "USD", Amount: d("2.50")},
	}
	r, q := loadedWithQuarantine(t, records, "V1")
	if r.Status != StatusLoaded || len(q) != 1 {
		t.Fatalf("status %s quarantine %+v", r.Status, q)
	}
	parties := map[string]struct{}{"V1": {}}
	if _, err := ReleaseOpenItem(r, q[0].Record, Correction{}, parties); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), RuleReferential) {
		t.Fatalf("an uncorrected record still fails the referential rule, got %v", err)
	}
	if r.Status != StatusLoaded || r.RecordsQuarantined != 1 {
		t.Fatalf("a refused release changes nothing: %+v", r)
	}
	rec, err := ReleaseOpenItem(r, q[0].Record, Correction{Party: " V1 "}, parties)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Party != "V1" || !rec.Amount.Equal(d("2.50")) {
		t.Fatalf("released %+v", rec)
	}
	if r.Status != StatusReconciled || r.RecordsLoaded != 2 || r.RecordsQuarantined != 0 {
		t.Fatalf("run %+v", r)
	}
	for _, tot := range r.Totals {
		if tot.Status != StatusMatch {
			t.Fatalf("total %+v", tot)
		}
	}
}

// A corrected account moves the record's legacy amount to the corrected subtotal, and the
// per-account totals still sum to the per-currency total on both sides.
func TestReleaseOpenItem_CorrectedAccountMovesTheSubtotal(t *testing.T) {
	records := []Record{
		{LegacyID: "1", Party: "V1", Account: "2100", Currency: "USD", Amount: d("10.00")},
		{LegacyID: "2", Party: "V1", Account: "", Currency: "USD", Amount: d("4.00")},
	}
	r, q := loadedWithQuarantine(t, records, "V1")
	if len(q) != 1 || q[0].Rule != RuleDomain {
		t.Fatalf("quarantine %+v", q)
	}
	if _, err := ReleaseOpenItem(r, q[0].Record, Correction{Account: "2100"}, map[string]struct{}{"V1": {}}); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusReconciled {
		t.Fatalf("status %s totals %+v", r.Status, r.Totals)
	}
	var source, target decimal.Decimal
	for _, tot := range r.Totals {
		if tot.Name != "open_items.amount_by_account" {
			continue
		}
		source, target = source.Add(tot.Source), target.Add(tot.Target)
		if tot.Key["account"] == "2100" && (!tot.Source.Equal(d("14")) || !tot.Target.Equal(d("14"))) {
			t.Fatalf("2100 subtotal %+v", tot)
		}
		if tot.Key["account"] == "" && (!tot.Source.IsZero() || !tot.Target.IsZero()) {
			t.Fatalf("blank-account subtotal keeps no amount: %+v", tot)
		}
	}
	if !source.Equal(d("14")) || !target.Equal(d("14")) {
		t.Fatalf("per-account totals sum to %s / %s", source, target)
	}
}

func TestReleaseOpenItem_Refusals(t *testing.T) {
	rec := Record{LegacyID: "1", Party: "V1", Account: "2100", Currency: "USD", Amount: d("1")}
	parties := map[string]struct{}{"V1": {}}
	t.Run("only LOADED runs", func(t *testing.T) {
		for _, st := range []RunStatus{StatusValidated, StatusReconciled, StatusRolledBack} {
			r := &PackRun{Pack: PackOpenItems, Status: st}
			if _, err := ReleaseOpenItem(r, rec, Correction{}, parties); !errors.Is(err, ErrTransition) {
				t.Fatalf("%s: got %v", st, err)
			}
		}
	})
	t.Run("only open_items", func(t *testing.T) {
		r := &PackRun{Pack: PackProjectWIP, Status: StatusLoaded}
		if _, err := ReleaseOpenItem(r, rec, Correction{}, parties); !errors.Is(err, ErrUnknownPack) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("currency is not correctable", func(t *testing.T) {
		r := &PackRun{Pack: PackOpenItems, Status: StatusLoaded, RecordsQuarantined: 1}
		bad := rec
		bad.Currency = "XXX"
		if _, err := ReleaseOpenItem(r, bad, Correction{Party: "V1", Account: "2100"}, parties); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "ISO 4217") {
			t.Fatalf("got %v", err)
		}
	})
}

// Property: however many quarantined records an operator fixes, in any order, the run reaches
// RECONCILED exactly when the last one is released, and the totals then match.
func TestReleaseOpenItem_ReleasingEveryRecordReconciles(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(rt, "n")
		bad := rapid.IntRange(1, n).Draw(rt, "bad")
		var records []Record
		for i := 0; i < n; i++ {
			party := "V1"
			if i < bad {
				party = "MISSING"
			}
			cents := rapid.Int64Range(-100000, 100000).Draw(rt, fmt.Sprintf("amt%d", i))
			records = append(records, Record{LegacyID: fmt.Sprint("R", i), Party: party, Account: "2100", Currency: "EUR", Amount: decimal.New(cents, -2)})
		}
		known := map[string]struct{}{"V1": {}}
		accepted, quarantined := Validate(records, known)
		r := &PackRun{SourceCompany: "1000", Status: StatusValidated, Pack: PackOpenItems, RecordsIn: n, RecordsQuarantined: len(quarantined)}
		present := map[string]decimal.Decimal{}
		for _, rec := range accepted {
			present[OpenItemKey(rec.Side(), rec.LegacyID)] = rec.Amount
		}
		if err := ReconcileOpenItems(r, records, accepted, len(accepted), present); err != nil {
			rt.Fatal(err)
		}
		order := rapid.Permutation(quarantined).Draw(rt, "order")
		for i, q := range order {
			if r.Status != StatusLoaded {
				rt.Fatalf("release %d: run is %s before the last release", i, r.Status)
			}
			if _, err := ReleaseOpenItem(r, q.Record, Correction{Party: "V1"}, known); err != nil {
				rt.Fatal(err)
			}
		}
		if r.Status != StatusReconciled || r.RecordsLoaded != n || r.RecordsQuarantined != 0 || r.HasMismatch() {
			rt.Fatalf("after every release: %+v", r)
		}
	})
}

// --- parallel-run review (FR-49.5) ---

func seqIDs() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("v%d", n) }
}

func parallelInput() ParallelInput {
	return ParallelInput{
		Period: "2026-11", SourceSystem: SourceAX2012, DocumentsReplayed: 42000, Revenue: d("1000000"), ExplainedMaxPct: DefaultExplainedMaxPct,
		Legacy: []Line{
			{Area: "TB", Key: "USMF|4000|USD|2026-11", Legacy: d("500000.00")},
			{Area: "TB", Key: "USMF|1100|USD|2026-11", Legacy: d("120.00")},
			{Area: "AR", Key: "CIV-00123", Legacy: d("99.99")},
		},
		Passage: []Line{
			{Area: "TB", Key: "USMF|4000|USD|2026-11", Passage: d("500000.01")},
			{Area: "TB", Key: "USMF|1100|USD|2026-11", Passage: d("150.00")},
			{Area: "AR", Key: "CIV-00123", Passage: d("99.99")},
		},
	}
}

func TestStartParallelRun_DiffsAndClassifies(t *testing.T) {
	run, err := StartParallelRun("p1", parallelInput(), DefaultRules(), "alice", seqIDs())
	if err != nil {
		t.Fatal(err)
	}
	if run.State != ParallelReview || run.Version != 1 || run.StartedBy != "alice" {
		t.Fatalf("run %+v", run)
	}
	if len(run.Variances) != 2 {
		t.Fatalf("equal AR tuples are not variances: %+v", run.Variances)
	}
	byKey := map[string]ParallelVariance{}
	for _, v := range run.Variances {
		byKey[v.Key] = v
	}
	if v := byKey["USMF|4000|USD|2026-11"]; v.Class != ClassExpected || v.RuleID != RuleRND001 {
		t.Fatalf("0.01 rounding is EXPECTED by RND-001 (BR-49.7): %+v", v)
	}
	if v := byKey["USMF|1100|USD|2026-11"]; v.Class != ClassUnexplained || !v.Delta.Equal(d("30")) {
		t.Fatalf("30.00 is UNEXPLAINED: %+v", v)
	}
	if run.Summary.UnexplainedCount != 1 || !run.Summary.UnexplainedAmount.Equal(d("30")) || run.Summary.ByRule[RuleRND001] != 1 {
		t.Fatalf("summary %+v", run.Summary)
	}
}

func TestParallelInput_Validate(t *testing.T) {
	cases := map[string]func(*ParallelInput){
		"period not YYYY-MM": func(in *ParallelInput) { in.Period = "2026-1" },
		"month 13":           func(in *ParallelInput) { in.Period = "2026-13" },
		"negative documents": func(in *ParallelInput) { in.DocumentsReplayed = -1 },
		"negative revenue":   func(in *ParallelInput) { in.Revenue = d("-1") },
		"no tuples":          func(in *ParallelInput) { in.Legacy, in.Passage = nil, nil },
		"blank key":          func(in *ParallelInput) { in.Passage[0].Key = " " },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := parallelInput()
			mutate(&in)
			if _, err := StartParallelRun("p", in, DefaultRules(), "alice", seqIDs()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestParallelInput_HashIgnoresTupleOrder(t *testing.T) {
	a := parallelInput()
	b := parallelInput()
	b.Legacy[0], b.Legacy[2] = b.Legacy[2], b.Legacy[0]
	if a.Hash() != b.Hash() {
		t.Fatal("same tuples in another order must hash the same")
	}
	b.Passage[1].Passage = d("150.01")
	if a.Hash() == b.Hash() {
		t.Fatal("a changed amount must change the hash")
	}
}

func TestParseExplainedMaxPct(t *testing.T) {
	if v, err := ParseExplainedMaxPct(""); err != nil || !v.Equal(d("0.1")) {
		t.Fatalf("blank is the 0.1 default: %v %v", v, err)
	}
	if v, err := ParseExplainedMaxPct(" 0.5 "); err != nil || !v.Equal(d("0.5")) {
		t.Fatalf("got %v %v", v, err)
	}
	for _, bad := range []string{"-0.1", "1.01", "abc"} {
		if _, err := ParseExplainedMaxPct(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: got %v", bad, err)
		}
	}
}

func TestParallelRun_ExplainAndSignOff(t *testing.T) {
	at := time.Date(2026, 12, 3, 9, 0, 0, 0, time.UTC)
	newRun := func(t *testing.T) (ParallelRun, string) {
		t.Helper()
		run, err := StartParallelRun("p1", parallelInput(), DefaultRules(), "alice", seqIDs())
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range run.Variances {
			if v.Class == ClassUnexplained {
				return run, v.ID
			}
		}
		t.Fatal("no unexplained variance")
		return run, ""
	}
	t.Run("sign-off is blocked by an unexplained variance (MIG-1509)", func(t *testing.T) {
		run, _ := newRun(t)
		if err := run.SignOff("bob", at); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "MIG-1509") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("the person who started the run cannot review it (MIG-001)", func(t *testing.T) {
		run, vid := newRun(t)
		if _, err := run.Explain(vid, "cut-off timing", "alice", at); !errors.Is(err, ErrSoD) {
			t.Fatalf("explain: got %v", err)
		}
		if err := run.SignOff("alice", at); !errors.Is(err, ErrSoD) {
			t.Fatalf("sign-off: got %v", err)
		}
		if _, err := run.Explain(vid, "cut-off timing", " ", at); !errors.Is(err, ErrSoD) {
			t.Fatalf("anonymous: got %v", err)
		}
	})
	t.Run("explain then sign off", func(t *testing.T) {
		run, vid := newRun(t)
		changed, err := run.Explain(vid, "  cut-off timing on invoice 7781 ", "bob", at)
		if err != nil || !changed {
			t.Fatalf("explain: %v %v", changed, err)
		}
		if run.Version != 2 || run.Summary.UnexplainedCount != 0 || !run.Summary.ExplainedAmount.Equal(d("30")) {
			t.Fatalf("after explain %+v", run.Summary)
		}
		changed, err = run.Explain(vid, "cut-off timing on invoice 7781", "carol", at)
		if err != nil || changed || run.Version != 2 {
			t.Fatalf("the same reason again is a no-op: %v %v v%d", changed, err, run.Version)
		}
		if _, err := run.Explain(vid, "another reason", "bob", at); !errors.Is(err, ErrTransition) {
			t.Fatalf("another reason: got %v", err)
		}
		if err := run.SignOff("bob", at); err != nil {
			t.Fatal(err)
		}
		if run.State != ParallelSignedOff || run.SignedOffBy != "bob" || run.SignedOffAt == nil || run.Version != 3 {
			t.Fatalf("signed off %+v", run)
		}
		if _, err := run.Explain(vid, "late", "bob", at); !errors.Is(err, ErrTransition) {
			t.Fatalf("explain after sign-off: got %v", err)
		}
		if err := run.SignOff("bob", at); !errors.Is(err, ErrTransition) {
			t.Fatalf("second sign-off: got %v", err)
		}
	})
	t.Run("explanation refusals", func(t *testing.T) {
		run, vid := newRun(t)
		var expected string
		for _, v := range run.Variances {
			if v.Class == ClassExpected {
				expected = v.ID
			}
		}
		if _, err := run.Explain(expected, "rounding", "bob", at); !errors.Is(err, ErrTransition) {
			t.Fatalf("EXPECTED variance: got %v", err)
		}
		if _, err := run.Explain("nope", "x", "bob", at); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown variance: got %v", err)
		}
		if _, err := run.Explain(vid, "  ", "bob", at); !errors.Is(err, ErrInvalid) {
			t.Fatalf("blank reason: got %v", err)
		}
		if _, err := run.Explain(vid, strings.Repeat("é", 2000), "bob", at); err != nil {
			t.Fatalf("2000 characters is the limit, not 2000 bytes: %v", err)
		}
		run2, vid2 := newRun(t)
		if _, err := run2.Explain(vid2, strings.Repeat("x", 2001), "bob", at); !errors.Is(err, ErrInvalid) {
			t.Fatalf("2001 characters: got %v", err)
		}
	})
	t.Run("explained amount above the revenue limit blocks sign-off", func(t *testing.T) {
		in := parallelInput()
		in.Revenue = d("10000") // 0.1 % is 10.00; the explained variance is 30.00
		run, err := StartParallelRun("p1", in, DefaultRules(), "alice", seqIDs())
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range run.Variances {
			if v.Class == ClassUnexplained {
				if _, err := run.Explain(v.ID, "known", "bob", at); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := run.SignOff("bob", at); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("got %v", err)
		}
		if run.State != ParallelReview {
			t.Fatalf("state %s", run.State)
		}
	})
}

// --- project beginning balances (FR-49.12) ---

var cutoff = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)

func hourLine(id, project string) OpeningLine {
	return OpeningLine{LegacyID: id, ProjectID: project, Category: CategoryHour, TransactionDate: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
		Qty: d("8"), CostAmount: d("400"), SalesAmount: d("800"), Currency: "USD", BillingStatus: BillingChargeableUnbilled,
		WIPCost: d("400"), WIPSales: d("800")}
}

func TestValidateOpeningLines_Rules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*OpeningLine)
		rule   string
	}{
		{"blank legacy id", func(l *OpeningLine) { l.LegacyID = " " }, RuleUniqueness},
		{"blank project", func(l *OpeningLine) { l.ProjectID = "" }, RuleReferential},
		{"currency not ISO 4217", func(l *OpeningLine) { l.Currency = "usd" }, RuleDomain},
		{"missing date", func(l *OpeningLine) { l.TransactionDate = time.Time{} }, RuleDomain},
		{"dated after the cutoff", func(l *OpeningLine) { l.TransactionDate = cutoff.AddDate(0, 0, 1) }, RuleDomain},
		{"on-account line not billed", func(l *OpeningLine) { l.Category = CategoryOnAccount }, RuleDomain},
		{"non-chargeable line with WIP sales", func(l *OpeningLine) { l.BillingStatus = BillingNonChargeable }, RuleDomain},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := hourLine("PT-1", "PRJ-1")
			c.mutate(&l)
			accepted, issues := ValidateOpeningLines([]OpeningLine{l, hourLine("PT-2", "PRJ-1")}, cutoff)
			if len(accepted) != 1 || accepted[0].LegacyID != "PT-2" || len(issues) != 1 || issues[0].Rule != c.rule {
				t.Fatalf("accepted %d issues %+v", len(accepted), issues)
			}
		})
	}
	t.Run("duplicate legacy id keeps the first", func(t *testing.T) {
		accepted, issues := ValidateOpeningLines([]OpeningLine{hourLine("PT-1", "PRJ-1"), hourLine("PT-1", "PRJ-2")}, cutoff)
		if len(accepted) != 1 || accepted[0].ProjectID != "PRJ-1" || len(issues) != 1 || issues[0].Rule != RuleUniqueness {
			t.Fatalf("accepted %+v issues %+v", accepted, issues)
		}
	})
	t.Run("a line dated on the cutoff loads", func(t *testing.T) {
		l := hourLine("PT-1", "PRJ-1")
		l.TransactionDate = cutoff
		if accepted, _ := ValidateOpeningLines([]OpeningLine{l}, cutoff); len(accepted) != 1 {
			t.Fatal("cutoff day refused")
		}
	})
}

func TestProjectTotals_PerCategoryAndMeasure(t *testing.T) {
	lines := []OpeningLine{hourLine("PT-1", "PRJ-1"), hourLine("PT-2", "PRJ-1"), hourLine("PT-3", "PRJ-2")}
	legacy := []LegacyCategoryTotal{{Category: CategoryHour, Cost: d("800"), Sales: d("1600"), WIPCost: d("800"), WIPSales: d("1600")}}
	totals, err := ProjectTotals("PRJ-1", lines, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 4 || ProjectStatus(totals) != ProjectReconciled {
		t.Fatalf("lines of another project must not count: %+v", totals)
	}
	for i, name := range []string{"project_wip.cost", "project_wip.sales", "project_wip.wip_cost", "project_wip.wip_sales"} {
		if totals[i].Name != name || totals[i].Key["project"] != "PRJ-1" || totals[i].Key["category"] != "HOUR" {
			t.Fatalf("total %d %+v", i, totals[i])
		}
	}
	// A category only legacy has is a mismatch against zero (BR-49.12).
	legacy = append(legacy, LegacyCategoryTotal{Category: CategoryExpense, Cost: d("12.50")})
	totals, err = ProjectTotals("PRJ-1", lines, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 8 || ProjectStatus(totals) != ProjectOpeningMismatch {
		t.Fatalf("totals %+v", totals)
	}
	var cost ControlTotal
	for _, tot := range totals {
		if tot.Name == "project_wip.cost" && tot.Key["category"] == "EXPENSE" {
			cost = tot
		}
	}
	if cost.Status != StatusMismatch || !cost.Delta.Equal(d("-12.5")) {
		t.Fatalf("expense cost %+v", cost)
	}
}

// AC-49.11 shape: 120 chargeable unbilled hour lines reconcile to the legacy HOUR totals, and a
// re-run that finds them all present loads nothing and still reconciles.
func TestReconcileProjects_HundredTwentyHourLines(t *testing.T) {
	var lines []OpeningLine
	for i := 0; i < 120; i++ {
		lines = append(lines, hourLine(fmt.Sprintf("PT-%03d", i), "PRJ-TM"))
	}
	legacy := map[string][]LegacyCategoryTotal{"PRJ-TM": {{Category: CategoryHour, Cost: d("48000"), Sales: d("96000"), WIPCost: d("48000"), WIPSales: d("96000")}}}
	for _, rerun := range []bool{false, true} {
		r := validatedRun(t)
		loaded := 120
		if rerun {
			loaded = 0
		}
		if err := ReconcileProjects(r, 120, loaded, []string{"PRJ-TM"}, lines, legacy); err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusReconciled || r.RecordsLoaded != loaded || r.AlreadyPresent != 120-loaded {
			t.Fatalf("rerun=%v run %+v", rerun, r)
		}
		if got := ProjectsOf(r.Totals); fmt.Sprint(got) != "[PRJ-TM]" {
			t.Fatalf("projects %v", got)
		}
	}
	t.Run("quarantined lines keep the run LOADED", func(t *testing.T) {
		r := validatedRun(t)
		r.RecordsQuarantined = 1
		if err := ReconcileProjects(r, 119, 119, []string{"PRJ-TM"}, lines[:119], legacy); err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusLoaded || ProjectStatus(TotalsOfProject(r.Totals, "PRJ-TM")) != ProjectOpeningMismatch {
			t.Fatalf("run %s project %s", r.Status, ProjectStatus(TotalsOfProject(r.Totals, "PRJ-TM")))
		}
	})
	t.Run("needs a validated run", func(t *testing.T) {
		r := &PackRun{Status: StatusLoaded}
		if err := ReconcileProjects(r, 0, 0, nil, nil, nil); !errors.Is(err, ErrTransition) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestParseCategoryAndBilling(t *testing.T) {
	for _, c := range Categories {
		if got, err := ParseCategory(string(c)); err != nil || got != c {
			t.Fatalf("%s: %v", c, err)
		}
	}
	if _, err := ParseCategory("hour"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("lower case: %v", err)
	}
	if _, err := ParseBilling("INVOICED"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown billing: %v", err)
	}
	if _, err := ParseArea("GL"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown area: %v", err)
	}
}
