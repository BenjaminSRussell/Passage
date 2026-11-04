package store

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

func glNet(t *testing.T, ctx context.Context, l ledgerclient.Ledger, ledgerID, account string) decimal.Decimal {
	t.Helper()
	b, err := l.Balance(ctx, ledgerclient.BalanceQuery{LedgerID: ledgerID, Account: account})
	if err != nil {
		t.Fatal(err)
	}
	return b.Net()
}

func unbookedRows(t *testing.T, pool *pgxpool.Pool, orderID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from production_wip_posting where order_id = $1 and journal_entry_id is null`, orderID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func partialRAFOrder(t *testing.T, s *Postgres, ctx context.Context, le, key string) *stepper {
	st := startedOrder(t, s, ctx, sampleOrder(t, key, le))
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{
		{LineNo: "10", Qty: d("203"), UnitCost: qty("10.20")}, {LineNo: "20.1", Qty: d("102"), UnitCost: qty("10.20")}}}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("46"), QtyGood: d("60")}}}).ID)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("60") })
	return st
}

// W21 AC4: an order ended after a partial RAF (60 of 100) leaves 1951.00 in WIP, which is the GL WIP balance once booked,
// and End clears exactly that off the GL WIP account into the variance accounts (1890.00 quantity, 61.00 price).
func TestGL_PartialRAFEndReconcilesToTheLedgerWIPAccount(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9100))
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	st := partialRAFOrder(t, s, ctx, le, "key-gl-ac4")
	recon := GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, Currency: "USD"}

	r, err := s.ReconcileWIP(ctx, fake, recon)
	if err != nil || r.Reconciled || len(r.Accounts) != 1 || !r.Accounts[0].Subledger.Equal(d("1951")) || !r.Accounts[0].Unbooked.Equal(d("1951")) || !r.Accounts[0].GL.IsZero() {
		t.Fatalf("before booking: %+v %v", r, err)
	}

	res, err := s.PostToGL(ctx, fake, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID})
	if err != nil || len(res.Failed) != 0 || len(res.Booked) == 0 || unbookedRows(t, pool, st.orderID) != 0 {
		t.Fatalf("post: %+v %v unbooked %d", res, err, unbookedRows(t, pool, st.orderID))
	}
	if r, err = s.ReconcileWIP(ctx, fake, recon); err != nil || !r.Reconciled || !r.Accounts[0].GL.Equal(d("1951")) || !r.Accounts[0].Unbooked.IsZero() {
		t.Fatalf("after RAF 60: %+v %v", r, err)
	}
	for acct, want := range map[string]decimal.Decimal{acctWIP: d("1951"), acctInv: d("-3111"), acctLabor: d("-1380"), acctOverhead: d("-460"), acctFG: d("3000")} {
		if got := glNet(t, ctx, fake, ledgerID, acct); !got.Equal(want) {
			t.Fatalf("GL %s = %s, want %s", acct, got, want)
		}
	}
	var linked int
	if err := pool.QueryRow(context.Background(), `select count(*) from production_journal where order_id = $1 and posted and journal_entry_id is not null`, st.orderID).Scan(&linked); err != nil || linked != len(res.Booked) {
		t.Fatalf("journals linked to their entries: %d of %d %v", linked, len(res.Booked), err)
	}
	for _, e := range fake.Entries() {
		if !ids.Valid(e.DocumentID) || e.Lines[0].Text == "" {
			t.Fatalf("the ledger takes a UUID document id and the journal number as line text: %+v", e.Entry)
		}
	}
	entries := len(fake.Entries())
	if again, err := s.PostToGL(ctx, fake, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID}); err != nil || len(again.Booked) != 0 || len(fake.Entries()) != entries {
		t.Fatalf("a second run books nothing: %+v %v", again, err)
	}

	st.must(domain.CmdEnd, nil)
	if r, err = s.ReconcileWIP(ctx, fake, recon); err != nil || r.Reconciled || !r.Accounts[0].Subledger.IsZero() || !r.Accounts[0].Unbooked.Equal(d("-1951")) {
		t.Fatalf("End not yet booked: %+v %v", r, err)
	}
	if res, err = s.PostToGL(ctx, fake, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, OrderID: st.orderID}); err != nil || len(res.Booked) != 1 || len(res.Failed) != 0 {
		t.Fatalf("settlement: %+v %v", res, err)
	}
	if r, err = s.ReconcileWIP(ctx, fake, recon); err != nil || !r.Reconciled || !r.Accounts[0].GL.IsZero() {
		t.Fatalf("after End: %+v %v", r, err)
	}
	for acct, want := range map[string]decimal.Decimal{acctVarQty: d("1890"), acctVarPrice: d("61")} {
		if got := glNet(t, ctx, fake, ledgerID, acct); !got.Equal(want) {
			t.Fatalf("GL %s = %s, want %s", acct, got, want)
		}
	}
	var open int
	if err := pool.QueryRow(context.Background(), `select count(*) from production_variance where order_id = $1 and journal_entry_id is null`, st.orderID).Scan(&open); err != nil || open != 0 {
		t.Fatalf("variances without an entry: %d %v", open, err)
	}
}

func TestGL_ARefusedPostStaysUnbookedAndRetriesToTheSameEntry(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9101))
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	st := partialRAFOrder(t, s, ctx, le, "key-gl-closed")
	req := GLPostRequest{LegalEntityID: le, LedgerID: ledgerID}

	fake.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	res, err := s.PostToGL(ctx, fake, req)
	if err != nil || len(res.Booked) != 0 || len(res.Failed) == 0 || res.Failed[0].Code != ledgerclient.CodePeriodClosed || unbookedRows(t, pool, st.orderID) == 0 {
		t.Fatalf("refused run: %+v %v", res, err)
	}
	r, err := s.ReconcileWIP(ctx, fake, GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, Currency: "USD"})
	if err != nil || r.Reconciled || !r.Accounts[0].Difference.Equal(d("-1951")) {
		t.Fatalf("a refused booking must show as a difference: %+v %v", r, err)
	}

	fake.FailPosts(nil)
	if res, err = s.PostToGL(ctx, fake, req); err != nil || len(res.Failed) != 0 || unbookedRows(t, pool, st.orderID) != 0 {
		t.Fatalf("retry: %+v %v", res, err)
	}
	keys := map[string]int{}
	for _, e := range fake.Entries() {
		keys[e.IdempotencyKey]++
	}
	for k, n := range keys {
		if n != 1 {
			t.Fatalf("key %s booked %d times", k, n)
		}
	}
}

func TestGL_OtherLegalEntitiesAndCurrenciesAreLeftOut(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9102))
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	partialRAFOrder(t, s, ctx, le, "key-gl-scope")

	if res, err := s.PostToGL(ctx, fake, GLPostRequest{LegalEntityID: ids.New(), LedgerID: ledgerID}); err != nil || len(res.Booked) != 0 || len(fake.Entries()) != 0 {
		t.Fatalf("other entity: %+v %v", res, err)
	}
	if r, err := s.ReconcileWIP(ctx, fake, GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, Currency: "EUR"}); err != nil || len(r.Accounts) != 0 || !r.Reconciled {
		t.Fatalf("other currency: %+v %v", r, err)
	}
}
