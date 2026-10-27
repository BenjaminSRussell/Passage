package domain

import "github.com/shopspring/decimal"

// Party roles of a rebate agreement (FR-26.15). A CUSTOMER agreement pays a rebate to a
// customer on posted sales invoices; a VENDOR agreement earns a rebate from a vendor on its
// posted vendor invoices and is settled by period claims through w19 AP.
const (
	PartyCustomer = "CUSTOMER"
	PartyVendor   = "VENDOR"
)

// Default rebate accounts per party role (§5.26.13 rebate_accrual_accounts and
// vendor_rebate_accounts). The accrual account is the balance-sheet side (2150 customer
// rebate payable, 2160 vendor rebate receivable); the expense account is the P&L side
// (4150 rebate expense, 5100 purchase rebate income).
const (
	CustomerAccrualAccount = "2150"
	CustomerExpenseAccount = "4150"
	VendorAccrualAccount   = "2160"
	VendorIncomeAccount    = "5100"
)

// Settlement modes of a vendor rebate claim (FR-26.15, BR-26.11, §5.26.13
// vendor_rebate_claim_mode). A positive claim settles by a w19 AP credit note (default) or a
// deduction from the next vendor payment; a negative claim (over-accrual) always creates a
// w19 vendor invoice, because the company owes the vendor the difference.
const (
	SettleCreditNote       = "CREDIT_NOTE"
	SettlePaymentDeduction = "PAYMENT_DEDUCTION"
	SettleVendorInvoice    = "VENDOR_INVOICE"
)

// Vendor rebate claim states: a claim is created OPEN and approved (duty
// VendorRebateClaimApprove) before w19 AP processes it.
const (
	ClaimOpen     = "OPEN"
	ClaimApproved = "APPROVED"
)

// PartyRoleOf returns the agreement's party role; an empty role is CUSTOMER (agreements
// created before vendor rebates existed).
func PartyRoleOf(a RebateAgreement) string {
	if a.PartyRole == "" {
		return PartyCustomer
	}
	return a.PartyRole
}

// DefaultRebateAccounts returns the accrual and expense accounts a new agreement of the
// party role takes when the request names none.
func DefaultRebateAccounts(partyRole string) (accrual, expense string) {
	if partyRole == PartyVendor {
		return VendorAccrualAccount, VendorIncomeAccount
	}
	return CustomerAccrualAccount, CustomerExpenseAccount
}

// AccrualPosting returns the debit and credit accounts of a positive accrual. A customer
// rebate is an expense owed to the customer: Dr 4150 / Cr 2150 (FR-26.11). A vendor rebate
// is income receivable from the vendor: Dr 2160 / Cr 5100 (FR-26.15, AC-26.11). A negative
// accrual posts the same accounts with a negative amount.
func AccrualPosting(a RebateAgreement) (debit, credit string) {
	if PartyRoleOf(a) == PartyVendor {
		return a.AccrualAccount, a.ExpenseAccount
	}
	return a.ExpenseAccount, a.AccrualAccount
}

// ClaimAmount is what a new claim for a period settles (BR-26.11): the period's accruals
// less what earlier claims of the period already claimed. It is negative when the period
// was over-claimed, for example after a credit note lowered the period.
func ClaimAmount(accrued, priorClaimed decimal.Decimal) decimal.Decimal {
	return accrued.Sub(priorClaimed)
}

// ClaimSettlement returns how a claim of amount settles: a negative claim creates a w19
// vendor invoice whatever the requested mode; otherwise the requested mode, CREDIT_NOTE by
// default.
func ClaimSettlement(amount decimal.Decimal, requested string) string {
	if amount.IsNegative() {
		return SettleVendorInvoice
	}
	if requested == "" {
		return SettleCreditNote
	}
	return requested
}

// ValidClaimMode reports whether a requested settlement mode is one a caller may choose
// (vendor_rebate_claim_mode); VENDOR_INVOICE follows from the amount and is never requested.
func ValidClaimMode(mode string) bool {
	return mode == "" || mode == SettleCreditNote || mode == SettlePaymentDeduction
}
