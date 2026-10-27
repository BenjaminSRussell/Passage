export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Non-negative decimal with up to 6 places, as the service accepts (never parsed to float). */
export const DECIMAL = /^\d+(\.\d{1,6})?$/;

export const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** "NotYetAcquired" → "Not yet acquired", "DisposalSale" → "Disposal sale". */
export function sentence(value: string): string {
  const words = value
    .replace(/([a-z])([A-Z])/g, "$1 $2")
    .replace(/[_-]+/g, " ")
    .toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** "1234.5" + "USD" → "1234.5 USD"; amounts stay decimal strings. */
export function money(amount: string | null | undefined, currency: string): string {
  return amount === undefined || amount === null || amount === "" ? dash : `${amount} ${currency}`;
}

/** "0.05" → "5%" without floats: the decimal point moves two places. */
export function percent(rate: string): string {
  const m = /^(\d+)(?:\.(\d+))?$/.exec(rate);
  if (!m) return rate;
  const frac = (m[2] ?? "").padEnd(2, "0");
  const whole = String(Number.parseInt(m[1] + frac.slice(0, 2), 10));
  const rest = frac.slice(2).replace(/0+$/, "");
  return `${whole}${rest ? `.${rest}` : ""}%`;
}

/** Today's UTC date as YYYY-MM-DD. */
export function today(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}

/** Signed decimal with up to 6 places, such as -500.00 for an impairment reversal. */
export const SIGNED_DECIMAL = /^-?\d+(\.\d{1,6})?$/;

/** A run period: "2026-03" for monthly books, "2026" for yearly ones. */
export const PERIOD = /^\d{4}(-(0[1-9]|1[0-2]))?$/;

/** A decimal string that is zero ("0", "0.00", "-0"). */
export function isZero(value: string): boolean {
  return /^-?0+(\.0+)?$/.test(value);
}

/** "amount currency", or the amount alone when the currency is unknown. */
export function amount(value: string | null | undefined, currency: string | null | undefined): string {
  return currency ? money(value, currency) : (value ?? dash);
}

/**
 * Parses "2026=200000.00" lines (also "2026: 200000.00") into a year → amount
 * map. Returns an error message for the first line that does not parse.
 */
export function parseYearLines(text: string): { values: Record<string, string>; error?: string } {
  const values: Record<string, string> = {};
  for (const raw of text.split(/\n/)) {
    const line = raw.trim();
    if (line === "") continue;
    const m = /^(\d{4})\s*[=:]\s*(\d+(?:\.\d{1,6})?)$/.exec(line);
    if (!m) return { values, error: `"${line}" is not a year and an amount, such as 2026=1000.00.` };
    if (values[m[1]] !== undefined) return { values, error: `${m[1]} is listed twice.` };
    values[m[1]] = m[2];
  }
  return { values };
}
