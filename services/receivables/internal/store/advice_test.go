package store

import (
	"testing"

	"github.com/shopspring/decimal"

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
