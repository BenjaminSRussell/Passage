package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func vendorAgreement() RebateAgreement {
	accrual, income := DefaultRebateAccounts(PartyVendor)
	return RebateAgreement{LegalEntityID: "le", Name: "Supplier bill-back", PartyRole: PartyVendor, VendorID: "v1",
		Basis: BasisNetAmount, Period: PeriodMonth, Tiers: []Tier{{RatePct: decimal.NewFromInt(3)}}, Currency: "USD",
		ValidFrom: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), ValidTo: time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC),
		AccrualAccount: accrual, ExpenseAccount: income}
}

// AC-26.11: a 3% vendor rebate on a 20,000.00 USD vendor invoice accrues 600.00, posts
// Dr 2160 / Cr 5100, and the period's claim preview is 600.00.
func TestVendorRebate_AC26_11(t *testing.T) {
	a := vendorAgreement()
	next, inc := Accrue(a, PeriodState{}, decimal.RequireFromString("20000.00"), decimal.Zero)
	if !inc.Equal(decimal.NewFromInt(600)) || !next.Accrued.Equal(decimal.NewFromInt(600)) {
		t.Fatalf("accrual = %s (period %s); want 600.00", inc, next.Accrued)
	}
	if debit, credit := AccrualPosting(a); debit != "2160" || credit != "5100" {
		t.Fatalf("posting = Dr %s / Cr %s; want Dr 2160 / Cr 5100", debit, credit)
	}
	claim := ClaimAmount(next.Accrued, decimal.Zero)
	if !claim.Equal(decimal.NewFromInt(600)) || ClaimSettlement(claim, "") != SettleCreditNote {
		t.Fatalf("claim preview = %s settled by %s; want 600.00 by CREDIT_NOTE", claim, ClaimSettlement(claim, ""))
	}
}

func TestAccrualPostingFollowsPartyRole(t *testing.T) {
	customer := RebateAgreement{AccrualAccount: "2150", ExpenseAccount: "4150"}
	if debit, credit := AccrualPosting(customer); debit != "4150" || credit != "2150" {
		t.Fatalf("customer posting = Dr %s / Cr %s; want Dr 4150 / Cr 2150", debit, credit)
	}
	vendor := RebateAgreement{PartyRole: PartyVendor, AccrualAccount: "2161", ExpenseAccount: "5101"}
	if debit, credit := AccrualPosting(vendor); debit != "2161" || credit != "5101" {
		t.Fatalf("vendor posting with own accounts = Dr %s / Cr %s; want Dr 2161 / Cr 5101", debit, credit)
	}
}

// BR-26.11: the claim is the period's accruals less earlier claims of the period; an
// over-accrued period creates a vendor invoice whatever mode was asked for.
func TestClaimAmountAndSettlement(t *testing.T) {
	cases := []struct {
		accrued, prior, mode string
		want, settle         string
	}{
		{"600", "0", "", "600", SettleCreditNote},
		{"750", "600", SettlePaymentDeduction, "150", SettlePaymentDeduction},
		{"600", "600", "", "0", SettleCreditNote},
		{"450", "600", SettlePaymentDeduction, "-150", SettleVendorInvoice},
	}
	for _, c := range cases {
		got := ClaimAmount(decimal.RequireFromString(c.accrued), decimal.RequireFromString(c.prior))
		if !got.Equal(decimal.RequireFromString(c.want)) || ClaimSettlement(got, c.mode) != c.settle {
			t.Errorf("claim(%s - %s, %q) = %s by %s; want %s by %s", c.accrued, c.prior, c.mode, got, ClaimSettlement(got, c.mode), c.want, c.settle)
		}
	}
	for mode, ok := range map[string]bool{"": true, SettleCreditNote: true, SettlePaymentDeduction: true, SettleVendorInvoice: false, "CASH": false} {
		if ValidClaimMode(mode) != ok {
			t.Errorf("ValidClaimMode(%q) = %v; want %v", mode, !ok, ok)
		}
	}
}

func TestValidateAgreementPartyRules(t *testing.T) {
	customer := vendorAgreement()
	customer.PartyRole, customer.VendorID, customer.CustomerID = PartyCustomer, "", "c1"
	cases := []struct {
		name   string
		mutate func(*RebateAgreement)
		field  string
	}{
		{"vendor with a vendor id", func(a *RebateAgreement) {}, ""},
		{"customer with a customer id", func(a *RebateAgreement) { *a = customer }, ""},
		{"empty role is a customer agreement", func(a *RebateAgreement) { *a = customer; a.PartyRole = "" }, ""},
		{"vendor without a vendor id", func(a *RebateAgreement) { a.VendorID = "" }, "vendorId"},
		{"vendor naming a customer group", func(a *RebateAgreement) { a.CustomerGroupID = "g1" }, "customer"},
		{"customer naming a vendor", func(a *RebateAgreement) { *a = customer; a.VendorID = "v1" }, "vendorId"},
		{"customer without a customer", func(a *RebateAgreement) { *a = customer; a.CustomerID = "" }, "customer"},
		{"unknown role", func(a *RebateAgreement) { a.PartyRole = "BROKER" }, "partyRole"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := vendorAgreement()
			c.mutate(&a)
			err := ValidateAgreement(a)
			if c.field == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err.Fields)
				}
				return
			}
			if err == nil || err.Fields[c.field] == "" {
				t.Fatalf("want an error on %s, got %v", c.field, err)
			}
		})
	}
}
