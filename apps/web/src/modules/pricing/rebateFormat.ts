import type { ClaimSettlementMode, ClaimStatus, RebateAgreement, RebateBasis, RebatePartyRole, RebatePeriod } from "./api/client";

export const BASIS_LABELS: Record<RebateBasis, string> = { NET_AMOUNT: "Net invoiced amount", QUANTITY: "Invoiced quantity" };
/** A vendor rebate's basis is the vendor-invoiced net or the received quantity (BR-26.11). */
export const VENDOR_BASIS_LABELS: Record<RebateBasis, string> = { NET_AMOUNT: "Vendor-invoiced net amount", QUANTITY: "Received quantity" };
export const PERIOD_LABELS: Record<RebatePeriod, string> = { MONTH: "Monthly", QUARTER: "Quarterly", YEAR: "Annual" };
export const PARTY_LABELS: Record<RebatePartyRole, string> = { CUSTOMER: "Customer rebate", VENDOR: "Vendor rebate" };
export const SETTLEMENT_LABELS: Record<ClaimSettlementMode, string> = {
  CREDIT_NOTE: "Vendor credit note",
  PAYMENT_DEDUCTION: "Deduction from the next payment",
  VENDOR_INVOICE: "Vendor invoice (over-accrued)",
};
export const CLAIM_STATUS_LABELS: Record<ClaimStatus, string> = { OPEN: "Open", APPROVED: "Approved" };

/** The basis label for an agreement's party role. */
export function basisLabel(a: Pick<RebateAgreement, "basis" | "partyRole">): string {
  return a.partyRole === "VENDOR" ? VENDOR_BASIS_LABELS[a.basis] : BASIS_LABELS[a.basis];
}

/** Who the agreement rebates: one customer, a customer group, or (vendor rebates) the vendor. */
export function describeRebateCustomer(a: Pick<RebateAgreement, "customerId" | "customerGroupId" | "vendorId" | "partyRole">): string {
  if (a.partyRole === "VENDOR") return `Vendor ${a.vendorId ?? ""}`;
  return a.customerId ? a.customerId : `Group ${a.customerGroupId ?? ""}`;
}

/** The ledger lines of a positive accrual: Dr 4150 / Cr 2150 for a customer, Dr 2160 / Cr 5100 for a vendor. */
export function describePosting(a: Pick<RebateAgreement, "partyRole" | "accrualAccount" | "expenseAccount">): string {
  return a.partyRole === "VENDOR" ? `Dr ${a.accrualAccount} / Cr ${a.expenseAccount}` : `Dr ${a.expenseAccount} / Cr ${a.accrualAccount}`;
}

/** "From 0: 1% · from 50000: 2.5%" (or "per unit" for a quantity basis). */
export function describeTiers(a: Pick<RebateAgreement, "basis" | "tiers" | "currency">): string {
  return a.tiers
    .map((t) => (a.basis === "NET_AMOUNT" ? `from ${t.from}: ${t.ratePct ?? "0"}%` : `from ${t.from}: ${t.amountPerUnit ?? "0"} ${a.currency} per unit`))
    .join(" · ");
}
