/** Pure helpers for the migration-factory screens. Mirrors tools/migration-factory/internal/domain. */
import type { Area, Category, BillingStatus, LegacyCategoryTotal, OpeningLine, OpenItemRecord, ParallelState, ProjectExtract, Tuple } from "./api/client";

/** The nine transactional packs (FR-49.1). */
export const PACKS = [
  "open_items",
  "gl_balances",
  "onhand",
  "open_orders",
  "wip_prod",
  "fixed_assets",
  "project_wip",
  "payroll_ytd",
  "bank_balances",
] as const;
export type Pack = (typeof PACKS)[number];

/** Packs the API runs today. */
export const AVAILABLE_PACKS: readonly Pack[] = ["open_items", "project_wip"];

export const SOURCE_SYSTEMS = ["AX2012", "D365FO", "SAPECC", "S4HANA"] as const;
export type SourceSystem = (typeof SOURCE_SYSTEMS)[number];

export const SOURCE_LABELS: Record<SourceSystem, string> = {
  AX2012: "AX 2012",
  D365FO: "D365 F&O",
  SAPECC: "SAP ECC",
  S4HANA: "SAP S/4HANA",
};

export const RUN_STATUSES = ["EXTRACTED", "VALIDATED", "LOADED", "RECONCILED", "FAILED", "ROLLED_BACK"] as const;
export type RunStatus = (typeof RUN_STATUSES)[number];
export type ControlStatus = "MATCH" | "WITHIN_TOLERANCE" | "MISMATCH";

export interface ControlTotal {
  name: string;
  key: Record<string, string>;
  source: string;
  target: string;
  delta: string;
  tolerance?: string;
  toleranceReason?: string;
  status: ControlStatus;
}

export type Tone = "ok" | "amber" | "red";

/** Runbook overrun tone: amber at 110 %, red at 125 % by default (9.49.13 parameters). */
export function overrunTone(planned: number, actual: number, amberPct = 110, redPct = 125): Tone {
  if (planned <= 0) return "ok";
  const pct = Math.floor((actual * 100) / planned);
  if (pct >= redPct) return "red";
  if (pct >= amberPct) return "amber";
  return "ok";
}

export const toneLabel: Record<Tone, string> = { ok: "On plan", amber: "Amber", red: "Red" };

/** A run may reach RECONCILED only when no control total is MISMATCH (9.49.4 invariant). */
export function canReconcile(totals: ControlTotal[]): boolean {
  return totals.every((t) => t.status !== "MISMATCH");
}

/** Formats a minute offset from T-0 as T-14d, T+30h, T+45m or T-0. */
export function formatOffset(minutes: number): string {
  if (minutes === 0) return "T-0";
  const sign = minutes < 0 ? "-" : "+";
  const abs = Math.abs(minutes);
  if (abs % 1440 === 0) return `T${sign}${abs / 1440}d`;
  if (abs % 60 === 0) return `T${sign}${abs / 60}h`;
  return `T${sign}${abs}m`;
}

/** Renders a control-total key as "company=USMF, currency=USD". */
export function formatKey(key: Record<string, string>): string {
  return Object.entries(key)
    .map(([k, v]) => `${k}=${v}`)
    .join(", ");
}

export const dash = "—";

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** A datetime-local value ("2026-12-31T18:00") read as UTC, as an RFC 3339 string; null when invalid. */
export function localToUtc(value: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return null;
  const ms = Date.parse(`${value}:00Z`);
  return Number.isNaN(ms) ? null : new Date(ms).toISOString().replace(".000Z", "Z");
}

/** Trims a decimal string with 6 places to at least 2: "125000.000000" -> "125000.00". */
export function money(value: string): string {
  const m = /^(-?\d+)(?:\.(\d*))?$/.exec(value);
  if (!m) return value;
  const frac = (m[2] ?? "").replace(/0+$/, "").padEnd(2, "0");
  return `${m[1]}.${frac}`;
}

const DECIMAL = /^-?\d{1,22}(\.\d{1,6})?$/;
export const CURRENCY = /^[A-Z]{3}$/;
const HEADER = ["legacyid", "party", "account", "currency", "amount"];

