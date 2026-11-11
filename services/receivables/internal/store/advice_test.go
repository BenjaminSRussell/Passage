package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

func adviceSetup(t *testing.T) (*Postgres, Payment, OpenItem) {
	t.Helper()
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, PaymentMode: "WIRE", Source: "MANUAL", SourceRef: ptr("BANK-7781"),
		Amount: amt("1000.00"), UnappliedAmount: amt("1000.00"), Currency: "USD", ReceivedDate: "2027-03-20", ValueDate: "2027-03-20"}, "adv-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, pay, item
}

func TestRemittanceAdvice_AttachingTheInvoiceLetsTheMatcherAutoApply(t *testing.T) {
	s, pay, item := adviceSetup(t)
	ctx := tenantCtx(1)
	// An unidentified payment of exactly 1000.00 scores only R2 (30): below 60, so it is ON_ACCOUNT and is not applied.
	first, err := s.AutoApplyPayments(ctx, []string{pay.ID})
	if err != nil || first.Applied != 0 || first.OnAccount != 1 {
		t.Fatalf("without advice: %+v %v", first, err)
	}
	lines := []domain.AdviceLine{
		{PaymentRef: "BANK-7781", References: []domain.RemittanceLine{{Reference: "INV-30001", Amount: amt("1000.00")}}},
		{PaymentRef: "NOPE", References: []domain.RemittanceLine{{Reference: "INV-30001", Amount: amt("1.00")}}},
	}
	res, err := s.ApplyRemittanceAdvice(ctx, legalEntity, lines)
	if err != nil || res.Attached != 1 || res.Skipped != 1 || res.Lines[0].Outcome != AdviceAttached || res.Lines[1].Outcome != AdviceUnmatched {
		t.Fatalf("advice: %+v %v", res, err)
	}
	if again, err := s.ApplyRemittanceAdvice(ctx, legalEntity, lines[:1]); err != nil || again.Attached != 1 {
		t.Fatalf("a repeat of the advice: %+v %v", again, err)
	}
	got, err := s.AutoApplyPayments(ctx, res.PaymentIDs)
	if err != nil || got.Applied != 1 || len(got.AppliedPaymentIDs) != 1 {
		t.Fatalf("with advice: %+v %v", got, err)
	}
	if p, _ := s.GetPayment(ctx, pay.ID); p.Status != "APPLIED" || !p.UnappliedAmount.IsZero() {
		t.Fatalf("payment %+v", p)
	}
	if it := itemByID(t, s, ctx, item.ID); it.Status != "SETTLED" {
		t.Fatalf("item %+v", it)
	}
	// An applied payment ignores a later advice; so does a second auto-application.
	late, err := s.ApplyRemittanceAdvice(ctx, legalEntity, lines[:1])
	if err != nil || late.Lines[0].Outcome != AdviceApplied || late.Attached != 0 {
		t.Fatalf("late advice: %+v %v", late, err)
	}
	if rerun, err := s.AutoApplyPayments(ctx, []string{pay.ID}); err != nil || rerun.Applied != 0 {
		t.Fatalf("rerun: %+v %v", rerun, err)
	}
}

func TestRemittanceAdvice_ReferencesAboveThePaymentAreRefusedForTheLine(t *testing.T) {
	s, _, _ := adviceSetup(t)
	res, err := s.ApplyRemittanceAdvice(tenantCtx(1), legalEntity, []domain.AdviceLine{
		{PaymentRef: "BANK-7781", References: []domain.RemittanceLine{{Reference: "INV-30001", Amount: decimal.RequireFromString("1000.01")}}}})
	if err != nil || res.Lines[0].Outcome != AdviceInvalid || res.Attached != 0 {
		t.Fatalf("advice: %+v %v", res, err)
	}
}

func TestRemittanceAdvice_FindsABankStatementPaymentByItsEntryReference(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	seedInvoice(t, s, ctx)
	if _, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, PaymentMode: "WIRE", Source: "MANUAL", SourceRef: ptr("STMT-1:ENTRY-42"),
		Amount: amt("500.00"), UnappliedAmount: amt("500.00"), Currency: "USD", ReceivedDate: "2027-03-20", ValueDate: "2027-03-20"}, "adv-2"); err != nil {
		t.Fatal(err)
	}
	res, err := s.ApplyRemittanceAdvice(ctx, legalEntity, []domain.AdviceLine{
		{PaymentRef: "ENTRY-42", References: []domain.RemittanceLine{{Reference: "INV-30001", Amount: amt("500.00")}}}})
	if err != nil || res.Attached != 1 {
		t.Fatalf("advice: %+v %v", res, err)
	}
}

