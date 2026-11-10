package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func stagedRun(p Pack) PackRun {
	r := PackRun{RunID: "r", Pack: p, SourceCompany: "1000", Status: StatusExtracted}
	_ = r.Advance(StatusValidated)
	return r
}

func heldStaged(in []StagedLine) map[string]StagedLine {
	out := map[string]StagedLine{}
	for _, l := range in {
		out[l.LegacyID] = l
	}
	return out
}

// D1a, BR-49.6: a fixed asset whose recomputed next depreciation is more than 0.01 off the legacy
// projection is quarantined with MIG-1507; straight line and declining balance both recompute.
func TestFixedAssets_NextDepreciationWithinOneCent(t *testing.T) {
	sl := FixedAssetLine{LegacyID: "A1", Asset: "PRESS-1", Book: "GAAP", Currency: "USD", Method: DepStraightLine, UsefulLifeMonths: 120,
		RemainingLifeMonths: 60, AcquisitionCost: d("120000"), AccumulatedDepreciation: d("60000"), NextDepreciation: d("1000.00")}
	db := FixedAssetLine{LegacyID: "A2", Asset: "VAN-1", Book: "TAX", Currency: "USD", Method: DepDecliningBalance, UsefulLifeMonths: 60,
		RemainingLifeMonths: 30, AcquisitionCost: d("50000"), AccumulatedDepreciation: d("20000"), NextDepreciation: d("1000.01")}
	off := sl
	off.LegacyID, off.Asset, off.NextDepreciation = "A3", "PRESS-2", d("1000.02")
	if got := db.ProjectNextDepreciation(); !got.Equal(d("1000")) {
		t.Fatalf("declining balance next depreciation %s, want 1000", got)
	}
	acc, q := ValidateFixedAssets([]FixedAssetLine{sl, db, off}, map[string]struct{}{"GAAP": {}, "TAX": {}})
	if len(acc) != 2 || len(q) != 1 || q[0].Record.LegacyID != "A3" || !strings.HasPrefix(q[0].Reason, MsgNextDepreciation) {
		t.Fatalf("accepted %d quarantined %+v", len(acc), q)
	}
	run := stagedRun(PackFixedAssets)
	all := []StagedLine{sl.Staged(), db.Staged(), off.Staged()}
	if err := ReconcileStaged(&run, FixedAssetsSpec, all, acc, 2, heldStaged(acc)); err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusLoaded || !run.HasMismatch() {
		t.Fatalf("quarantined asset must keep the run LOADED: %s", run.Status)
	}
	clean := stagedRun(PackFixedAssets)
	if err := ReconcileStaged(&clean, FixedAssetsSpec, acc, acc, 2, heldStaged(acc)); err != nil || clean.Status != StatusReconciled {
		t.Fatalf("clean run %s %v", clean.Status, err)
	}
	names := map[string]int{}
	for _, c := range clean.Totals {
		names[c.Name]++
		if c.Name == "fixed_assets.nbv" && c.Key["book"] == "GAAP" && !c.Source.Equal(d("60000")) {
			t.Fatalf("GAAP nbv %s", c.Source)
		}
	}
	if names["fixed_assets.count"] != 2 || names["fixed_assets.nbv"] != 2 || names["fixed_assets.acquisition_cost"] != 2 {
		t.Fatalf("totals %v", names)
	}
}

func TestFixedAssets_DomainRules(t *testing.T) {
	base := FixedAssetLine{LegacyID: "A", Asset: "X", Book: "GAAP", Currency: "USD", Method: DepStraightLine, UsefulLifeMonths: 12, RemainingLifeMonths: 12,
		AcquisitionCost: d("1200"), NextDepreciation: d("100")}
	cases := map[string]func(*FixedAssetLine){
		"book":    func(a *FixedAssetLine) { a.Book = "IFRS" },
		"method":  func(a *FixedAssetLine) { a.Method = "SYD" },
		"accum":   func(a *FixedAssetLine) { a.AccumulatedDepreciation = d("1300") },
		"salvage": func(a *FixedAssetLine) { a.SalvageValue = d("1500") },
		"life":    func(a *FixedAssetLine) { a.RemainingLifeMonths = 13 },
	}
	for name, mut := range cases {
		a := base
		mut(&a)
		if acc, q := ValidateFixedAssets([]FixedAssetLine{a}, map[string]struct{}{"GAAP": {}}); len(acc) != 0 || len(q) != 1 {
			t.Fatalf("%s: accepted %d", name, len(acc))
		}
	}
	if acc, _ := ValidateFixedAssets([]FixedAssetLine{base}, map[string]struct{}{"GAAP": {}}); len(acc) != 1 {
		t.Fatal("base asset refused")
	}
}

