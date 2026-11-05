package store

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/ledgertest"
)

// profileAccounts are the accounts of testProfile, opened in the real ledger's chart.
func profileAccounts() []string {
	p := testProfile(domain.AllKey, domain.AllKey, acctWIP)
	return []string{p.WIP, p.Inventory, p.FinishedGoods, p.LaborAbsorption, p.OverheadAbsorption, p.VarLotSize, p.VarQuantity, p.VarPrice, p.VarSubstitution}
}

// W21 AC4 against the real ledger service (#478): the partial-RAF order of TestGL_PartialRAFEndReconcilesToTheLedgerWIPAccount
// is booked through ledgerclient over HTTP with production's signed service token, so the ledger's own validation, posting,
// idempotency and balance reads decide the outcome. WIP holds 1951.00 in the GL after booking, End clears it into the
// variance accounts, and a second run books nothing new.
func TestGL_RealLedger_PartialRAFEndReconciles(t *testing.T) {
	led := ledgertest.Start(t)
	pool, s := migrated(t)
	tenantID, le := testkit.TenantID(9110), ids.New()
	ctx := scopedCtx(tenantID, le)
	ledgerID := led.CreateLedger(t, tenantID, le, "PRD", "USD", profileAccounts())
	var l ledgerclient.Ledger = led.Client

	st := partialRAFOrder(t, s, ctx, le, "key-gl-real")
	recon := GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, Currency: "USD"}
	if r, err := s.ReconcileWIP(ctx, l, recon); err != nil || r.Reconciled || len(r.Accounts) != 1 || !r.Accounts[0].GL.IsZero() || !r.Accounts[0].Unbooked.Equal(d("1951")) {
		t.Fatalf("before booking: %+v %v", r, err)
	}

	res, err := s.PostToGL(ctx, l, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID})
	if err != nil || len(res.Failed) != 0 || len(res.Booked) == 0 || unbookedRows(t, pool, st.orderID) != 0 {
		t.Fatalf("post: %+v %v", res, err)
	}
	for _, b := range res.Booked {
		if !ids.Valid(b.EntryID) {
			t.Fatalf("booked %s without a ledger entry id: %q", b.Document, b.EntryID)
		}
	}
	r, err := s.ReconcileWIP(ctx, l, recon)
	if err != nil || !r.Reconciled || !r.Accounts[0].GL.Equal(d("1951")) || !r.Accounts[0].Unbooked.IsZero() {
		t.Fatalf("after RAF 60: %+v %v", r, err)
	}
	for acct, want := range map[string]decimal.Decimal{acctWIP: d("1951"), acctInv: d("-3111"), acctLabor: d("-1380"), acctOverhead: d("-460"), acctFG: d("3000")} {
		if got := realNet(t, ctx, l, le, ledgerID, acct); !got.Equal(want) {
			t.Fatalf("GL %s = %s, want %s", acct, got, want)
		}
	}

	// A re-run finds nothing unbooked; clearing the links forces a replay, which the ledger answers with the same entries.
	if again, err := s.PostToGL(ctx, l, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID}); err != nil || len(again.Booked) != 0 || len(again.Failed) != 0 {
		t.Fatalf("a second run books nothing: %+v %v", again, err)
	}
	if _, err := pool.Exec(context.Background(), `update production_wip_posting set journal_entry_id = null where order_id = $1`, st.orderID); err != nil {
		t.Fatal(err)
	}
	replay, err := s.PostToGL(ctx, l, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID})
	if err != nil || len(replay.Booked) != len(res.Booked) || len(replay.Failed) != 0 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	first := map[string]string{}
	for _, b := range res.Booked {
		first[b.Document] = b.EntryID
	}
	for _, b := range replay.Booked {
		if first[b.Document] != b.EntryID {
			t.Fatalf("replay of %s booked %s, first run %s", b.Document, b.EntryID, first[b.Document])
		}
	}
	if got := realNet(t, ctx, l, le, ledgerID, acctWIP); !got.Equal(d("1951")) {
		t.Fatalf("GL WIP after replay = %s, want 1951 (no double booking)", got)
	}

	st.must(domain.CmdEnd, nil)
	if res, err = s.PostToGL(ctx, l, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, OrderID: st.orderID}); err != nil || len(res.Booked) != 1 || len(res.Failed) != 0 {
		t.Fatalf("settlement: %+v %v", res, err)
	}
	if r, err = s.ReconcileWIP(ctx, l, recon); err != nil || !r.Reconciled || !r.Accounts[0].GL.IsZero() || !r.Accounts[0].Subledger.IsZero() {
		t.Fatalf("after End: %+v %v", r, err)
	}
	for acct, want := range map[string]decimal.Decimal{acctVarQty: d("1890"), acctVarPrice: d("61")} {
		if got := realNet(t, ctx, l, le, ledgerID, acct); !got.Equal(want) {
			t.Fatalf("GL %s = %s, want %s", acct, got, want)
		}
	}
}

// The real ledger refuses an account the chart does not hold; the group stays unbooked, is reported with the ledger's code
// and books on the next run once the account exists.
func TestGL_RealLedger_ARefusedGroupIsParkedAndRetried(t *testing.T) {
	led := ledgertest.Start(t)
	pool, s := migrated(t)
	tenantID, le := testkit.TenantID(9111), ids.New()
	ctx := scopedCtx(tenantID, le)
	accounts := profileAccounts()
	ledgerID := led.CreateLedger(t, tenantID, le, "PRD", "USD", accounts[1:]) // no WIP account yet
	st := partialRAFOrder(t, s, ctx, le, "key-gl-real-park")

	res, err := s.PostToGL(ctx, led.Client, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID})
	if err != nil || len(res.Booked) != 0 || len(res.Failed) == 0 || res.Failed[0].Code != "LEDGER_ACCOUNT_NOT_FOUND" || unbookedRows(t, pool, st.orderID) == 0 {
		t.Fatalf("refused run: %+v %v", res, err)
	}

	led.PutAccounts(t, tenantID, le, "PRD", accounts[:1])
	if res, err = s.PostToGL(ctx, led.Client, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID}); err != nil || len(res.Failed) != 0 || unbookedRows(t, pool, st.orderID) != 0 {
		t.Fatalf("retry: %+v %v", res, err)
	}
	if r, err := s.ReconcileWIP(ctx, led.Client, GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, Currency: "USD"}); err != nil || !r.Reconciled {
		t.Fatalf("after retry: %+v %v", r, err)
	}
}

func realNet(t *testing.T, ctx context.Context, l ledgerclient.Ledger, le, ledgerID, account string) decimal.Decimal {
	t.Helper()
	b, err := l.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: le, LedgerID: ledgerID, Account: account})
	if err != nil {
		t.Fatal(err)
	}
	return b.Net()
}
