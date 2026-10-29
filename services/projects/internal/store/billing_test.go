package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// billingProject creates an IN_PROCESS project of type pt in USD with the
// LABOR (HOUR) and MILESTONE (ON_ACCOUNT) categories.
func billingProject(t *testing.T, s *Postgres, ctx context.Context, id string, pt domain.ProjectType) {
	t.Helper()
	p := sampleProject(id)
	p.ProjectType = pt
	if _, _, err := s.CreateProject(ctx, p, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionProject(ctx, id, 1, domain.StageInProcess); err != nil {
		t.Fatal(err)
	}
	for _, c := range []domain.Category{{ID: "LABOR", Name: "Labour", TxnType: domain.TxnHour}, {ID: "MILESTONE", Name: "Milestones", TxnType: domain.TxnOnAccount}} {
		if _, _, err := s.UpsertCategory(ctx, c, 0); err != nil && problemCode(err) != CodePrecondition {
			t.Fatal(err)
		}
	}
}

func hours(projectID, prop, date, qty string) domain.Transaction {
	worker := "0b4a4c43-6f3c-4c55-9f7c-2f7f5d1c0a11"
	t := domain.Transaction{ProjectID: projectID, TxnType: domain.TxnHour, CategoryID: "LABOR", LineProperty: prop, TxnDate: date,
		Quantity: qty, CostPrice: "50", SalesPrice: "90.005", Currency: "USD", WorkerID: &worker, SourceDocType: domain.SourceManual}
	if err := domain.PriceLine(&t); err != nil {
		panic(err)
	}
	return t
}

func mustTxn(t *testing.T, s *Postgres, ctx context.Context, in domain.Transaction) domain.Transaction {
	t.Helper()
	out, created, err := s.CreateTransaction(ctx, in, ids.New())
	if err != nil || !created {
		t.Fatalf("create transaction: created=%v %v", created, err)
	}
	return out
}

func TestCategories_UpsertKeepsTypeAndVersion(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	c, created, err := s.UpsertCategory(ctx, domain.Category{ID: "TRAVEL", Name: "Travel", TxnType: domain.TxnCost}, 0)
	if err != nil || !created || c.Version != 1 {
		t.Fatalf("create: %+v %v", c, err)
	}
	if _, _, err := s.UpsertCategory(ctx, domain.Category{ID: "TRAVEL", Name: "Travel", TxnType: domain.TxnFee}, 1); ruleCode(err) != domain.CodeCategoryType {
		t.Fatalf("type change: %v", err)
	}
	if _, _, err := s.UpsertCategory(ctx, domain.Category{ID: "TRAVEL", Name: "Travel and lodging", TxnType: domain.TxnCost}, 2); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	c, created, err = s.UpsertCategory(ctx, domain.Category{ID: "TRAVEL", Name: "Travel and lodging", TxnType: domain.TxnCost}, 1)
	if err != nil || created || c.Version != 2 || c.Name != "Travel and lodging" {
		t.Fatalf("rename: %+v %v", c, err)
	}
	if list, err := s.ListCategories(tenantCtx(2)); err != nil || len(list) != 0 {
		t.Fatalf("tenant two sees %d categories (%v)", len(list), err)
	}
}

func TestTransactions_RulesKeysAndScope(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	billingProject(t, s, ctx, "ES-TXN", domain.TypeTimeMaterial)
	if _, _, err := s.CreateProject(ctx, sampleProject("ES-NEW"), ""); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.CreateTransaction(ctx, hours("ES-NEW", "CHARGEABLE", "2026-10-02", "1"), "k0"); ruleCode(err) != domain.CodeTxnStage {
		t.Fatalf("CREATED project: %v", err)
	}
	eur := hours("ES-TXN", "CHARGEABLE", "2026-10-02", "1")
	eur.Currency = "EUR"
	if _, _, err := s.CreateTransaction(ctx, eur, "k1"); ruleCode(err) != domain.CodeTxnCurrency {
		t.Fatalf("currency: %v", err)
	}
	fee := hours("ES-TXN", "CHARGEABLE", "2026-10-02", "1")
	fee.TxnType = domain.TxnFee
	if _, _, err := s.CreateTransaction(ctx, fee, "k2"); ruleCode(err) != domain.CodeTxnCategory {
		t.Fatalf("FEE under an HOUR category: %v", err)
	}
	unknown := hours("ES-TXN", "CHARGEABLE", "2026-10-02", "1")
	unknown.CategoryID = "NOPE"
	if _, _, err := s.CreateTransaction(ctx, unknown, "k3"); ruleCode(err) != domain.CodeTxnCategory {
		t.Fatalf("unknown category: %v", err)
	}
	node := ids.New()
	withNode := hours("ES-TXN", "CHARGEABLE", "2026-10-02", "1")
	withNode.WBSNodeID = &node
	if _, _, err := s.CreateTransaction(ctx, withNode, "k4"); problemCode(err) != CodeValidation {
		t.Fatalf("node of no plan: %v", err)
	}
	if _, _, err := s.CreateTransaction(ctx, hours("NOPE", "CHARGEABLE", "2026-10-02", "1"), "k5"); problemCode(err) != CodeNotFound {
		t.Fatalf("unknown project: %v", err)
	}

	in := hours("ES-TXN", "CHARGEABLE", "2026-10-02", "7.5")
	first, created, err := s.CreateTransaction(ctx, in, "k6")
	if err != nil || !created || first.SalesAmount != "675.04" || first.CostAmount != "375.00" || first.Quantity != "7.500" ||
		first.SalesPrice != "90.005000" || first.CreatedBy != "user-test" {
		t.Fatalf("create: %+v %v", first, err)
	}
	again, created, err := s.CreateTransaction(ctx, in, "k6")
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay: %+v created=%v %v", again, created, err)
	}
	if _, _, err := s.CreateTransaction(ctx, hours("ES-TXN", "CHARGEABLE", "2026-10-02", "8"), "k6"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}

	list, err := s.ListTransactions(ctx, "ES-TXN", "", 0)
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != first.ID || list.NextID != "" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := s.ListTransactions(tenantCtx(2), "ES-TXN", "", 0); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant two lists tenant one's project: %v", err)
	}
	if _, err := s.GetTransactionBilling(tenantCtx(2), first.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant two reads tenant one's transaction: %v", err)
	}
	if _, err := s.GetTransactionBilling(leCtx(1, ids.New()), first.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("another legal entity reads the transaction: %v", err)
	}
}

