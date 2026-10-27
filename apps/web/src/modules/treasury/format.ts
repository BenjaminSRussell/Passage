// Display and validation helpers for the treasury pages. Amounts are decimal
// strings from the service and are shown as given, never parsed into a float.

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DAY = /^\d{4}-\d{2}-\d{2}$/;
export const CURRENCY = /^[A-Z]{3}$/;
export const ACCOUNT_CODE = /^[A-Z0-9-]{1,20}$/;
export const BIC = /^[A-Z]{6}[A-Z0-9]{2}([A-Z0-9]{3})?$/;
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
