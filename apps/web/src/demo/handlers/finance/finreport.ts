// Financial reporting (/api/v1/reporting) for the demo: published income statement, balance sheet and trial balance
// definitions over the ledger's chart, statement runs (with cells and a balance drill), new runs, the read-model
// lag, the report library of scheduled month-end packs, and ledger balance verifications. Figures come from one
// small statement model, so a run, its drill and the library agree, and the income statement ties into the
// balance sheet's current-year earnings.
import type {
  BalanceKey,
  CellSummary,
  Definition,
  DefinitionColumn,
  DefinitionRow,
  DefinitionSummary,
  DrillResponse,
  LagStatus,
  LibraryEntry,
  RunSummary,
  Verification,
  VerificationSummary,
} from "../../../modules/finreport/api/client";
import { LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { at, newId, table, uid } from "../../store";
import { ACCOUNTS, LEDGER_ID } from "./ledger";
import { ME } from "./people";

const BASE = "/api/v1/reporting";
const K_RUN = 630;
const K_LIBRARY = 631;
const K_VERIFY = 632;

// --- statement model ---------------------------------------------------------------------

/** Typical monthly activity of each income statement account, in dollars, natural sign (revenue and cost positive). */
const MONTHLY: Record<string, number> = {
  "4000": 2_046_800,
  "4100": 264_350,
  "4200": 91_700,
  "5000": 1_712_400,
  "5100": 37_920,
  "6100": 172_420,
  "6200": 29_695,
  "6300": 14_870,
  "6400": 31_240,
  "6500": 46_310,
  "6600": 23_260,
  "6700": 4_250,
  "6800": 6_940,
  "7000": 9_850,
  "7100": 1_240,
  "7200": 118,
};

/** Balance sheet accounts at the latest closed period end, natural sign (assets debit, the rest credit). */
const BALANCES: Record<string, number> = {
  "1010": 1_284_560.4,
  "1020": 186_400,
  "1050": 3_250,
  "1200": 3_742_180.15,
  "1300": 1_126_400,
  "1310": 412_750,
  "1320": 1_948_300,
  "1400": 38_250,
  "1500": 1_596_850,
  "1510": 4_850_000,
  "1520": 93_100,
  "1530": 72_640,
  "1590": 1_982_400,
  "2000": 1_862_450.6,
  "2100": 412_380,
  "2150": 168_900,
  "2200": 94_320,
  "2400": 1_500_000,
  "3000": 2_000_000,
};
const CONTRA = new Set(["1590"]);
const CREDIT = (account: string) => (account >= "2000" && account < "5000") || account === "7200";

/** Seasonality: pumps ship more in spring and late summer. Deterministic, so reruns agree. */
const season = (no: number) => [0.86, 0.88, 1.02, 1.08, 1.1, 1.04, 0.98, 1.06, 1.04, 1.01, 0.97, 0.96][(no - 1) % 12];
const growth = (year: number) => Math.pow(1.07, year - new Date().getFullYear());

const round2 = (n: number) => Math.round(n * 100) / 100;

function activity(account: string, year: number, no: number): number {
  const base = MONTHLY[account] ?? 0;
  const revenueLike = account < "5000" || account === "7200";
  // Costs follow volume less than revenue does.
  const f = revenueLike ? season(no) : 0.6 + 0.4 * season(no);
  return round2(base * f * growth(year));
}

function ytd(account: string, year: number, no: number): number {
  let s = 0;
  for (let k = 1; k <= no; k++) s += activity(account, year, k);
  return round2(s);
}

/** Current-year earnings through the period, the figure the balance sheet carries above retained earnings. */
function earnings(year: number, no: number): number {
  let s = 0;
  for (const a of Object.keys(MONTHLY)) s += (a < "5000" || a === "7200" ? 1 : -1) * ytd(a, year, no);
  return round2(s);
}

const today = new Date();
/** The latest closed period: last month. */
export const CLOSED = { year: today.getMonth() === 0 ? today.getFullYear() - 1 : today.getFullYear(), no: today.getMonth() === 0 ? 12 : today.getMonth() };

function balance(account: string, year: number, no: number): number {
  const monthsBack = (CLOSED.year - year) * 12 + (CLOSED.no - no);
  const drift = account === "1200" ? 0.012 : account === "1010" ? -0.02 : account === "1590" ? -0.012 : 0.004;
  return round2((BALANCES[account] ?? 0) * (1 - drift * monthsBack));
}

/** Retained earnings brought forward, so assets equal liabilities plus equity in every period. */
function retained(year: number, no: number): number {
  let assets = 0;
  let claims = 0;
  for (const a of Object.keys(BALANCES)) {
    const v = balance(a, year, no);
    if (a < "2000") assets += CONTRA.has(a) ? -v : v;
    else claims += v;
  }
  return round2(assets - claims - earnings(year, no));
}

// --- definitions -------------------------------------------------------------------------

const fmt = (bold = false, indent = 0) => ({ bold, indent, printZero: false, pageBreak: false });
const range = (code: string, label: string, from: string, to: string, normalBalance: "debit" | "credit", indent = 1): DefinitionRow => ({ code, kind: "account_range", label, accountFrom: from, accountTo: to, normalBalance, format: fmt(false, indent) });
const header = (code: string, label: string): DefinitionRow => ({ code, kind: "header", label, normalBalance: "debit", format: fmt(true, 0) });
const total = (code: string, label: string, references: string[], normalBalance: "debit" | "credit" = "credit"): DefinitionRow => ({ code, kind: "total", label, references, normalBalance, format: fmt(true, 0) });
const calc = (code: string, label: string, expression: string, normalBalance: "debit" | "credit" = "credit"): DefinitionRow => ({ code, kind: "calculation", label, expression, normalBalance, format: fmt(true, 0) });
const col = (code: string, label: string, periodExpr: string): DefinitionColumn => ({ code, label, periodExpr });

const IS: Definition = {
  schemaVersion: "1",
  code: "IS-MONTH",
  name: "Income statement",
  version: 3,
  status: "published",
  kind: "income_statement",
  includeAdjustmentPeriods: false,
  rows: [
    header("H-REV", "Revenue"),
    range("R-PUMP", "Pump sales", "4000", "4099", "credit"),
    range("R-PARTS", "Spare parts sales", "4100", "4199", "credit"),
    range("R-SVC", "Service and commissioning", "4200", "4299", "credit"),
    total("T-REV", "Total revenue", ["R-PUMP", "R-PARTS", "R-SVC"]),
    header("H-COGS", "Cost of sales"),
    range("C-COGS", "Cost of goods sold", "5000", "5099", "debit"),
    range("C-FRT", "Freight in", "5100", "5199", "debit"),
    total("T-COGS", "Total cost of sales", ["C-COGS", "C-FRT"], "debit"),
    calc("GP", "Gross profit", "T-REV - T-COGS"),
    header("H-OPEX", "Operating expenses"),
    range("E-SAL", "Salaries and wages", "6100", "6199", "debit"),
    range("E-UTL", "Utilities", "6200", "6299", "debit"),
    range("E-MNT", "Maintenance and repairs", "6300", "6399", "debit"),
    range("E-FRT", "Freight out", "6400", "6499", "debit"),
    range("E-ADM", "Office and administration", "6500", "6599", "debit"),
    range("E-DEP", "Depreciation", "6600", "6699", "debit"),
    range("E-INS", "Insurance", "6700", "6799", "debit"),
    range("E-TRV", "Travel and entertainment", "6800", "6899", "debit"),
    total("T-OPEX", "Total operating expenses", ["E-SAL", "E-UTL", "E-MNT", "E-FRT", "E-ADM", "E-DEP", "E-INS", "E-TRV"], "debit"),
    calc("OI", "Operating income", "GP - T-OPEX"),
    range("F-INT", "Interest expense", "7000", "7099", "debit"),
    range("F-BANK", "Bank fees", "7100", "7199", "debit"),
    range("F-INC", "Interest income", "7200", "7299", "credit"),
    calc("NI", "Net income", "OI - F-INT - F-BANK + F-INC"),
  ],
  columns: [col("P", "This month", "P"), col("PY", "Same month last year", "PY"), col("YTD", "Year to date", "YTD"), col("PYTD", "Last year to date", "PYTD")],
};

const BS: Definition = {
  schemaVersion: "1",
  code: "BS-MONTH",
  name: "Balance sheet",
  version: 2,
  status: "published",
  kind: "balance_sheet",
  includeAdjustmentPeriods: false,
  rows: [
    header("H-A", "Assets"),
    range("A-CASH", "Cash and cash equivalents", "1010", "1099", "debit"),
    range("A-AR", "Accounts receivable", "1200", "1299", "debit"),
    range("A-INV", "Inventories", "1300", "1399", "debit"),
    range("A-PRE", "Prepaid expenses", "1400", "1499", "debit"),
    total("T-CA", "Total current assets", ["A-CASH", "A-AR", "A-INV", "A-PRE"], "debit"),
    range("A-PPE", "Property, plant and equipment, at cost", "1500", "1589", "debit"),
    range("A-DEP", "Accumulated depreciation", "1590", "1599", "debit"),
    total("T-A", "Total assets", ["T-CA", "A-PPE", "A-DEP"], "debit"),
    header("H-L", "Liabilities"),
    range("L-AP", "Accounts payable", "2000", "2099", "credit"),
    range("L-ACC", "Accrued liabilities and payroll", "2100", "2199", "credit"),
    range("L-TAX", "Sales tax payable", "2200", "2299", "credit"),
    range("L-LOC", "Revolving line of credit", "2400", "2499", "credit"),
    total("T-L", "Total liabilities", ["L-AP", "L-ACC", "L-TAX", "L-LOC"]),
    header("H-E", "Equity"),
    range("Q-STK", "Common stock", "3000", "3099", "credit"),
    range("Q-RE", "Retained earnings", "3100", "3199", "credit"),
    calc("Q-CYE", "Current-year earnings", "NI"),
    total("T-E", "Total equity", ["Q-STK", "Q-RE", "Q-CYE"]),
    total("T-LE", "Total liabilities and equity", ["T-L", "T-E"]),
  ],
  columns: [col("P", "End of month", "P"), col("P1", "End of prior month", "P-1"), col("FY", "End of last year", "PY")],
};

const TB: Definition = {
  schemaVersion: "1",
  code: "TB-ALL",
  name: "Trial balance",
  version: 1,
  status: "published",
  kind: "trial_balance",
  includeAdjustmentPeriods: true,
  rows: Object.entries(ACCOUNTS).map(([n, label]) => range(`A${n}`, `${n} ${label}`, n, n, CREDIT(n) ? "credit" : "debit", 0)),
  columns: [
    { code: "DR", label: "Debit", periodExpr: "YTD", balanceSide: "debit" },
    { code: "CR", label: "Credit", periodExpr: "YTD", balanceSide: "credit" },
  ],
};

const DEFINITIONS = [IS, BS, TB];
const summaryOf = (d: Definition, updatedDaysAgo: number): DefinitionSummary => ({ code: d.code, name: d.name, kind: d.kind, status: d.status, latestVersion: d.version, hasDraft: d.code === "IS-MONTH", updatedAt: at(-updatedDaysAgo) });

// --- evaluating a definition ------------------------------------------------------------------

type Period = { year: number; no: number };
const shift = (p: Period, months: number): Period => {
  const i = p.year * 12 + (p.no - 1) + months;
  return { year: Math.floor(i / 12), no: (i % 12) + 1 };
};

/** Value of one account for a column, natural sign, and the balance key period it reads. */
function accountValue(d: Definition, account: string, column: DefinitionColumn, p: Period): number {
  if (d.kind === "balance_sheet" || (d.kind === "trial_balance" && account < "4000")) {
    const at_ = column.periodExpr === "P-1" ? shift(p, -1) : column.periodExpr === "PY" ? { year: p.year - 1, no: 12 } : p;
    if (account === "3100") return retained(at_.year, at_.no);
    if (!(account in BALANCES)) return 0;
    return balance(account, at_.year, at_.no);
  }
  if (!(account in MONTHLY)) return 0;
  switch (column.periodExpr) {
    case "P":
      return activity(account, p.year, p.no);
    case "PY":
      return activity(account, p.year - 1, p.no);
    case "PYTD":
      return ytd(account, p.year - 1, p.no);
    default:
      return ytd(account, p.year, p.no);
  }
}

function accountsIn(row: DefinitionRow): string[] {
  return Object.keys(ACCOUNTS).filter((a) => a >= (row.accountFrom ?? "") && a <= (row.accountTo ?? ""));
}

/** Signed so that a row shows positive in its normal balance: contra accounts (accumulated depreciation) show negative. */
function rowAccountValue(d: Definition, _row: DefinitionRow, account: string, column: DefinitionColumn, p: Period): number {
  const v = accountValue(d, account, column, p);
  return CONTRA.has(account) ? -v : v;
}

function evaluate(d: Definition, p: Period): Map<string, CellSummary> {
  const cells = new Map<string, CellSummary>();
  for (const column of d.columns) {
    const values = new Map<string, number>();
    for (const row of d.rows) {
      const key = `${row.code}/${column.code}`;
      if (row.kind === "header") {
        cells.set(key, { row: row.code, column: column.code, value: "0", blank: true, computed: false, keyCount: 0 });
        continue;
      }
      let v = 0;
      let keyCount = 0;
      let computed = false;
      if (row.kind === "account_range") {
        const accounts = accountsIn(row);
        for (const a of accounts) v += rowAccountValue(d, row, a, column, p);
        keyCount = accounts.filter((a) => accountValue(d, a, column, p) !== 0).length;
        if (d.kind === "trial_balance") {
          const debit = CREDIT(accounts[0] ?? "") ? -v : v;
          v = column.balanceSide === "debit" ? Math.max(debit, 0) : Math.max(-debit, 0);
          if (v === 0) keyCount = 0;
        }
      } else if (row.kind === "total") {
        v = (row.references ?? []).reduce((s, r) => s + (values.get(r) ?? 0), 0);
        computed = true;
      } else if (row.kind === "calculation") {
        if (row.expression === "NI") {
          v = earnings(column.periodExpr === "P-1" ? shift(p, -1).year : column.periodExpr === "PY" ? p.year - 1 : p.year, column.periodExpr === "P-1" ? shift(p, -1).no : column.periodExpr === "PY" ? 12 : p.no);
        } else {
          const tokens = (row.expression ?? "").split(/\s+/);
          let sign = 1;
          for (const t of tokens) {
            if (t === "+") sign = 1;
            else if (t === "-") sign = -1;
            else v += sign * (values.get(t) ?? 0);
          }
        }
        computed = true;
      }
      v = round2(v);
      values.set(row.code, v);
      cells.set(key, { row: row.code, column: column.code, value: v.toFixed(2), blank: false, computed, keyCount });
    }
  }
  return cells;
}

// --- runs ------------------------------------------------------------------------------------

interface StoredRun extends RunSummary {
  cells: CellSummary[];
}

let sequence = 128_434;
const hash = (s: string) => {
  let h = 2166136261;
  for (const ch of s) h = Math.imul(h ^ ch.charCodeAt(0), 16777619) >>> 0;
  return `${h.toString(16).padStart(8, "0")}${(h ^ 0x5bd1e995).toString(16).padStart(8, "0")}`.repeat(4);
};

function makeRun(id: string, d: Definition, p: Period, requestedBy: string, startedAt: string, hwm: number): StoredRun {
  const cells = [...evaluate(d, p).values()];
  return {
    id,
    definitionCode: d.code,
    definitionVersion: d.version,
    ledgerId: LEDGER_ID,
    currencyType: "acct",
    periodYear: p.year,
    periodNo: p.no,
    status: "completed",
    hwmByLedger: { [LEDGER_ID]: hwm },
    resultHash: hash(`${d.code}/${p.year}/${p.no}/${hwm}`),
    cellCount: cells.length,
    requestedBy,
    startedAt,
    durationMs: 180 + cells.length * 3,
    checks: [],
    cells,
  };
}

const dom = today.getDate();
const runs = table<StoredRun>([
  makeRun(uid(K_RUN, 1), IS, CLOSED, ME.username, at(-0.3), 128_431),
  makeRun(uid(K_RUN, 2), BS, CLOSED, ME.username, at(-0.29), 128_431),
  makeRun(uid(K_RUN, 3), TB, CLOSED, "ana.ortiz", at(-1.2), 128_412),
  makeRun(uid(K_RUN, 4), IS, shift(CLOSED, -1), "schedule:MONTH-END", at(-dom - 26), 121_870),
  makeRun(uid(K_RUN, 5), BS, shift(CLOSED, -1), "schedule:MONTH-END", at(-dom - 26), 121_870),
  makeRun(uid(K_RUN, 6), IS, shift(CLOSED, -2), "schedule:MONTH-END", at(-dom - 56), 115_204),
  makeRun(uid(K_RUN, 7), BS, shift(CLOSED, -2), "schedule:MONTH-END", at(-dom - 56), 115_204),
]);

const summary = (r: StoredRun): RunSummary => {
  const { cells: _cells, ...rest } = r;
  return rest;
};

const periodKey = (p: Period) => `${p.year}-${String(p.no).padStart(2, "0")}`;

// --- library -----------------------------------------------------------------------------------

function libraryEntry(n: number, runId: string, d: Definition, p: Period, format: "pdf" | "xlsx", daysAgo: number): LibraryEntry {
  const ext = format === "pdf" ? "pdf" : "xlsx";
  return {
    id: uid(K_LIBRARY, n),
    runId,
    scheduleCode: "MONTH-END",
    definitionCode: d.code,
    definitionVersion: d.version,
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    periodYear: p.year,
    periodNo: p.no,
    currencyType: "acct",
    format,
    fileName: `${LEGAL_ENTITY.code}_${d.code}_${periodKey(p)}.${ext}`,
    contentType: format === "pdf" ? "application/pdf" : "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    sizeBytes: format === "pdf" ? 84_000 + n * 1_310 : 31_000 + n * 977,
    sha256: hash(`${n}/${d.code}/${format}`).slice(0, 64),
    retainUntil: new Date(Date.UTC(p.year + 7, p.no, 0)).toISOString().slice(0, 10),
    createdAt: at(-daysAgo),
    onHold: false,
  };
}

const library = table<LibraryEntry>([
  libraryEntry(1, uid(K_RUN, 4), IS, shift(CLOSED, -1), "pdf", dom + 26),
  libraryEntry(2, uid(K_RUN, 4), IS, shift(CLOSED, -1), "xlsx", dom + 26),
  libraryEntry(3, uid(K_RUN, 5), BS, shift(CLOSED, -1), "pdf", dom + 26),
  libraryEntry(4, uid(K_RUN, 5), BS, shift(CLOSED, -1), "xlsx", dom + 26),
  libraryEntry(5, uid(K_RUN, 6), IS, shift(CLOSED, -2), "pdf", dom + 56),
  libraryEntry(6, uid(K_RUN, 6), IS, shift(CLOSED, -2), "xlsx", dom + 56),
  libraryEntry(7, uid(K_RUN, 7), BS, shift(CLOSED, -2), "pdf", dom + 56),
  libraryEntry(8, uid(K_RUN, 7), BS, shift(CLOSED, -2), "xlsx", dom + 56),
]);

// --- verifications ---------------------------------------------------------------------------------

const verifications = table<Verification>([
  { id: uid(K_VERIFY, 1), ledgerId: LEDGER_ID, legalEntityId: LEGAL_ENTITY.id, status: "matched", hwmBefore: 128_431, hwmAfter: 128_431, entriesPending: 0, accountsCompared: 34, diffCount: 0, requestedBy: ME.username, verifiedAt: at(-0.31), diffs: [] },
  { id: uid(K_VERIFY, 2), ledgerId: LEDGER_ID, legalEntityId: LEGAL_ENTITY.id, status: "inconclusive", hwmBefore: 128_402, hwmAfter: 128_409, entriesPending: 3, accountsCompared: 34, diffCount: 0, requestedBy: "ana.ortiz", verifiedAt: at(-dom + 0.6), diffs: [] },
  {
    id: uid(K_VERIFY, 3),
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    status: "differs",
    hwmBefore: 121_866,
    hwmAfter: 121_866,
    entriesPending: 0,
    accountsCompared: 34,
    diffCount: 2,
    requestedBy: ME.username,
    verifiedAt: at(-dom - 27),
    diffs: [
      { account: "1200", readModel: "3505301.95", ledger: "3517781.95", difference: "-12480.00" },
      { account: "4000", readModel: "17823140.00", ledger: "17835620.00", difference: "-12480.00" },
    ],
  },
  { id: uid(K_VERIFY, 4), ledgerId: LEDGER_ID, legalEntityId: LEGAL_ENTITY.id, status: "matched", hwmBefore: 121_870, hwmAfter: 121_870, entriesPending: 0, accountsCompared: 34, diffCount: 0, requestedBy: ME.username, verifiedAt: at(-dom - 26.5), diffs: [] },
]);

const verificationSummary = (v: Verification): VerificationSummary => {
  const { diffs: _diffs, diffsWithheld: _w, ...rest } = v;
  return rest;
};

// --- routes ------------------------------------------------------------------------------------------

const notFound = (code: string, what: string) => reply.problem(404, code, `${what} not found`);

route("GET", `${BASE}/definitions`, () => ({ items: DEFINITIONS.map((d, i) => summaryOf(d, 30 + i * 41)) }));
route("GET", `${BASE}/definitions/{code}`, ({ params }) => {
  const d = DEFINITIONS.find((x) => x.code === params.code);
  return d ? reply.versioned(d, d.version) : notFound("REPORT-0404", "definition");
});
route("GET", `${BASE}/standard-definitions`, () => ({ items: [] }));
route("GET", `${BASE}/hierarchies`, () => ({ items: [] }));
route("GET", `${BASE}/trees`, () => ({ items: [] }));

route("GET", `${BASE}/lag`, (): LagStatus => ({
  items: [{ ledgerId: LEDGER_ID, postingSequence: sequence, updatedAt: at(-0.01), entriesApplied: 42_877, entriesPending: 0, lagSeconds: 0, hwmLedger: sequence }],
  maxLagSeconds: 300,
}));

route("GET", `${BASE}/runs`, ({ query }) => {
  const def = query.get("definition");
  const ledger = query.get("ledger");
  const p = query.get("period");
  const items = runs
    .all()
    .filter((r) => (!def || r.definitionCode === def) && (!ledger || r.ledgerId === ledger) && (!p || periodKey({ year: r.periodYear, no: r.periodNo }) === p))
    .sort((a, b) => b.startedAt.localeCompare(a.startedAt))
    .map(summary);
  return { items, limit: 50, nextCursor: null };
});

route("POST", `${BASE}/runs`, ({ body }) => {
  const input = (body ?? {}) as { definition?: string; ledgerId?: string; period?: { year: number; no: number } };
  const d = DEFINITIONS.find((x) => x.code === input.definition);
  if (!d) return reply.problem(422, "REPORT-0002", "no published version of that definition");
  if (input.ledgerId !== LEDGER_ID) return reply.problem(422, "REPORT-0003", `no ledger with that id; ${LEGAL_ENTITY.code}'s ledger is ${LEDGER_ID}`);
  const p = input.period ?? CLOSED;
  sequence += 2;
  const run = makeRun(newId(), d, { year: p.year, no: p.no }, ME.username, at(0), sequence);
  runs.insert(run);
  return reply.created(summary(run));
});

route("GET", `${BASE}/runs/{id}/cells/{row}/{column}/balances`, ({ params }): DrillResponse | ReturnType<typeof notFound> => {
  const run = runs.get(params.id);
  const d = DEFINITIONS.find((x) => x.code === run?.definitionCode);
  const row = d?.rows.find((r) => r.code === params.row);
  const column = d?.columns.find((c) => c.code === params.column);
  const cell = run?.cells.find((c) => c.row === params.row && c.column === params.column);
  if (!run || !d || !row || !column || !cell) return notFound("REPORT-0404", "cell");
  const p = { year: run.periodYear, no: run.periodNo };
  const keys: BalanceKey[] =
    row.kind === "account_range"
      ? accountsIn(row)
          .map((a) => ({ a, v: rowAccountValue(d, row, a, column, p) }))
          .filter(({ v }) => v !== 0)
          .map(({ a, v }) => ({ ledgerId: LEDGER_ID, period: periodKey(p), account: a, combination: a, currencyType: "acct", amount: round2(v).toFixed(2) }))
      : [];
  const sum = keys.reduce((s, k) => s + Number(k.amount), 0);
  return { runId: run.id, row: row.code, column: column.code, value: cell.value, blank: cell.blank, computed: cell.computed, sumOfKeys: round2(d.kind === "trial_balance" ? Number(cell.value) : sum).toFixed(2), keys: d.kind === "trial_balance" ? keys.map((k) => ({ ...k, amount: cell.value })) : keys };
});
route("GET", `${BASE}/runs/{id}/cells`, ({ params }) => {
  const run = runs.get(params.id);
  return run ? { items: run.cells } : notFound("REPORT-0404", "run");
});
route("GET", `${BASE}/runs/{id}/definition`, ({ params }) => {
  const run = runs.get(params.id);
  return DEFINITIONS.find((d) => d.code === run?.definitionCode) ?? notFound("REPORT-0404", "run");
});
route("GET", `${BASE}/runs/{id}`, ({ params }) => {
  const run = runs.get(params.id);
  return run ? summary(run) : notFound("REPORT-0404", "run");
});

route("GET", `${BASE}/library`, ({ query }) => {
  const def = query.get("definition");
  const format = query.get("format");
  const p = query.get("period");
  const items = library
    .all()
    .filter((e) => (!def || e.definitionCode === def) && (!format || e.format === format) && (!p || periodKey({ year: e.periodYear, no: e.periodNo }) === p))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || a.fileName.localeCompare(b.fileName));
  return { items, limit: 50, nextCursor: null };
});
route("GET", `${BASE}/library/{id}/share-links`, () => ({ items: [] }));
route("GET", `${BASE}/library/{id}`, ({ params }) => library.get(params.id) ?? notFound("REPORT-0404", "library file"));

route("GET", `${BASE}/balance-verifications`, () => ({ items: verifications.all().sort((a, b) => b.verifiedAt.localeCompare(a.verifiedAt)).map(verificationSummary) }));
route("GET", `${BASE}/balance-verifications/{id}`, ({ params }) => verifications.get(params.id) ?? notFound("REPORT-0404", "verification"));
route("POST", `${BASE}/balance-verifications`, ({ body }) => {
  const ledgerId = ((body ?? {}) as { ledgerId?: string }).ledgerId;
  if (ledgerId !== LEDGER_ID) return reply.problem(422, "REPORT-0003", `no ledger with that id; ${LEGAL_ENTITY.code}'s ledger is ${LEDGER_ID}`);
  return reply.created(verifications.insert({ id: newId(), ledgerId, legalEntityId: LEGAL_ENTITY.id, status: "matched", hwmBefore: sequence, hwmAfter: sequence, entriesPending: 0, accountsCompared: 34, diffCount: 0, requestedBy: ME.username, verifiedAt: at(0), diffs: [] }));
});
