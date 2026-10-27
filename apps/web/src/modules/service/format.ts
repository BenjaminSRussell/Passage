export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Decimal with up to 6 places, as the service accepts (never parsed to float). */
export const DECIMAL = /^\d+(\.\d{1,6})?$/;

export const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** The first block of an id, enough to tell rows apart on screen. */
export function shortId(id?: string | null): string {
  if (!id) return dash;
  return id.split("-")[0] ?? id;
}

/** "WAITING_CUSTOMER" → "Waiting customer". */
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

/** 480 → "08:00". */
export function clock(minute: number): string {
  const h = Math.floor(minute / 60);
  const m = minute % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;
}

/** "08:00" → 480; NaN when malformed. "24:00" is the end of the day. */
export function minuteOf(value: string): number {
  const m = /^(\d{2}):(\d{2})$/.exec(value);
  if (!m) return Number.NaN;
  const total = Number(m[1]) * 60 + Number(m[2]);
  return Number(m[2]) < 60 && total <= 24 * 60 ? total : Number.NaN;
}

/** 90 → "1 h 30 min"; 1440 → "24 h". */
export function duration(minutes: number): string {
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  if (h === 0) return `${m} min`;
  return m === 0 ? `${h} h` : `${h} h ${m} min`;
}

/** Today's UTC date as YYYY-MM-DD. */
export function today(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}
