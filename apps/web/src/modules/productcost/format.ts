/** Shown for a missing value. */
export const dash = "—";

const DECIMAL = /^-?\d+(\.\d+)?$/;

/**
 * Pads a decimal string to at least `min` decimals without rounding or float
 * arithmetic: "21.2" → "21.20", "54" → "54.00", "13.333333" stays as is.
 */
export function amount(value: string | undefined, min = 2): string {
  if (value === undefined || value === "") return dash;
  if (!DECIMAL.test(value)) return value;
  const [whole, fraction = ""] = value.split(".");
  if (min === 0 && fraction === "") return whole;
  return `${whole}.${fraction.padEnd(min, "0")}`;
}

/** True for a non-negative decimal string such as "2.50" (what the forms accept). */
export function isDecimal(value: string): boolean {
  return /^\d+(\.\d+)?$/.test(value.trim());
}

/** True for a decimal string that may be negative. */
export function isSignedDecimal(value: string): boolean {
  return DECIMAL.test(value.trim());
}

/** True when a decimal string is above zero. */
export function isPositive(value: string): boolean {
  return isDecimal(value) && !/^0+(\.0+)?$/.test(value.trim());
}

/** True when a decimal string is below zero (favourable variance). */
export function isNegative(value: string): boolean {
  return value.startsWith("-") && !/^-0+(\.0+)?$/.test(value);
}
