package store

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

func creditSetup(t *testing.T) (*Postgres, context.Context, map[string]OpenItem) {
	t.Helper()
	s := setup(t)
	ctx := scopedCtx(42, legalEntity)
	// 1000.00 in three installments (333.34, 333.33, 333.33), and a credit note of 150.00 against it.
	if _, err := s.ApplyBillingDocument(ctx, invoiceDoc(t, invoiceDocID, "INV-2027-0000003",
		domain.BillingInstallment{No: 1, Amount: amt("333.34"), DueDate: "2027-04-30"},
		domain.BillingInstallment{No: 2, Amount: amt("333.33"), DueDate: "2027-05-30"},
		domain.BillingInstallment{No: 3, Amount: amt("333.33"), DueDate: "2027-06-30"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyBillingDocument(ctx, creditDoc(t, creditDocID, invoiceDocID, "CN-2027-0000001")); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListOpenItems(ctx, OpenItemFilter{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]OpenItem{}
	for _, it := range items {
		out[it.DocumentNumber] = it
	}
	return s, ctx, out
}

func TestSettleCredit_AgainstOriginalTakesTheOldestInstallmentFirst(t *testing.T) {
	s, ctx, items := creditSetup(t)
	credit := items["CN-2027-0000001"]
	res, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, AgainstOriginal: true}, "cs-1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Total.Equal(amt("150.00")) || res.CreditStatus != "SETTLED" || !res.CreditOpenAfter.IsZero() || len(res.Settlements) != 1 ||
		res.Settlements[0].DocumentNumber != "INV-2027-0000003/1" || !res.Settlements[0].OpenAfter.Equal(amt("183.34")) || res.Settlements[0].ItemStatus != "PARTIALLY_SETTLED" {
		t.Fatalf("result %+v", res)
	}
	replay, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, AgainstOriginal: true}, "cs-1")
	if err != nil || replay.BatchID != res.BatchID {
		t.Fatalf("replay %+v %v", replay, err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.CreditSettled); n != 1 {
		t.Fatalf("events %d", n)
	}
	if _, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, AgainstOriginal: true}, ""); problemCode(err) != CodeNotCredit {
		t.Fatalf("a settled credit: %v", err)
	}
	// The net balance of the customer is the same before and after: 1000 - 150 owed.
	if _, err := s.RunAgingSnapshot(ctx, legalEntity, parseDay("2027-07-31")); err != nil {
		t.Fatal(err)
	}
	snaps, err := s.ListBalanceSnapshots(ctx, SnapshotFilter{LegalEntityID: legalEntity, AsOf: "2027-07-31"}, 10)
	if err != nil || len(snaps) != 1 || !snaps[0].TotalOpen.Equal(amt("850.00")) {
		t.Fatalf("snapshot %+v %v", snaps, err)
	}
}

func TestSettleCredit_NamedDebitsAreCheckedAndAtomic(t *testing.T) {
	s, ctx, items := creditSetup(t)
	credit, i1, i2 := items["CN-2027-0000001"], items["INV-2027-0000003/1"], items["INV-2027-0000003/2"]
	cases := []struct {
		name   string
		debits []CreditDebit
		code   string
	}{
		{"more than the credit holds", []CreditDebit{{OpenItemID: i1.ID, Amount: amt("100.00")}, {OpenItemID: i2.ID, Amount: amt("60.00")}}, CodeCreditSettleInvalid},
		{"more than the debit has open", []CreditDebit{{OpenItemID: i1.ID, Amount: amt("400.00")}}, CodeCreditSettleInvalid},
		{"a credit as a debit", []CreditDebit{{OpenItemID: credit.ID, Amount: amt("10.00")}}, CodeNotDebit},
		{"the same debit twice", []CreditDebit{{OpenItemID: i1.ID, Amount: amt("10.00")}, {OpenItemID: i1.ID, Amount: amt("10.00")}}, CodeCreditSettleInvalid},
	}
	for _, c := range cases {
		if _, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, Debits: c.debits}, ""); problemCode(err) != c.code {
			t.Fatalf("%s: got %v, want %s", c.name, err, c.code)
		}
	}
	got := itemByID(t, s, ctx, credit.ID)
	if !got.OpenAmount.Equal(amt("-150.00")) || got.Status != "OPEN" {
		t.Fatalf("a refused settlement changed the credit: %+v", got)
	}
	res, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID,
		Debits: []CreditDebit{{OpenItemID: i1.ID, Amount: amt("100.00")}, {OpenItemID: i2.ID, Amount: amt("30.00")}}}, "")
	if err != nil || !res.Total.Equal(amt("130.00")) || res.CreditStatus != "PARTIALLY_SETTLED" || !res.CreditOpenAfter.Equal(amt("-20.00")) {
		t.Fatalf("settle: %+v %v", res, err)
	}
	if again, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, Debits: []CreditDebit{{OpenItemID: i2.ID, Amount: amt("20.00")}}}, ""); err != nil ||
		again.CreditStatus != "SETTLED" {
		t.Fatalf("second settlement: %+v %v", again, err)
	}
}

func TestSettleCredit_AsOfStatementAddsALaterSettlementBack(t *testing.T) {
	s, ctx, items := creditSetup(t)
	credit := items["CN-2027-0000001"]
	if _, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, AgainstOriginal: true}, ""); err != nil {
		t.Fatal(err)
	}
	execAdmin(t, s, `update credit_settlement set settled_at = '2027-06-01'`)
	// Settled on 2027-06-01: as of an earlier date the credit and the installment are still open in full.
	after, err := s.Statement(ctx, legalEntity, billCustomer, parseDay("2027-06-30"))
	if err != nil || len(after) != 1 || len(after[0].Lines) != 3 {
		t.Fatalf("statement after the settlement %+v %v", after, err)
	}
	secs, err := s.Statement(ctx, legalEntity, billCustomer, parseDay("2027-04-15"))
	if err != nil || len(secs) != 1 {
		t.Fatalf("statement %+v %v", secs, err)
	}
	open := map[string]string{}
	for _, l := range secs[0].Lines {
		open[l.DocumentNumber] = l.OpenAmount.String()
	}
	if open["CN-2027-0000001"] != "-150" || open["INV-2027-0000003/1"] != "333.34" {
		t.Fatalf("as-of lines %+v", open)
	}
}
