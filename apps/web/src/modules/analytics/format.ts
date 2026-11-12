import type { Metric, TileValue } from "./api/client";

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

const TILE_STATUS_TEXT: Record<Exclude<TileValue["status"], "OK">, string> = {
  NO_SCOPE: "No data scope",
  NOT_MODELLED: "Not modelled yet",
  UNAVAILABLE: "Unavailable",
  MIXED_CURRENCY: "Several currencies",
};

/** What a tile shows: the value (a percentage for a ratio), "No data" without rows, or why there is no value. */
export function tileValueText(t: TileValue): string {
  if (t.status !== "OK") return TILE_STATUS_TEXT[t.status];
  if (t.value === null) return "No data";
  const n = Number(t.value);
  if (!Number.isFinite(n)) return t.value;
  if (t.unit === "RATIO") return `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 }).format(n * 100)}%`;
  const amount = new Intl.NumberFormat("en-US", { maximumFractionDigits: 2, minimumFractionDigits: 2 }).format(n);
  return t.currency ? `${amount} ${t.currency}` : amount;
}

/** One line under a tile value that explains a status the person cannot fix themselves. */
export function tileStatusHint(t: TileValue): string {
  switch (t.status) {
    case "NO_SCOPE":
      return "You have no legal entity assigned, so no figures are shown.";
    case "NOT_MODELLED":
      return "This metric is governed but the analytics engine does not calculate it yet.";
    case "MIXED_CURRENCY":
      return "Your data scope holds more than one currency, and amounts are never added across currencies. Use the explorer, grouped by currency.";
    case "UNAVAILABLE":
      return "The analytics engine did not answer. The tile refreshes on its own.";
    default:
      return t.unit === "RATIO" ? "Share of the period" : t.currency ? "Ledger currency" : "No amounts in the period";
  }
}
