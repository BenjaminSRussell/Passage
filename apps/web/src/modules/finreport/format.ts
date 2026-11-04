import type { CalendarPeriod, DefinitionKind, HierarchySubject, RowKind } from "./api/client";

export const KIND_LABELS: Record<DefinitionKind, string> = {
  trial_balance: "Trial balance",
  balance_sheet: "Balance sheet",
  income_statement: "Income statement",
  cash_flow: "Cash flow",
  custom: "Custom",
};

export const ROW_KIND_LABELS: Record<RowKind, string> = {
  header: "Header",
  account_range: "Account range",
  dimension_filter: "Dimension filter",
  total: "Total",
  calculation: "Calculation",
};

export const SUBJECT_LABELS: Record<HierarchySubject, string> = {
  main_account: "Main accounts",
  main_account_category: "Main account categories",
  dimension_value: "Dimension values",
};

export const CURRENCY_TYPE_LABELS = { acct: "Accounting", rpt: "Reporting", txn: "Transaction" } as const;

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DEFINITION_CODE = /^[A-Z][A-Z0-9-]{1,29}$/;

export function periodLabel(year: number, no: number): string {
  return `${year}-${String(no).padStart(2, "0")}`;
}

export function shortHash(hash: string): string {
  return hash.slice(0, 12);
}

/**
 * A fiscal calendar of whole years: an opening period 0 (brought-forward
 * balances) and `periods` regular periods per year, quarters spread evenly.
 * Stands in for the ledger calendar API until it exists (wave 14 handoff).
 */
export function fiscalCalendar(fromYear: number, toYear: number, periods = 12): CalendarPeriod[] {
  const out: CalendarPeriod[] = [];
  const perQuarter = Math.ceil(periods / 4);
  for (let year = fromYear; year <= toYear; year++) {
    out.push({ year, no: 0, quarter: 1 });
    for (let no = 1; no <= periods; no++) {
      out.push({ year, no, quarter: Math.min(4, Math.floor((no - 1) / perQuarter) + 1) });
    }
  }
  return out;
}

/** "cost_center=CC100,CC200; region=*" → { cost_center: ["CC100","CC200"], region: ["*"] }. */
export function parseDimensionFilter(text: string): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const part of text.split(";")) {
    const [attr, values = ""] = part.split("=");
    const name = attr.trim();
    if (name === "") continue;
    out[name] = values
      .split(",")
      .map((v) => v.trim())
      .filter((v) => v !== "");
  }
  return out;
}

export function formatDimensionFilter(filter: Record<string, string[]> | undefined): string {
  return Object.entries(filter ?? {})
    .map(([attr, values]) => `${attr}=${values.join(",")}`)
    .join("; ");
}

/** "A, B C" → ["A", "B", "C"]. */
export function parseReferences(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((r) => r.trim())
    .filter((r) => r !== "");
}

export const VERIFICATION_LABELS = { matched: "Matched", differs: "Differs", inconclusive: "Inconclusive" } as const;
export const VERIFICATION_TONES = { matched: "success", differs: "danger", inconclusive: "warning" } as const;
