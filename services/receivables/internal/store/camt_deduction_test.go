package store

import (
	"context"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

func camtCredit(stmt, ref, amount string) domain.CAMTCredit {
	return domain.CAMTCredit{Line: 8, StatementID: stmt, Reference: ref, Currency: "EUR", Amount: amt(amount), ValueDate: "2027-03-20"}
}

// FR-30.5 and BR-30.7: each booked credit is one unidentified CAMT053 payment; a
// repeated file is AR-0003; an overlapping statement does not import an entry twice.
func TestImportBankStatement_PaymentsDuplicatesAndOverlap(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(5)
	first := camtCredit("STMT-0327", "E2E-1", "2380.00")
	first.DebtorName, first.RemittanceRef = "Acme GmbH", "INV-2001"
	in := BankStatementImport{LegalEntityID: legalEntity, FileSHA256: strings.Repeat("b", 64), StatementCount: 1, SkippedCount: 3,
		Credits: []domain.CAMTCredit{first, camtCredit("STMT-0327", "E2E-2", "75.00")}}

	batch, err := s.ImportBankStatement(ctx, in)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if batch.PaymentCount != 2 || batch.DuplicateCount != 0 || batch.SkippedCount != 3 || len(batch.Payments) != 2 || batch.ImportedAt == nil {
		t.Fatalf("batch = %+v, want two payments and the skipped count", batch)
	}
	pay := batch.Payments[0]
	if pay.Source != "CAMT053" || pay.PaymentMode != "WIRE" || pay.Status != "RECEIVED" || pay.CustomerID != nil ||
		pay.SourceRef == nil || *pay.SourceRef != "STMT-0327:E2E-1" || pay.PayerName == nil || *pay.PayerName != "Acme GmbH" ||
		pay.ValueDate != "2027-03-20" || !pay.UnappliedAmount.Equal(amt("2380")) {
		t.Fatalf("payment = %+v, want an unidentified CAMT053 wire keyed by statement and entry", pay)
	}
	if len(pay.Remittance) != 1 || pay.Remittance[0].Reference != "INV-2001" || !pay.Remittance[0].Amount.Equal(amt("2380")) {
		t.Fatalf("remittance = %+v, want the creditor reference for the whole amount", pay.Remittance)
	}
	if len(batch.Payments[1].Remittance) != 0 || batch.Payments[1].PayerName != nil {
		t.Fatalf("second payment = %+v, want no remittance and no payer", batch.Payments[1])
	}
	if n := countOutbox(t, s, ctx, rcvevents.PaymentReceived); n != 2 {
		t.Fatalf("payment.received events = %d, want 2", n)
	}

	if _, err := s.ImportBankStatement(ctx, in); problemCode(err) != CodeLockboxDuplicate {
		t.Fatalf("same file again: %v, want AR-0003", err)
	}

	// The bank re-sends the statement with one more entry: only the new one is created.
	overlap := in
	overlap.FileSHA256 = strings.Repeat("c", 64)
	overlap.Credits = append(append([]domain.CAMTCredit{}, in.Credits...), camtCredit("STMT-0327", "E2E-3", "10.00"))
	again, err := s.ImportBankStatement(ctx, overlap)
	if err != nil {
		t.Fatalf("overlapping statement: %v", err)
	}
	if again.PaymentCount != 1 || again.DuplicateCount != 2 || *again.Payments[0].SourceRef != "STMT-0327:E2E-3" {
		t.Fatalf("overlap = %+v, want one new payment and two duplicates", again)
	}
	// The same entry reference in another statement is a different entry.
	other := BankStatementImport{LegalEntityID: legalEntity, FileSHA256: strings.Repeat("d", 64), StatementCount: 1,
		Credits: []domain.CAMTCredit{camtCredit("STMT-0328", "E2E-1", "5.00")}}
	if b, err := s.ImportBankStatement(ctx, other); err != nil || b.PaymentCount != 1 {
		t.Fatalf("next statement: %+v %v", b, err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.PaymentReceived); n != 4 {
		t.Fatalf("payment.received events = %d, want 4", n)
	}

	list, err := s.ListBankStatementImports(ctx, 10)
	if err != nil || len(list) != 3 || list[2].ID != batch.ID || list[2].PaymentCount != 2 || list[1].DuplicateCount != 2 || len(list[0].Payments) != 0 {
		t.Fatalf("list = %+v (%v), want three files, newest first, without payments", list, err)
	}
	if other, err := s.ListBankStatementImports(tenantCtx(6), 10); err != nil || len(other) != 0 {
		t.Fatalf("another tenant sees %d imports (%v)", len(other), err)
	}
}

// shortPaid seeds an invoice and settles it with a short payment, leaving short open.
func shortPaid(t *testing.T, s *Postgres, ctx context.Context, doc, total, paid, key string) (OpenItem, Payment) {
	t.Helper()
	item := seedItem(t, s, ctx, doc, custA, "2027-03-31", total, "USD")
	pay := newPayment(t, s, ctx, ptr(custA), paid, "USD", key)
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt(paid)}}}, ""); err != nil {
		t.Fatalf("apply short payment: %v", err)
	}
	return item, pay
}

