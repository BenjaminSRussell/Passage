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
