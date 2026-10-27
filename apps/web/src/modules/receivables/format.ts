// Display and arithmetic helpers for the receivables pages. Amounts are decimal
// strings; sums use scaled BigInt so no value ever passes through a float.

import type { BadgeTone } from "../../shell/ui";
import type { PaymentMode, PaymentStatus } from "./api/client";

const SCALE = 6;
const FACTOR = 10n ** BigInt(SCALE);

export const DECIMAL = /^\d+(\.\d{1,6})?$/;
export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DAY = /^\d{4}-\d{2}-\d{2}$/;
export const CURRENCY = /^[A-Z]{3}$/;

export const MODE_LABELS: Record<PaymentMode, string> = {
  CHECK: "Check",
  ACH: "ACH",
  WIRE: "Wire",
  CARD: "Card",
  CASH: "Cash",
};

export const STATUS_LABELS: Record<PaymentStatus, string> = {
  RECEIVED: "Received",
  SUGGESTED: "Suggested",
  APPLIED: "Applied",
  ON_ACCOUNT: "On account",
  REVERSED: "Reversed",
};

export const STATUS_TONES: Record<PaymentStatus, BadgeTone> = {
  RECEIVED: "info",
  SUGGESTED: "info",
  APPLIED: "success",
  ON_ACCOUNT: "warning",
  REVERSED: "neutral",
};

export const DECISION_LABELS: Record<string, string> = {
  AUTO_APPLY: "Auto-apply: one candidate scores 85 or more, 20 clear of the next",
  SUGGESTED: "Suggested: the best candidate scores 60 to 84",
  ON_ACCOUNT: "On account: no candidate scores 60",
};

/** Parses a decimal string (up to 6 places, optional sign) into micro-units. */
export function toMicros(value: string): bigint {
  const text = value.trim();
  if (!/^-?\d+(\.\d{1,6})?$/.test(text)) throw new RangeError(`not a decimal: ${value}`);
  const negative = text.startsWith("-");
  const [whole, frac = ""] = text.replace("-", "").split(".");
  const micros = BigInt(whole) * FACTOR + BigInt(frac.padEnd(SCALE, "0"));
  return negative ? -micros : micros;
}

/** Formats micro-units as a decimal string with at least two places. */
export function fromMicros(micros: bigint): string {
  const negative = micros < 0n;
  const abs = negative ? -micros : micros;
  const whole = abs / FACTOR;
  let frac = (abs % FACTOR).toString().padStart(SCALE, "0").replace(/0+$/, "");
  if (frac.length < 2) frac = frac.padEnd(2, "0");
  return `${negative ? "-" : ""}${whole}.${frac}`;
}

/** Sum of decimal strings; empty strings count as zero. */
export function sumDecimals(values: string[]): string {
  return fromMicros(values.reduce((acc, v) => acc + (v.trim() === "" ? 0n : toMicros(v)), 0n));
}

/** a - b as a decimal string. */
export function subtract(a: string, b: string): string {
  return fromMicros(toMicros(a) - toMicros(b));
}

/** Compares two decimal strings: -1, 0 or 1. */
export function compareDecimals(a: string, b: string): number {
  const d = toMicros(a) - toMicros(b);
  return d === 0n ? 0 : d < 0n ? -1 : 1;
}

/** Shows a money amount with its currency, normalised to two or more places. */
export function money(amount: string, currency: string): string {
  try {
    return `${fromMicros(toMicros(amount))} ${currency}`;
  } catch {
    return `${amount} ${currency}`;
  }
}

/** Today's calendar day in the browser's zone, as YYYY-MM-DD. */
export function today(now: Date = new Date()): string {
  const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 10);
}

/** A short form of a UUID for tables; the full id stays in the title attribute. */
export function shortId(id: string | null | undefined): string {
  if (!id) return "Unidentified";
  return id.length > 8 ? `${id.slice(0, 8)}…` : id;
}
