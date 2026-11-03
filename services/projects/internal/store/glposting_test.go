package store

import (
	"context"
	"errors"
	"net/http"
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
	accWIP, accAccrued, accRevenue, accOnAccount = "1410", "1420", "4000", "2400"
	accOffset, accCoS, accProvision, accLossExp  = "2100", "5100", "2500", "5200"
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
	g := &glRig{t: t, s: s, ctx: ctx, led: ledgerclient.NewFake(), ledgerID: ids.New(), entity: p.LegalEntityID}
	g.led.AddLedger(g.ledgerID, g.entity)
	return g
}

func (g *glRig) profile(offset string) {
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
	pr := domain.PostingProfile{Key: key, CostAccount: "5000", WIPAccount: accWIP, AccruedRevenueAccount: accAccrued, InvoicedRevenueAccount: accRevenue, OnAccountAccount: accOnAccount,
		OffsetAccount: offset, CostOfSalesAccount: accCoS, LossProvisionAccount: accProvision, LossExpenseAccount: accLossExp}
	if _, _, err := g.s.UpsertPostingProfile(g.ctx, pr, version); err != nil {
		g.t.Fatal(err)
	}
}

func (g *glRig) request(period, project string) GLPostRequest {
	return GLPostRequest{LegalEntityID: g.entity, LedgerID: g.ledgerID, Currency: "USD", Period: period, ProjectID: project}
}

func (g *glRig) postReq(in GLPostRequest) GLPostResult {
	g.t.Helper()
	g.clearBackoff()
	res, err := g.s.PostGL(g.ctx, g.led, in)
	if err != nil {
		g.t.Fatal(err)
	}
	return res
}

func (g *glRig) post(period, project string) GLPostResult {
	g.t.Helper()
	return g.postReq(g.request(period, project))
}

func (g *glRig) clearBackoff() {
	g.t.Helper()
	err := dbx.WithTenantTx(g.ctx, g.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(g.ctx, `update project_transaction set gl_next_attempt_at = null where gl_parked_at is null`)
		return err
	})
	if err != nil {
		g.t.Fatal(err)
	}
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

func (g *glRig) forecast(project, name string, costByPeriod map[string]string) domain.ForecastModel {
	g.t.Helper()
	var lines []domain.ForecastLine
	for period, cost := range costByPeriod {
		lines = append(lines, forecastLine(period, domain.TxnHour, nil, "1", cost, "0"))
	}
	m, _, err := g.s.CreateForecastModel(g.ctx, project, ForecastInput{Name: name, Lines: lines}, ids.New())
	if err != nil {
		g.t.Fatal(err)
	}
	return m
}

