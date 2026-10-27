import type { PayLine } from "./api/client";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const DAY_MS = 86_400_000;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Today's date as YYYY-MM-DD (UTC). */
export function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/** Adds whole days to a YYYY-MM-DD date. */
export function addDays(date: string, days: number): string {
  return new Date(Date.parse(`${date}T00:00:00Z`) + days * DAY_MS).toISOString().slice(0, 10);
}

/** Inclusive number of days from `from` to `to`; null when either is not a date. */
export function daysBetween(from: string, to: string): number | null {
  if (!DATE.test(from) || !DATE.test(to)) return null;
  return Math.round((Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / DAY_MS) + 1;
}

/** The viewer's IANA zone, or UTC when the runtime does not say. */
export function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** Decimal hours as "10.50 h". Strings stay decimal; no float arithmetic on values. */
export function formatHours(value?: string | null): string {
  if (value === undefined || value === null || value === "") return dash;
  return `${fixed(value, 2)} h`;
}

/** Decimal string with exactly `places` decimals, without converting to a float. */
export function fixed(value: string, places: number): string {
  const negative = value.startsWith("-");
  const [whole = "0", frac = ""] = (negative ? value.slice(1) : value).split(".");
  if (frac.length <= places) {
    return `${negative ? "-" : ""}${whole}${places > 0 ? `.${frac.padEnd(places, "0")}` : ""}`;
  }
  // Round half away from zero on the digit string.
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

/** Sums decimal strings exactly by scaling to integers (BigInt). */
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
  const whole = abs / scale;
  const frac = (abs % scale).toString().padStart(places, "0");
  return `${negative ? "-" : ""}${whole}${places > 0 ? `.${frac}` : ""}`;
}

/** Total pay of a day per currency, e.g. "235.00 USD". */
export function formatPayTotal(lines: PayLine[]): string {
  if (lines.length === 0) return dash;
  const byCurrency = new Map<string, string[]>();
  for (const l of lines) byCurrency.set(l.currency, [...(byCurrency.get(l.currency) ?? []), l.amount]);
  return [...byCurrency.entries()].map(([currency, amounts]) => `${sumDecimals(amounts, 2)} ${currency}`).join(", ");
}

/** "base 8.00 h, overtime-1.5 2.50 h". */
export function describeLines(lines: PayLine[]): string {
  if (lines.length === 0) return dash;
  return lines.map((l) => `${l.type} ${fixed(l.hours, 2)} h`).join(", ");
}

export const FLAG_LABELS: Record<string, string> = {
  "missing-clock-out": "Missing clock-out",
  "orphan-clock-out": "Clock-out without clock-in",
};

/** A decimal ratio string as a percentage, e.g. "0.8750" -> "87.5 %", without floats. */
export function percent(value?: string | null, places = 1): string {
  if (value === undefined || value === null || value === "") return dash;
  const negative = value.startsWith("-");
  const [whole = "0", frac = ""] = (negative ? value.slice(1) : value).split(".");
  const padded = frac.padEnd(2, "0");
  const shifted = `${whole}${padded.slice(0, 2)}`.replace(/^0+(?=\d)/, "");
  return `${negative ? "-" : ""}${fixed(`${shifted}.${padded.slice(2) || "0"}`, places)} %`;
}

/** Whole minutes from start to end (or to now while open), e.g. "22 min". */
export function durationMinutes(start: string, end?: string | null, now: number = Date.now()): string {
  const from = Date.parse(start);
  const to = end ? Date.parse(end) : now;
  if (Number.isNaN(from) || Number.isNaN(to)) return dash;
  return `${Math.max(0, Math.floor((to - from) / 60_000))} min`;
}

/** A device clock correction in seconds as "+7 min" or "−45 s" (server minus device). */
export function formatOffset(seconds?: number | null): string {
  if (seconds === undefined || seconds === null) return dash;
  const sign = seconds < 0 ? "−" : "+";
  const abs = Math.abs(seconds);
  return abs % 60 === 0 ? `${sign}${abs / 60} min` : abs < 60 ? `${sign}${abs} s` : `${sign}${Math.floor(abs / 60)} min ${abs % 60} s`;
}
