import type { Metric } from "./api/client";

const grainLabels: Record<Metric["grain"], string> = {
  journal_line: "Journal line",
  sales_line: "Sales line",
  resource_shift: "Resource shift",
};

export function grainLabel(grain: Metric["grain"]): string {
  return grainLabels[grain];
}

/** "legal_entity_id" -> "Legal entity id". */
export function dimensionLabel(dimension: string): string {
  const spaced = dimension.replace(/_/g, " ");
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

export function reconciledAgainst(m: Metric): string {
  return m.nonFinancial ? "Non-financial" : (m.reconciliationSource ?? "");
}

/** Today in the browser's time zone as YYYY-MM-DD. */
export function today(now = new Date()): string {
  const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 10);
}

/** The day before a YYYY-MM-DD date: the default reconciliation day, which runs at 02:00 for yesterday. */
export function dayBefore(date: string): string {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() - 1);
  return d.toISOString().slice(0, 10);
}

/** The first day of the month of a YYYY-MM-DD date. */
export function monthStart(date: string): string {
  return `${date.slice(0, 8)}01`;
}

export const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Short form of a UUID for table cells; the full value stays in the title. */
export function shortId(id: string): string {
  return id.length > 8 ? `${id.slice(0, 8)}…` : id;
}

/** "2026-10-28T08:00:00Z" -> "2026-10-28 08:00 UTC"; empty for a missing value. */
export function dateTime(iso: string | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** "COMPLETENESS" -> "Completeness". */
export function enumLabel(value: string): string {
  const spaced = value.toLowerCase().replace(/_/g, " ");
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

/** Thousands separators for row counts; counts are integers, never money. */
export function count(n: number): string {
  return n.toLocaleString("en-US");
}