func (g *glRig) activate(m domain.ForecastModel) {
	g.t.Helper()
	if _, err := g.s.ActivateForecastModel(g.ctx, m.ProjectID, m.ID, m.Version); err != nil {
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

func (g *glRig) wantReconciled(period string) {
	g.t.Helper()
	rec := g.reconcile("")
	compared := 0
	for _, a := range rec.Accounts {
		if a.Control {
			compared++
			if !a.Difference.IsZero() || !a.GL.Equal(a.Subledger) {
				g.t.Fatalf("%s: account %s subledger %s, GL %s", period, a.Account, a.Subledger, a.GL)
			}
		}
	}
	if !rec.Reconciled || rec.Unposted.Count != 0 || compared == 0 {
		g.t.Fatalf("%s: reconciliation %+v", period, rec)
	}
}

func problemStatus(err error) int {
	switch problemCode(err) {
	case domain.CodeLedgerPinned:
		return http.StatusConflict
	case domain.CodeLedgerForeign, domain.CodeBalanceCurrency:
		return http.StatusUnprocessableEntity
	}
	return 0
}

// TestGL_AC4_SubledgerReconcilesToWIPAndRevenue is AC4 of PSG-41 ("project subledger reconciles to GL WIP/revenue accounts") on the PRD worked
// example (testdata/poc-worked-example.csv: contract 500,000, three periods, billing ahead then behind, expected loss 20,000 from period 3). Each
// period the hours are posted and the estimate booked from stored data only; the expected balances are the worked example's own numbers read back
// from the ledger fake. Cost moves from WIP to cost of sales as revenue is recognized, so the margin is real (28,888.89 in period 2) and the total
// loss of period 3 is exactly 20,000: 15,000 through revenue below cost and 5,000 as a provision, never both for the same loss.
func TestGL_AC4_SubledgerReconcilesToWIPAndRevenue(t *testing.T) {
	g := newGLRig(t, "ES-POC", "500000.00")
	g.profile(accOffset)
	g.activate(g.forecast("ES-POC", "To complete", map[string]string{"2026-11": "90000", "2026-12": "60000", "2027-01": "130000"}))

	g.hours("ES-POC", "2026-10-15", "2400")
	g.bill("ES-POC", "2026-10-31", "100000")
	if res := g.post("2026-10", ""); len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("period 1: %+v", res)
	}
	g.wantNets("period 1", map[string]string{accWIP: "0.00", accOffset: "-120000.00", accCoS: "120000.00", accRevenue: "-150000.00", accAccrued: "50000.00", accOnAccount: "100000.00", accProvision: "0.00"})
	g.wantReconciled("period 1")

	g.hours("ES-POC", "2026-11-15", "2800")
	g.bill("ES-POC", "2026-11-30", "200000")
	if res := g.post("2026-11", ""); len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("period 2: %+v", res)
	}
	g.wantNets("period 2", map[string]string{accWIP: "0.00", accOffset: "-260000.00", accCoS: "260000.00", accRevenue: "-288888.89", accAccrued: "-11111.11", accOnAccount: "300000.00", accProvision: "0.00"})
	g.wantReconciled("period 2")

	g.hours("ES-POC", "2026-12-15", "2600")
	g.bill("ES-POC", "2026-12-31", "100000")
	if res := g.post("2026-12", ""); len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("period 3: %+v", res)
	}
	g.wantNets("period 3", map[string]string{accWIP: "0.00", accOffset: "-390000.00", accCoS: "390000.00", accRevenue: "-375000.00", accAccrued: "-25000.00", accOnAccount: "400000.00",
		accProvision: "-5000.00", accLossExp: "5000.00"})
	pnl := decimal.RequireFromString(g.glNet(accRevenue)).Add(decimal.RequireFromString(g.glNet(accCoS))).Add(decimal.RequireFromString(g.glNet(accLossExp)))
	if !pnl.Equal(decimal.RequireFromString("20000")) {
		t.Fatalf("total loss through the P&L = %s, want exactly 20,000.00 (debit)", pnl)
	}
	g.wantReconciled("period 3")

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
	if rec.Reconciled || wip == nil || !wip.Control || !wip.Difference.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("an entry outside the subledger on a control account must break the reconciliation: %+v", rec)
	}
	for _, a := range rec.Accounts {
		if a.Account == accOffset && a.Control {
			t.Fatalf("a shared account must not be compared with the ledger: %+v", a)
		}
	}
	if _, err := g.led.Post(g.ctx, ledgerclient.Entry{LegalEntityID: g.entity, LedgerID: g.ledgerID, DocumentID: "EUR", PostingDate: "2026-12-31", Currency: "EUR", IdempotencyKey: "manual-2", SourceType: "manual",
		Lines: []ledgerclient.Line{{Account: accWIP, Amount: decimal.RequireFromString("5.00")}, {Account: "3000", Credit: true, Amount: decimal.RequireFromString("5.00")}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.s.ReconcileGL(g.ctx, g.led, GLReconcileRequest{LegalEntityID: g.entity, LedgerID: g.ledgerID, Currency: "USD"}); problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a ledger balance in another currency must be a problem, not a 500: %v", err)
	}
}

func TestGL_LossProvisionIsReleasedWhenTheEstimateImproves(t *testing.T) {
	g := newGLRig(t, "ES-REL", "100000.00")
	g.profile(accOffset)
	g.activate(g.forecast("ES-REL", "To complete", map[string]string{"2026-11": "60000"}))

	g.hours("ES-REL", "2026-10-15", "1200")
	if res := g.post("2026-10", ""); len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("october: %+v", res)
	}
	g.wantNets("october", map[string]string{accRevenue: "-50000.00", accCoS: "60000.00", accProvision: "-10000.00", accLossExp: "10000.00", accWIP: "0.00"})

	g.hours("ES-REL", "2026-11-10", "400")
	g.activate(g.forecast("ES-REL", "Revised", map[string]string{"2026-12": "10000"}))
	if res := g.post("2026-11", ""); len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("november books the new hours and the estimate: %+v", res)
	}
	g.wantNets("november", map[string]string{accRevenue: "-88888.89", accCoS: "80000.00", accProvision: "0.00", accLossExp: "0.00"})
	g.wantReconciled("november")
}