// TestProposals_BuildRemoveCancelAndFinishGate is FR-41.6 with BR-41.3, BR-41.5
// and BR-41.7 against Postgres.
func TestProposals_BuildRemoveCancelAndFinishGate(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	billingProject(t, s, ctx, "ES-BILL", domain.TypeTimeMaterial)
	a := mustTxn(t, s, ctx, hours("ES-BILL", "CHARGEABLE", "2026-10-02", "8"))
	b := mustTxn(t, s, ctx, hours("ES-BILL", "CHARGEABLE", "2026-10-20", "2"))
	mustTxn(t, s, ctx, hours("ES-BILL", "NON_CHARGEABLE", "2026-10-05", "1"))
	mustTxn(t, s, ctx, hours("ES-BILL", "CHARGEABLE", "2026-11-01", "1"))

	oct := domain.ProposalRequest{ProjectID: "ES-BILL", From: "2026-10-01", To: "2026-10-31"}
	if _, _, err := s.CreateProposal(ctx, domain.ProposalRequest{ProjectID: "ES-BILL", From: "2026-09-01", To: "2026-09-30"}, "p0"); ruleCode(err) != domain.CodeProposalEmpty {
		t.Fatalf("empty window: %v", err)
	}
	pr, created, err := s.CreateProposal(ctx, oct, "p1")
	if err != nil || !created || pr.Status != domain.ProposalDraft || pr.LineCount != 2 || len(pr.Lines) != 2 ||
		pr.Lines[0].TransactionID != a.ID || pr.Lines[1].TransactionID != b.ID || pr.TotalSales != "900.05" || pr.TotalCost != "500.00" ||
		len(pr.TxnTypes) != 5 || pr.CreatedBy != "user-test" {
		t.Fatalf("build: %+v %v", pr, err)
	}
	if pr.Lines[0].Amount != "720.04" || pr.Lines[0].UnitPrice != "90.005000" || pr.Lines[0].Quantity != "8.000" {
		t.Fatalf("line 1: %+v", pr.Lines[0])
	}
	if again, created, err := s.CreateProposal(ctx, oct, "p1"); err != nil || created || again.ID != pr.ID || len(again.Lines) != 2 {
		t.Fatalf("replay: %+v created=%v %v", again, created, err)
	}
	if _, _, err := s.CreateProposal(ctx, domain.ProposalRequest{ProjectID: "ES-BILL", From: "2026-10-01", To: "2026-10-30"}, "p1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := s.CreateProposal(ctx, oct, "p2"); ruleCode(err) != domain.CodeProposalEmpty {
		t.Fatalf("second build selects proposed lines: %v", err)
	}
	got, err := s.GetTransactionBilling(ctx, a.ID)
	if err != nil || got.Transaction.ProposalID == nil || *got.Transaction.ProposalID != pr.ID || !got.Candidate.Proposed || got.ProjectType != domain.TypeTimeMaterial {
		t.Fatalf("billing view: %+v %v", got, err)
	}

	// BR-41.7: an open proposal blocks FINISHED and the refused move changes nothing.
	if _, err := s.TransitionProject(ctx, "ES-BILL", 2, domain.StageFinished); ruleCode(err) != domain.CodeFinish {
		t.Fatalf("finish with an open proposal: %v", err)
	}

	approved, err := s.DecideProposal(ctx, pr.ID, 1, domain.ProposalApproved)
	if err != nil || approved.Status != domain.ProposalApproved || approved.ApprovedBy == nil || approved.Version != 2 {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	if _, err := s.DecideProposal(ctx, pr.ID, 1, domain.ProposalCanceled); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale cancel: %v", err)
	}
	if _, err := s.RemoveProposalLine(ctx, pr.ID, 2, ids.New()); problemCode(err) != CodeNotFound {
		t.Fatalf("remove a line not on the proposal: %v", err)
	}
	removed, err := s.RemoveProposalLine(ctx, pr.ID, 2, b.ID)
	if err != nil || removed.Status != domain.ProposalDraft || removed.ApprovedBy != nil || removed.LineCount != 1 ||
		removed.TotalSales != "720.04" || removed.TotalCost != "400.00" || removed.Version != 3 {
		t.Fatalf("remove line: %+v %v", removed, err)
	}
	// The removed transaction is back in the pool and a new build bills it.
	again, _, err := s.CreateProposal(ctx, oct, "p3")
	if err != nil || len(again.Lines) != 1 || again.Lines[0].TransactionID != b.ID {
		t.Fatalf("rebuild after removal: %+v %v", again, err)
	}

	canceled, err := s.DecideProposal(ctx, pr.ID, 3, domain.ProposalCanceled)
	if err != nil || canceled.Status != domain.ProposalCanceled || len(canceled.Lines) != 1 {
		t.Fatalf("cancel keeps its lines as history: %+v %v", canceled, err)
	}
	if got, _ := s.GetTransactionBilling(ctx, a.ID); got.Transaction.ProposalID != nil {
		t.Fatalf("cancel did not return the transaction to the pool: %+v", got.Transaction)
	}
	if _, err := s.RemoveProposalLine(ctx, pr.ID, 4, a.ID); ruleCode(err) != domain.CodeProposalState {
		t.Fatalf("remove from a canceled proposal: %v", err)
	}
	list, err := s.ListProposals(ctx, ProposalFilter{ProjectID: "ES-BILL", Status: domain.ProposalDraft})
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != again.ID || len(list.Items[0].Lines) != 0 || list.Items[0].LineCount != 1 {
		t.Fatalf("list drafts: %+v %v", list, err)
	}
	if _, err := s.DecideProposal(ctx, again.ID, 1, domain.ProposalCanceled); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionProject(ctx, "ES-BILL", 2, domain.StageFinished); err != nil {
		t.Fatalf("finish once no proposal is open: %v", err)
	}
	if _, err := s.ListProposals(tenantCtx(2), ProposalFilter{ProjectID: "ES-BILL"}); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant two lists proposals: %v", err)
	}
	if _, err := s.GetProposal(tenantCtx(2), pr.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant two reads a proposal: %v", err)
	}
}

