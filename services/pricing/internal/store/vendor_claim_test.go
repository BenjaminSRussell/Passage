package store_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const vendor = "00000000-0000-0000-0008-000000000001"

func user(n int, id string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: id})
}

func vendorAgreement() domain.RebateAgreement {
	accrual, income := domain.DefaultRebateAccounts(domain.PartyVendor)
	return domain.RebateAgreement{LegalEntityID: legalEntity, Name: "Supplier bill-back", PartyRole: domain.PartyVendor, VendorID: vendor,
		Basis: domain.BasisNetAmount, Period: domain.PeriodMonth, Tiers: []domain.Tier{tier("0", "3")}, Currency: "USD",
		ValidFrom: day("2027-01-01"), ValidTo: day("2027-12-31"), AccrualAccount: accrual, ExpenseAccount: income}
}

func vendorInvoice(agreementID, line, value string) store.AccrualInput {
	in := invoice(agreementID, line, "2027-03-10", value)
	in.Party = domain.PartyVendor
	return in
}

func httpStatus(err error) int {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status
	}
	return 0
}

// AC-26.11 against Postgres: a 3% vendor agreement and a 20,000.00 vendor invoice accrue
// 600.00 with rebateaccrual.posted Dr 2160 / Cr 5100, and the claim preview shows 600.00.
// The sales invoice feed cannot accrue the vendor agreement (PRC-0045).
func TestPostgres_VendorRebateAccrual(t *testing.T) {
	pool := appPool(t)
	ctx := user(1, "u1")
	s := store.New(pool)
	a, err := s.CreateAgreement(ctx, vendorAgreement(), "va-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgreement(ctx, a.ID)
	if err != nil || got.PartyRole != domain.PartyVendor || got.VendorID != vendor || got.AccrualAccount != "2160" || got.ExpenseAccount != "5100" {
		t.Fatalf("read = %+v, %v; want a VENDOR agreement on 2160 / 5100", got, err)
	}
	acc, created, err := s.AccrueInvoiceLine(ctx, vendorInvoice(a.ID, "VI-1/1", "20000.00"))
	if err != nil || !created || !acc.Accrued.Equal(decimal.NewFromInt(600)) {
		t.Fatalf("vendor accrual = %+v created=%v, %v; want 600.00", acc, created, err)
	}
	sales := invoice(a.ID, "INV-1/1", "2027-03-10", "100")
	sales.Party = domain.PartyCustomer
	if _, _, err := s.AccrueInvoiceLine(ctx, sales); problemCode(err) != store.CodePartyMismatch {
		t.Fatalf("sales invoice on a vendor agreement = %v; want %s", err, store.CodePartyMismatch)
	}
	events := outboxRows(t, ctx, pool)
	if len(events) != 1 {
		t.Fatalf("events = %+v; want one rebateaccrual.posted", events)
	}
	if d := events[0].Data; d["amount"] != "600" || d["debit_account"] != "2160" || d["credit_account"] != "5100" || d["party_role"] != "VENDOR" {
		t.Fatalf("payload = %v; want 600 Dr 2160 / Cr 5100 for a VENDOR agreement", d)
	}
	preview, err := s.PreviewVendorClaim(ctx, a.ID, "2027-03")
	if err != nil || !preview.Amount.Equal(decimal.NewFromInt(600)) || preview.SettlementMode != domain.SettleCreditNote || preview.Claims != 0 {
		t.Fatalf("preview = %+v, %v; want 600.00 by CREDIT_NOTE", preview, err)
	}
	vendors, err := s.ListAgreements(ctx, store.AgreementFilter{PartyRole: domain.PartyVendor})
	if err != nil || len(vendors) != 1 || vendors[0].ID != a.ID {
		t.Fatalf("vendor agreements = %+v, %v", vendors, err)
	}
	customers, err := s.ListAgreements(ctx, store.AgreementFilter{PartyRole: domain.PartyCustomer})
	if err != nil || len(customers) != 0 {
		t.Fatalf("customer agreements = %+v, %v; want none", customers, err)
	}
}

// BR-26.11 and the claim lifecycle against Postgres: claims are numbered per legal entity,
// settle the period's accruals less earlier claims, replay on their Idempotency-Key, refuse
// a fully claimed period (PRC-0015), turn an over-claim into a vendor invoice, and are
// approved by someone other than their creator with an event for w19 AP.
func TestPostgres_VendorRebateClaims(t *testing.T) {
	pool := appPool(t)
	maker, checker := user(1, "u1"), user(1, "u2")
	s := store.New(pool)
	a, err := s.CreateAgreement(maker, vendorAgreement(), "")
	if err != nil {
		t.Fatal(err)
	}
	accrue := func(line, value string) {
		t.Helper()
		if _, _, err := s.AccrueInvoiceLine(maker, vendorInvoice(a.ID, line, value)); err != nil {
			t.Fatal(err)
		}
	}
	claim := func(key, mode string) (store.VendorClaim, error) {
		return s.CreateVendorClaim(maker, store.VendorClaimInput{AgreementID: a.ID, PeriodKey: "2027-03", SettlementMode: mode}, key)
	}

	if _, err := claim("", ""); problemCode(err) != store.CodeNothingToClaim {
		t.Fatalf("claim with nothing accrued = %v; want %s", err, store.CodeNothingToClaim)
	}
	accrue("VI-1/1", "20000")
	first, err := claim("rc-1", "")
	if err != nil || first.Number != "RC-000001" || !first.Amount.Equal(decimal.NewFromInt(600)) || first.Status != domain.ClaimOpen ||
		first.SettlementMode != domain.SettleCreditNote || first.CreatedBy != "u1" || first.Version != 1 {
		t.Fatalf("first claim = %+v, %v; want RC-000001 for 600 OPEN", first, err)
	}
	if replay, err := claim("rc-1", ""); err != nil || replay.ID != first.ID {
		t.Fatalf("replayed claim = %+v, %v; want %s", replay, err, first.ID)
	}
	if _, err := claim("rc-1", domain.SettlePaymentDeduction); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other mode = %v; want %s", err, store.CodeIdempotencyUse)
	}
	if _, err := claim("rc-2", ""); problemCode(err) != store.CodeClaimAlreadyProcessed || httpStatus(err) != http.StatusConflict {
		t.Fatalf("claim of a fully claimed period = %v; want 409 %s", err, store.CodeClaimAlreadyProcessed)
	}

	accrue("VI-2/1", "5000")
	second, err := claim("rc-3", domain.SettlePaymentDeduction)
	if err != nil || second.Number != "RC-000002" || !second.Amount.Equal(decimal.NewFromInt(150)) || !second.PriorClaimed.Equal(decimal.NewFromInt(600)) ||
		second.SettlementMode != domain.SettlePaymentDeduction {
		t.Fatalf("second claim = %+v, %v; want RC-000002 for the 150 difference", second, err)
	}
	accrue("VI-3/1", "-5000")
	refund, err := claim("rc-4", domain.SettlePaymentDeduction)
	if err != nil || !refund.Amount.Equal(decimal.NewFromInt(-150)) || refund.SettlementMode != domain.SettleVendorInvoice {
		t.Fatalf("over-claimed period = %+v, %v; want -150 by VENDOR_INVOICE", refund, err)
	}
	if p, err := s.PreviewVendorClaim(maker, a.ID, "2027-03"); err != nil || !p.Amount.IsZero() || p.Claims != 3 {
		t.Fatalf("preview after three claims = %+v, %v; want nothing left to claim", p, err)
	}

	if _, err := s.ApproveVendorClaim(maker, first.ID, 1, ""); problemCode(err) != store.CodeClaimFourEyes {
		t.Fatalf("approval by the creator = %v; want %s", err, store.CodeClaimFourEyes)
	}
	if _, err := s.ApproveVendorClaim(checker, first.ID, 2, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("approval at a stale version = %v; want %s", err, store.CodeVersionConflict)
	}
	approved, err := s.ApproveVendorClaim(checker, first.ID, 1, "ap-1")
	if err != nil || approved.Status != domain.ClaimApproved || approved.Version != 2 || approved.ApprovedBy != "u2" || approved.ApprovedAt == nil {
		t.Fatalf("approval = %+v, %v; want APPROVED at version 2 by u2", approved, err)
	}
	if replay, err := s.ApproveVendorClaim(checker, first.ID, 1, "ap-1"); err != nil || replay.Version != 2 {
		t.Fatalf("replayed approval = %+v, %v; want the stored response", replay, err)
	}
	if _, err := s.ApproveVendorClaim(checker, first.ID, 2, "ap-2"); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("second approval = %v; want %s", err, store.CodeStateInvalid)
	}

	open, err := s.ListVendorClaims(maker, store.ClaimFilter{AgreementID: a.ID, Status: domain.ClaimOpen})
	if err != nil || len(open) != 2 {
		t.Fatalf("open claims = %+v, %v; want RC-000002 and RC-000003", open, err)
	}
	var types []string
	for _, e := range outboxRows(t, maker, pool) {
		if e.Type == "dev.passage.pricing.vendorrebateclaim.created.v1" || e.Type == "dev.passage.pricing.vendorrebateclaim.approved.v1" {
			types = append(types, e.Type)
			if e.Data["vendor_id"] != vendor || e.Data["agreement_id"] != a.ID {
				t.Fatalf("claim payload = %v", e.Data)
			}
		}
	}
	if len(types) != 4 || types[3] != "dev.passage.pricing.vendorrebateclaim.approved.v1" {
		t.Fatalf("claim events = %v; want three created and one approved", types)
	}

	if _, err := s.GetVendorClaim(user(2, "u9"), first.ID); problemCode(err) != store.CodeClaimNotFound {
		t.Fatalf("other tenant read = %v; want %s", err, store.CodeClaimNotFound)
	}
	if rows, err := s.ListVendorClaims(principal(1, otherLegalEntity), store.ClaimFilter{}); err != nil || len(rows) != 0 {
		t.Fatalf("claims seen from another legal entity's scope = %+v, %v; want none", rows, err)
	}
	if _, err := s.PreviewVendorClaim(principal(1, otherLegalEntity), a.ID, "2027-03"); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("preview from another legal entity's scope = %v; want %s", err, store.CodeAgreementNotFound)
	}
}

// Claims of a customer agreement are refused, and the 0006 migration keeps the old
// customer-or-group rule for CUSTOMER agreements.
func TestPostgres_ClaimsNeedAVendorAgreement(t *testing.T) {
	pool := appPool(t)
	ctx := user(1, "u1")
	s := store.New(pool)
	c, err := s.CreateAgreement(ctx, agreement(tier("0", "2")), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.PartyRole != domain.PartyCustomer {
		t.Fatalf("party role = %q; want CUSTOMER by default", c.PartyRole)
	}
	if _, err := s.CreateVendorClaim(ctx, store.VendorClaimInput{AgreementID: c.ID, PeriodKey: "2026-10"}, ""); problemCode(err) != store.CodePartyMismatch {
		t.Fatalf("claim on a customer agreement = %v; want %s", err, store.CodePartyMismatch)
	}
	both := agreement(tier("0", "2"))
	both.CustomerGroupID = "00000000-0000-0000-0005-000000000009"
	if _, err := s.CreateAgreement(ctx, both, ""); err == nil {
		t.Fatal("a customer agreement naming a customer and a group must violate rebate_agreement_party_check")
	}
}
