package store

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

type glFixture struct {
	s      *Postgres
	pool   *pgxpool.Pool
	ctx    context.Context
	le     string
	ledger *ledgerclient.Fake
	glID   string
}

func newGLFixture(t *testing.T, tenantN int) glFixture {
	t.Helper()
	s, pool := setup(t)
	f := glFixture{s: s, pool: pool, ctx: scopedCtx(testkit.TenantID(tenantN)), le: ids.New(), ledger: ledgerclient.NewFake(), glID: ids.New()}
	f.ledger.AddLedger(f.glID, f.le)
	if _, err := s.SavePostingProfile(f.ctx, PostingProfile{LegalEntityID: f.le, GLLedgerID: f.glID, InventoryAccount: "1400", GRIRAccount: "2100",
		APAccount: "2000", PPVAccount: "5100", AdjustmentAccount: "5000"}, "tester"); err != nil {
		t.Fatal(err)
	}
	f.record(t, costRow("seed", f.le, ids.New(), ids.New(), domain.KindPhysical, "0", "0", "2025-12-01"))
	return f
}

func (f glFixture) ppvRow(key, qty, amount, grir, date string) CostTransaction {
	c := costRow(key, f.le, ids.New(), ids.New(), domain.KindPPV, qty, amount, date)
	c.GrirAmount = grir
	return c
}

func (f glFixture) record(t *testing.T, rows ...CostTransaction) {
	t.Helper()
	if _, err := f.s.RecordCostTransactions(f.ctx, rows); err != nil {
		t.Fatal(err)
	}
}