export interface ParsedRecords {
  records: OpenItemRecord[];
  errors: string[];
}

/**
 * Parses pasted open items, one per line: legacyId,party,account,currency,amount. A first line
 * that names the columns is skipped. Amounts stay decimal strings. Problems are reported per line
 * so the user can fix the paste; the service applies the referential and domain rules itself.
 */
export function parseRecords(text: string): ParsedRecords {
  const records: OpenItemRecord[] = [];
  const errors: string[] = [];
  const lines = text.split(/\r?\n/);
  lines.forEach((raw, index) => {
    const line = raw.trim();
    if (line === "") return;
    const cells = line.split(/[,;\t]/).map((c) => c.trim());
    if (index === 0 && cells.map((c) => c.toLowerCase()).join(",") === HEADER.join(",")) return;
    const n = index + 1;
    if (cells.length !== 5) {
      errors.push(`Line ${n}: expected 5 values (legacyId, party, account, currency, amount), found ${cells.length}.`);
      return;
    }
    const [legacyId, party, account, currency, amount] = cells;
    if (!legacyId) {
      errors.push(`Line ${n}: the legacy id is blank.`);
      return;
    }
    if (!DECIMAL.test(amount)) {
      errors.push(`Line ${n}: amount "${amount}" is not a decimal such as 1250.00.`);
      return;
    }
    records.push({ legacyId, party, account, currency: currency.toUpperCase(), amount });
  });
  return { records, errors };
}

/** Splits a comma, space or newline separated list and drops blanks and repeats. */
export function splitList(text: string): string[] {
  return [...new Set(text.split(/[\s,;]+/).map((s) => s.trim()).filter(Boolean))];
}

/** Distinct project ids named by a project_wip run's control totals, sorted. */
export function projectsOf(totals: ControlTotal[]): string[] {
  const ids = totals.filter((t) => t.name.startsWith("project_wip.") && t.key.project).map((t) => t.key.project);
  return [...new Set(ids)].sort();
}

/** A project's reconciliation status from its totals (BR-49.12). */
export function projectStatus(totals: ControlTotal[], projectId: string): "RECONCILED" | "OPENING_MISMATCH" {
  return totals.some((t) => t.name.startsWith("project_wip.") && t.key.project === projectId && t.status === "MISMATCH") ? "OPENING_MISMATCH" : "RECONCILED";
}

export const AREAS: readonly Area[] = ["TB", "INV", "AP", "AR", "FA"];
export const CATEGORIES: readonly Category[] = ["HOUR", "EXPENSE", "ITEM", "FEE", "ON_ACCOUNT"];
const BILLING: readonly BillingStatus[] = ["CHARGEABLE_UNBILLED", "BILLED", "NON_CHARGEABLE"];
export const PARALLEL_STATES: readonly ParallelState[] = ["REPLAYING", "DIFFING", "REVIEW", "SIGNED_OFF"];
export const PERIOD = /^\d{4}-(0[1-9]|1[0-2])$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

function rows(text: string, header: string[], each: (cells: string[], n: number) => string | undefined): string[] {
  const errors: string[] = [];
  text.split(/\r?\n/).forEach((raw, index) => {
    const line = raw.trim();
    if (line === "") return;
    const cells = line.split(/[,;\t]/).map((c) => c.trim());
    if (index === 0 && cells.map((c) => c.toLowerCase()).join(",") === header.map((h) => h.toLowerCase()).join(",")) return;
    const n = index + 1;
    if (cells.length !== header.length) {
      errors.push(`Line ${n}: expected ${header.length} values (${header.join(", ")}), found ${cells.length}.`);
      return;
    }
    const problem = each(cells, n);
    if (problem) errors.push(problem);
  });
  return errors;
}

const optionalDecimal = (v: string) => v === "" || DECIMAL.test(v);

