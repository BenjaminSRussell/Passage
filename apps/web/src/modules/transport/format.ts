import type { BadgeTone } from "../../shell/ui";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/** A non-negative decimal string such as 412.50 (amounts never become floats). */
export const AMOUNT = /^\d{1,14}(\.\d{1,6})?$/;

export const DATE = /^\d{4}-\d{2}-\d{2}$/;

export const CURRENCY = /^[A-Z]{3}$/;

/**
 * Formats a decimal string to `places` decimals, rounding half away from zero on
 * the digits so no float rounding error can creep in.
 */
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

/** "412.50 USD"; a missing amount shows a dash. */
export function money(value: string | undefined | null, currency: string): string {
  return value === undefined || value === null || value === "" ? dash : `${fixed(value, 2)} ${currency}`;
}

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** Shortens a UUID for table cells; the full id stays in the title attribute. */
export function shortId(id: string): string {
  return id.length > 13 ? `${id.slice(0, 8)}…` : id;
}

/** Badge tone for a freight bill or carrier invoice match state (BR-34.4). */
export function matchTone(status: string): BadgeTone {
  switch (status) {
    case "MATCHED":
    case "APPROVED":
    case "SETTLED":
      return "success";
    case "VARIANCE":
    case "DISPUTED":
      return "warning";
    case "UNEXPECTED_INVOICE":
      return "danger";
    default:
      return "neutral";
  }
}

const labels: Record<string, string> = {
  UNMATCHED: "Unmatched",
  MATCHED: "Matched",
  VARIANCE: "Variance",
  DISPUTED: "Disputed",
  APPROVED: "Approved",
  SETTLED: "Settled",
  UNEXPECTED_INVOICE: "Unexpected invoice",
  OK: "Rated",
  NO_VALID_RATE_CARD: "No valid rate card",
  NO_LANE_RATE: "No lane rate",
};

export function statusLabel(status: string): string {
  return labels[status] ?? status;
}
