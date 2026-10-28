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

export function normalizeCode(raw: string): string {
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

export interface RateEntry {
  rateType: string;
  from: string;
  to: string;
  validFrom: string;
  rate: string;
}

export const emptyEntry: RateEntry = { rateType: "", from: "", to: "", validFrom: "", rate: "" };

export function validateEntry(v: RateEntry): Errors {
  const errors: Errors = {};
  if (v.rateType === "") errors.rateType = "Choose a rate type.";
  const from = currencyError(v.from, true);
  if (from) errors.from = from;
  const to = currencyError(v.to, true);
  if (to) errors.to = to;
  if (!from && !to && v.from === v.to) errors.to = "Choose a different currency from the first one.";
  if (v.validFrom === "") errors.validFrom = "Enter the date the rate takes effect.";
  else if (!DATE.test(v.validFrom)) errors.validFrom = "Enter the date as YYYY-MM-DD.";
  const rate = rateError(v.rate);
  if (rate) errors.rate = rate;
  return errors;
}

const MAX_RATE_DECIMALS = 10;
const MAX_RATE_WHOLE_DIGITS = 10;

function rateError(raw: string): string | undefined {
  const match = /^(\d+)(?:\.(\d+))?$/.exec(raw.trim());
  if (!match || !/[1-9]/.test(raw)) return "Enter a rate greater than zero, such as 1.0855.";
  if ((match[2] ?? "").length > MAX_RATE_DECIMALS) return `Use at most ${MAX_RATE_DECIMALS} decimal places.`;
  if (match[1].replace(/^0+/, "").length > MAX_RATE_WHOLE_DIGITS) return `Use at most ${MAX_RATE_WHOLE_DIGITS} digits before the decimal point.`;
  return undefined;
}

export interface RateTypeValues {
  code: string;
  name: string;
  provider: string;
  allowInverse: boolean;
}

export const emptyRateType: RateTypeValues = { code: "", name: "", provider: "", allowInverse: true };

export function validateRateType(v: RateTypeValues): Errors {
  const errors: Errors = {};
  if (v.code.trim() === "") errors.code = "Enter a code, for example CLOSING.";
  if (v.name.trim() === "") errors.name = "Enter a name, for example Month-end closing.";
  return errors;
}

const DEVIATION_SCALE = 12;
export const DEVIATION_LIMIT_PERCENT = 10;

function scaled(value: string): bigint | null {
  const match = /^(\d+)(?:\.(\d+))?$/.exec(value.trim());
  if (!match) return null;
  return BigInt(match[1] + (match[2] ?? "").padEnd(DEVIATION_SCALE, "0").slice(0, DEVIATION_SCALE));
}

export interface Deviation {
  percent: string;
  direction: "above" | "below";
}

export function deviationBeyondLimit(previous: string, next: string): Deviation | null {
  const before = scaled(previous);
  const after = scaled(next);
  if (before === null || after === null || before === 0n) return null;
  const change = after - before;
  const size = change < 0n ? -change : change;
  if (size * 100n <= before * BigInt(DEVIATION_LIMIT_PERCENT)) return null;
  const hundredths = (size * 20000n + before) / (before * 2n);
  return { percent: `${hundredths / 100n}.${(hundredths % 100n).toString().padStart(2, "0")}`, direction: change > 0n ? "above" : "below" };
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
