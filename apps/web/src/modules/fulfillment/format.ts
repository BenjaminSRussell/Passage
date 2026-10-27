import type { ReleaseSkip, ShipmentStatus } from "./api/client";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** A decimal string with at most 6 decimals, the precision the service stores. */
export const DECIMAL = /^\d{1,14}(\.\d{1,6})?$/;

/** Today's date as YYYY-MM-DD (UTC). */
export function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/** The date part of an ISO timestamp, or a dash. */
export function formatDate(iso?: string | null): string {
  if (!iso) return dash;
  return /^\d{4}-\d{2}-\d{2}/.test(iso) ? iso.slice(0, 10) : iso;
}

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** Decimal string with exactly `places` decimals, rounded half away from zero on the digits (no floats). */
export function fixed(value: string, places: number): string {
  const negative = value.startsWith("-");
  const [whole = "0", frac = ""] = (negative ? value.slice(1) : value).split(".");
  if (frac.length <= places) {
    return `${negative ? "-" : ""}${whole}${places > 0 ? `.${frac.padEnd(places, "0")}` : ""}`;
  }
  const digits = `${whole}${frac.slice(0, places)}`.split("").map(Number);
  if (Number(frac[places]) >= 5) {
    let i = digits.length - 1;
    while (i >= 0) {
      if (digits[i] === 9) {
        digits[i] = 0;
        i -= 1;
      } else {
        digits[i] += 1;
        break;
      }
    }
    if (i < 0) digits.unshift(1);
  }
  const s = digits.join("");
  const w = s.slice(0, s.length - places) || "0";
  return `${negative ? "-" : ""}${w}${places > 0 ? `.${s.slice(s.length - places)}` : ""}`;
}

/** A money amount as "125.00 USD". */
export function formatMoney(value: string, currency: string): string {
  return `${fixed(value, 2)} ${currency}`;
}

/** Compares two non-negative decimal strings exactly: -1, 0 or 1. */
export function compareDecimal(a: string, b: string): number {
  const [aw = "0", af = ""] = a.split(".");
  const [bw = "0", bf = ""] = b.split(".");
  const width = Math.max(af.length, bf.length);
  const x = BigInt(`${aw}${af.padEnd(width, "0")}`);
  const y = BigInt(`${bw}${bf.padEnd(width, "0")}`);
  return x === y ? 0 : x < y ? -1 : 1;
}

/** a - b for decimal strings, exactly (BigInt on scaled digits). */
export function subtractDecimal(a: string, b: string): string {
  const [aw = "0", af = ""] = a.split(".");
  const [bw = "0", bf = ""] = b.split(".");
  const width = Math.max(af.length, bf.length);
  const diff = BigInt(`${aw}${af.padEnd(width, "0")}`) - BigInt(`${bw}${bf.padEnd(width, "0")}`);
  const negative = diff < 0n;
  const digits = (negative ? -diff : diff).toString().padStart(width + 1, "0");
  const whole = digits.slice(0, digits.length - width);
  const frac = width > 0 ? digits.slice(digits.length - width).replace(/0+$/, "") : "";
  return `${negative ? "-" : ""}${whole}${frac ? `.${frac}` : ""}`;
}

/** a × b for non-negative decimal strings, rounded half away from zero to `places` (journal preview). */
export function multiplyDecimal(a: string, b: string, places: number): string {
  const [aw = "0", af = ""] = a.split(".");
  const [bw = "0", bf = ""] = b.split(".");
  const product = (BigInt(`${aw}${af}`) * BigInt(`${bw}${bf}`)).toString();
  const scale = af.length + bf.length;
  const padded = product.padStart(scale + 1, "0");
  return fixed(`${padded.slice(0, padded.length - scale)}${scale > 0 ? `.${padded.slice(padded.length - scale)}` : ""}`, places);
}

/** Sum of decimal strings with `places` decimals, exactly. */
export function sumDecimals(values: string[], places: number): string {
  const scale = 10n ** BigInt(places);
  let total = 0n;
  for (const v of values) {
    const [whole = "0", frac = ""] = fixed(v, places).replace("-", "").split(".");
    const n = BigInt(whole) * scale + BigInt(frac || "0");
    total += v.startsWith("-") ? -n : n;
  }
  const negative = total < 0n;
  const abs = negative ? -total : total;
  const frac = (abs % scale).toString().padStart(places, "0");
  return `${negative ? "-" : ""}${abs / scale}${places > 0 ? `.${frac}` : ""}`;
}

export const SHIPMENT_STATUS_LABELS: Record<ShipmentStatus, string> = {
  OPEN: "Open",
  RELEASED: "Released",
  PICKING: "Picking",
  PICKED: "Picked",
  PACKED: "Packed",
  SHIPPED: "Shipped",
  DELIVERED: "Delivered",
  CANCELLED: "Cancelled",
};

export const SKIP_LABELS: Record<ReleaseSkip["reason"], string> = {
  held: "On hold",
  beyond_horizon: "Ships after the release horizon",
  atp_short: "Not fully available (ATP)",
  non_positive_qty: "No quantity to release",
  already_released: "Already on an open shipment",
};

/** Short form of a UUID for dense tables: the first 8 characters. */
export function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}
