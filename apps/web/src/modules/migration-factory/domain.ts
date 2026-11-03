/** Pure helpers for the migration-factory screens. Mirrors tools/migration-factory/internal/domain. */
import type {
  Area,
  Category,
  BillingStatus,
  GLBalanceRecord,
  BankBalanceRecord,
  OnhandLineRecord,
  LegacyCategoryTotal,
  OpeningLine,
  OpenItemRecord,
  ParallelState,
  ProjectExtract,
  RekeyPosting,
  Tuple,
} from "./api/client";

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

/** Whole minutes since a step started, for the live runbook timer (FR-49.6). Never negative; 0 for a bad time. */
export function elapsedSince(startedAt: string, nowMs: number): number {
  const started = Date.parse(startedAt);
  if (Number.isNaN(started)) return 0;
  return Math.max(0, Math.floor((nowMs - started) / 60_000));
}

/** "2 h 05 min" or "45 min". */
export function formatMinutes(minutes: number): string {
  if (minutes < 60) return `${minutes} min`;
  return `${Math.floor(minutes / 60)} h ${String(minutes % 60).padStart(2, "0")} min`;
}

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
const DATE = /^\d{4}-\d{2}-\d{2}$/;
export const CURRENCY = /^[A-Z]{3}$/;
const HEADER = ["legacyid", "party", "account", "currency", "amount"];

export interface ParsedRecords {
  records: OpenItemRecord[];
  errors: string[];
}

const KINDS = ["INVOICE", "CREDIT_NOTE", "PAYMENT", "ADJUSTMENT"] as const;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Columns of a pasted extract that loads into receivables, after the five control columns. */
export const TARGET_COLUMNS = "legacyId, party, account, currency, amount, documentDate, dueDate, then optionally ledger, kind, documentNumber, customerId, originalAmount";

/**
 * Parses pasted open items, one per line: legacyId,party,account,currency,amount. A first line
 * that names the columns is skipped. Amounts stay decimal strings. Problems are reported per line
 * so the user can fix the paste; the service applies the referential and domain rules itself.
 */