/** Parses pasted parallel-run tuples, one per line: area, key, amount (FR-49.5 canonical tuples). */
export function parseTuples(text: string): { tuples: Tuple[]; errors: string[] } {
  const tuples: Tuple[] = [];
  const errors = rows(text, ["area", "key", "amount"], ([area, key, amount], n) => {
    const a = area.toUpperCase() as Area;
    if (!AREAS.includes(a)) return `Line ${n}: area "${area}" is not TB, INV, AP, AR or FA.`;
    if (!key) return `Line ${n}: the key is blank.`;
    if (!DECIMAL.test(amount)) return `Line ${n}: amount "${amount}" is not a decimal such as 1250.00.`;
    tuples.push({ area: a, key, amount });
    return undefined;
  });
  return { tuples, errors };
}

const LINE_HEADER = ["projectId", "legacyId", "category", "transactionDate", "qty", "costAmount", "salesAmount", "currency", "billingStatus", "wipCost", "wipSales"];
const TOTAL_HEADER = ["projectId", "category", "cost", "sales", "wipCost", "wipSales"];

/**
 * Parses a project_wip extract: opening lines (projectId, legacyId, category, transactionDate,
 * qty, costAmount, salesAmount, currency, billingStatus, wipCost, wipSales) and the legacy
 * category totals (projectId, category, cost, sales, wipCost, wipSales), grouped by project in
 * first-seen order. The service applies the FR-49.12 rules; this only checks the shape.
 */
export function parseProjectExtract(linesText: string, totalsText: string): { projects: ProjectExtract[]; lineErrors: string[]; totalErrors: string[] } {
  const byId = new Map<string, ProjectExtract>();
  const project = (id: string) => {
    let p = byId.get(id);
    if (!p) {
      p = { projectId: id, legacyTotals: [], lines: [] };
      byId.set(id, p);
    }
    return p;
  };
  const lineErrors = rows(linesText, LINE_HEADER, (c, n) => {
    const [projectId, legacyId, category, transactionDate, qty, costAmount, salesAmount, currency, billingStatus, wipCost, wipSales] = c;
    if (!projectId || !legacyId) return `Line ${n}: the project and legacy id are required.`;
    const cat = category.toUpperCase() as Category;
    if (!CATEGORIES.includes(cat)) return `Line ${n}: category "${category}" is not HOUR, EXPENSE, ITEM, FEE or ON_ACCOUNT.`;
    if (!DATE.test(transactionDate)) return `Line ${n}: date "${transactionDate}" is not YYYY-MM-DD.`;
    const bill = billingStatus.toUpperCase() as BillingStatus;
    if (!BILLING.includes(bill)) return `Line ${n}: billing status "${billingStatus}" is not CHARGEABLE_UNBILLED, BILLED or NON_CHARGEABLE.`;
    if (![qty, costAmount, salesAmount, wipCost, wipSales].every(optionalDecimal)) return `Line ${n}: amounts must be decimals such as 400.00.`;
    const line: OpeningLine = { legacyId, category: cat, transactionDate, costAmount: costAmount || "0", salesAmount: salesAmount || "0", currency: currency.toUpperCase(), billingStatus: bill };
    if (qty) line.qty = qty;
    if (wipCost) line.wipCost = wipCost;
    if (wipSales) line.wipSales = wipSales;
    project(projectId).lines.push(line);
    return undefined;
  });
  const totalErrors = rows(totalsText, TOTAL_HEADER, ([projectId, category, cost, sales, wipCost, wipSales], n) => {
    if (!projectId) return `Line ${n}: the project id is required.`;
    const cat = category.toUpperCase() as Category;
    if (!CATEGORIES.includes(cat)) return `Line ${n}: category "${category}" is not HOUR, EXPENSE, ITEM, FEE or ON_ACCOUNT.`;
    if (![cost, sales, wipCost, wipSales].every(optionalDecimal)) return `Line ${n}: totals must be decimals such as 48000.00.`;
    const p = project(projectId);
    if (p.legacyTotals.some((t) => t.category === cat)) return `Line ${n}: project ${projectId} already has a ${cat} total.`;
    const total: LegacyCategoryTotal = { category: cat };
    if (cost) total.cost = cost;
    if (sales) total.sales = sales;
    if (wipCost) total.wipCost = wipCost;
    if (wipSales) total.wipSales = wipSales;
    p.legacyTotals.push(total);
    return undefined;
  });
  return { projects: [...byId.values()], lineErrors, totalErrors };
}
