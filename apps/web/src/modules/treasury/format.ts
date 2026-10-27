// Display and validation helpers for the treasury pages. Amounts are decimal
// strings from the service and are shown as given, never parsed into a float.

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DAY = /^\d{4}-\d{2}-\d{2}$/;
export const CURRENCY = /^[A-Z]{3}$/;
export const ACCOUNT_CODE = /^[A-Z0-9-]{1,20}$/;
export const BIC = /^[A-Z]{6}[A-Z0-9]{2}([A-Z0-9]{3})?$/;
/** A statement account number after normalizeIban: SWIFT 35x subset accepted by the service. */
export const STATEMENT_ACCOUNT = /^[A-Z0-9/.-]{1,35}$/;
/** A signed decimal with up to six places, the shape the service stores (numeric(20,6)). */
export const SIGNED_DECIMAL = /^-?\d{1,14}(\.\d{1,6})?$/;

/** "1337.50 EUR": the service's decimal string with its currency. */
export function money(amount: string, currency: string): string {
  return `${amount} ${currency}`;
}

/** Normalizes an IBAN the way the service does: no spaces, upper case. */
export function normalizeIban(raw: string): string {
  return raw.replace(/\s+/g, "").toUpperCase();
}

/**
 * ISO 13616 shape and mod-97 check digits, the same rule as the service
 * (TRS-0401), so the form can say what is wrong before the round trip.
 */
export function validIban(raw: string): boolean {
  const iban = normalizeIban(raw);
  if (!/^[A-Z]{2}[0-9]{2}[A-Z0-9]{11,30}$/.test(iban)) return false;
  const rearranged = iban.slice(4) + iban.slice(0, 4);
  let rem = 0;
  for (const ch of rearranged) {
    const digits = ch >= "A" && ch <= "Z" ? String(ch.charCodeAt(0) - 55) : ch;
    for (const d of digits) rem = (rem * 10 + Number(d)) % 97;
  }
  return rem === 1;
}

export function shortId(id: string): string {
  return id.slice(0, 8);
}

export function dateTime(iso: string | undefined): string {
  return iso ? iso.replace("T", " ").replace(/\.\d+/, "").replace("Z", " UTC") : "Not recorded";
}

/** Today's date in UTC as YYYY-MM-DD, the service's default asOf. */
export function today(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}

/** A positive decimal with up to six places; the service also refuses more places than the currency's minor unit. */
export const POSITIVE_DECIMAL = /^(?!0+(\.0+)?$)\d{1,14}(\.\d{1,6})?$/;

/** ISO 4217 exponents that differ from two, the same table as platform/go/money. */
const MINOR_UNITS: Record<string, number> = {
  BHD: 3, IQD: 3, JOD: 3, KWD: 3, LYD: 3, OMR: 3, TND: 3,
  BIF: 0, CLP: 0, DJF: 0, GNF: 0, ISK: 0, JPY: 0, KMF: 0, KRW: 0, PYG: 0, RWF: 0, UGX: 0, UYI: 0, VND: 0, VUV: 0, XAF: 0, XOF: 0, XPF: 0,
  CLF: 4, UYW: 4,
};

export function minorUnits(currency: string): number {
  return MINOR_UNITS[currency] ?? 2;
}

/** A positive amount with no more places than the currency allows (two for EUR, none for JPY). */
export function validAmount(amount: string, currency: string): boolean {
  const value = amount.trim();
  if (!POSITIVE_DECIMAL.test(value)) return false;
  const places = value.includes(".") ? value.split(".")[1].replace(/0+$/, "").length : 0;
  return places <= minorUnits(currency.trim());
}

/** "3.125%" from the service's fraction "0.03125", by moving the decimal point; no float arithmetic. */
export function percent(fraction: string): string {
  const [whole, frac = ""] = fraction.trim().split(".");
  const point = whole.length + 2;
  const padded = whole + frac.padEnd(2, "0");
  const intPart = padded.slice(0, point).replace(/^0+(?=\d)/, "");
  const fracPart = padded.slice(point).replace(/0+$/, "");
  return `${intPart}${fracPart ? `.${fracPart}` : ""}%`;
}

/** A rate in percent with up to four places (the service stores the fraction with six). */
export const PERCENT = /^\d{1,3}(\.\d{1,4})?$/;

/** "0.03125" from "3.125" (percent to the service's fraction), by moving the decimal point. */
export function fractionFromPercent(pct: string): string {
  const [whole, frac = ""] = pct.trim().split(".");
  const padded = whole.padStart(3, "0");
  const intPart = padded.slice(0, -2).replace(/^0+(?=\d)/, "");
  const fracPart = (padded.slice(-2) + frac).replace(/0+$/, "");
  return fracPart ? `${intPart}.${fracPart}` : intPart;
}
