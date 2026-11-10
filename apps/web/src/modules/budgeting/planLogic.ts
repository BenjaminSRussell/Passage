/** Pure helpers for the plan grid: pivoting cells into rows, checking edits and totalling. No I/O, no React. */
import { fromUnits, isMoney, toUnits } from "./logic";
import type { PlanCell, SpreadInput, SpreadMethod } from "./api/plans";

export const PERIODS_PER_YEAR = 12;

/** The 12 periods of a fiscal year (January start), as YYYY-MM. */
export function periodsOf(year: number): string[] {
  return Array.from({ length: PERIODS_PER_YEAR }, (_, i) => `${year}-${String(i + 1).padStart(2, "0")}`);
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

export function periodLabel(period: string): string {
  const month = Number(period.slice(5, 7));
  return MONTHS[month - 1] ?? period;
}

/** Key of one cell in the edit map. */
export function cellKey(account: string, costCenter: string, period: string): string {
  return `${account}\t${costCenter}\t${period}`;
}

export interface GridRow {
  account: string;
  costCenter: string;
}

/** Account and cost center pairs that hold a cell, in account then cost center order. */
export function rowsOf(cells: PlanCell[], extra: GridRow[] = []): GridRow[] {
  const seen = new Map<string, GridRow>();
  for (const c of [...cells, ...extra]) seen.set(`${c.account}\t${c.costCenter}`, { account: c.account, costCenter: c.costCenter });
  return [...seen.values()].sort((a, b) => a.account.localeCompare(b.account) || a.costCenter.localeCompare(b.costCenter));
}

export function amountMap(cells: PlanCell[]): Map<string, string> {
  return new Map(cells.map((c) => [cellKey(c.account, c.costCenter, c.period), c.amount]));
}

/** A grid amount is not negative and has at most 4 decimal places; an empty box means "no cell". */
export function cellError(value: string): string | undefined {
  const v = value.trim();
  if (v === "") return undefined;
  if (v.startsWith("-")) return "Amounts cannot be negative.";
  if (!isMoney(v)) return "Use a number with at most 4 decimal places.";
  return undefined;
}

/** The cells that differ from what was loaded: new cells and changed amounts, never empty boxes. */
export function changedCells(loaded: Map<string, string>, edits: Map<string, string>): PlanCell[] {
  const out: PlanCell[] = [];
  for (const [key, raw] of edits) {
    const value = raw.trim();
    if (value === "" || cellError(value)) continue;
    const before = loaded.get(key);
    if (before !== undefined && toUnits(before) === toUnits(value)) continue;
    const [account, costCenter, period] = key.split("\t");
    out.push({ account, costCenter, period, amount: fromUnits(toUnits(value), 4).replace(/(\.\d*?)0+$/, "$1").replace(/\.$/, "") });
  }
  return out;
}

export function editErrors(edits: Map<string, string>): string[] {
  return [...edits.entries()].filter(([, v]) => cellError(v)).map(([k]) => k);
}

/** Total of a row, a column or the whole grid as shown (loaded amounts overlaid with the edits). */
export function total(values: (string | undefined)[]): string {
  return fromUnits(values.reduce<bigint>((sum, v) => (v && isMoney(v.trim()) ? sum + toUnits(v.trim()) : sum), 0n));
}

export interface SpreadForm {
  account: string;
  total: string;
  method: SpreadMethod;
  periodWeights: string[];
  costCenters: { costCenter: string; weight: string }[];
}

export function emptySpread(): SpreadForm {
  return { account: "", total: "", method: "even", periodWeights: Array(PERIODS_PER_YEAR).fill("1"), costCenters: [{ costCenter: "", weight: "1" }] };
}

const WEIGHT = /^\d+(\.\d{1,4})?$/;

/** Mirrors the service's checks (BUD-0060 and the field rules) so a mistake shows before the round trip. */
export function validateSpread(f: SpreadForm): Record<string, string> {
  const errors: Record<string, string> = {};
  if (f.account.trim() === "") errors.account = "Enter the account to spread.";
  if (!isMoney(f.total.trim()) || f.total.trim().startsWith("-")) errors.total = "Enter a total, not negative, with at most 4 decimal places.";
  const centers = f.costCenters.filter((c) => c.costCenter.trim() !== "" || c.weight.trim() !== "");
  if (centers.length === 0) errors.costCenters = "Add at least one cost center.";
  else if (centers.some((c) => c.costCenter.trim() === "")) errors.costCenters = "Every weight needs a cost center.";
  else if (new Set(centers.map((c) => c.costCenter.trim())).size !== centers.length) errors.costCenters = "A cost center can appear only once.";
  else if (centers.some((c) => !WEIGHT.test(c.weight.trim()))) errors.costCenters = "Weights are numbers, not negative, with at most 4 decimal places.";
  else if (centers.every((c) => Number(c.weight) === 0)) errors.costCenters = "Give at least one cost center a weight above zero.";
  if (f.method === "custom") {
    if (f.periodWeights.some((w) => !WEIGHT.test(w.trim()))) errors.periodWeights = "Each period needs a weight: a number, not negative, with at most 4 decimal places.";
    else if (f.periodWeights.every((w) => Number(w) === 0)) errors.periodWeights = "Give at least one period a weight above zero.";
  }
  return errors;
}

export function toSpreadInput(f: SpreadForm): SpreadInput {
  return {
    account: f.account.trim(),
    total: f.total.trim(),
    method: f.method,
    ...(f.method === "custom" ? { periodWeights: f.periodWeights.map((w) => w.trim()) } : {}),
    costCenters: f.costCenters.filter((c) => c.costCenter.trim() !== "").map((c) => ({ costCenter: c.costCenter.trim(), weight: c.weight.trim() })),
  };
}

export const METHOD_LABELS: Record<SpreadMethod, string> = {
  even: "Evenly over the 12 periods",
  "prior-year-actuals": "Like last year's actuals",
  custom: "By my own period weights",
};
