import type { RebateAgreement, RebateBasis, RebatePeriod } from "./api/client";

export const BASIS_LABELS: Record<RebateBasis, string> = { NET_AMOUNT: "Net invoiced amount", QUANTITY: "Invoiced quantity" };
export const PERIOD_LABELS: Record<RebatePeriod, string> = { MONTH: "Monthly", QUARTER: "Quarterly", YEAR: "Annual" };

/** Who the agreement rebates: one customer, or a customer group. */
export function describeRebateCustomer(a: Pick<RebateAgreement, "customerId" | "customerGroupId">): string {
  return a.customerId ? a.customerId : `Group ${a.customerGroupId ?? ""}`;
}

/** "From 0: 1% · from 50000: 2.5%" (or "per unit" for a quantity basis). */
export function describeTiers(a: Pick<RebateAgreement, "basis" | "tiers" | "currency">): string {
  return a.tiers
    .map((t) => (a.basis === "NET_AMOUNT" ? `from ${t.from}: ${t.ratePct ?? "0"}%` : `from ${t.from}: ${t.amountPerUnit ?? "0"} ${a.currency} per unit`))
    .join(" · ");
}
