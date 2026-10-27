import type { FundSource } from "./api/client";

export const FUND_SOURCE_LABELS: Record<FundSource, string> = {
  CAMPAIGN_ACCRUAL: "Campaign accrual",
  REBATE_ACCRUAL: "Rebate accrual",
  CLAIM: "Claim",
  DEDUCTION: "Deduction",
};

/** A fiscal period key such as 2027-03 (the fund budget applies per period). */
export const PERIOD = /^\d{4}-(0[1-9]|1[0-2])$/;

/** ISO 4217 currency code. */
export const CURRENCY = /^[A-Z]{3}$/;

/** Who a fund or promotion serves, in words; empty means everyone. */
export function describeScope(scope: string): string {
  return scope.trim() === "" ? "All customers" : scope;
}

/** "+12.5%" / "-3%" for an effectiveness or lift in percent. */
export function signedPct(value: string): string {
  return value.startsWith("-") ? `${value}%` : `+${value}%`;
}