func openItem(t *testing.T, s *Postgres, ctx context.Context, id string) OpenItem {
	t.Helper()
	items, err := s.ListOpenItems(ctx, OpenItemFilter{}, 500)
	if err != nil {
		t.Fatalf("list open items: %v", err)
	}
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("open item %s not found", id)
	return OpenItem{}
}

// customerBalance is the customer's balance in a balance confirmation run dated after every movement.
func customerBalance(t *testing.T, s *Postgres, ctx context.Context, cust, key string) decimal.Decimal {
	t.Helper()
	run, err := s.CreateConfirmationRun(ctx, ConfirmationRun{LegalEntityID: legalEntity, AsOfDate: "2099-12-31", IncludeZero: true, ReminderDays: 14}, key)
	if err != nil {
		t.Fatalf("confirmation run: %v", err)
	}
	for _, c := range run.Confirmations {
		if c.CustomerID == cust {
			return c.OurBalance
		}
	}
	t.Fatalf("customer %s not in the run", cust)
	return decimal.Zero
}

// AC-30.13: a payment short by 300.00 USD with reason "promo"; the deduction is
// matched to a 300.00 promotion fund accrual and approved, and the approval is
// published for billing's credit note and the fund usage.
func TestDeduction_AC3013ShortPayMatchApprove(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(7)
	item, pay := shortPaid(t, s, ctx, "INV-DED-1", "1000.00", "700.00", "pay-ded-1")
	in := Deduction{LegalEntityID: legalEntity, OpenItemID: item.ID, PaymentID: &pay.ID, Type: "PROMO", ReasonCode: "promo", Amount: amt("300.00")}

	ded, err := s.CreateDeduction(ctx, in, "ded-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ded.Status != domain.DeductionOpen || ded.CustomerID != custA || ded.Currency != "USD" || ded.DocumentNumber != "INV-DED-1" || ded.Version != 1 {
		t.Fatalf("deduction = %+v, want OPEN with the item's customer and currency", ded)
	}
	if after := openItem(t, s, ctx, item.ID); !after.OpenAmount.IsZero() || after.Status != "SETTLED" {
		t.Fatalf("invoice = %s %s, want the 300.00 moved off it (SETTLED)", after.OpenAmount, after.Status)
	}
	var opened rcvevents.DeductionOpenedData
	lastEvent(t, s, ctx, rcvevents.DeductionOpened, &opened)
	if opened.DeductionID != ded.ID || opened.OpenItemID != item.ID || opened.PaymentID == nil || *opened.PaymentID != pay.ID || !opened.Amount.Equal(amt("300")) {
		t.Fatalf("deduction.opened = %+v", opened)
	}
	if replay, err := s.CreateDeduction(ctx, in, "ded-1"); err != nil || replay.ID != ded.ID {
		t.Fatalf("replay = %v (%v), want the stored deduction", replay.ID, err)
	}
	changed := in
	changed.Amount = amt("299.00")
	if _, err := s.CreateDeduction(ctx, changed, "ded-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("other body under the same key: %v", err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.DeductionOpened); n != 1 {
		t.Fatalf("opened events = %d, want 1: a replay publishes nothing", n)
	}
	// While the deduction is worked the customer still owes it.
	if got := customerBalance(t, s, ctx, custA, "bal-open"); !got.Equal(amt("300")) {
		t.Fatalf("balance with the deduction open = %s, want 300.00", got)
	}

	if _, err := s.ApproveDeduction(ctx, ded.ID, ded.Version, nil); problemCode(err) != domain.CodeDeductionState {
		t.Fatalf("approve before matching: %v", err)
	}
	fund := ClaimMatch{Type: "PROMOTION_FUND", Ref: "FUND-2027-SPRING", Remaining: amt("300.00")}
	if _, err := s.MatchDeduction(ctx, ded.ID, ded.Version+1, fund); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	matched, err := s.MatchDeduction(ctx, ded.ID, ded.Version, fund)
	if err != nil || matched.Status != domain.DeductionMatched || matched.ClaimRef == nil || *matched.ClaimRef != "FUND-2027-SPRING" || matched.Version != 2 {
		t.Fatalf("match = %+v (%v)", matched, err)
	}
	approved, err := s.ApproveDeduction(ctx, ded.ID, matched.Version, ptr("fund confirmed"))
	if err != nil || approved.Status != domain.DeductionApproved || approved.ResolvedAt == nil || approved.DecisionNote == nil {
		t.Fatalf("approve = %+v (%v)", approved, err)
	}
	if got := customerBalance(t, s, ctx, custA, "bal-approved"); !got.IsZero() {
		t.Fatalf("balance after approval = %s, want zero: the claim's credit note takes the 300.00", got)
	}
	var resolved rcvevents.DeductionResolvedData
	lastEvent(t, s, ctx, rcvevents.DeductionResolved, &resolved)
	if resolved.Resolution != domain.DeductionApproved || !resolved.Amount.Equal(amt("300")) || resolved.ClaimType == nil ||
		*resolved.ClaimType != "PROMOTION_FUND" || resolved.ClaimRef == nil || *resolved.ClaimRef != "FUND-2027-SPRING" || len(resolved.Postings) != 0 {
		t.Fatalf("deduction.resolved = %+v", resolved)
	}
	if _, err := s.DenyDeduction(ctx, ded.ID, approved.Version, "EXPIRED", nil); problemCode(err) != domain.CodeDeductionState {
		t.Fatalf("deny after approval: %v", err)
	}
	if _, err := s.GetDeduction(tenantCtx(8), ded.ID); problemCode(err) != CodeDeductionNotFound {
		t.Fatalf("another tenant reads the deduction: %v", err)
	}
}

