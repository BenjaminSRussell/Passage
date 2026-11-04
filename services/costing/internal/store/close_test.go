package store

import (
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

func mustDecimal(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func (f glFixture) bookProvisionalStock(t *testing.T, inventory, offset string) {
	t.Helper()
	for _, e := range []ledgerclient.Entry{
		{LegalEntityID: f.le, LedgerID: f.glID, DocumentID: "RCV", PostingDate: "2026-01-20", Currency: "EUR", IdempotencyKey: "rcv", Lines: []ledgerclient.Line{
			{Account: "1400", Amount: mustDecimal(inventory)}, {Account: "2100", Credit: true, Amount: mustDecimal(inventory)}}},
		{LegalEntityID: f.le, LedgerID: f.glID, DocumentID: "ISS", PostingDate: "2026-01-25", Currency: "EUR", IdempotencyKey: "iss", Lines: []ledgerclient.Line{
			{Account: "5000", Amount: mustDecimal(offset)}, {Account: "1400", Credit: true, Amount: mustDecimal(offset)}}},
	} {
		if _, err := f.ledger.Post(f.ctx, e); err != nil {
			t.Fatal(err)
		}
	}
}

func postEntry(f glFixture, doc, date, account, amount string) error {
	_, err := f.ledger.Post(f.ctx, ledgerclient.Entry{LegalEntityID: f.le, LedgerID: f.glID, DocumentID: doc, PostingDate: date, Currency: "EUR", IdempotencyKey: doc,
		Lines: []ledgerclient.Line{{Account: account, Amount: mustDecimal(amount)}, {Account: "2100", Credit: true, Amount: mustDecimal(amount)}}})
	return err
}

func TestPostgres_CloseSettlesTheIssueLocksThePeriodPostsTheAdjustmentAndTiesToTheLedger(t *testing.T) {
	f := newGLFixture(t, 26)
	product, site := ids.New(), ids.New()
	f.record(t,
		costRow("R1", f.le, product, site, domain.KindFinancial, "100", "1000.00", "2026-01-05"),
		costRow("R2", f.le, product, site, domain.KindFinancial, "100", "1200.00", "2026-01-20"),
		costRow("I1", f.le, product, site, domain.KindFinancial, "-150", "-1500.00", "2026-01-25"))
	f.bookProvisionalStock(t, "2200.00", "1500.00")

	c, err := f.s.RunClose(f.ctx, f.ledger, CloseRequest{LegalEntityID: f.le, PeriodEnd: "2026-01-31"}, "tester")
	if err != nil || c.Status != "COMPLETED" || c.Groups != 1 || c.AdjustedGroups != 1 || c.AdjustmentTotal != "-100.0000" || c.JournalEntryID == "" {
		t.Fatalf("close %+v err %v, want FIFO issue cost 1600.00 booked at 1500.00 and a -100.00 adjustment voucher", c, err)
	}
	if got := netOf(f, "5000").String(); got != "1600" {
		t.Errorf("adjustment account net = %s, want 1600 (issue cost 1500 plus the 100 adjustment)", got)
	}
	rec, err := f.s.ReconcileInventory(f.ctx, f.ledger, f.le, "2026-01-31")
	if err != nil || !rec.Ties || rec.Inventory.String() != "600" || rec.Ledger.String() != "600" || rec.BookedByCosting.String() != "-100" {
		t.Fatalf("reconciliation %+v err %v, want value 600.00 tied to the ledger with -100.00 booked by costing", rec, err)
	}

	replay, err := f.s.RunClose(f.ctx, f.ledger, CloseRequest{LegalEntityID: f.le, PeriodEnd: "2026-01-31"}, "tester")
	if err != nil || replay.ID != c.ID || len(f.ledger.Entries()) != 3 {
		t.Errorf("replay %+v err %v entries %d, want the same close and no new entry", replay, err, len(f.ledger.Entries()))
	}
	if _, err := f.s.RecordCostTransactions(f.ctx, []CostTransaction{costRow("F1", f.le, product, site, domain.KindFinancial, "10", "100.00", "2026-02-10")}); err != nil {
		t.Fatal(err)
	}
	if err := postEntry(f, "FEB", "2026-02-10", "1400", "100.00"); err != nil {
		t.Fatal(err)
	}
	if rec, err := f.s.ReconcileInventory(f.ctx, f.ledger, f.le, "2026-01-31"); err != nil || !rec.Ties || rec.Ledger.String() != "600" {
		t.Errorf("January tie-out with February postings %+v err %v, want 600.00 tied", rec, err)
	}
	for _, asOf := range []string{"2026-01-15", "2026-02-27", "2026-01-31x"} {
		if _, err := f.s.ReconcileInventory(f.ctx, f.ledger, f.le, asOf); problemCode(err) != CodeTieOutPeriod || problemStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("asOf %s: %v, want 422 %s", asOf, err, CodeTieOutPeriod)
		}
	}
	late := costRow("R3", f.le, product, site, domain.KindFinancial, "10", "100.00", "2026-01-30")
	if _, err := f.s.RecordCostTransactions(f.ctx, []CostTransaction{late}); problemCode(err) != CodePeriodLocked {
		t.Errorf("a row inside the closed period: %v, want %s", err, CodePeriodLocked)
	}
	late.SourceKey, late.FinancialDate = "R4", "2026-02-01"
	if _, err := f.s.RecordCostTransactions(f.ctx, []CostTransaction{late}); err != nil {
		t.Errorf("a row after the period end: %v", err)
	}
	if _, err := f.s.RunClose(f.ctx, f.ledger, CloseRequest{LegalEntityID: f.le, PeriodEnd: "2026-01-15"}, "tester"); problemCode(err) != CodeCloseOrder || problemStatus(err) != http.StatusConflict {
		t.Errorf("an earlier close: %v, want 409 %s", err, CodeCloseOrder)
	}
}

func TestPostgres_ACloseThatStoppedResumesWithoutAdjustingTwice(t *testing.T) {
	f := newGLFixture(t, 27)
	product, site := ids.New(), ids.New()
	f.record(t,
		costRow("R1", f.le, product, site, domain.KindFinancial, "100", "1000.00", "2026-01-05"),
		costRow("I1", f.le, product, site, domain.KindFinancial, "-50", "-400.00", "2026-01-25"))
	f.bookProvisionalStock(t, "1000.00", "400.00")
	f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusBadGateway, Code: "LEDGER_DOWN"})
	if _, err := f.s.RunClose(f.ctx, f.ledger, CloseRequest{LegalEntityID: f.le, PeriodEnd: "2026-01-31"}, "tester"); err == nil {
		t.Fatal("close completed while the ledger refused its voucher")
	}
	if _, err := f.s.RecordCostTransactions(f.ctx, []CostTransaction{costRow("L1", f.le, product, site, domain.KindFinancial, "1", "10.00", "2026-01-31")}); problemCode(err) != CodePeriodLocked {
		t.Errorf("a stopped close must keep its lock: %v", err)
	}
	f.ledger.FailPosts(nil)
	c, err := f.s.RunClose(f.ctx, f.ledger, CloseRequest{LegalEntityID: f.le, PeriodEnd: "2026-01-31"}, "tester")
	if err != nil || c.Status != "COMPLETED" || c.AdjustedGroups != 1 || c.AdjustmentTotal != "-100.0000" {
		t.Fatalf("resumed close %+v err %v, want one -100.00 adjustment (FIFO cost 500.00 against 400.00 booked)", c, err)
	}
	rec, err := f.s.ReconcileInventory(f.ctx, f.ledger, f.le, "2026-01-31")
	if err != nil || !rec.Ties || rec.Inventory.String() != "500" {
		t.Fatalf("reconciliation %+v err %v, want 500.00 tied", rec, err)
	}
}

func TestPostgres_CloseNeedsAProfileAndRefusesAForeignLedger(t *testing.T) {
	f := newGLFixture(t, 28)
	if _, err := f.s.RunClose(f.ctx, f.ledger, CloseRequest{LegalEntityID: ids.New(), PeriodEnd: "2026-01-31"}, "tester"); problemCode(err) != CodeNoProfile {
		t.Errorf("no profile: %v, want %s", err, CodeNoProfile)
	}
	foreign := ledgerclient.NewFake()
	foreign.AddLedger(f.glID, ids.New())
	if _, err := f.s.RunClose(f.ctx, foreign, CloseRequest{LegalEntityID: f.le, PeriodEnd: "2026-01-31"}, "tester"); problemCode(err) != CodeLedgerForeign {
		t.Errorf("foreign ledger: %v, want %s", err, CodeLedgerForeign)
	}
}