func TestGL_RetryResendsStoredBookingsAfterLostAnswerAndProfileChange(t *testing.T) {
	g := newGLRig(t, "ES-RETRY", "100000.00")
	g.profile(accOffset)
	g.activate(g.forecast("ES-RETRY", "To complete", map[string]string{"2026-11": "1000"}))
	txn := g.hours("ES-RETRY", "2026-10-15", "10")

	g.led.LoseAnswers(errors.New("connection reset"))
	res := g.post("2026-10", "")
	if len(res.Booked) != 0 || len(res.Failed) != 2 {
		t.Fatalf("lost answers: %+v", res)
	}
	if rec := g.reconcile("2026-10"); rec.Reconciled || rec.Unposted.Count != 1 || rec.Unposted.Items[0].Reason != domain.CodeGLFailed || rec.PendingEstimates != 1 {
		t.Fatalf("the unconfirmed transaction and estimate must show: %+v", rec)
	}
	g.led.LoseAnswers(nil)

	g.profile("2199")
	res = g.post("2026-10", "")
	if len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("retry: %+v", res)
	}
	entries := g.led.Entries()
	if len(entries) != 2 {
		t.Fatalf("a resend must not create a second entry: %d", len(entries))
	}
	for _, e := range entries {
		for _, l := range e.Lines {
			if l.Account == "2199" {
				t.Fatalf("the stored booking must be resent unchanged, not rebuilt from the new profile: %+v", e)
			}
		}
	}
	byKey := map[string]string{}
	for _, e := range entries {
		byKey[e.IdempotencyKey] = e.ID
	}
	for _, b := range res.Booked {
		if byKey["projects:txn:"+txn.ID+":1"] != b.EntryID && byKey["projects:wip:ES-RETRY:2026-10"] != b.EntryID {
			t.Fatalf("booked entry %s is not one of the ledger's: %+v", b.EntryID, byKey)
		}
	}
	g.wantReconciled("retry")
	got, err := g.s.ListTransactions(g.ctx, "ES-RETRY", "", 10)
	if err != nil || len(got.Items) != 1 || got.Items[0].JournalID == nil || *got.Items[0].JournalID != byKey["projects:txn:"+txn.ID+":1"] {
		t.Fatalf("the transaction must carry the ledger entry: %+v %v", got, err)
	}
}

func TestGL_RejectedBookingIsRebuiltFromTheCurrentProfile(t *testing.T) {
	g := newGLRig(t, "ES-REJ", "100000.00")
	g.profile("2900")
	g.hours("ES-REJ", "2026-10-15", "10")

	g.led.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "LEDGER_ACCOUNT_UNKNOWN"})
	res := g.post("", "")
	if len(res.Failed) != 1 || res.Failed[0].Code != "LEDGER_ACCOUNT_UNKNOWN" || len(g.led.Entries()) != 0 {
		t.Fatalf("rejected: %+v", res)
	}
	g.led.FailPosts(nil)
	g.profile("2901")
	res = g.post("", "")
	entries := g.led.Entries()
	if len(res.Booked) != 1 || len(entries) != 1 {
		t.Fatalf("rebuilt: %+v", res)
	}
	found := false
	for _, l := range entries[0].Lines {
		found = found || l.Account == "2901"
	}
	if !found {
		t.Fatalf("a rejected booking is dropped and rebuilt from the current profile: %+v", entries[0])
	}
}

