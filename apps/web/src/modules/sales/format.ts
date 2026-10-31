export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Decimal with up to 6 places, as the service accepts (never parsed to float). */
export const DECIMAL = /^\d+(\.\d{1,6})?$/;

const SCALE = 6;

function toMicros(value: string): bigint {
  const negative = value.startsWith("-");
  const [whole = "0", fraction = ""] = value.replace(/^[-+]/, "").split(".");
  const micros = BigInt(whole || "0") * 10n ** BigInt(SCALE) + BigInt((fraction + "0".repeat(SCALE)).slice(0, SCALE) || "0");
  return negative ? -micros : micros;
}

function fromMicros(micros: bigint): string {
  const negative = micros < 0n;
  const abs = negative ? -micros : micros;
  const unit = 10n ** BigInt(SCALE);
  const fraction = (abs % unit).toString().padStart(SCALE, "0").replace(/0+$/, "");
  return `${negative ? "-" : ""}${abs / unit}${fraction ? `.${fraction}` : ""}`;
}

/** a − b for decimal strings of up to 6 places, exact (never parsed to float): "0.1", "0.3" → "-0.2". */
export function subtractDecimal(a: string, b: string): string {
  return fromMicros(toMicros(a) - toMicros(b));
}

/** The first block of an id, enough to tell rows apart on screen. */
export function shortId(id: string): string {
  return id.split("-")[0] ?? id;
}

/** "BALANCE_PACKING_SLIP" → "Balance packing slip". */
export function sentence(value: string): string {
  const words = value.replace(/[_-]+/g, " ").toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** "1234.5" + "USD" → "1234.5 USD"; amounts stay decimal strings. */
export function money(amount: string | undefined, currency: string): string {
  return amount === undefined || amount === "" ? dash : `${amount} ${currency}`;
}

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** Today's UTC date as YYYY-MM-DD. */
export function today(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}

/**
 * " It also has 2 mandatory supplementary lines." for an order the service
 * added supplementary lines to at commit (FR-27.14, SLS-0012); "" otherwise.
 */
export function supplementaryNote(lines: { supplementaryOf?: string }[]): string {
  const n = lines.filter((l) => l.supplementaryOf).length;
  if (n === 0) return "";
  return ` It also has ${n} mandatory supplementary ${n === 1 ? "line" : "lines"}.`;
}
