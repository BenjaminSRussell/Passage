package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

const (
	accCost, accWIP, accAccrued, accRevenue, accOnAccount = "5000", "1410", "1420", "4000", "2400"
)

type glRig struct {
	t        *testing.T
	s        *Postgres
	ctx      context.Context
	led      *ledgerclient.Fake
	ledgerID string
	entity   string
}

func newGLRig(t *testing.T, projectID string, contract string) *glRig {
	t.Helper()
	s, _ := setup(t)
	ctx := tenantCtx(1)
	p := sampleProject(projectID)
	p.ProjectType = domain.TypeFixedPrice
	p.ContractValue = contract
	if _, _, err := s.CreateProject(ctx, p, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionProject(ctx, projectID, 1, domain.StageInProcess); err != nil {
		t.Fatal(err)
	}
	for _, c := range []domain.Category{{ID: "LABOR", Name: "Labour", TxnType: domain.TxnHour}, {ID: "MILESTONE", Name: "Milestones", TxnType: domain.TxnOnAccount}} {
		if _, _, err := s.UpsertCategory(ctx, c, 0); err != nil {
			t.Fatal(err)
		}
	}
	return &glRig{t: t, s: s, ctx: ctx, led: ledgerclient.NewFake(), ledgerID: ids.New(), entity: p.LegalEntityID}
}

func (g *glRig) profile(wip string) {
	g.t.Helper()
	key := domain.ProfileKey{ProjectGroup: domain.Wildcard, Category: domain.Wildcard, LineProperty: domain.Wildcard}
	existing, err := g.s.ListPostingProfiles(g.ctx)
	if err != nil {
		g.t.Fatal(err)
	}
	version := 0
	for _, pr := range existing {
		if pr.Key == key {
			version = pr.Version
		}
	}
	pr := domain.PostingProfile{Key: key, CostAccount: accCost, WIPAccount: wip, AccruedRevenueAccount: accAccrued, InvoicedRevenueAccount: accRevenue, OnAccountAccount: accOnAccount}
	if _, _, err := g.s.UpsertPostingProfile(g.ctx, pr, version); err != nil {
		g.t.Fatal(err)
	}
}

func (g *glRig) post(period, project string) GLPostResult {
	g.t.Helper()
	res, err := g.s.PostGL(g.ctx, g.led, GLPostRequest{LegalEntityID: g.entity, LedgerID: g.ledgerID, Currency: "USD", Period: period, ProjectID: project})
	if err != nil {
		g.t.Fatal(err)
	}
	return res
}

func (g *glRig) reconcile(periodTo string) GLReconciliation {
	g.t.Helper()
	rec, err := g.s.ReconcileGL(g.ctx, g.led, GLReconcileRequest{LegalEntityID: g.entity, LedgerID: g.ledgerID, Currency: "USD", PeriodTo: periodTo})
	if err != nil {
		g.t.Fatal(err)
	}
	return rec
}

func (g *glRig) glNet(account string) string {
	g.t.Helper()
	bal, err := g.led.Balance(g.ctx, ledgerclient.BalanceQuery{LegalEntityID: g.entity, LedgerID: g.ledgerID, Account: account})
	if err != nil {
		g.t.Fatal(err)
	}
	return bal.Net().StringFixed(2)
}

func (g *glRig) hours(project, date, qty string) domain.Transaction {
	g.t.Helper()
	return mustTxn(g.t, g.s, g.ctx, hours(project, domain.LineChargeable, date, qty))
}

func (g *glRig) forecast(project string, costByPeriod map[string]string) {
	g.t.Helper()
	var lines []domain.ForecastLine
	for period, cost := range costByPeriod {
		lines = append(lines, forecastLine(period, domain.TxnHour, nil, "1", cost, "0"))
	}
	if _, _, err := g.s.CreateForecastModel(g.ctx, project, ForecastInput{Name: "To complete", Lines: lines}, ids.New()); err != nil {
		g.t.Fatal(err)
	}
}

// bill records an approved invoice proposal of amount for the project, ending on toDate.
func (g *glRig) bill(project, toDate, amount string) {
	g.t.Helper()
	x := domain.Transaction{ProjectID: project, TxnType: domain.TxnOnAccount, CategoryID: "MILESTONE", LineProperty: domain.LineChargeable, TxnDate: toDate,
		Quantity: "1", CostPrice: "0", SalesPrice: amount, Currency: "USD", SourceDocType: domain.SourceManual}
	if err := domain.PriceLine(&x); err != nil {
		g.t.Fatal(err)
	}
	txn := mustTxn(g.t, g.s, g.ctx, x)
	proposal := ids.New()
	tenantID := testkit.TenantID(1)
	err := dbx.WithTenantTx(g.ctx, g.s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(g.ctx, `insert into project_invoice_proposal (tenant_id, id, project_id, status, currency, from_date, to_date, txn_types, total_sales, created_by, approved_by)
			values ($1, $2, $3, 'APPROVED', 'USD', '2026-01-01', $4::date, ARRAY['ON_ACCOUNT'], $5::numeric, 'u', 'a')`, tenantID, proposal, project, toDate, amount); err != nil {
			return err
		}
		if _, err := tx.Exec(g.ctx, `insert into project_invoice_proposal_line (tenant_id, proposal_id, line_no, transaction_id, quantity, unit_price, cost_amount, amount)
			values ($1, $2, 1, $3, 1, $4::numeric, 0, $4::numeric)`, tenantID, proposal, txn.ID, amount); err != nil {
			return err
		}
		_, err := tx.Exec(g.ctx, `update project_transaction set proposal_id = $2 where tenant_id = $1 and id = $3`, tenantID, proposal, txn.ID)
		return err
	})
	if err != nil {
		g.t.Fatal(err)
	}
}