func TestWIPProd_RulesAndTotals(t *testing.T) {
	lines := []WIPProdLine{
		{LegacyID: "W1", ProductionOrder: "PO-1", Item: "FG-1", Currency: "EUR", Material: d("100"), Labor: d("40"), Overhead: d("10")},
		{LegacyID: "W2", ProductionOrder: "PO-2", Item: "FG-1", Currency: "EUR", Material: d("5")},
		{LegacyID: "W3", ProductionOrder: "PO-3", Item: "GONE", Currency: "EUR", Material: d("5")},
		{LegacyID: "W4", ProductionOrder: "PO-4", Item: "FG-1", Currency: "EUR", Labor: d("-1")},
		{LegacyID: "W5", ProductionOrder: "PO-1", Item: "FG-1", Currency: "EUR"},
	}
	acc, q := ValidateWIPProd(lines, map[string]struct{}{"FG-1": {}})
	if len(acc) != 2 || len(q) != 3 || q[0].Rule != RuleReferential || q[1].Rule != RuleDomain || q[2].Rule != RuleUniqueness {
		t.Fatalf("accepted %d quarantined %+v", len(acc), q)
	}
	run := stagedRun(PackWIPProd)
	if err := ReconcileStaged(&run, WIPProdSpec, acc, acc, 2, heldStaged(acc)); err != nil || run.Status != StatusReconciled {
		t.Fatalf("%s %v", run.Status, err)
	}
	for _, c := range run.Totals {
		if c.Name == "wip_prod.wip_entity" && !c.Target.Equal(d("155")) {
			t.Fatalf("entity WIP %s, want 155", c.Target)
		}
		if c.Name == "wip_prod.order_count" && !c.Source.Equal(d("2")) {
			t.Fatalf("order count %s", c.Source)
		}
	}
	// A held line that differs from the extract (the run before stored another value) mismatches.
	held := heldStaged(acc)
	h := held["W1"]
	h.Measures = map[string]decimal.Decimal{"wip": d("149")}
	held["W1"] = h
	again := stagedRun(PackWIPProd)
	if err := ReconcileStaged(&again, WIPProdSpec, acc, acc, 0, held); err != nil || again.Status != StatusLoaded {
		t.Fatalf("changed held line must mismatch: %s %v", again.Status, err)
	}
}

func TestPayrollYTD_NetMustTie(t *testing.T) {
	ok := PayrollYTDLine{LegacyID: "P1", Worker: "E1", TaxYear: "2026", Currency: "USD", Gross: d("90000"), Tax: d("20000"), Deductions: d("5000"), Net: d("65000")}
	bad := ok
	bad.LegacyID, bad.Worker, bad.Net = "P2", "E2", d("65000.01")
	yen := PayrollYTDLine{LegacyID: "P3", Worker: "E3", TaxYear: "2026", Currency: "JPY", Gross: d("100.5"), Net: d("100.5")}
	year := ok
	year.LegacyID, year.Worker, year.TaxYear = "P4", "E4", "26"
	acc, q := ValidatePayrollYTD([]PayrollYTDLine{ok, bad, yen, year}, map[string]struct{}{"E1": {}, "E2": {}, "E3": {}, "E4": {}})
	if len(acc) != 1 || len(q) != 3 {
		t.Fatalf("accepted %d quarantined %+v", len(acc), q)
	}
	if !strings.Contains(q[0].Reason, "gross - tax - deductions") || !strings.Contains(q[1].Reason, "decimal places of JPY") || !strings.Contains(q[2].Reason, "YYYY") {
		t.Fatalf("reasons %+v", q)
	}
	if HashStaged(PayrollYTDSpec, []StagedLine{ok.Staged()}) == HashStaged(PayrollYTDSpec, []StagedLine{bad.Staged()}) {
		t.Fatal("hash ignores the measures")
	}
}