// 30-4: the scheduled pass applies what the matcher is sure of, leaves the rest, and goes to the payments considered longest ago first.
func TestAutoApplyDue_AppliesSurePaymentsAndRotatesTheRest(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	sure, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, PaymentMode: "WIRE", Source: "MANUAL", SourceRef: ptr("BANK-1"),
		Amount: amt("1000.00"), UnappliedAmount: amt("1000.00"), Currency: "USD", ReceivedDate: "2027-03-20", ValueDate: "2027-03-20",
		Remittance: []domain.RemittanceLine{{Reference: "INV-30001", Amount: amt("1000.00")}}}, "due-1")
	if err != nil {
		t.Fatal(err)
	}
	unsure, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, PaymentMode: "WIRE", Source: "MANUAL", SourceRef: ptr("BANK-2"),
		Amount: amt("77.00"), UnappliedAmount: amt("77.00"), Currency: "USD", ReceivedDate: "2027-03-21", ValueDate: "2027-03-21"}, "due-2")
	if err != nil {
		t.Fatal(err)
	}
	entities, err := s.AutoApplyLegalEntities(ctx)
	if err != nil || len(entities) != 1 || entities[0] != legalEntity {
		t.Fatalf("entities = %v, %v", entities, err)
	}
	res, err := s.AutoApplyDue(ctx, 10)
	if err != nil || res.Applied != 1 || res.OnAccount != 1 || res.AppliedPaymentIDs[0] != sure.ID {
		t.Fatalf("first pass = %+v, %v", res, err)
	}
	if p, _ := s.GetPayment(ctx, sure.ID); p.Status != "APPLIED" {
		t.Fatalf("the sure payment is %s", p.Status)
	}
	if it := itemByID(t, s, ctx, item.ID); it.Status != "SETTLED" {
		t.Fatalf("item %+v", it)
	}
	// Only the payment left over is considered again, and a batch of one still reaches it.
	if again, err := s.AutoApplyDue(ctx, 1); err != nil || again.Applied != 0 || again.OnAccount != 1 {
		t.Fatalf("second pass = %+v, %v", again, err)
	}
	if p, _ := s.GetPayment(ctx, unsure.ID); p.Status == "APPLIED" {
		t.Fatalf("an on-account payment was applied")
	}
	var stamped int
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from payment where auto_apply_at is not null`).Scan(&stamped)
	}); err != nil || stamped != 2 {
		t.Fatalf("payments marked considered = %d (%v)", stamped, err)
	}
	if entities, _ := s.AutoApplyLegalEntities(ctx); len(entities) != 1 {
		t.Errorf("the legal entity with a payment left over vanished from the list: %v", entities)
	}
}

// 30-4: an advice with no bank credit records an ADVISED payment that books no cash and cannot be applied; the bank credit that
// arrives later (here a camt.053-style reference) confirms it, takes the advice's references and is the payment that is applied.
func TestAdvisedPayment_BooksNoCashAndTheBankCreditConfirmsIt(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	lines := []domain.AdviceLine{{PaymentRef: "WIRE-5521", References: []domain.RemittanceLine{{Reference: "INV-30001", Amount: amt("1000.00")}}}}

	none, err := s.ApplyRemittanceAdviceWith(ctx, legalEntity, lines, AdviceOptions{})
	if err != nil || none.Created != 0 || none.Lines[0].Outcome != AdviceUnmatched {
		t.Fatalf("without createMissing: %+v %v", none, err)
	}
	res, err := s.ApplyRemittanceAdviceWith(ctx, legalEntity, lines, AdviceOptions{CreateMissing: true, Currency: "USD", ValueDate: "2027-03-18"})
	if err != nil || res.Created != 1 || res.Lines[0].Outcome != AdviceAdvised || res.Lines[0].PaymentID == "" {
		t.Fatalf("createMissing: %+v %v", res, err)
	}
	advised, err := s.GetPayment(ctx, res.Lines[0].PaymentID)
	if err != nil || advised.Status != "ADVISED" || advised.Source != "REMITTANCE" || !advised.Amount.Equal(amt("1000.00")) || advised.ValueDate != "2027-03-18" {
		t.Fatalf("advised payment: %+v %v", advised, err)
	}
	// A repeat of the advice finds it instead of recording a second one; it books no journal, cannot be applied, and is not auto-applied.
	if again, err := s.ApplyRemittanceAdviceWith(ctx, legalEntity, lines, AdviceOptions{CreateMissing: true, Currency: "USD"}); err != nil || again.Created != 0 || again.Attached != 1 {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	if n := journalCount(t, s, ctx, "payment_receipt"); n != 0 {
		t.Fatalf("an advised payment booked %d receipt journals", n)
	}
	if _, err := s.ApplyPayment(ctx, advised.ID, advised.Version+1, ApplyRequest{}, "adv-apply"); err == nil {
		t.Fatal("an advised payment was applied")
	}
	if got, err := s.AutoApplyPayments(ctx, []string{advised.ID}); err != nil || got.Applied != 0 {
		t.Fatalf("auto-apply of an advised payment: %+v %v", got, err)
	}
	// The bank credit arrives with the same reference, currency and amount.
	bank, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, PaymentMode: "WIRE", Source: "MANUAL", SourceRef: ptr("WIRE-5521"),
		Amount: amt("1000.00"), UnappliedAmount: amt("1000.00"), Currency: "USD", ReceivedDate: "2027-03-20", ValueDate: "2027-03-20"}, "adv-bank")
	if err != nil {
		t.Fatal(err)
	}
	if len(bank.Remittance) != 1 || bank.Remittance[0].Reference != "INV-30001" {
		t.Fatalf("the bank payment did not take the advice's references: %+v", bank.Remittance)
	}
	if was, _ := s.GetPayment(ctx, advised.ID); was.Status != "CONFIRMED" {
		t.Fatalf("the advised payment is %s, want CONFIRMED", was.Status)
	}
	if n := journalCount(t, s, ctx, "payment_receipt"); n != 1 {
		t.Fatalf("%d receipt journals, want the bank payment's one", n)
	}
	if got, err := s.AutoApplyPayments(ctx, []string{bank.ID}); err != nil || got.Applied != 1 {
		t.Fatalf("auto-apply of the bank payment: %+v %v", got, err)
	}
	if it := itemByID(t, s, ctx, item.ID); it.Status != "SETTLED" {
		t.Fatalf("item %+v", it)
	}
	// A confirmed advice is not found by a later advice for the same reference.
	if late, err := s.ApplyRemittanceAdviceWith(ctx, legalEntity, lines, AdviceOptions{}); err != nil || late.Lines[0].Outcome != AdviceApplied {
		t.Fatalf("late advice: %+v %v", late, err)
	}
}

func TestAdvisedPayment_ADifferentAmountIsNotConfirmed(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	seedInvoice(t, s, ctx)
	lines := []domain.AdviceLine{{PaymentRef: "WIRE-9", References: []domain.RemittanceLine{{Reference: "INV-30001", Amount: amt("1000.00")}}}}
	res, err := s.ApplyRemittanceAdviceWith(ctx, legalEntity, lines, AdviceOptions{CreateMissing: true, Currency: "USD"})
	if err != nil || res.Created != 1 {
		t.Fatalf("advice: %+v %v", res, err)
	}
	if _, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, PaymentMode: "WIRE", Source: "MANUAL", SourceRef: ptr("WIRE-9"),
		Amount: amt("999.00"), UnappliedAmount: amt("999.00"), Currency: "USD", ReceivedDate: "2027-03-20", ValueDate: "2027-03-20"}, "adv-short"); err != nil {
		t.Fatal(err)
	}
	if was, _ := s.GetPayment(ctx, res.Lines[0].PaymentID); was.Status != "ADVISED" {
		t.Fatalf("a short bank payment confirmed the advice: %s", was.Status)
	}
}

func journalCount(t *testing.T, s *Postgres, ctx context.Context, sourceType string) int {
	t.Helper()
	var n int
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from gl_journal where source_type = $1`, sourceType).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}
