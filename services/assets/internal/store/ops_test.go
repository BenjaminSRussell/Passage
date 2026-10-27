package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// monthly is a 12-month straight-line book of 1,200.00 from 2026-01-01: 100.00 a month.
func monthly(code string) store.Book {
	return store.Book{BookCode: code, Currency: "USD", Method: "StraightLine", ServiceLifePeriods: 12, Convention: "None",
		InServiceDate: day("2026-01-01"), Cost: d("1200"), SalvageValue: d("0")}
}

func lineCode(r store.DepreciationRun, i int) string {
	if i >= len(r.Lines) || r.Lines[i].ExceptionCode == nil {
		return ""
	}
	return *r.Lines[i].ExceptionCode
}

// TestStore_DepreciationRunLifecycle covers FR-37.5 and AC-37.4 on a real
// database: propose, refresh, post at the reviewed version, rerun rejection
// (AST-0502), the sequential rule (BR-37.3, AST-0503), the prospective charge
// after a revaluation (BR-37.4), the backdated-disposal guard (BR-37.6) and the
// subledger invariant Σ transactions = projections.
func TestStore_DepreciationRunLifecycle(t *testing.T) {
	s, pool, ctx := setup(t)
	le := ids.New()
	asset := newAsset(t, s, ctx, le, "FA-RUN-1")
	bk, err := s.AddBook(ctx, asset.ID, monthly("IFRS"), "b-ifrs")
	if err != nil {
		t.Fatal(err)
	}
	other := newAsset(t, s, ctx, le, "FA-RUN-2")
	if _, err := s.AddBook(ctx, other.ID, monthly("TAX"), "b-tax"); err != nil {
		t.Fatal(err)
	}

	jan := store.RunInput{LegalEntityID: le, BookCode: "IFRS", Period: "2026-01"}
	run, err := s.ProposeRun(ctx, jan, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "Proposed" || run.AssetCount != 1 || run.ExceptionCount != 0 || !run.TotalAmount.Equal(d("100")) || run.Version != 1 ||
		len(run.Lines) != 1 || run.Lines[0].AssetNumber != "FA-RUN-1" || run.Lines[0].Status != "Due" {
		t.Fatalf("proposal = %+v (only the IFRS book of the entity is in the run)", run)
	}
	if replay, err := s.ProposeRun(ctx, jan, "run-1"); err != nil || replay.ID != run.ID || replay.Version != 1 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	refreshed, err := s.ProposeRun(ctx, jan, "run-1b")
	if err != nil || refreshed.ID != run.ID || refreshed.Version != 2 {
		t.Fatalf("refresh = %+v, %v; want the same run at version 2", refreshed, err)
	}
	if _, err := s.PostRun(ctx, run.ID, 1); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("post at the stale version: err = %v", err)
	}
	posted, err := s.PostRun(ctx, run.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if posted.Status != "Posted" || posted.PostedAt == nil || posted.AssetCount != 1 || !posted.TotalAmount.Equal(d("100")) || posted.Lines[0].Status != "Posted" {
		t.Fatalf("posted = %+v", posted)
	}
	if _, err := s.PostRun(ctx, run.ID, posted.Version); problemCode(err) != domain.CodeAlreadyPosted {
		t.Fatalf("post twice: err = %v", err)
	}
	if _, err := s.ProposeRun(ctx, jan, "run-2"); problemCode(err) != domain.CodeAlreadyPosted {
		t.Fatalf("rerun of a posted period: err = %v", err)
	}
	after, err := s.GetBook(ctx, asset.ID, bk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.AccumulatedDepreciation.Equal(d("100")) || !after.NBV.Equal(d("1100")) || after.LastDepreciatedPeriod == nil || *after.LastDepreciatedPeriod != "2026-01" {
		t.Fatalf("book after January = %+v", after)
	}

	// March before February: the sequential rule keeps the book out of the run.
	gap, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "IFRS", Period: "2026-03"}, "run-3")
	if err != nil {
		t.Fatal(err)
	}
	if gap.AssetCount != 0 || gap.ExceptionCount != 1 || lineCode(gap, 0) != domain.CodePreviousUnposted {
		t.Fatalf("gap run = %+v", gap)
	}
	// A yearly period does not touch monthly books.
	if yearly, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "IFRS", Period: "2026"}, "run-4"); err != nil || len(yearly.Lines) != 0 {
		t.Fatalf("yearly run on a monthly book = %+v, %v", yearly, err)
	}

	// Revalue by +110: February spreads (1,310 − 100) over the 11 remaining months.
	if _, err := s.Adjust(ctx, asset.ID, store.AdjustmentInput{BookID: bk.ID, Type: domain.AdjustmentRevaluation, Date: day("2026-02-01"), Amount: d("110")}, "reval"); err != nil {
		t.Fatal(err)
	}
	feb, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "IFRS", Period: "2026-02"}, "run-5")
	if err != nil {
		t.Fatal(err)
	}
	if feb.AssetCount != 1 || !feb.Lines[0].Amount.Equal(d("110")) || !feb.Lines[0].ScheduledAmount.Equal(d("100")) {
		t.Fatalf("February after revaluation = %+v", feb.Lines)
	}
	// A change after the proposal makes the book an exception at posting time.
	if _, err := s.Adjust(ctx, asset.ID, store.AdjustmentInput{BookID: bk.ID, Type: domain.AdjustmentAcquisition, Date: day("2026-02-10"), Amount: d("11")}, "adj-late"); err != nil {
		t.Fatal(err)
	}
	stale, err := s.PostRun(ctx, feb.ID, feb.Version)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != "Posted" || stale.AssetCount != 0 || stale.ExceptionCount != 1 || lineCode(stale, 0) != domain.CodeRunStale {
		t.Fatalf("post after a change = %+v", stale)
	}
	if b, _ := s.GetBook(ctx, asset.ID, bk.ID); b.LastDepreciatedPeriod == nil || *b.LastDepreciatedPeriod != "2026-01" {
		t.Fatalf("the stale book moved: %+v", b)
	}

	// A disposal dated inside the last depreciated period is refused (BR-37.6).
	detail, err := s.GetAsset(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispose(ctx, asset.ID, store.DisposalInput{Version: detail.Version, Kind: domain.DisposalScrap, Percentage: d("100"), Proceeds: d("0"),
		Date: day("2026-01-15")}, "backdated"); problemCode(err) != domain.CodeBackdated {
		t.Fatalf("backdated disposal: err = %v", err)
	}

	txs, err := s.ListTransactions(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := s.GetBook(ctx, asset.ID, bk.ID)
	cost, acc := decimal.Zero, decimal.Zero
	for _, tx := range txs {
		cost, acc = cost.Add(tx.CostAmount), acc.Add(tx.AccumulatedAmount)
		if tx.Type == "Depreciation" && (tx.RunID == nil || *tx.RunID != run.ID || tx.TransactionDate.String() != "2026-01-31") {
			t.Errorf("depreciation transaction = %+v", tx)
		}
	}
	if !cost.Equal(cur.Cost) || !acc.Equal(cur.AccumulatedDepreciation) || !cur.Cost.Equal(d("1321")) {
		t.Fatalf("Σ cost %s / Σ acc %s vs book %s / %s", cost, acc, cur.Cost, cur.AccumulatedDepreciation)
	}
	types := outboxTypes(t, pool, ctx)
	if types["dev.passage.assets.depreciation.posted.v1"] != 2 || types["dev.passage.assets.asset.adjusted.v1"] != 2 {
		t.Fatalf("outbox = %v", types)
	}
	page, err := s.ListRuns(ctx, store.RunFilter{LegalEntityID: le, Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" || page.Items[0].Lines != nil {
		t.Fatalf("run list = %+v, %v", page, err)
	}
	if _, err := s.GetRun(ctx, ids.New()); problemCode(err) != store.CodeRunNotFound {
		t.Fatalf("unknown run: err = %v", err)
	}
}

// TestStore_RunAdvisoryLock holds the (legal entity, book, period) advisory
// lock in another session: a second run request is 409 AST-0504 (BR-37.9).
func TestStore_RunAdvisoryLock(t *testing.T) {
	s, pool, ctx := setup(t)
	le := ids.New()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	key := "assets.depreciation-run/" + testkit.TenantID(1) + "/" + le + "/IFRS/2026-01"
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "IFRS", Period: "2026-01"}, "locked"); problemCode(err) != domain.CodeRunLocked {
		t.Fatalf("run while locked: err = %v", err)
	}
	if _, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "IFRS", Period: "2026-02"}, "other-period"); err != nil {
		t.Fatalf("another period is not locked: %v", err)
	}
}