func TestCatalog_AllNinePacksAvailableAndStagedOnesNotPosted(t *testing.T) {
	for _, p := range Packs {
		if !PackAvailable(p) {
			t.Fatalf("pack %s not available", p)
		}
	}
	for _, p := range []Pack{PackWIPProd, PackFixedAssets, PackPayrollYTD} {
		if NotPosted(p) == "" {
			t.Fatalf("pack %s must be staged", p)
		}
		if _, ok := StagedSpecFor(p); !ok {
			t.Fatalf("pack %s has no spec", p)
		}
	}
}

func rekey(entity, doc string, line int, account, amount string) RekeyPosting {
	return RekeyPosting{Kind: KindJournal, Entity: entity, DocumentRef: doc, Line: line, PostedAt: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
		Account: account, Currency: "USD", Amount: d(amount)}
}

// D9: each re-keying journal gets one compensation with a fixed key; its entry flips every line,
// and the reconciliation counts only this plan's journals.
func TestCompensation_EntryFlipsLinesAndReconciles(t *testing.T) {
	journals := []RekeyPosting{rekey("US01", "JE-1", 1, "1100", "250"), rekey("US01", "JE-1", 2, "4000", "-250"),
		rekey("US01", "JE-2", 1, "6000", "80"), rekey("US01", "JE-2", 2, "2000", "-80")}
	comps := PlanCompensations("plan-1", journals)
	if len(comps) != 2 || comps[0].Key != CompensationKey("plan-1", "US01", "JE-1", "USD") || comps[0].Key == comps[1].Key {
		t.Fatalf("compensations %+v", comps)
	}
	if again := PlanCompensations("plan-1", journals); again[0].Key != comps[0].Key {
		t.Fatal("key is not stable")
	}
	e, err := CompensationEntry(comps[0], journals, "le-1", "ledger-1", "2026-07-03")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Lines) != 2 || !e.Lines[0].Credit || e.Lines[0].Account != "1100" || !e.Lines[0].Amount.Equal(d("250")) || e.Lines[1].Credit {
		t.Fatalf("entry lines %+v", e.Lines)
	}
	if e.IdempotencyKey != comps[0].Key || e.SourceType != CompensationSourceType || e.LegalEntityID != "le-1" || e.PostingDate != "2026-07-03" {
		t.Fatalf("entry %+v", e)
	}
	if _, err := e.Rounded(); err != nil {
		t.Fatalf("entry does not balance for the ledger: %v", err)
	}
	comps[0].Status = CompensationPosted
	totals, err := ReconcileCompensations(journals, comps)
	if err != nil {
		t.Fatal(err)
	}
	mismatch := 0
	for _, c := range totals {
		if c.Status == StatusMismatch {
			mismatch++
		}
	}
	if len(totals) != 4 || mismatch != 2 {
		t.Fatalf("totals %d mismatch %d", len(totals), mismatch)
	}
}

func TestCompensation_BackoffAndPark(t *testing.T) {
	now := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	c := Compensation{Status: CompensationPending}
	for i := 1; i < MaxCompensationAttempts; i++ {
		FailCompensation(&c, "TIMEOUT", false, now)
		if c.Status != CompensationPending {
			t.Fatalf("parked after %d transient failures", i)
		}
	}
	if got := c.NextAttemptAt.Sub(now); got != 8*time.Minute {
		t.Fatalf("backoff after 4 attempts %s", got)
	}
	FailCompensation(&c, "TIMEOUT", false, now)
	if c.Status != CompensationParked {
		t.Fatal("not parked at the attempt limit")
	}
	r := Compensation{Status: CompensationPending}
	FailCompensation(&r, "PERIOD_CLOSED", true, now)
	if r.Status != CompensationParked || r.LastCode != "PERIOD_CLOSED" {
		t.Fatalf("rejection must park at once: %+v", r)
	}
	if CompensationBackoff(20) != time.Hour {
		t.Fatal("backoff is not capped at an hour")
	}
}

