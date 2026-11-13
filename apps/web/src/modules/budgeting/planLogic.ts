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

/** "6100, 6200" becomes ["6100", "6200"]; empty entries are dropped. */
export function parseAccounts(text: string): string[] {
  return text
    .split(/[,\n]/)
    .map((a) => a.trim())
    .filter((a) => a !== "");
}

/** "CC1=2, CC2=1" becomes weights; a bare "CC1" has weight 1. Returns null for a weight that is not a number. */
export function parseCostCenterWeights(text: string): { costCenter: string; weight: string }[] | null {
  const out: { costCenter: string; weight: string }[] = [];
  for (const part of text.split(/[,\n]/).map((p) => p.trim()).filter((p) => p !== "")) {
    const [name, weight = "1"] = part.split("=").map((p) => p.trim());
    if (name === "" || !WEIGHT.test(weight)) return null;
    out.push({ costCenter: name, weight });
  }
  return out;
}

export type GenerateSourceKind = import("./api/plans").GenerateSource;

export const GENERATE_SOURCES: { value: GenerateSourceKind; label: string }[] = [
  { value: "prior-plan", label: "Last year's approved plan" },
  { value: "gl-actuals", label: "What was posted last year" },
  { value: "fixed-assets", label: "Depreciation of the fixed assets" },
  { value: "positions", label: "Cost of the HR positions" },
  { value: "projects", label: "Project forecasts" },
  { value: "demand", label: "Demand forecast times unit price" },
];

export interface RateRow {
  jobCode: string;
  monthlyCost: string;
}
export interface DepartmentRow {
  departmentId: string;
  costCenter: string;
}
export interface ProjectRow {
  projectId: string;
  modelId: string;
  costCenter: string;
  amount: "cost" | "sales";
}
export interface UnitPriceRow {
  itemId: string;
  price: string;
}

export interface GenerateForm {
  source: GenerateSourceKind;
  adjustment: string;
  /** prior-plan and gl-actuals: the accounts, separated by commas. */
  accounts: string;
  /** gl-actuals, fixed-assets and demand: CC1=2, CC2=1. */
  costCenters: string;
  /** The account the fixed-assets, positions, projects and demand amounts are planned on. */
  account: string;
  book: string;
  modelCode: string;
  rates: RateRow[];
  departments: DepartmentRow[];
  projects: ProjectRow[];
  unitPrices: UnitPriceRow[];
}

export const emptyRate = (): RateRow => ({ jobCode: "", monthlyCost: "" });
export const emptyDepartment = (): DepartmentRow => ({ departmentId: "", costCenter: "" });
export const emptyProject = (): ProjectRow => ({ projectId: "", modelId: "", costCenter: "", amount: "cost" });
export const emptyUnitPrice = (): UnitPriceRow => ({ itemId: "", price: "" });

export function emptyGenerate(): GenerateForm {
  return { source: "prior-plan", adjustment: "", accounts: "", costCenters: "", account: "", book: "", modelCode: "", rates: [emptyRate()], departments: [emptyDepartment()], projects: [emptyProject()], unitPrices: [emptyUnitPrice()] };
}

/** The driver sources: amounts read from another service (FR-38.14) and planned on one account. */
export const isDriverSource = (s: GenerateSourceKind) => s === "fixed-assets" || s === "positions" || s === "projects" || s === "demand";
/** The sources whose one amount per month is shared over cost centers by weight. */
export const usesWeights = (s: GenerateSourceKind) => s === "gl-actuals" || s === "fixed-assets" || s === "demand";

const MONEY4 = /^\d{1,15}(\.\d{1,4})?$/;
const PROJECT_ID = /^[A-Z0-9][A-Z0-9-]{0,19}$/;
const MAX_SOURCE_ROWS = 200;
const positive = (v: string) => MONEY4.test(v.trim()) && Number(v.trim()) > 0;