// BR-30.12: the claim bounds approval, a split's children sum to the parent, the
// write-off limit needs receivables.write-off.approve above it, and a denial re-bills.
func TestDeduction_GuardsSplitWriteOffAndDeny(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(9)
	item, _ := shortPaid(t, s, ctx, "INV-DED-2", "500.00", "100.00", "pay-ded-2")
	base := Deduction{LegalEntityID: legalEntity, OpenItemID: item.ID, Type: "SHORT_SHIP", ReasonCode: "2 cartons short"}

	tooMuch := base
	tooMuch.Amount = amt("400.01")
	if _, err := s.CreateDeduction(ctx, tooMuch, "ded-big"); problemCode(err) != domain.CodeAmountRange {
		t.Fatalf("more than the item's open amount: %v", err)
	}
	otherEntity := base
	otherEntity.LegalEntityID, otherEntity.Amount = "00000000-0000-0000-0000-0000000000a2", amt("1.00")
	if _, err := s.CreateDeduction(ctx, otherEntity, "ded-le"); problemCode(err) != domain.CodeDeductionMismatch {
		t.Fatalf("item of another legal entity: %v", err)
	}
	foreign := newPayment(t, s, ctx, ptr(custB), "10.00", "USD", "pay-foreign")
	wrongPayment := base
	wrongPayment.PaymentID, wrongPayment.Amount = &foreign.ID, amt("1.00")
	if _, err := s.CreateDeduction(ctx, wrongPayment, "ded-pay"); problemCode(err) != domain.CodeDeductionMismatch {
		t.Fatalf("payment of another customer: %v", err)
	}

	in := base
	in.Amount = amt("200.00")
	ded, err := s.CreateDeduction(ctx, in, "ded-2")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if after := openItem(t, s, ctx, item.ID); !after.OpenAmount.Equal(amt("200")) || after.Status != "PARTIALLY_SETTLED" {
		t.Fatalf("invoice = %s %s, want 200.00 still open", after.OpenAmount, after.Status)
	}
	if _, err := s.MatchDeduction(ctx, ded.ID, ded.Version, ClaimMatch{Type: "REBATE_ACCRUAL", Ref: "RB-1", Remaining: amt("150.00")}); problemCode(err) != domain.CodeDeductionExceedsClaim {
		t.Fatalf("claim smaller than the deduction: %v, want AR-0014", err)
	}
	if _, err := s.SplitDeduction(ctx, ded.ID, ded.Version, []decimal.Decimal{amt("150.00"), amt("49.99")}); problemCode(err) != domain.CodeDeductionSplit {
		t.Fatalf("parts not summing to the parent: %v", err)
	}
	parent, err := s.SplitDeduction(ctx, ded.ID, ded.Version, []decimal.Decimal{amt("150.00"), amt("50.00")})
	if err != nil || parent.Status != domain.DeductionSplit || len(parent.Children) != 2 {
		t.Fatalf("split = %+v (%v)", parent, err)
	}
	sum := decimal.Zero
	for _, c := range parent.Children {
		if c.Status != domain.DeductionOpen || c.ParentID == nil || *c.ParentID != ded.ID || c.OpenItemID != item.ID || c.Type != "SHORT_SHIP" {
			t.Fatalf("child = %+v, want an OPEN copy on the same item", c)
		}
		sum = sum.Add(c.Amount)
	}
	if !sum.Equal(parent.Amount) {
		t.Fatalf("children sum to %s, want %s", sum, parent.Amount)
	}
	var split rcvevents.DeductionResolvedData
	lastEvent(t, s, ctx, rcvevents.DeductionResolved, &split)
	if split.Resolution != domain.DeductionSplit || len(split.ChildIDs) != 2 {
		t.Fatalf("split event = %+v", split)
	}
	if n := countOutbox(t, s, ctx, rcvevents.DeductionOpened); n != 3 {
		t.Fatalf("opened events = %d, want the parent and both children", n)
	}
	big, small := parent.Children[0], parent.Children[1]

	// 150.00 USD is above the 50.00 limit: the clerk is refused, the approver is not.
	if _, err := s.WriteOffDeduction(ctx, big.ID, big.Version, false, nil); problemCode(err) != domain.CodeWriteOffTolerance {
		t.Fatalf("write-off above the limit without approval: %v", err)
	}
	if _, err := s.MatchDeduction(ctx, big.ID, big.Version, ClaimMatch{Type: "REBATE_ACCRUAL", Ref: "RB-1", Remaining: amt("150.00")}); err != nil {
		t.Fatalf("the 150.00 part fits the claim: %v", err)
	}
	cur, _ := s.GetDeduction(ctx, big.ID)
	written, err := s.WriteOffDeduction(ctx, big.ID, cur.Version, true, ptr("claim expired after match"))
	if err != nil || written.Status != domain.DeductionWrittenOff {
		t.Fatalf("approved write-off: %+v %v", written, err)
	}
	var wo rcvevents.DeductionResolvedData
	lastEvent(t, s, ctx, rcvevents.DeductionResolved, &wo)
	if wo.Resolution != domain.DeductionWrittenOff || len(wo.Postings) != 2 || wo.Postings[0].Account != "6110" ||
		!wo.Postings[0].Debit.Equal(amt("150")) || wo.Postings[1].Account != "1200" || !wo.Postings[1].Credit.Equal(amt("150")) {
		t.Fatalf("write-off event = %+v, want Dr 6110 / Cr 1200 150.00", wo)
	}

	denied, err := s.DenyDeduction(ctx, small.ID, small.Version, "NOT_AUTHORIZED", ptr("no promotion on file"))
	if err != nil || denied.Status != domain.DeductionDenied || denied.RebillItemID == nil || denied.DenialReason == nil {
		t.Fatalf("deny = %+v (%v)", denied, err)
	}
	// 500 - 100 paid - 150 written off; the denied 50.00 is owed again on the re-bill, not twice.
	if got := customerBalance(t, s, ctx, custA, "bal-deny"); !got.Equal(amt("250")) {
		t.Fatalf("balance after write-off and denial = %s, want 250.00", got)
	}
	rebill := openItem(t, s, ctx, *denied.RebillItemID)
	if rebill.SourceType != "DEDUCTION" || rebill.CustomerID != custA || !rebill.OpenAmount.Equal(amt("50")) || rebill.Status != "OPEN" || rebill.Currency != "USD" {
		t.Fatalf("re-bill = %+v, want a 50.00 USD DEDUCTION debit item for the customer", rebill)
	}

	// A disputed item takes no deduction; one at the limit is written off by a clerk.
	disputedItem := seedItem(t, s, ctx, "INV-DED-3", custA, "2027-03-31", "80.00", "USD")
	if _, err := s.OpenDispute(ctx, Dispute{LegalEntityID: legalEntity, OpenItemID: disputedItem.ID, ReasonCode: "PRICE", DisputedAmount: amt("80.00"), Owner: "ana"}, "dsp-ded"); err != nil {
		t.Fatalf("dispute: %v", err)
	}
	onDisputed := base
	onDisputed.OpenItemID, onDisputed.Amount = disputedItem.ID, amt("10.00")
	if _, err := s.CreateDeduction(ctx, onDisputed, "ded-disp"); problemCode(err) != domain.CodeDisputeExists {
		t.Fatalf("deduction on a disputed item: %v", err)
	}
	limitItem := seedItem(t, s, ctx, "INV-DED-4", custB, "2027-03-31", "50.00", "USD")
	atLimit := base
	atLimit.OpenItemID, atLimit.Amount, atLimit.Type = limitItem.ID, amt("50.00"), "UNKNOWN"
	d4, err := s.CreateDeduction(ctx, atLimit, "ded-4")
	if err != nil {
		t.Fatalf("create at limit: %v", err)
	}
	if _, err := s.WriteOffDeduction(ctx, d4.ID, d4.Version, false, nil); err != nil {
		t.Fatalf("write-off at the 50.00 limit: %v", err)
	}

	open, err := s.ListDeductions(ctx, DeductionFilter{Status: domain.DeductionOpen}, 50)
	if err != nil || len(open) != 0 {
		t.Fatalf("open deductions = %d (%v), want none", len(open), err)
	}
	all, err := s.ListDeductions(ctx, DeductionFilter{Type: "SHORT_SHIP", CustomerID: custA}, 50)
	if err != nil || len(all) != 3 {
		t.Fatalf("SHORT_SHIP deductions of the customer = %d (%v), want the parent and two children", len(all), err)
	}
}