func TestGL_FailingBacklogDoesNotStarveNewerTransactions(t *testing.T) {
	g := newGLRig(t, "ES-STARVE", "100000.00")
	g.profile(accOffset)
	g.led.RefuseClosedThrough("2026-06-30")
	for i := 0; i < MaxGLItemsPerRun+1; i++ {
		g.hours("ES-STARVE", "2026-01-05", "1")
	}
	for i := 0; i < 3; i++ {
		g.hours("ES-STARVE", "2026-10-05", "1")
	}
	res := g.post("", "")
	if len(res.Booked) != 0 || len(res.Failed) != MaxGLItemsPerRun || !res.More {
		t.Fatalf("first run: booked %d failed %d more %v", len(res.Booked), len(res.Failed), res.More)
	}
	res, err := g.s.PostGL(g.ctx, g.led, g.request("", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Booked) != 3 || len(res.Failed) != 1 || res.More {
		t.Fatalf("second run must reach the newer transactions: booked %d failed %d more %v", len(res.Booked), len(res.Failed), res.More)
	}
	for _, b := range res.Booked {
		if b.Kind != domain.GLKindTxn {
			t.Fatalf("%+v", b)
		}
	}
}

func TestGL_FailingTransactionBacksOffIsParkedWithReasonAndRetried(t *testing.T) {
	g := newGLRig(t, "ES-PARK", "100000.00")
	g.profile(accOffset)
	g.led.RefuseClosedThrough("2026-12-31")
	txn := g.hours("ES-PARK", "2026-10-15", "10")

	if res := g.post("", ""); len(res.Failed) != 1 {
		t.Fatalf("first attempt: %+v", res)
	}
	res, err := g.s.PostGL(g.ctx, g.led, g.request("", ""))
	if err != nil || len(res.Failed) != 0 || len(res.Booked) != 0 {
		t.Fatalf("a transaction in its backoff must not be attempted again: %+v %v", res, err)
	}
	for i := 1; i < MaxGLAttempts; i++ {
		if res := g.post("", ""); len(res.Failed) != 1 {
			t.Fatalf("attempt %d: %+v", i+1, res)
		}
	}
	if res := g.post("", ""); len(res.Failed) != 0 || len(res.Booked) != 0 {
		t.Fatalf("a parked transaction must not be attempted: %+v", res)
	}
	rec := g.reconcile("")
	if rec.Reconciled || rec.Unposted.Count != 1 || rec.Unposted.Items[0].ID != txn.ID || !rec.Unposted.Items[0].Parked ||
		rec.Unposted.Items[0].Reason != ledgerclient.CodePeriodClosed || rec.Unposted.Items[0].Attempts != MaxGLAttempts {
		t.Fatalf("a parked transaction shows with its reason: %+v", rec)
	}

	g.led.RefuseClosedThrough("")
	req := g.request("", "")
	req.RetryParked = true
	if res := g.postReq(req); len(res.Booked) != 1 || len(res.Failed) != 0 {
		t.Fatalf("after reopening with retryParked: %+v", res)
	}
	g.wantReconciled("after retry")
}

func TestGL_MissingProfileIsReportedAndPostedOnceCompleted(t *testing.T) {
	g := newGLRig(t, "ES-FAIL", "100000.00")
	g.hours("ES-FAIL", "2026-10-15", "10")

	res := g.post("", "")
	if len(res.Failed) != 1 || res.Failed[0].Code != domain.CodePostingProfile || len(g.led.Entries()) != 0 {
		t.Fatalf("without a profile: %+v", res)
	}
	g.profile("")
	if res = g.post("", ""); len(res.Failed) != 1 || res.Failed[0].Code != domain.CodeProfileIncomplete {
		t.Fatalf("a profile without the offset account: %+v", res)
	}
	g.profile(accOffset)
	if res = g.post("", ""); len(res.Booked) != 1 || len(res.Failed) != 0 {
		t.Fatalf("with the profile: %+v", res)
	}
}

func TestGL_EstimateNeedsActiveForecastOfProjectCurrencyAndPeriodOrderAndEntityScope(t *testing.T) {
	g := newGLRig(t, "ES-ORDER", "100000.00")
	g.profile(accOffset)
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

	model := g.forecast("ES-ORDER", "What if", map[string]string{"2026-12": "1000"})
	res := g.post("2026-10", "")
	if len(res.Booked) != 1 || res.Booked[0].Kind != domain.GLKindTxn || len(res.Failed) != 1 || res.Failed[0].Code != domain.CodeNoEstimate {
		t.Fatalf("only the entity's transaction is booked and a model that is not active is no estimate: %+v", res)
	}
	if rec := g.reconcile("2026-10"); rec.Reconciled || rec.UnbookedEstimates != 1 {
		t.Fatalf("an unbooked estimate must keep the reconciliation open: %+v", rec)
	}

	g.activate(model)
	err := dbx.WithTenantTx(g.ctx, g.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(g.ctx, `update project_forecast_model set currency = 'EUR' where project_id = 'ES-ORDER'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if res = g.post("2026-10", "ES-ORDER"); len(res.Failed) != 1 || res.Failed[0].Code != domain.CodeEstimateCurrency {
		t.Fatalf("a model in another currency: %+v", res)
	}
	err = dbx.WithTenantTx(g.ctx, g.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(g.ctx, `update project_forecast_model set currency = 'USD' where project_id = 'ES-ORDER'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if res = g.post("2026-11", "ES-ORDER"); len(res.Booked) != 1 || res.Booked[0].Kind != domain.GLKindWIP {
		t.Fatalf("november: %+v", res)
	}
	if res = g.post("2026-10", "ES-ORDER"); len(res.Failed) != 1 || res.Failed[0].Code != domain.CodePeriodOrder {
		t.Fatalf("an earlier period after a later one is booked: %+v", res)
	}
}

func TestGL_LedgerIsPinnedPerLegalEntityAndMustBelongToIt(t *testing.T) {
	g := newGLRig(t, "ES-PIN", "100000.00")
	g.profile(accOffset)
	g.hours("ES-PIN", "2026-10-15", "10")

	foreign := ids.New()
	g.led.AddLedger(foreign, ids.New())
	in := g.request("", "")
	in.LedgerID = foreign
	if _, err := g.s.PostGL(g.ctx, g.led, in); problemCode(err) != domain.CodeLedgerForeign {
		t.Fatalf("a ledger of another legal entity: %v", err)
	}
	in.LedgerID = ids.New()
	if _, err := g.s.PostGL(g.ctx, g.led, in); problemCode(err) != domain.CodeLedgerForeign {
		t.Fatalf("an unknown ledger: %v", err)
	}

	if res := g.post("", ""); len(res.Booked) != 1 {
		t.Fatalf("pinning post: %+v", res)
	}
	second := ids.New()
	g.led.AddLedger(second, g.entity)
	in.LedgerID = second
	if _, err := g.s.PostGL(g.ctx, g.led, in); problemCode(err) != domain.CodeLedgerPinned {
		t.Fatalf("a second ledger of the same entity after pinning: %v", err)
	}
	if _, err := g.s.ReconcileGL(g.ctx, g.led, GLReconcileRequest{LegalEntityID: g.entity, LedgerID: second, Currency: "USD"}); problemCode(err) != domain.CodeLedgerPinned {
		t.Fatalf("reconciling another ledger: %v", err)
	}
	if len(g.led.Entries()) != 1 {
		t.Fatalf("nothing may reach the other ledger: %d entries", len(g.led.Entries()))
	}
}
