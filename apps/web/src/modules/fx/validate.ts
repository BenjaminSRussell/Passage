import { CURRENCY, DATE, DECIMAL } from "./api/client";

export type Errors = Record<string, string>;

export interface RateFilter {
  rateType: string;
  from: string;
  to: string;
  date: string;
  via: string;
}

export const emptyFilter: RateFilter = { rateType: "", from: "", to: "", date: "", via: "" };

export function normalizeCurrency(raw: string): string {
  return raw.trim().toUpperCase();
}

function currencyError(value: string, required: boolean): string | undefined {
  if (value === "") return required ? "Enter a 3-letter currency code, for example EUR." : undefined;
  return CURRENCY.test(value) ? undefined : "Use three capital letters, for example EUR.";
}

export function validateFilter(v: RateFilter, opts: { pair: boolean; dated: boolean }): Errors {
  const errors: Errors = {};
  if (v.rateType === "") errors.rateType = "Choose a rate type.";
  const to = currencyError(v.to, true);
  if (to) errors.to = to;
  if (opts.pair) {
    const from = currencyError(v.from, true);
    if (from) errors.from = from;
  }
  if (opts.dated) {
    const via = currencyError(v.via, false);
    if (via) errors.via = via;
    if (v.date !== "" && !DATE.test(v.date)) errors.date = "Enter the date as YYYY-MM-DD.";
  }
  return errors;
}

export function amountError(raw: string): string | undefined {
  return DECIMAL.test(raw.trim()) ? undefined : "Enter a number such as 1250.50.";
}

export function formatRate(rate: string): string {
  const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(rate);
  if (!match) return rate;
  const [, sign, whole, fraction = ""] = match;
  const padded = fraction.padEnd(7, "0");
  let scaled = BigInt(whole + padded.slice(0, 6));
  if (padded[6] >= "5") scaled += 1n;
  const digits = scaled.toString().padStart(7, "0");
  return `${sign}${digits.slice(0, -6)}.${digits.slice(-6)}`;
}
