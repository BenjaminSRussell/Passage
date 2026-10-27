/** Shown for a missing value. */
export const dash = "—";

const DECIMAL = /^-?\d+(\.\d+)?$/;

/**
 * Pads a decimal string to at least `min` decimals without rounding or float
 * arithmetic: "4" → "4.00", "-1.5" → "-1.50", "923.70" stays as is.
 */
export function amount(value: string | undefined, min = 2): string {
  if (value === undefined || value === "") return dash;
  if (!DECIMAL.test(value)) return value;
  const [whole, fraction = ""] = value.split(".");
  return `${whole}.${fraction.padEnd(min, "0")}`;
}

/** True for a non-negative decimal string with at most `places` decimals. */
export function isDecimal(value: string, places = 6): boolean {
  return new RegExp(`^\\d+(\\.\\d{1,${places}})?$`).test(value.trim());
}

/** True for a decimal string that may be negative, with at most `places` decimals. */
export function isSignedDecimal(value: string, places = 6): boolean {
  return new RegExp(`^-?\\d+(\\.\\d{1,${places}})?$`).test(value.trim());
}

/** True when a decimal string is above zero. */
export function isPositive(value: string): boolean {
  return isDecimal(value) && !/^0+(\.0+)?$/.test(value.trim());
}

/** True for an ISO 4217 code such as USD. */
export function isCurrency(value: string): boolean {
  return /^[A-Z]{3}$/.test(value.trim());
}

/** True when a decimal string is not zero, so it reads as a difference. */
export function isNonZero(value: string): boolean {
  return DECIMAL.test(value) && !/^-?0+(\.0+)?$/.test(value);
}

/** True for an ISO 8601 calendar date such as 2026-12-10. */
export function isDate(value: string): boolean {
  const v = value.trim();
  return /^\d{4}-\d{2}-\d{2}$/.test(v) && !Number.isNaN(Date.parse(`${v}T00:00:00Z`));
}

/** True for a whole number from 0 up, such as a lead time in days. */
export function isCount(value: string): boolean {
  return /^\d{1,6}$/.test(value.trim());
}