// TestStore_AdjustmentsAndAdditions covers FR-37.9 impairment and its capped
// reversal (AST-0512), the BR-37.2 state guard, AC-37.12 financial additions on
// every Open book, idempotent replay and the FR-37.14 descriptive additions.
func TestStore_AdjustmentsAndAdditions(t *testing.T) {
	s, pool, ctx := setup(t)
	le := ids.New()
	asset := newAsset(t, s, ctx, le, "FA-ADJ-1")
	ifrs, err := s.AddBook(ctx, asset.ID, book("IFRS", "10000"), "b-ifrs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBook(ctx, asset.ID, book("TAX", "10000"), "b-tax"); err != nil {
		t.Fatal(err)
	}
	imp := store.AdjustmentInput{BookID: ifrs.ID, Type: domain.AdjustmentImpairment, Date: day("2026-06-30"), Amount: d("1500")}
	res, err := s.Adjust(ctx, asset.ID, imp, "imp-1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Book.NBV.Equal(d("8500")) || !res.Transaction.ImpairmentAmount.Equal(d("1500")) || res.Transaction.Type != "Impairment" {
		t.Fatalf("impairment = %+v", res)
	}
	if again, err := s.Adjust(ctx, asset.ID, imp, "imp-1"); err != nil || again.Transaction.ID != res.Transaction.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	rev, err := s.Adjust(ctx, asset.ID, store.AdjustmentInput{BookID: ifrs.ID, Type: domain.AdjustmentImpairment, Date: day("2026-07-31"), Amount: d("-2000")}, "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	if !rev.Limited || !rev.Transaction.AccumulatedAmount.Equal(d("-1500")) || !rev.Requested.Equal(d("-2000")) || !rev.Book.AccumulatedDepreciation.IsZero() {
		t.Fatalf("capped reversal = %+v", rev)
	}
	if _, err := s.Adjust(ctx, asset.ID, store.AdjustmentInput{BookID: ifrs.ID, Type: domain.AdjustmentDepreciation, Date: day("2026-02-01"), Amount: d("10")}, "early"); problemCode(err) != domain.CodeInvalidAdjustment {
		t.Fatalf("adjustment before in-service: err = %v", err)
	}
	if _, err := s.Adjust(ctx, asset.ID, store.AdjustmentInput{BookID: ids.New(), Type: domain.AdjustmentAcquisition, Date: day("2026-06-30"), Amount: d("10")}, "nobook"); problemCode(err) != store.CodeBookNotFound {
		t.Fatalf("unknown book: err = %v", err)
	}

	// AC-37.12: a financial addition of 2,000.00 posts on every Open book.
	add := store.AdditionInput{Type: "Improvement", Description: "Coolant system", Quantity: d("1"), AddedDate: day("2026-08-01"), Financial: true, Cost: d("2000"), Currency: "USD"}
	added, err := s.AddAddition(ctx, asset.ID, add, "add-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(added.Transactions) != 2 || added.Transactions[0].Type != "AcquisitionAdj" || !added.Transactions[1].CostAmount.Equal(d("2000")) ||
		added.Transactions[0].SourceID == nil || *added.Transactions[0].SourceID != added.ID {
		t.Fatalf("addition transactions = %+v", added.Transactions)
	}
	if replay, err := s.AddAddition(ctx, asset.ID, add, "add-1"); err != nil || replay.ID != added.ID {
		t.Fatalf("addition replay = %+v, %v", replay, err)
	}
	changed := add
	changed.Cost = d("3000")
	if _, err := s.AddAddition(ctx, asset.ID, changed, "add-1"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: err = %v", err)
	}
	eur := add
	eur.Currency = "EUR"
	if _, err := s.AddAddition(ctx, asset.ID, eur, "add-eur"); problemCode(err) != domain.CodeInvalidAdjustment {
		t.Fatalf("addition in another currency: err = %v", err)
	}
	onlyIFRS := add
	onlyIFRS.BookIDs = []string{ifrs.ID}
	if one, err := s.AddAddition(ctx, asset.ID, onlyIFRS, "add-ifrs"); err != nil || len(one.Transactions) != 1 || one.Transactions[0].AssetBookID != ifrs.ID {
		t.Fatalf("addition on a selected book = %+v, %v", one, err)
	}
	detail, err := s.GetAsset(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range detail.Books {
		want := map[string]string{"IFRS": "14000", "TAX": "12000"}[b.BookCode]
		if !b.Cost.Equal(d(want)) {
			t.Errorf("book %s cost = %s, want %s", b.BookCode, b.Cost, want)
		}
	}

	// Descriptive additions need no Open book; financial ones do.
	planned := newAsset(t, s, ctx, le, "FA-ADJ-2")
	serial := "SN-1"
	if _, err := s.AddAddition(ctx, planned.ID, store.AdditionInput{Type: "Accessory", Description: "Tow bar", Quantity: d("2"), SerialNumber: &serial,
		AddedDate: day("2026-08-01")}, "add-desc"); err != nil {
		t.Fatalf("descriptive addition on a planned asset: %v", err)
	}
	if _, err := s.AddAddition(ctx, planned.ID, add, "add-fin-planned"); problemCode(err) != domain.CodeAssetState {
		t.Fatalf("financial addition on a NotYetAcquired asset: err = %v", err)
	}
	list, err := s.ListAdditions(ctx, asset.ID)
	if err != nil || len(list) != 2 || len(list[0].Transactions)+len(list[1].Transactions) != 3 {
		t.Fatalf("additions = %+v, %v", list, err)
	}

	// No postings on a scrapped asset (BR-37.2).
	if _, err := s.Dispose(ctx, asset.ID, store.DisposalInput{Version: detail.Version, Kind: domain.DisposalScrap, Percentage: d("100"), Proceeds: d("0"),
		Date: day("2026-09-01")}, "scrap"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Adjust(ctx, asset.ID, store.AdjustmentInput{BookID: ifrs.ID, Type: domain.AdjustmentAcquisition, Date: day("2026-09-30"), Amount: d("10")}, "after-scrap"); problemCode(err) != domain.CodeAssetState {
		t.Fatalf("adjustment on a scrapped asset: err = %v", err)
	}
	if types := outboxTypes(t, pool, ctx); types["dev.passage.assets.asset.adjusted.v1"] != 5 {
		t.Fatalf("outbox = %v (impairment, reversal, two addition books, one selected book)", types)
	}
}

// TestStore_InsuranceIndexedValueAC37_9 covers the AC-37.9 worked example, the
// AST-0531 fallback year and AST-0532 under-insurance.
func TestStore_InsuranceIndexedValueAC37_9(t *testing.T) {
	s, _, ctx := setup(t)
	series, err := s.UpsertIndexSeries(ctx, store.IndexSeries{Code: "US-MACH", Country: "US", ClassCode: "MACHINE",
		Values: []store.IndexPoint{{Year: 2026, Value: d("118.5")}, {Year: 2024, Value: d("112.0")}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Values) != 2 || series.Values[0].Year != 2024 {
		t.Fatalf("series = %+v", series)
	}
	asset := newAsset(t, s, ctx, ids.New(), "FA-INS-1")
	code, base, y2026 := "US-MACH", 2024, 2026
	policy := store.InsurancePolicy{InsurerPartyID: ids.New(), PolicyNumber: "P-1", InsuranceType: "Property", CoverageFrom: day("2026-01-01"),
		CoverageTo: day("2026-12-31"), Premium: d("1200"), Currency: "USD", Basis: "Replacement", BaseValue: d("100000"), SumInsured: d("95000"),
		IndexSeriesCode: &code, IndexBaseYear: &base, ValuationYear: &y2026}
	got, err := s.AddInsurancePolicy(ctx, asset.ID, policy, "pol-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.IndexedValue == nil || got.IndexedValue.StringFixed(2) != "105803.57" || !got.UnderInsured || got.IndexYearUsed == nil || *got.IndexYearUsed != 2026 {
		t.Fatalf("policy = %+v", got)
	}
	if len(got.Warnings) != 1 || !strings.HasPrefix(got.Warnings[0], domain.CodeUnderInsured) {
		t.Fatalf("warnings = %v", got.Warnings)
	}
	covered := policy
	covered.PolicyNumber, covered.SumInsured = "P-2", d("95223.21")
	if p, err := s.AddInsurancePolicy(ctx, asset.ID, covered, "pol-2"); err != nil || p.UnderInsured {
		t.Fatalf("policy at 90%% of indexed value = %+v, %v", p, err)
	}
	y2027 := 2027
	fallback := policy
	fallback.PolicyNumber, fallback.ValuationYear, fallback.SumInsured = "P-3", &y2027, d("200000")
	p, err := s.AddInsurancePolicy(ctx, asset.ID, fallback, "pol-3")
	if err != nil {
		t.Fatal(err)
	}
	if *p.IndexYearUsed != 2026 || p.IndexedValue.StringFixed(2) != "105803.57" || len(p.Warnings) != 1 || !strings.HasPrefix(p.Warnings[0], domain.CodeIndexMissing) {
		t.Fatalf("fallback policy = %+v", p)
	}

	if _, err := s.AddInsurancePolicy(ctx, asset.ID, policy, "pol-dup"); problemCode(err) != store.CodePolicyNumberDup {
		t.Fatalf("duplicate policy number: err = %v", err)
	}
	noBase := policy
	y2023 := 2023
	noBase.PolicyNumber, noBase.IndexBaseYear = "P-4", &y2023
	if _, err := s.AddInsurancePolicy(ctx, asset.ID, noBase, "pol-4"); problemCode(err) != domain.CodeInvalidInsurance {
		t.Fatalf("base year without a value: err = %v", err)
	}
	unknown := policy
	missing := "NOPE"
	unknown.PolicyNumber, unknown.IndexSeriesCode = "P-5", &missing
	if _, err := s.AddInsurancePolicy(ctx, asset.ID, unknown, "pol-5"); problemCode(err) != store.CodeIndexSeriesNotFound {
		t.Fatalf("unknown series: err = %v", err)
	}
	plain := store.InsurancePolicy{InsurerPartyID: ids.New(), PolicyNumber: "P-6", InsuranceType: "Transit", CoverageFrom: day("2026-01-01"),
		CoverageTo: day("2026-06-30"), Currency: "USD", Basis: "Agreed", BaseValue: d("5000"), SumInsured: d("5000")}
	if p, err := s.AddInsurancePolicy(ctx, asset.ID, plain, "pol-6"); err != nil || p.IndexedValue != nil || p.UnderInsured || len(p.Warnings) != 0 {
		t.Fatalf("policy without index = %+v, %v", p, err)
	}

	list, err := s.ListInsurancePolicies(ctx, asset.ID)
	if err != nil || len(list) != 4 {
		t.Fatalf("policies = %d, %v", len(list), err)
	}
	if _, err := s.UpsertIndexSeries(ctx, store.IndexSeries{Code: "US-MACH", Country: "US", ClassCode: "MACHINE", Values: []store.IndexPoint{{Year: 2027, Value: d("121")}}}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListIndexSeries(ctx)
	if err != nil || len(all) != 1 || len(all[0].Values) != 3 {
		t.Fatalf("series after a second load = %+v, %v (earlier years are kept)", all, err)
	}
	if other, err := s.ListIndexSeries(scoped(2, "")); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 sees %+v, %v", other, err)
	}
	if _, err := s.ListInsurancePolicies(scoped(2, ""), asset.ID); problemCode(err) != store.CodeAssetNotFound {
		t.Fatalf("tenant 2 reads policies: err = %v", err)
	}
}

// TestStore_CapexCreateAndSubmit covers FR-37.17 create with variants and the
// AC-37.11 routing of a 320,000.00 variant to the CFO level.
func TestStore_CapexCreateAndSubmit(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	in := store.CapexRequest{LegalEntityID: le, Number: "CR-1", Title: "Press line", RequesterID: "plant.manager", Currency: "USD",
		Variants: []store.CapexVariant{
			{Description: "Full line", AmountsByYear: map[string]decimal.Decimal{"2026": d("200000"), "2027": d("120000")}, InServiceDate: day("2027-06-01"), ProfileAssumption: "SL-120"},
			{Description: "Refurbish", AmountsByYear: map[string]decimal.Decimal{"2026": d("40000")}, InServiceDate: day("2026-10-01"), ProfileAssumption: "SL-60"},
		}}
	req, err := s.CreateCapexRequest(ctx, in, "cr-1")
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != "Draft" || req.Version != 1 || len(req.Variants) != 2 || !req.Variants[0].TotalAmount.Equal(d("320000")) || req.Variants[1].Position != 2 {
		t.Fatalf("created = %+v", req)
	}
	if replay, err := s.CreateCapexRequest(ctx, in, "cr-1"); err != nil || replay.ID != req.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := s.CreateCapexRequest(ctx, in, "cr-dup"); problemCode(err) != store.CodeCapexNumberDup {
		t.Fatalf("duplicate number: err = %v", err)
	}
	if _, err := s.SubmitCapexRequest(ctx, req.ID, store.SubmitInput{VariantID: req.Variants[0].ID, Version: 7}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale submit: err = %v", err)
	}
	if _, err := s.SubmitCapexRequest(ctx, req.ID, store.SubmitInput{VariantID: ids.New(), Version: 1}); problemCode(err) != store.CodeCapexVariantNotFound {
		t.Fatalf("unknown variant: err = %v", err)
	}
	sub, err := s.SubmitCapexRequest(ctx, req.ID, store.SubmitInput{VariantID: req.Variants[0].ID, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "InApproval" || sub.ApprovalLevel == nil || *sub.ApprovalLevel != "CFO" || sub.RequestedAmount == nil || !sub.RequestedAmount.Equal(d("320000")) ||
		sub.SelectedVariantID == nil || *sub.SelectedVariantID != req.Variants[0].ID || sub.Version != 2 {
		t.Fatalf("submitted = %+v", sub)
	}
	if _, err := s.SubmitCapexRequest(ctx, req.ID, store.SubmitInput{VariantID: req.Variants[0].ID, Version: 2}); problemCode(err) != domain.CodeCapexState {
		t.Fatalf("submit twice: err = %v", err)
	}

	small := in
	small.Number = "CR-2"
	small.Variants = in.Variants[1:]
	two, err := s.CreateCapexRequest(ctx, small, "cr-2")
	if err != nil {
		t.Fatal(err)
	}
	if sub2, err := s.SubmitCapexRequest(ctx, two.ID, store.SubmitInput{VariantID: two.Variants[0].ID, Version: 1}); err != nil || *sub2.ApprovalLevel != "PlantManager" {
		t.Fatalf("40,000 request = %+v, %v", sub2, err)
	}
	page, err := s.ListCapexRequests(ctx, store.CapexFilter{Status: "InApproval", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Number != "CR-1" || page.NextCursor == "" || page.Items[0].Variants != nil {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	next, err := s.ListCapexRequests(ctx, store.CapexFilter{Status: "InApproval", After: page.NextCursor, Limit: 1})
	if err != nil || len(next.Items) != 1 || next.Items[0].Number != "CR-2" {
		t.Fatalf("second page = %+v, %v", next, err)
	}
	if drafts, err := s.ListCapexRequests(ctx, store.CapexFilter{Status: "Draft", Limit: 50}); err != nil || len(drafts.Items) != 0 {
		t.Fatalf("drafts = %+v, %v", drafts, err)
	}
	if _, err := s.GetCapexRequest(ctx, ids.New()); problemCode(err) != store.CodeCapexNotFound {
		t.Fatalf("unknown request: err = %v", err)
	}
	if _, err := s.GetCapexRequest(scoped(2, ""), req.ID); problemCode(err) != store.CodeCapexNotFound {
		t.Fatalf("tenant 2 reads the request: err = %v", err)
	}
}
