export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** A positive or zero quantity as the service accepts it: up to 12 integer digits and 6 decimals. */
export const QUANTITY = /^\d{1,12}(\.\d{1,6})?$/;

/** A percentage with at most 4 decimals. */
export const PERCENT = /^\d{1,3}(\.\d{1,4})?$/;

/** The first block of an id, enough to tell rows apart on screen. */
export function shortId(id: string): string {
  return id.split("-")[0] ?? id;
}

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

const SCALE = 4;

function toUnits(value: string): bigint | null {
  const m = /^(\d+)(?:\.(\d{0,4}))?$/.exec(value.trim());
  if (!m) return null;
  return BigInt(m[1]) * 10n ** BigInt(SCALE) + BigInt((m[2] ?? "").padEnd(SCALE, "0") || "0");
}

/**
 * Sums percentages exactly (4 decimals, as numeric(9,4)) without floating
 * point. Returns null when a value is not a percentage.
 */
export function sumPercent(values: string[]): string | null {
  let total = 0n;
  for (const v of values) {
    const units = toUnits(v === "" ? "0" : v);
    if (units === null) return null;
    total += units;
  }
  const s = total.toString().padStart(SCALE + 1, "0");
  return `${s.slice(0, -SCALE)}.${s.slice(-SCALE)}`;
}

/** True when a percentage is at most 100. */
export function percentAtMost100(value: string): boolean {
  const units = toUnits(value);
  return units !== null && units <= 1_000_000n;
}

/** FR-24.1: percent-controlled scalable lines sum to 100 within ±0.01. */
export function percentBalanced(total: string): boolean {
  const units = toUnits(total);
  if (units === null) return false;
  const diff = units - 1_000_000n;
  return diff >= -100n && diff <= 100n;
}

export const LINE_TYPE_LABELS: Record<string, string> = {
  ingredient: "Ingredient",
  "active-ingredient": "Active ingredient",
  phantom: "Phantom",
  "co-product": "Co-product",
  "by-product": "By-product",
};
