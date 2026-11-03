import type { AccrualKind, BrokerContract, BrokerRateKind, ContractKind, ContractStatus, RoyaltyAgreement, RoyaltyBasis } from "./api/client";

export const KIND_LABELS: Record<ContractKind, string> = { BROKER: "Broker", ROYALTY: "Royalty" };
export const CONTRACT_STATUS_LABELS: Record<ContractStatus, string> = { ACTIVE: "Active", CLOSED: "Closed" };
export const RATE_KIND_LABELS: Record<BrokerRateKind, string> = { PERCENT_NET: "Percentage of net", PER_UNIT: "Amount per unit" };
export const ROYALTY_BASIS_LABELS: Record<RoyaltyBasis, string> = { NET_SALES: "Net sales", QUANTITY: "Quantity sold" };
export const ACCRUAL_KIND_LABELS: Record<AccrualKind, string> = { ACCRUAL: "Invoice line", CREDIT: "Credit note line", SHORTFALL: "Minimum royalty shortfall" };

/** Default accounts (§5.26.2): broker commission 6210 / broker payable 2175; royalty 6220 / 2176. */
export const DEFAULT_CONTRACT_ACCOUNTS: Record<ContractKind, { payableAccount: string; expenseAccount: string }> = {
  BROKER: { payableAccount: "2175", expenseAccount: "6210" },
  ROYALTY: { payableAccount: "2176", expenseAccount: "6220" },
};

/** "2.5% of net" or "0.35 USD per unit". */
export function describeBrokerRate(c: Pick<BrokerContract, "rateKind" | "rate" | "currency">): string {
  return c.rateKind === "PERCENT_NET" ? `${c.rate}% of net` : `${c.rate} ${c.currency} per unit`;
}

/** Who and what a broker contract earns on; an empty scope is all. */
export function describeBrokerScope(c: Pick<BrokerContract, "customerId" | "customerGroupId" | "productId" | "productGroupId">): string {
  const who = c.customerId ? `Customer ${c.customerId}` : c.customerGroupId ? `Customer group ${c.customerGroupId}` : "All customers";
  const what = c.productId ? `product ${c.productId}` : c.productGroupId ? `product group ${c.productGroupId}` : "all products";
  return `${who}, ${what}`;
}

/** The product or product group a royalty agreement covers. */
export function describeRoyaltyProduct(a: Pick<RoyaltyAgreement, "productId" | "productGroupId">): string {
  return a.productId ? `Product ${a.productId}` : `Product group ${a.productGroupId ?? ""}`;
}

/** "from 0: 4% · from 50000: 5%" (or "per unit" on a quantity basis). */
export function describeRoyaltyTiers(a: Pick<RoyaltyAgreement, "basis" | "tiers" | "currency">): string {
  return a.tiers
    .map((t) => (a.basis === "NET_SALES" ? `from ${t.from}: ${t.ratePct ?? "0"}%` : `from ${t.from}: ${t.amountPerUnit ?? "0"} ${a.currency} per unit`))
    .join(" · ");
}

/** Where the claim page of a contract lives. */
export function contractLink(kind: ContractKind, id: string): string {
  return kind === "BROKER" ? `/pricing/brokers/${id}` : `/pricing/royalties/${id}`;
}
