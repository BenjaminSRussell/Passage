import type { ConditionRecord, ConditionType } from "./api/client";

export const TYPE_LABELS: Record<ConditionType, string> = {
  PRICE: "Price",
  LINE_DISC: "Line discount",
  TOTAL_DISC: "Total discount",
};

/** Who a record applies to, in words. */
export function describeCustomer(r: Pick<ConditionRecord, "customerKind" | "customerKey">): string {
  if (r.customerKind === "all") return "All customers";
  if (r.customerKind === "customer_group") return `Group ${r.customerKey}`;
  return r.customerKey;
}

/** What a record applies to, in words. */
export function describeProduct(r: Pick<ConditionRecord, "productKind" | "productKey" | "variant">): string {
  if (r.productKind === "all") return "All products";
  if (r.productKind === "product_group") return `Group ${r.productKey}`;
  if (r.productKind === "variant") return `${r.productKey} / ${r.variant}`;
  return r.productKey;
}

/** "10% + 5%" for the two sequential percentages; empty when neither is set. */
export function describePercent(r: Pick<ConditionRecord, "discPct1" | "discPct2">): string {
  return [r.discPct1, r.discPct2]
    .filter((p) => p !== "" && Number(p) !== 0)
    .map((p) => `${p}%`)
    .join(" + ");
}

/** Today's calendar day in the browser's zone, as YYYY-MM-DD. */
export function today(now: Date = new Date()): string {
  const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 10);
}

/** Adds whole days to a YYYY-MM-DD day. */
export function addDays(day: string, days: number): string {
  const d = new Date(`${day}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
}

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DAY = /^\d{4}-\d{2}-\d{2}$/;
export const DECIMAL = /^\d+(\.\d+)?$/;