func TestReplay_ValidateOrderAndTrialBalance(t *testing.T) {
	docs := []ReplayDocument{
		{Seq: 2, DocumentID: "GJ-2", PostingDate: "2026-03-05", Currency: "USD", Lines: []ReplayLine{{Account: "6000", Amount: d("10")}, {Account: "2000", Amount: d("-10")}}},
		{Seq: 1, DocumentID: "GJ-1", PostingDate: "2026-03-05", Currency: "USD", Lines: []ReplayLine{{Account: "1100", Amount: d("100")}, {Account: "4000", Amount: d("-100")}}},
		{Seq: 3, DocumentID: "GJ-3", PostingDate: "2026-03-01", Currency: "USD", Lines: []ReplayLine{{Account: "1100", Amount: d("5")}, {Account: "4000", Amount: d("-5")}}},
	}
	if err := ValidateReplay("2026-03", docs); err != nil {
		t.Fatal(err)
	}
	order := OrderReplay(docs)
	if order[0].DocumentID != "GJ-3" || order[1].DocumentID != "GJ-1" || order[2].DocumentID != "GJ-2" {
		t.Fatalf("order %s %s %s", order[0].DocumentID, order[1].DocumentID, order[2].DocumentID)
	}
	tb := TBLines(TrialBalanceFromDocuments(docs), "2026-03", "legacy", nil)
	if len(tb) != 4 || tb[0].Key != "1100|USD|2026-03" || !tb[0].Legacy.Equal(d("105")) {
		t.Fatalf("tb %+v", tb)
	}
	bad := []ReplayDocument{{Seq: 1, DocumentID: "X", PostingDate: "2026-04-01", Currency: "USD", Lines: docs[0].Lines}}
	if err := ValidateReplay("2026-03", bad); err == nil {
		t.Fatal("document outside the period accepted")
	}
	unbalanced := []ReplayDocument{{Seq: 1, DocumentID: "X", PostingDate: "2026-03-01", Currency: "USD", Lines: []ReplayLine{{Account: "1", Amount: d("1")}, {Account: "2", Amount: d("-2")}}}}
	if err := ValidateReplay("2026-03", unbalanced); err == nil {
		t.Fatal("unbalanced document accepted")
	}
	if ReplayKey(SourceAX2012, "2026-03", "GJ-1") == ReplayKey(SourceSAPECC, "2026-03", "GJ-1") {
		t.Fatal("replay key ignores the system")
	}
}

// BR-49.10: a cut-over entity refuses legacy changes made at or after its cutover, a known legacy
// id matches its Passage record, and a batch keeps only the latest change of a record.
func TestCoexistence_RouteInbound(t *testing.T) {
	cut := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	f := CoexistFlag{SourceSystem: SourceAX2012, Company: "USMF", Entity: "customer", Mode: CoexistLegacy}
	if err := f.SetMode(CoexistLegacy, "u", cut); err == nil {
		t.Fatal("setting the current mode must be refused")
	}
	if err := f.SetMode(CoexistPassage, "", cut); err == nil {
		t.Fatal("an unsigned change must be refused")
	}
	if err := f.SetMode(CoexistPassage, "mgr", cut); err != nil || f.Version != 1 || f.CutoverAt == nil {
		t.Fatalf("cutover %+v %v", f, err)
	}
	in := []InboundRecord{
		{Entity: "customer", SourceTable: "CustTable", LegacyID: "C1", ChangedAt: cut.Add(-time.Hour)},
		{Entity: "customer", SourceTable: "CustTable", LegacyID: "C2", ChangedAt: cut.Add(time.Hour)},
		{Entity: "customer", SourceTable: "CustTable", LegacyID: "C3", ChangedAt: cut.Add(-2 * time.Hour)},
		{Entity: "customer", SourceTable: "CustTable", LegacyID: "C3", ChangedAt: cut.Add(-3 * time.Hour)},
		{Entity: "vendor", SourceTable: "VendTable", LegacyID: "V1", ChangedAt: cut.Add(-time.Hour)},
	}
	got := RouteInbound(f, in, map[string]string{"CustTable|C1": "p-1"})
	want := []string{RouteMatch, RouteRefused, RouteCreate, RouteDupBatch, RouteRefused}
	for i, r := range got {
		if r.Decision != want[i] {
			t.Fatalf("record %d: %s, want %s (%s)", i, r.Decision, want[i], r.Reason)
		}
	}
	if got[0].PassageID != "p-1" || !strings.HasPrefix(got[1].Reason, MsgCoexistCutOver) {
		t.Fatalf("routed %+v", got[:2])
	}
	if err := f.SetMode(CoexistLegacy, "mgr", cut.Add(time.Hour)); err != nil || f.CutoverAt != nil || f.Version != 2 {
		t.Fatalf("rollback flip %+v %v", f, err)
	}
	if r := RouteInbound(f, in[1:2], nil); r[0].Decision != RouteCreate {
		t.Fatalf("after the flip back legacy changes flow again: %s", r[0].Decision)
	}
}