export function parseRecords(text: string, options: { forTarget?: boolean } = {}): ParsedRecords {
  if (options.forTarget) return parseTargetRecords(text);
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

/**
 * Parses pasted open items for a run that loads into receivables: the five control columns, the document and due
 * dates (YYYY-MM-DD), and optionally ledger (AR or AP), kind, document number, Passage customer id and original
 * amount. Blank optional cells keep the service's defaults. A negative amount is a credit.
 */
function parseTargetRecords(text: string): ParsedRecords {
  const records: OpenItemRecord[] = [];
  const errors: string[] = [];
  text.split(/\r?\n/).forEach((raw, index) => {
    const line = raw.trim();
    if (line === "") return;
    const cells = line.split(/[,;\t]/).map((c) => c.trim());
    if (index === 0 && cells[0].toLowerCase() === HEADER[0] && cells[1]?.toLowerCase() === HEADER[1]) return;
    const n = index + 1;
    if (cells.length < 7 || cells.length > 12) {
      errors.push(`Line ${n}: expected 7 to 12 values (${TARGET_COLUMNS}), found ${cells.length}.`);
      return;
    }
    const [legacyId, party, account, currency, amount, documentDate, dueDate, ledger = "", kind = "", documentNumber = "", customerId = "", originalAmount = ""] = cells;
    const bad = (message: string) => errors.push(`Line ${n}: ${message}`);
    if (!legacyId) return bad("the legacy id is blank.");
    if (!DECIMAL.test(amount)) return bad(`amount "${amount}" is not a decimal such as 1250.00.`);
    if (!DATE.test(documentDate)) return bad(`documentDate "${documentDate}" is not a YYYY-MM-DD date.`);
    if (!DATE.test(dueDate)) return bad(`dueDate "${dueDate}" is not a YYYY-MM-DD date.`);
    if (ledger !== "" && ledger.toUpperCase() !== "AR" && ledger.toUpperCase() !== "AP") return bad(`ledger "${ledger}" must be AR or AP.`);
    if (kind !== "" && !(KINDS as readonly string[]).includes(kind.toUpperCase())) return bad(`kind "${kind}" must be ${KINDS.join(", ")}.`);
    if (customerId !== "" && !UUID.test(customerId)) return bad(`customerId "${customerId}" is not a UUID.`);
    if (originalAmount !== "" && !/^\d{1,22}(\.\d{1,6})?$/.test(originalAmount)) return bad(`originalAmount "${originalAmount}" is not a positive decimal.`);
    records.push({
      legacyId,
      party,
      account,
      currency: currency.toUpperCase(),
      amount,
      documentDate,
      dueDate,
      ...(ledger !== "" ? { ledger: ledger.toUpperCase() as "AR" | "AP" } : {}),
      ...(kind !== "" ? { kind: kind.toUpperCase() as (typeof KINDS)[number] } : {}),
      ...(documentNumber !== "" ? { documentNumber } : {}),
      ...(customerId !== "" ? { customerId: customerId.toLowerCase() } : {}),
      ...(originalAmount !== "" ? { originalAmount } : {}),
    });
  });
  return { records, errors };
}

/** Parses "legacyParty=customer UUID" lines (or comma separated) into the load object's customer map. */
export function parseCustomerMap(text: string): { map: Record<string, string>; errors: string[] } {
  const map: Record<string, string> = {};
  const errors: string[] = [];
  text.split(/[\r\n,;]+/).forEach((raw) => {
    const entry = raw.trim();
    if (entry === "") return;
    const [party, id, ...rest] = entry.split("=").map((c) => c.trim());
    if (!party || !id || rest.length > 0 || !UUID.test(id)) errors.push(`"${entry}" is not legacyParty=customer UUID.`);
    else map[party] = id.toLowerCase();
  });
  return { map, errors };
}

/** True for a status that needs attention before the pack is done: the target's totals differ, rows are pending, or it failed. */
export function loadNeedsAttention(status: string): boolean {
  return status === "MISMATCH" || status === "PARTIAL" || status === "UNRECONCILED" || status === "FAILED";
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

const GL_HEADER = ["legacyId", "account", "dimensions", "currency", "balance"];

/**
 * Parses a pasted gl_balances extract, one balance per line: legacyId, account, dimensions,
 * currency, balance. Dimensions may be blank and use "-" or "|" between values, since commas,
 * semicolons and tabs separate the columns. The balance is signed (debit positive, credit
 * negative) and stays a decimal string. The service applies the FR-49.11 rules; this checks the shape.
 */
export function parseGLBalances(text: string): { records: GLBalanceRecord[]; errors: string[] } {
  const records: GLBalanceRecord[] = [];
  const errors = rows(text, GL_HEADER, ([legacyId, account, dimensions, currency, balance], n) => {
    if (!legacyId || !account) return `Line ${n}: the legacy id and account are required.`;
    const cur = currency.toUpperCase();
    if (!CURRENCY.test(cur)) return `Line ${n}: currency "${currency}" is not a three-letter code.`;
    if (!DECIMAL.test(balance)) return `Line ${n}: balance "${balance}" is not a decimal such as -1250.00.`;
    const record: GLBalanceRecord = { legacyId, account, currency: cur, balance };
    if (dimensions) record.dimensions = dimensions;
    records.push(record);
    return undefined;
  });
  return { records, errors };
}

const ONHAND_HEADER = ["legacyId", "item", "site", "warehouse", "location", "batch", "serial", "unit", "currency", "quantity", "value"];

/**
 * Parses a pasted onhand extract, one line per row: legacyId, item, site, warehouse, location,
 * batch, serial, unit, currency, quantity, value. Location, batch and serial may be blank. The
 * quantity is above zero, the value (the total at legacy cost) is not negative, and a serial holds
 * quantity 1. Both stay decimal strings. The service applies the FR-49.11 rules; this checks the shape.
 */
export function parseOnhandLines(text: string): { records: OnhandLineRecord[]; errors: string[] } {
  const records: OnhandLineRecord[] = [];
  const errors = rows(text, ONHAND_HEADER, ([legacyId, item, site, warehouse, location, batch, serial, unit, currency, quantity, value], n) => {
    if (!legacyId || !item) return `Line ${n}: the legacy id and item are required.`;
    if (!site || !warehouse) return `Line ${n}: the site and warehouse are required.`;
    if (!unit) return `Line ${n}: the unit of measure is required.`;
    const cur = currency.toUpperCase();
    if (!CURRENCY.test(cur)) return `Line ${n}: currency "${currency}" is not a three-letter code.`;
    const qty = toMicro(quantity);
    if (qty === null || qty <= BigInt(0)) return `Line ${n}: quantity "${quantity}" must be a decimal above zero such as 12.5.`;
    const val = toMicro(value);
    if (val === null || val < BigInt(0)) return `Line ${n}: value "${value}" must be a decimal of zero or more such as 250.50.`;
    if (serial && qty !== BigInt(1_000_000)) return `Line ${n}: serial "${serial}" holds quantity ${quantity}; a serial is exactly one.`;
    const record: OnhandLineRecord = { legacyId, item, site, warehouse, unit, currency: cur, quantity, value };
    if (location) record.location = location;
    if (batch) record.batch = batch;
    if (serial) record.serial = serial;
    records.push(record);
    return undefined;
  });
  return { records, errors };
}

const BANK_HEADER = ["legacyId", "bankAccount", "currency", "balance"];

/**
 * Parses a pasted bank_balances extract, one account per line: legacyId, bankAccount, currency,
 * balance. The balance is signed (an overdrawn account is negative) and stays a decimal string.
 * The service applies the FR-49.11 rules; this checks the shape.
 */
export function parseBankBalances(text: string): { records: BankBalanceRecord[]; errors: string[] } {
  const records: BankBalanceRecord[] = [];
  const errors = rows(text, BANK_HEADER, ([legacyId, bankAccount, currency, balance], n) => {
    if (!legacyId || !bankAccount) return `Line ${n}: the legacy id and bank account are required.`;
    const cur = currency.toUpperCase();
    if (!CURRENCY.test(cur)) return `Line ${n}: currency "${currency}" is not a three-letter code.`;
    if (toMicro(balance) === null) return `Line ${n}: balance "${balance}" is not a decimal such as -1250.00.`;
    records.push({ legacyId, bankAccount, currency: cur, balance });
    return undefined;
  });
  return { records, errors };
}

const MICRO = BigInt(1_000_000);

/** A decimal string (at most six places) as integer millionths, so sums are exact. Null when not a decimal. */
export function toMicro(value: string): bigint | null {
  const v = value.trim();
  if (!DECIMAL.test(v)) return null;
  const negative = v.startsWith("-");
  const [whole, frac = ""] = (negative ? v.slice(1) : v).split(".");
  const micro = BigInt(whole) * MICRO + BigInt(frac.padEnd(6, "0"));
  return negative ? -micro : micro;
}

/** Integer millionths as a decimal string with two places at least: -1500000n -> "-1.50". */
export function fromMicro(micro: bigint): string {
  const negative = micro < BigInt(0);
  const abs = negative ? -micro : micro;
  const frac = (abs % MICRO).toString().padStart(6, "0");
  return money(`${negative ? "-" : ""}${(abs / MICRO).toString()}.${frac}`);
}

/**
 * The currencies in which a GL extract's debits differ from its credits, with the difference
 * (debits less credits). The service refuses such an extract (FR-49.2 trial balance), so the form
 * shows it before sending. Empty when every currency balances.
 */
export function glImbalances(records: GLBalanceRecord[]): { currency: string; net: string }[] {
  const net = new Map<string, bigint>();
  for (const r of records) {
    const micro = toMicro(r.balance);
    if (micro === null) continue;
    net.set(r.currency, (net.get(r.currency) ?? BigInt(0)) + micro);
  }
  return [...net.entries()]
    .filter(([, n]) => n !== BigInt(0))
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([currency, n]) => ({ currency, net: fromMicro(n) }));
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

const POSTING_HEADER = ["kind", "entity", "documentRef", "postedAt", "account", "dimensions", "currency", "amount", "text"];
const POSTED_AT = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?(Z|[+-]\d{2}:\d{2})$/;

/**
 * Parses the Passage postings since T-0 that a rollback exports for re-keying (FR-49.9), one line
 * each: kind, entity, documentRef, postedAt, account, dimensions, currency, amount, text.
 * Dimensions and text may be blank and text may be left out. Values are separated by commas,
 * semicolons or tabs, so text cannot hold one. postedAt is an ISO date-time with an offset and is
 * sent as UTC. Entities must be those of the plan. The service checks the rest, including that each
 * journal balances.
 */
export function parsePostings(text: string, entities: readonly string[]): { postings: RekeyPosting[]; errors: string[] } {
  const postings: RekeyPosting[] = [];
  const errors: string[] = [];
  text.split(/\r?\n/).forEach((raw, index) => {
    const line = raw.trim();
    if (line === "") return;
    const cells = line.split(/[,;\t]/).map((c) => c.trim());
    if (index === 0 && cells.slice(0, 4).map((c) => c.toLowerCase()).join(",") === POSTING_HEADER.slice(0, 4).map((h) => h.toLowerCase()).join(",")) return;
    const n = index + 1;
    if (cells.length < 8 || cells.length > POSTING_HEADER.length) {
      errors.push(`Line ${n}: expected 8 or 9 values (${POSTING_HEADER.join(", ")}), found ${cells.length}.`);
      return;
    }
    const [kind, entity, documentRef, postedAt, account, dimensions, currency, amount, note = ""] = cells;
    const k = kind.toUpperCase();
    if (k !== "JOURNAL" && k !== "DOCUMENT") {
      errors.push(`Line ${n}: kind "${kind}" is not JOURNAL or DOCUMENT.`);
      return;
    }
    if (!entity || !documentRef || !account) {
      errors.push(`Line ${n}: the entity, document and account are required.`);
      return;
    }
    if (!entities.includes(entity)) {
      errors.push(`Line ${n}: entity ${entity} is not cut over by this plan (${entities.join(", ")}).`);
      return;
    }
    const ms = POSTED_AT.test(postedAt) ? Date.parse(postedAt) : Number.NaN;
    if (Number.isNaN(ms)) {
      errors.push(`Line ${n}: posted at "${postedAt}" is not a date-time such as 2027-01-02T10:00:00Z.`);
      return;
    }
    const cur = currency.toUpperCase();
    if (!CURRENCY.test(cur)) {
      errors.push(`Line ${n}: currency "${currency}" is not a three-letter code.`);
      return;
    }
    if (!DECIMAL.test(amount)) {
      errors.push(`Line ${n}: amount "${amount}" is not a decimal such as -250.00.`);
      return;
    }
    const posting: RekeyPosting = {
      kind: k,
      entity,
      documentRef,
      postedAt: new Date(ms).toISOString().replace(".000Z", "Z"),
      account,
      currency: cur,
      amount,
    };
    if (dimensions) posting.dimensions = dimensions;
    if (note) posting.text = note;
    postings.push(posting);
  });
  return { postings, errors };
}