func (f glFixture) dueNow(t *testing.T) {
	t.Helper()
	err := dbx.WithTenantTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(f.ctx, `update costing_gl_voucher set next_attempt_at = now() where status = 'PENDING'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func netOf(f glFixture, account string) decimal.Decimal {
	b, _ := f.ledger.Balance(f.ctx, ledgerclient.BalanceQuery{LedgerID: f.glID, Account: account})
	return b.Net()
}

func TestPostgres_PPVVouchersPostReceiptAndInvoiceOncePerCostTransaction(t *testing.T) {
	f := newGLFixture(t, 21)
	f.record(t,
		f.ppvRow("ppv:RECEIPT:"+f.le+":GR-1:1", "100", "50.00", "1050.00", "2026-03-05"),
		f.ppvRow("ppv:INVOICE:"+f.le+":INV-1:1", "0", "30.00", "1050.00", "2026-03-20"),
		f.ppvRow("ppv:RECEIPT:"+f.le+":GR-2:1", "10", "0.00", "0.00", "2026-03-06"))
	res, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("result %+v err %v, want 2 booked (the zero-amount PPV has nothing to post)", res, err)
	}
	want := map[string]string{"1400": "1000", "5100": "80", "2100": "0", "2000": "-1080"}
	for account, net := range want {
		if got := netOf(f, account); got.String() != net {
			t.Errorf("net of %s = %s, want %s (GR/IR clears, AP takes the invoice, PPV carries both variances)", account, got, net)
		}
	}
	again, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(again.Booked) != 0 || len(f.ledger.Entries()) != 2 {
		t.Errorf("second run %+v err %v entries %d, want nothing more booked", again, err, len(f.ledger.Entries()))
	}
}

func TestPostgres_PPVVouchersRefuseALedgerOfAnotherEntityAndAMissingProfile(t *testing.T) {
	f := newGLFixture(t, 22)
	f.record(t, f.ppvRow("ppv:RECEIPT:"+f.le+":GR-1:1", "100", "50.00", "1050.00", "2026-03-05"))
	foreign := ledgerclient.NewFake()
	foreign.AddLedger(f.glID, ids.New())
	if _, err := f.s.PostCostVouchers(f.ctx, foreign, GLPostRequest{LegalEntityID: f.le}); problemCode(err) != CodeLedgerForeign || problemStatus(err) != http.StatusConflict {
		t.Errorf("foreign ledger: %v, want 409 %s", err, CodeLedgerForeign)
	}
	if len(foreign.Entries()) != 0 {
		t.Error("an entry reached a ledger of another legal entity")
	}
	if _, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: ids.New()}); problemCode(err) != CodeNoProfile {
		t.Errorf("no profile: %v, want %s", err, CodeNoProfile)
	}
}

func TestPostgres_FailingVouchersDoNotStarveNewerOnesAndParkAfterFiveAttempts(t *testing.T) {
	f := newGLFixture(t, 23)
	f.ledger.RefuseClosedThrough("2026-01-31")
	var rows []CostTransaction
	for i := 0; i < MaxVouchersPerRun+5; i++ {
		rows = append(rows, f.ppvRow(fmt.Sprintf("ppv:RECEIPT:%s:OLD-%03d:1", f.le, i), "1", "1.00", "11.00", "2026-01-15"))
	}
	for i := 0; i < 3; i++ {
		rows = append(rows, f.ppvRow(fmt.Sprintf("ppv:RECEIPT:%s:NEW-%d:1", f.le, i), "1", "1.00", "11.00", "2026-03-01"))
	}
	f.record(t, rows...)
	first, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(first.Failed) != MaxVouchersPerRun || len(first.Booked) != 0 || !first.More {
		t.Fatalf("first run: %d failed, %d booked, more %v, err %v; want the first batch of old rows failing", len(first.Failed), len(first.Booked), first.More, err)
	}
	second, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(second.Booked) != 3 || len(second.Failed) != 5 {
		t.Fatalf("second run: %d booked, %d failed, err %v; want the 3 newer rows booked past the 200 backed-off ones", len(second.Booked), len(second.Failed), err)
	}
	for _, b := range second.Booked {
		if b.Lines != 3 {
			t.Errorf("voucher has %d lines, want 3", b.Lines)
		}
	}

	single := newGLFixture(t, 24)
	single.ledger.RefuseClosedThrough("2026-01-31")
	single.record(t, single.ppvRow("ppv:RECEIPT:"+single.le+":GR-1:1", "1", "1.00", "11.00", "2026-01-15"))
	for attempt := 1; attempt <= MaxVoucherAttempts; attempt++ {
		single.dueNow(t)
		res, err := single.s.PostCostVouchers(single.ctx, single.ledger, GLPostRequest{LegalEntityID: single.le})
		if err != nil || len(res.Failed) != 1 || res.Failed[0].Code != ledgerclient.CodePeriodClosed {
			t.Fatalf("attempt %d: %+v err %v, want one failure with %s", attempt, res, err, ledgerclient.CodePeriodClosed)
		}
	}
	single.dueNow(t)
	res, err := single.s.PostCostVouchers(single.ctx, single.ledger, GLPostRequest{LegalEntityID: single.le})
	if err != nil || len(res.Failed) != 0 || res.Parked != 1 {
		t.Fatalf("after five attempts: %+v err %v, want the voucher parked and not tried", res, err)
	}
	rec, err := single.s.ReconcileInventory(single.ctx, single.ledger, single.le, "2026-03-31")
	if err != nil || rec.Parked != 1 || rec.ParkedReasons[ledgerclient.CodePeriodClosed] != 1 || rec.Unposted != 1 || rec.Ties {
		t.Fatalf("reconciliation %+v err %v, want one parked voucher with its reason and no tie", rec, err)
	}
	single.ledger.RefuseClosedThrough("")
	if _, err := single.s.SavePostingProfile(single.ctx, PostingProfile{LegalEntityID: single.le, GLLedgerID: single.glID, InventoryAccount: "1400", GRIRAccount: "2100",
		APAccount: "2000", PPVAccount: "5100", AdjustmentAccount: "5000"}, "tester"); err != nil {
		t.Fatal(err)
	}
	res, err = single.s.PostCostVouchers(single.ctx, single.ledger, GLPostRequest{LegalEntityID: single.le})
	if err != nil || len(res.Booked) != 1 || res.Parked != 0 {
		t.Fatalf("after saving the profile: %+v err %v, want the voucher retried and booked", res, err)
	}
}

func TestPostgres_ARefusedVoucherIsDerivedAgainFromTheCurrentProfile(t *testing.T) {
	f := newGLFixture(t, 25)
	f.record(t, f.ppvRow("ppv:RECEIPT:"+f.le+":GR-1:1", "100", "50.00", "1050.00", "2026-03-05"))
	f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "LEDGER_ACCOUNT_UNKNOWN"})
	res, err := f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(res.Failed) != 1 || res.Failed[0].Code != "LEDGER_ACCOUNT_UNKNOWN" {
		t.Fatalf("result %+v err %v, want the refusal reported", res, err)
	}
	f.ledger.FailPosts(nil)
	if _, err := f.s.SavePostingProfile(f.ctx, PostingProfile{LegalEntityID: f.le, GLLedgerID: f.glID, InventoryAccount: "1410", GRIRAccount: "2100",
		APAccount: "2000", PPVAccount: "5100", AdjustmentAccount: "5000"}, "tester"); err != nil {
		t.Fatal(err)
	}
	f.dueNow(t)
	res, err = f.s.PostCostVouchers(f.ctx, f.ledger, GLPostRequest{LegalEntityID: f.le})
	if err != nil || len(res.Booked) != 1 || netOf(f, "1410").String() != "1000" || !netOf(f, "1400").IsZero() {
		t.Fatalf("result %+v err %v net 1410 %s, want the voucher rebuilt on the new inventory account", res, err, netOf(f, "1410"))
	}
}
