package store_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func agreement(tiers ...domain.Tier) domain.RebateAgreement {
	return domain.RebateAgreement{LegalEntityID: legalEntity, Name: "Monthly", CustomerID: "00000000-0000-0000-0005-000000000001",
		Basis: domain.BasisNetAmount, Period: domain.PeriodMonth, Tiers: tiers, Currency: "USD",
		ValidFrom: day("2026-01-01"), ValidTo: day("2026-12-31"), AccrualAccount: "2150", ExpenseAccount: "4150"}
}

func tier(from, pct string) domain.Tier {
	return domain.Tier{From: decimal.RequireFromString(from), RatePct: decimal.RequireFromString(pct)}
}

func invoice(agreementID, line, date, value string) store.AccrualInput {
	return store.AccrualInput{AgreementID: agreementID, InvoiceLineID: line, InvoiceDate: day(date),
		Value: decimal.RequireFromString(value), Currency: "USD"}
}

// AC-26.8 against Postgres: a 2% monthly rebate on a 10,000.00 invoice accrues 200.00 and
// writes rebateaccrual.posted for Dr 4150 / Cr 2150; a redelivered line accrues nothing more.
func TestPostgres_RebateAccrual(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "2")), "ra-1")
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.CreateAgreement(ctx, agreement(tier("0", "2")), "ra-1"); err != nil || replay.ID != a.ID {
		t.Fatalf("replayed create = %+v, %v; want agreement %s", replay, err, a.ID)
	}
	if _, err := s.CreateAgreement(ctx, agreement(tier("0", "3")), "ra-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, different tiers = %v; want %s", err, store.CodeIdempotencyUse)
	}
	got, err := s.GetAgreement(ctx, a.ID)
	if err != nil || len(got.Tiers) != 1 || !got.Tiers[0].RatePct.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("read = %+v, %v", got, err)
	}

	acc, created, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-1/1", "2026-10-05", "10000.00"))
	if err != nil || !created || !acc.Accrued.Equal(decimal.NewFromInt(200)) || acc.PeriodKey != "2026-10" {
		t.Fatalf("accrual = %+v created=%v, %v; want 200.00 in 2026-10", acc, created, err)
	}
	again, created, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-1/1", "2026-10-05", "10000.00"))
	if err != nil || created || again.ID != acc.ID {
		t.Fatalf("redelivery = %+v created=%v, %v; want the first accrual", again, created, err)
	}
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-1/1", "2026-10-05", "12000.00")); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("redelivery with another value = %v; want %s", err, store.CodeIdempotencyUse)
	}
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-9/1", "2027-01-05", "1")); problemCode(err) != domain.CodeValidation {
		t.Fatalf("invoice outside validity = %v; want %s", err, domain.CodeValidation)
	}

	events := outboxRows(t, ctx, pool)
	if len(events) != 1 || events[0].Type != "dev.passage.pricing.rebateaccrual.posted.v1" {
		t.Fatalf("events = %+v; want one rebateaccrual.posted", events)
	}
	d := events[0].Data
	if d["amount"] != "200" || d["debit_account"] != "4150" || d["credit_account"] != "2150" || d["period_id"] != "2026-10" ||
		d["agreement_id"] != a.ID || d["legal_entity_id"] != legalEntity {
		t.Fatalf("payload = %v; want 200 Dr 4150 / Cr 2150 for 2026-10", d)
	}
	if _, err := s.GetAgreement(principal(2, ""), a.ID); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("other tenant read = %v; want %s", err, store.CodeAgreementNotFound)
	}
}

// BR-26.8 and the reconciliation rule against Postgres: crossing a tier recomputes the
// period and books the delta; the period's accruals sum to the target at its basis.
func TestPostgres_RebateTierCrossing(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "1"), tier("5000", "2.5")), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"30", "120", "-105"}
	for i, value := range []string{"3000", "3000", "-1500"} {
		acc, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "L"+value+string(rune('a'+i)), "2026-03-1"+string(rune('0'+i)), value))
		if err != nil || !acc.Accrued.Equal(decimal.RequireFromString(want[i])) {
			t.Fatalf("invoice %d = %+v, %v; want %s", i+1, acc, err, want[i])
		}
	}
	rows, err := s.ListAccruals(ctx, store.AccrualFilter{AgreementID: a.ID, PeriodKey: "2026-03"})
	if err != nil || len(rows) != 3 {
		t.Fatalf("accruals = %v, %v; want three", rows, err)
	}
	sum := decimal.Zero
	for _, r := range rows {
		sum = sum.Add(r.Accrued)
	}
	if !sum.Equal(decimal.NewFromInt(45)) {
		t.Fatalf("period accruals sum to %s; want 45.00 (4,500 x 1%%)", sum)
	}
	list, err := s.ListAgreements(ctx, store.AgreementFilter{LegalEntityID: legalEntity})
	if err != nil || len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("agreements = %+v, %v", list, err)
	}
}