func (g *glRig) wantNets(period string, want map[string]string) {
	g.t.Helper()
	for account, net := range want {
		if got := g.glNet(account); got != net {
			g.t.Errorf("%s: ledger balance of %s = %s, want %s", period, account, got, net)
		}
	}
}

func (g *glRig) wantReconciled(period string, accounts int) {
	g.t.Helper()
	rec := g.reconcile("")
	if !rec.Reconciled || rec.Unposted.Count != 0 || len(rec.Accounts) != accounts {
		g.t.Fatalf("%s: reconciliation %+v", period, rec)
	}
	for _, a := range rec.Accounts {
		if !a.Difference.IsZero() || !a.GL.Equal(a.Subledger) {
			g.t.Fatalf("%s: account %s subledger %s, GL %s", period, a.Account, a.Subledger, a.GL)
		}
	}
}

// TestGL_AC4_SubledgerReconcilesToWIPAndRevenue is AC4 of PSG-41 ("project subledger reconciles to GL WIP/revenue accounts") on the PRD worked
// example (testdata/poc-worked-example.csv: contract 500,000, three periods, billing ahead then behind, loss of 20,000 in period 3). Each period the
// hours are posted and the estimate booked from stored data only; the expected account balances are the worked example's own numbers (revenue to
// date, accrued, deferred, provision), read back from the ledger fake, and the reconciliation must tie to them with no unposted transaction.
func TestGL_AC4_SubledgerReconcilesToWIPAndRevenue(t *testing.T) {
	g := newGLRig(t, "ES-POC", "500000.00")
	g.profile(accWIP)
	g.forecast("ES-POC", map[string]string{"2026-11": "90000", "2026-12": "60000", "2027-01": "130000"})

	g.hours("ES-POC", "2026-10-15", "2400")
	g.bill("ES-POC", "2026-10-31", "100000")
	if res := g.post("2026-10", ""); len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("period 1: %+v", res)
	}
	g.wantNets("period 1", map[string]string{accWIP: "120000.00", accCost: "-120000.00", accRevenue: "-150000.00", accAccrued: "50000.00", accOnAccount: "100000.00"})
	g.wantReconciled("period 1", 5)

	g.hours("ES-POC", "2026-11-15", "2800")
	g.bill("ES-POC", "2026-11-30", "200000")
	if res := g.post("2026-11", ""); len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("period 2: %+v", res)
	}
	g.wantNets("period 2", map[string]string{accWIP: "260000.00", accCost: "-260000.00", accRevenue: "-288888.89", accAccrued: "-11111.11", accOnAccount: "300000.00"})
	g.wantReconciled("period 2", 5)

	g.hours("ES-POC", "2026-12-15", "2600")
	g.bill("ES-POC", "2026-12-31", "100000")
	if res := g.post("2026-12", ""); len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("period 3: %+v", res)
	}
	g.wantNets("period 3", map[string]string{accWIP: "370000.00", accCost: "-370000.00", accRevenue: "-375000.00", accAccrued: "-25000.00", accOnAccount: "400000.00"})
	g.wantReconciled("period 3", 5)

	before := len(g.led.Entries())
	if res := g.post("2026-12", ""); len(res.Booked) != 0 || len(res.Failed) != 0 || len(g.led.Entries()) != before {
		t.Fatalf("a repeated run booked again: %+v, entries %d to %d", res, before, len(g.led.Entries()))
	}
	if before != 6 {
		t.Fatalf("entries = %d, want 3 transactions and 3 estimates", before)
	}

	if _, err := g.led.Post(g.ctx, ledgerclient.Entry{LegalEntityID: g.entity, LedgerID: g.ledgerID, DocumentID: "MANUAL", PostingDate: "2026-12-31", Currency: "USD", IdempotencyKey: "manual-1", SourceType: "manual",
		Lines: []ledgerclient.Line{{Account: accWIP, Amount: decimal.RequireFromString("5.00")}, {Account: "3000", Credit: true, Amount: decimal.RequireFromString("5.00")}}}); err != nil {
		t.Fatal(err)
	}
	rec := g.reconcile("")
	var wip *GLAccountRecon
	for i := range rec.Accounts {
		if rec.Accounts[i].Account == accWIP {
			wip = &rec.Accounts[i]
		}
	}
	if rec.Reconciled || wip == nil || !wip.Difference.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("an entry outside the subledger must break the reconciliation: %+v", rec)
	}
}