export function validateGenerate(f: GenerateForm): Record<string, string> {
  const errors: Record<string, string> = {};
  const adj = f.adjustment.trim();
  if (adj !== "" && (!/^-?\d{1,4}(\.\d{1,4})?$/.test(adj) || Number(adj) <= -100 || Number(adj) > 1000)) errors.adjustment = "Enter a percentage above -100 and at most 1000, such as 5 or -10.";
  if (!isDriverSource(f.source)) {
    const accounts = parseAccounts(f.accounts);
    if (new Set(accounts).size !== accounts.length) errors.accounts = "An account appears twice.";
    if (f.source === "gl-actuals" && accounts.length === 0) errors.accounts = "Name the accounts to read from the ledger.";
  } else {
    const account = f.account.trim();
    if (account === "" || account.length > 100) errors.account = "Name the account the amounts are planned on.";
  }
  if (usesWeights(f.source)) {
    const weights = parseCostCenterWeights(f.costCenters);
    if (weights === null) errors.costCenters = "Write each cost center as CC1 or CC1=2 (the weight), separated by commas.";
    else if (weights.length === 0) errors.costCenters = "Name at least one cost center.";
    else if (weights.every((w) => Number(w.weight) === 0)) errors.costCenters = "Give at least one cost center a weight above zero.";
    else if (new Set(weights.map((w) => w.costCenter)).size !== weights.length) errors.costCenters = "A cost center appears twice.";
  }
  const rowsError = (rows: unknown[], name: string, what: string) => {
    if (rows.length === 0 || rows.length > MAX_SOURCE_ROWS) errors[name] = `Add 1 to ${MAX_SOURCE_ROWS} ${what}.`;
  };
  switch (f.source) {
    case "fixed-assets":
      if (f.book.trim().length > 50) errors.book = "A book code has at most 50 characters.";
      break;
    case "positions": {
      rowsError(f.rates, "rates", "job costs");
      const jobs = new Set<string>();
      f.rates.forEach((r, i) => {
        const job = r.jobCode.trim();
        if (job === "") errors[`rates.${i}.jobCode`] = "Enter the job code.";
        else if (jobs.has(job)) errors[`rates.${i}.jobCode`] = "This job appears twice.";
        jobs.add(job);
        if (!positive(r.monthlyCost)) errors[`rates.${i}.monthlyCost`] = "Enter a monthly cost above 0, such as 6500.";
      });
      rowsError(f.departments, "departments", "departments");
      const deps = new Set<string>();
      f.departments.forEach((d, i) => {
        if (d.departmentId === "") errors[`departments.${i}.departmentId`] = "Choose the department.";
        else if (deps.has(d.departmentId)) errors[`departments.${i}.departmentId`] = "This department appears twice.";
        deps.add(d.departmentId);
        if (d.costCenter.trim() === "" || d.costCenter.trim().length > 100) errors[`departments.${i}.costCenter`] = "Enter the cost center.";
      });
      break;
    }
    case "projects": {
      rowsError(f.projects, "projects", "projects");
      const pairs = new Set<string>();
      f.projects.forEach((p, i) => {
        if (!PROJECT_ID.test(p.projectId)) errors[`projects.${i}.projectId`] = "Choose the project.";
        if (p.modelId === "") errors[`projects.${i}.modelId`] = "Choose the forecast model.";
        else if (pairs.has(`${p.projectId}|${p.modelId}`)) errors[`projects.${i}.modelId`] = "This project and forecast appear twice.";
        pairs.add(`${p.projectId}|${p.modelId}`);
        if (p.costCenter.trim() === "" || p.costCenter.trim().length > 100) errors[`projects.${i}.costCenter`] = "Enter the cost center.";
      });
      break;
    }
    case "demand": {
      const code = f.modelCode.trim();
      if (code === "" || code.length > 100) errors.modelCode = "Enter the code of the planning forecast.";
      rowsError(f.unitPrices, "unitPrices", "items with a price");
      const items = new Set<string>();
      f.unitPrices.forEach((u, i) => {
        if (u.itemId === "") errors[`unitPrices.${i}.itemId`] = "Choose the item.";
        else if (items.has(u.itemId)) errors[`unitPrices.${i}.itemId`] = "This item appears twice.";
        items.add(u.itemId);
        if (!positive(u.price)) errors[`unitPrices.${i}.price`] = "Enter a unit price above 0, such as 12.50.";
      });
      break;
    }
  }
  return errors;
}

export function toGenerateInput(f: GenerateForm): import("./api/plans").GenerateInput {
  const base = { source: f.source, ...(f.adjustment.trim() !== "" ? { adjustmentPct: f.adjustment.trim() } : {}) };
  const weights = usesWeights(f.source) ? { costCenters: parseCostCenterWeights(f.costCenters) ?? [] } : {};
  switch (f.source) {
    case "prior-plan":
    case "gl-actuals": {
      const accounts = parseAccounts(f.accounts);
      return { ...base, ...(accounts.length > 0 ? { accounts } : {}), ...weights };
    }
    case "fixed-assets":
      return { ...base, account: f.account.trim(), ...(f.book.trim() !== "" ? { book: f.book.trim() } : {}), ...weights };
    case "positions":
      return {
        ...base,
        account: f.account.trim(),
        rates: f.rates.map((r) => ({ jobCode: r.jobCode.trim(), monthlyCost: r.monthlyCost.trim() })),
        departments: f.departments.map((d) => ({ departmentId: d.departmentId, costCenter: d.costCenter.trim() })),
      };
    case "projects":
      return { ...base, account: f.account.trim(), projects: f.projects.map((p) => ({ projectId: p.projectId, modelId: p.modelId, costCenter: p.costCenter.trim(), amount: p.amount })) };
    case "demand":
      return { ...base, account: f.account.trim(), modelCode: f.modelCode.trim(), unitPrices: f.unitPrices.map((u) => ({ itemId: u.itemId, price: u.price.trim() })), ...weights };
  }
}