// TestProposals_FixedPriceSelectsDueMilestones is the BR-41.3 fixed-price
// condition over a stored and scheduled WBS: only ON_ACCOUNT lines on billable
// milestones finishing by the end date are selected.
func TestProposals_FixedPriceSelectsDueMilestones(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	billingProject(t, s, ctx, "ES-FP", domain.TypeFixedPrice)
	working, _, err := s.CreatePlanVersion(ctx, "ES-FP", NewPlan{Kind: domain.KindWorking}, "fp-w")
	if err != nil {
		t.Fatal(err)
	}
	nodes := []domain.WBSNodeInput{
		{WBSCode: "1", Name: "Design approved", DurationDays: 5, IsBillableMilestone: true},
		{WBSCode: "2", Name: "Install complete", DurationDays: 30, IsBillableMilestone: true},
		{WBSCode: "3", Name: "Ordinary task", DurationDays: 5},
	}
	domain.NormalizeWBS(nodes)
	if _, err := s.ReplaceWBS(ctx, "ES-FP", working.ID, 1, nodes, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SchedulePlan(ctx, "ES-FP", working.ID); err != nil {
		t.Fatal(err)
	}
	w, err := s.LoadWBS(ctx, "ES-FP", working.ID)
	if err != nil {
		t.Fatal(err)
	}
	byCode := map[string]domain.WBSNode{}
	for _, n := range w.Nodes {
		byCode[n.WBSCode] = n
	}
	end := *byCode["1"].EarlyFinish // node 2 finishes weeks later
	onAccount := func(code string) domain.Transaction {
		id := byCode[code].ID
		x := domain.Transaction{ProjectID: "ES-FP", WBSNodeID: &id, TxnType: domain.TxnOnAccount, CategoryID: "MILESTONE",
			LineProperty: domain.LineChargeable, TxnDate: "2026-10-01", Quantity: "1", CostPrice: "0", SalesPrice: "100000",
			Currency: "USD", SourceDocType: domain.SourceManual}
		if err := domain.PriceLine(&x); err != nil {
			t.Fatal(err)
		}
		return mustTxn(t, s, ctx, x)
	}
	m1, m2, plain := onAccount("1"), onAccount("2"), onAccount("3")
	if m1.WBSCode == nil || *m1.WBSCode != "1" {
		t.Fatalf("WBS code not stored: %+v", m1)
	}
	mustTxn(t, s, ctx, hours("ES-FP", "CHARGEABLE", "2026-10-02", "8"))

	if _, _, err := s.CreateProposal(ctx, domain.ProposalRequest{ProjectID: "ES-FP", From: "2026-10-01", To: end, TxnTypes: []domain.TxnType{domain.TxnHour}}, "f0"); ruleCode(err) != domain.CodeProposalType {
		t.Fatalf("fixed-price HOUR billing: %v", err)
	}
	pr, _, err := s.CreateProposal(ctx, domain.ProposalRequest{ProjectID: "ES-FP", From: "2026-10-01", To: end}, "f1")
	if err != nil || len(pr.Lines) != 1 || pr.Lines[0].TransactionID != m1.ID || pr.TotalSales != "100000.00" {
		t.Fatalf("fixed-price build: %+v %v", pr, err)
	}
	for _, x := range []domain.Transaction{m2, plain} {
		got, err := s.GetTransactionBilling(ctx, x.ID)
		if err != nil || got.Candidate.ProjectStage != domain.StageInProcess {
			t.Fatal(err)
		}
		if got.Candidate.Milestone && got.Candidate.MilestoneDate.Format("2006-01-02") <= end {
			t.Fatalf("%s should not be a due milestone: %+v", *x.WBSCode, got.Candidate)
		}
	}
	// Replacing the WORKING WBS changes node ids; the code still finds the milestone.
	if _, err := s.ReplaceWBS(ctx, "ES-FP", working.ID, w.Plan.Version, nodes, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SchedulePlan(ctx, "ES-FP", working.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetTransactionBilling(ctx, m2.ID); err != nil || !got.Candidate.Milestone || got.Candidate.MilestoneDate.IsZero() {
		t.Fatalf("milestone after WBS replace: %+v %v", got.Candidate, err)
	}
}

// TestProposals_ConcurrentBuildsAreDisjoint is BR-41.8: parallel builds over
// the same window never put one transaction on two proposals, and together
// they bill every candidate exactly once.
func TestProposals_ConcurrentBuildsAreDisjoint(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	billingProject(t, s, ctx, "ES-RACE", domain.TypeTimeMaterial)
	const n = 30
	for i := 0; i < n; i++ {
		mustTxn(t, s, ctx, hours("ES-RACE", "CHARGEABLE", fmt.Sprintf("2026-10-%02d", 1+i%28), "1"))
	}
	var wg sync.WaitGroup
	results := make([]domain.InvoiceProposal, 6)
	errs := make([]error, 6)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _, errs[i] = s.CreateProposal(ctx, domain.ProposalRequest{ProjectID: "ES-RACE", From: "2026-10-01", To: "2026-10-31"}, fmt.Sprintf("race-%d", i))
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, pr := range results {
		if errs[i] != nil {
			if ruleCode(errs[i]) != domain.CodeProposalEmpty {
				t.Fatalf("build %d: %v", i, errs[i])
			}
			continue
		}
		for _, l := range pr.Lines {
			if seen[l.TransactionID] {
				t.Fatalf("transaction %s selected by two builds", l.TransactionID)
			}
			seen[l.TransactionID] = true
		}
	}
	if len(seen) != n {
		t.Fatalf("builds billed %d of %d transactions", len(seen), n)
	}
}

// TestProposals_ConcurrentSameKeyReplays: parallel requests with one key build
// one proposal and the others replay it (none answers PROJ-PROP-002 because
// the first request holds the rows).
func TestProposals_ConcurrentSameKeyReplays(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	billingProject(t, s, ctx, "ES-KEY", domain.TypeTimeMaterial)
	for i := 0; i < 5; i++ {
		mustTxn(t, s, ctx, hours("ES-KEY", "CHARGEABLE", fmt.Sprintf("2026-10-%02d", 1+i), "1"))
	}
	var wg sync.WaitGroup
	results := make([]domain.InvoiceProposal, 5)
	created := make([]bool, 5)
	errs := make([]error, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], created[i], errs[i] = s.CreateProposal(ctx, domain.ProposalRequest{ProjectID: "ES-KEY", From: "2026-10-01", To: "2026-10-31"}, "same-key")
		}(i)
	}
	wg.Wait()
	n := 0
	for i := range results {
		if errs[i] != nil || results[i].ID != results[0].ID || len(results[i].Lines) != 5 {
			t.Fatalf("request %d: %+v %v", i, results[i], errs[i])
		}
		if created[i] {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d requests created a proposal, want 1", n)
	}
}

// TestBillingGrants_AppRoleCannotDelete checks the slice-4 grants: the app
// role deletes only proposal lines.
func TestBillingGrants_AppRoleCannotDelete(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	for _, stmt := range []string{`delete from project_transaction`, `delete from project_invoice_proposal`, `delete from project_category`} {
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}