func TestGL_RetryResendsStoredBookingAfterLostAnswerAndProfileChange(t *testing.T) {
	g := newGLRig(t, "ES-RETRY", "100000.00")
	g.profile(accWIP)
	txn := g.hours("ES-RETRY", "2026-10-15", "10")

	g.led.LoseAnswers(errors.New("connection reset"))
	res := g.post("", "")
	if len(res.Booked) != 0 || len(res.Failed) != 1 || res.Failed[0].Ref != txn.ID || res.Failed[0].Code != domain.CodeGLFailed {
		t.Fatalf("lost answer: %+v", res)
	}
	if rec := g.reconcile(""); rec.Reconciled || rec.Unposted.Count != 1 {
		t.Fatalf("the unconfirmed transaction must be unposted: %+v", rec)
	}
	g.led.LoseAnswers(nil)

	g.profile("1499")
	res = g.post("", "")
	if len(res.Booked) != 1 || len(res.Failed) != 0 {
		t.Fatalf("retry: %+v", res)
	}
	entries := g.led.Entries()
	if len(entries) != 1 || entries[0].Lines[0].Account != accWIP || entries[0].IdempotencyKey != "projects:txn:"+txn.ID+":1" || res.Booked[0].EntryID != entries[0].ID {
		t.Fatalf("the stored booking must be resent unchanged: %+v", entries)
	}
	g.wantReconciled("retry", 2)
	got, err := g.s.ListTransactions(g.ctx, "ES-RETRY", "", 10)
	if err != nil || len(got.Items) != 1 || got.Items[0].JournalID == nil || *got.Items[0].JournalID != entries[0].ID {
		t.Fatalf("the transaction must carry the ledger entry: %+v %v", got, err)
	}
}

func TestGL_ClosedPeriodAndMissingProfileAreReportedAndRetried(t *testing.T) {
	g := newGLRig(t, "ES-FAIL", "100000.00")
	g.hours("ES-FAIL", "2026-10-15", "10")

	res := g.post("", "")
	if len(res.Failed) != 1 || res.Failed[0].Code != domain.CodePostingProfile || len(g.led.Entries()) != 0 {
		t.Fatalf("without a profile: %+v", res)
	}
	g.profile(accWIP)
	g.led.RefuseClosedThrough("2026-12-31")
	res = g.post("", "")
	if len(res.Failed) != 1 || res.Failed[0].Code != ledgerclient.CodePeriodClosed {
		t.Fatalf("closed period: %+v", res)
	}
	g.led.RefuseClosedThrough("")
	if res = g.post("", ""); len(res.Booked) != 1 || len(res.Failed) != 0 {
		t.Fatalf("after reopening: %+v", res)
	}
}

func TestGL_EstimateNeedsForecastAndPeriodOrderAndEntityScope(t *testing.T) {
	g := newGLRig(t, "ES-ORDER", "100000.00")
	g.profile(accWIP)
	g.hours("ES-ORDER", "2026-10-15", "100")

	other := sampleProject("ES-OTHER")
	other.ProjectType = domain.TypeFixedPrice
	if _, _, err := g.s.CreateProject(g.ctx, other, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.s.TransitionProject(g.ctx, "ES-OTHER", 1, domain.StageInProcess); err != nil {
		t.Fatal(err)
	}
	g.hours("ES-OTHER", "2026-10-15", "7")

	res := g.post("2026-10", "")
	if len(res.Booked) != 1 || res.Booked[0].Kind != domain.GLKindTxn || len(res.Failed) != 1 || res.Failed[0].Code != domain.CodeNoEstimate {
		t.Fatalf("only the entity's transaction is booked and the estimate needs a forecast: %+v", res)
	}

	g.forecast("ES-ORDER", map[string]string{"2026-12": "1000"})
	if res = g.post("2026-11", "ES-ORDER"); len(res.Booked) != 1 || res.Booked[0].Kind != domain.GLKindWIP {
		t.Fatalf("november: %+v", res)
	}
	if res = g.post("2026-10", "ES-ORDER"); len(res.Failed) != 1 || res.Failed[0].Code != domain.CodePeriodOrder {
		t.Fatalf("an earlier period after a later one is booked: %+v", res)
	}
}
