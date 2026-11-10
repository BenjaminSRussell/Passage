// Fixed assets (/api/v1/assets) for the demo: the plant and DC asset register with an IFRS book (straight line,
// monthly) and a TAX book (double declining, yearly), schedules, transactions, and depreciation runs that can be
// proposed and posted. The machines are the resources the production screens schedule (MCH-01, BAL-01, TST-01 ...),
// and the CNC spindle rebuild is the same 18,400.00 the ledger's capitalization journal moves to 1500.
import type { Addition, Asset, AssetDetail, AssetStatus, Book, DepreciationRun, RunLine, Schedule, ScheduleLine, Transaction } from "../../../modules/assets/api/client";
import { LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { at, day, newId, table, uid } from "../../store";

const BASE = "/api/v1/assets";
const K_ASSET = 610;
const K_BOOK = 611;
const K_RUN = 612;
const K_TXN = 613;
const K_ADDITION = 614;

// Money in cents so sums stay exact.
const c = (s: string): number => Math.round(Number(s) * 100);
const m = (cents: number): string => (cents / 100).toFixed(2);

/** "YYYY-MM" of the month offset from this one (0 = this month). */
function period(offset: number): string {
  const d = new Date();
  const x = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + offset, 1));
  return x.toISOString().slice(0, 7);
}
const monthIndex = (p: string): number => Number(p.slice(0, 4)) * 12 + Number(p.slice(5, 7)) - 1;
const periodOf = (index: number): string => `${Math.floor(index / 12)}-${String((index % 12) + 1).padStart(2, "0")}`;
const lastDay = (p: string): string => new Date(Date.UTC(Number(p.slice(0, 4)), Number(p.slice(5, 7)), 0)).toISOString().slice(0, 10);

const THIS_YEAR = new Date().getFullYear();
/** IFRS books are depreciated through two months ago; last month's run is proposed and waits to be posted. */
const IFRS_POSTED = period(-2);
const TAX_POSTED = String(THIS_YEAR - 1);

interface AssetSeed {
  n: number;
  number: string;
  name: string;
  classCode: string;
  /** Days ago placed in service. */
  age: number;
  cost: number;
  lifeMonths: number;
  salvage: number;
  taxYears: number;
  barcode?: string;
  status?: AssetStatus;
}

const SEEDS: AssetSeed[] = [
  { n: 1, number: "FA-000101", name: "Cedar Rapids plant building", classCode: "BUILDING", age: 3650, cost: 4_850_000, lifeMonths: 480, salvage: 850_000, taxYears: 39, barcode: "CRP-0101" },
  { n: 2, number: "FA-000214", name: "5-axis CNC machining cell (MCH-01)", classCode: "MACHINERY", age: 1290, cost: 612_500, lifeMonths: 120, salvage: 40_000, taxYears: 7, barcode: "CRP-0214" },
  { n: 3, number: "FA-000221", name: "Dynamic impeller balancing machine (BAL-01)", classCode: "MACHINERY", age: 980, cost: 148_900, lifeMonths: 120, salvage: 8_000, taxYears: 7, barcode: "CRP-0221" },
  { n: 4, number: "FA-000233", name: "Hydrostatic and performance test stand (TST-01)", classCode: "MACHINERY", age: 720, cost: 236_400, lifeMonths: 144, salvage: 12_000, taxYears: 7, barcode: "CRP-0233" },
  { n: 5, number: "FA-000240", name: "Paint booth and cure oven (PNT-01)", classCode: "MACHINERY", age: 1520, cost: 184_750, lifeMonths: 120, salvage: 0, taxYears: 7, barcode: "CRP-0240" },
  { n: 6, number: "FA-000252", name: "Assembly line 1 conveyors and fixtures (ASM-01)", classCode: "MACHINERY", age: 2210, cost: 318_000, lifeMonths: 120, salvage: 15_000, taxYears: 7, barcode: "CRP-0252" },
  { n: 7, number: "FA-000258", name: "Overhead bridge crane, 10 t", classCode: "MACHINERY", age: 3100, cost: 96_300, lifeMonths: 180, salvage: 5_000, taxYears: 7, barcode: "CRP-0258" },
  { n: 8, number: "FA-000305", name: "Electric forklift, Dallas DC", classCode: "VEHICLE", age: 430, cost: 38_900, lifeMonths: 60, salvage: 4_000, taxYears: 5, barcode: "DAL-0305" },
  { n: 9, number: "FA-000311", name: "Field service van, commissioning crew", classCode: "VEHICLE", age: 610, cost: 54_200, lifeMonths: 60, salvage: 9_000, taxYears: 5, barcode: "CRP-0311" },
  { n: 10, number: "FA-000402", name: "Plant network and server refresh", classCode: "COMPUTER", age: 300, cost: 72_640, lifeMonths: 48, salvage: 0, taxYears: 5 },
  { n: 11, number: "FA-000298", name: "LPG forklift, Cedar Rapids", classCode: "VEHICLE", age: 2400, cost: 29_500, lifeMonths: 72, salvage: 2_500, taxYears: 5, status: "Sold" },
  { n: 12, number: "FA-000415", name: "Laser shaft alignment system", classCode: "MACHINERY", age: -20, cost: 21_800, lifeMonths: 84, salvage: 0, taxYears: 7, status: "NotYetAcquired" },
];

interface StoredAsset extends AssetDetail {
  /** Monthly IFRS charge in cents, for runs and schedules. */
  monthly: number;
}

function ifrsBook(s: AssetSeed, assetId: string): Book {
  const inService = day(-s.age);
  const start = monthIndex(inService.slice(0, 7));
  const monthly = Math.round((c(String(s.cost)) - c(String(s.salvage))) / s.lifeMonths);
  const months = Math.max(0, Math.min(s.lifeMonths, monthIndex(IFRS_POSTED) - start + 1));
  const acc = monthly * months;
  return {
    id: uid(K_BOOK, s.n * 2 - 1),
    assetId,
    legalEntityId: LEGAL_ENTITY.id,
    bookCode: "IFRS",
    currency: "USD",
    method: "StraightLine",
    factor: "1",
    switchToStraightLine: false,
    serviceLifePeriods: s.lifeMonths,
    salvageValue: m(c(String(s.salvage))),
    convention: "FullMonth",
    inServiceDate: inService,
    cost: m(c(String(s.cost))),
    accumulatedDepreciation: m(acc),
    nbv: m(c(String(s.cost)) - acc),
    status: s.status === "Sold" ? "Sold" : "Open",
    lastDepreciatedPeriod: months > 0 ? periodOf(start + months - 1) : null,
  };
}

/** Double declining balance with the half-year convention, switching to straight line, through TAX_POSTED. */
function taxBook(s: AssetSeed, assetId: string): Book {
  const inService = day(-s.age);
  const firstYear = Number(inService.slice(0, 4));
  let nbv = c(String(s.cost));
  let acc = 0;
  for (let y = firstYear, i = 0; y <= Number(TAX_POSTED) && i < s.taxYears + 1; y++, i++) {
    const remaining = s.taxYears - i + 0.5;
    const ddb = (nbv * 2) / s.taxYears * (i === 0 ? 0.5 : 1);
    const sl = remaining > 0 ? nbv / remaining : nbv;
    const charge = Math.round(Math.min(nbv, Math.max(ddb, sl)));
    acc += charge;
    nbv -= charge;
  }
  return {
    id: uid(K_BOOK, s.n * 2),
    assetId,
    legalEntityId: LEGAL_ENTITY.id,
    bookCode: "TAX",
    currency: "USD",
    method: "DecliningBalance",
    factor: "2",
    switchToStraightLine: true,
    serviceLifePeriods: s.taxYears,
    salvageValue: "0.00",
    convention: "HalfYear",
    inServiceDate: inService,
    cost: m(c(String(s.cost))),
    accumulatedDepreciation: m(acc),
    nbv: m(c(String(s.cost)) - acc),
    status: s.status === "Sold" ? "Sold" : "Open",
    lastDepreciatedPeriod: firstYear <= Number(TAX_POSTED) ? TAX_POSTED : null,
  };
}

function seedAsset(s: AssetSeed): StoredAsset {
  const id = uid(K_ASSET, s.n);
  const acquired = s.status !== "NotYetAcquired";
  const books = acquired ? [ifrsBook(s, id), taxBook(s, id)] : [];
  return {
    id,
    legalEntityId: LEGAL_ENTITY.id,
    assetNumber: s.number,
    name: s.name,
    classCode: s.classCode,
    barcode: s.barcode ?? null,
    quantity: "1",
    status: s.status ?? "Open",
    acquiredDate: acquired ? day(-s.age) : null,
    bookCount: books.length,
    version: 3,
    createdAt: at(-Math.max(s.age, 25) - 5),
    books,
    monthly: Math.round((c(String(s.cost)) - c(String(s.salvage))) / s.lifeMonths),
  };
}

const assets = table<StoredAsset>(SEEDS.map(seedAsset));

const plain = (a: StoredAsset): Asset => {
  const { books: _books, monthly: _monthly, ...rest } = a;
  return rest;
};
const detail = (a: StoredAsset): AssetDetail => {
  const { monthly: _monthly, ...rest } = a;
  return rest;
};

// The rebuilt spindle on the CNC cell: an improvement capitalized on both books.
const additions = new Map<string, Addition[]>([
  [
    uid(K_ASSET, 2),
    [
      {
        id: uid(K_ADDITION, 1),
        assetId: uid(K_ASSET, 2),
        type: "Improvement",
        description: "Spindle rebuild and new tool changer carousel",
        quantity: "1",
        serialNumber: "SPN-77412",
        addedDate: day(-5),
        financial: false,
        cost: "0.00",
        currency: null,
        createdAt: at(-5),
        transactions: [],
      },
      {
        id: uid(K_ADDITION, 2),
        assetId: uid(K_ASSET, 2),
        type: "Accessory",
        description: "Fourth-axis rotary table",
        quantity: "1",
        serialNumber: "RT-210-0093",
        addedDate: day(-640),
        financial: false,
        cost: "0.00",
        currency: null,
        createdAt: at(-640),
        transactions: [],
      },
    ],
  ],
]);

// --- schedules and transactions ------------------------------------------------------

function schedule(a: StoredAsset, b: Book): Schedule {
  const cost = c(b.cost);
  const lines: ScheduleLine[] = [];
  if (b.method === "StraightLine") {
    const start = monthIndex(b.inServiceDate.slice(0, 7));
    let acc = 0;
    for (let i = 0; i < b.serviceLifePeriods; i++) {
      const amount = i === b.serviceLifePeriods - 1 ? cost - c(b.salvageValue) - acc : a.monthly;
      acc += amount;
      lines.push({ index: i + 1, period: periodOf(start + i), amount: m(amount), accumulated: m(acc), nbv: m(cost - acc) });
    }
  } else {
    const firstYear = Number(b.inServiceDate.slice(0, 4));
    let nbv = cost;
    let acc = 0;
    for (let i = 0; i <= b.serviceLifePeriods && nbv > 0; i++) {
      const remaining = b.serviceLifePeriods - i + 0.5;
      const ddb = (nbv * 2) / b.serviceLifePeriods * (i === 0 ? 0.5 : 1);
      const sl = remaining > 0 ? nbv / remaining : nbv;
      const amount = Math.round(Math.min(nbv, Math.max(ddb, sl)));
      acc += amount;
      nbv -= amount;
      lines.push({ index: i + 1, period: String(firstYear + i), amount: m(amount), accumulated: m(acc), nbv: m(nbv) });
    }
  }
  return { bookId: b.id, bookCode: b.bookCode, currency: b.currency, total: m(lines.reduce((s, l) => s + c(l.amount), 0)), lines };
}

function transactions(a: StoredAsset): Transaction[] {
  const out: Transaction[] = [];
  let n = 0;
  const txn = (b: Book, type: Transaction["type"], date: string, costAmount: number, accumulatedAmount: number, sourceType: string, runId: string | null = null): Transaction => ({
    id: uid(K_TXN, Number(a.assetNumber.slice(3)) * 1000 + n++),
    assetBookId: b.id,
    bookCode: b.bookCode,
    currency: b.currency,
    type,
    transactionDate: date,
    costAmount: m(costAmount),
    accumulatedAmount: m(accumulatedAmount),
    proceedsAmount: null,
    gainLoss: null,
    sourceType,
    runId,
    createdAt: at(-1),
  });
  for (const b of a.books) {
    out.push(txn(b, "Acquisition", b.inServiceDate, c(b.cost), 0, "PurchaseInvoice"));
    if (b.method === "StraightLine" && b.lastDepreciatedPeriod) {
      // The last six months one by one; earlier months as the balance brought over at go-live.
      const last = monthIndex(b.lastDepreciatedPeriod);
      const first = monthIndex(b.inServiceDate.slice(0, 7));
      const recent = Math.min(6, last - first + 1);
      const opening = c(b.accumulatedDepreciation) - recent * a.monthly;
      if (opening > 0) out.push(txn(b, "Depreciation", lastDay(periodOf(last - recent)), 0, opening, "OpeningBalance"));
      for (let i = last - recent + 1; i <= last; i++) out.push(txn(b, "Depreciation", lastDay(periodOf(i)), 0, a.monthly, "DepreciationRun", runFor(b.bookCode, periodOf(i))));
    } else if (b.lastDepreciatedPeriod) {
      out.push(txn(b, "Depreciation", `${b.lastDepreciatedPeriod}-12-31`, 0, c(b.accumulatedDepreciation), "DepreciationRun", runFor(b.bookCode, b.lastDepreciatedPeriod)));
    }
    if (a.status === "Sold") {
      const sale: Transaction = { ...txn(b, "DisposalSale", day(-40), -c(b.cost), -c(b.accumulatedDepreciation), "Disposal"), proceedsAmount: "4800.00", gainLoss: m(c("4800.00") - c(b.nbv)) };
      out.push(sale);
    }
  }
  return out.sort((x, y) => y.transactionDate.localeCompare(x.transactionDate));
}

// --- depreciation runs ------------------------------------------------------------------

const runs = table<DepreciationRun>([]);
const runFor = (bookCode: string, p: string): string | null => runs.all().find((r) => r.bookCode === bookCode && r.period === p)?.id ?? null;

function previousPeriod(p: string): string {
  return p.length === 4 ? String(Number(p) - 1) : periodOf(monthIndex(p) - 1);
}

/** Decides each book's line for a run the way the service does: sequential, no catch-up, closed books are exceptions. */
function runLines(bookCode: string, p: string): RunLine[] {
  const out: RunLine[] = [];
  for (const a of assets.all()) {
    for (const b of a.books.filter((x) => x.bookCode === bookCode)) {
      if (b.inServiceDate > (p.length === 4 ? `${p}-12-31` : lastDay(p))) continue;
      const base = { assetBookId: b.id, assetId: a.id, assetNumber: a.assetNumber };
      if (b.status !== "Open") {
        out.push({ ...base, status: "Exception", amount: "0.00", scheduledAmount: "0.00", exceptionCode: "AST-0509", detail: `book is ${b.status}` });
        continue;
      }
      const line = schedule(a, b).lines.find((l) => l.period === p);
      if (!line) continue;
      const last = b.lastDepreciatedPeriod ?? "";
      if (last >= p) continue;
      if (last !== previousPeriod(p) && line.index > 1) {
        out.push({ ...base, status: "Exception", amount: "0.00", scheduledAmount: line.amount, exceptionCode: "AST-0503", detail: `period ${previousPeriod(p)} is not posted on this book yet` });
        continue;
      }
      out.push({ ...base, status: "Due", amount: line.amount, scheduledAmount: line.amount, exceptionCode: null, detail: null });
    }
  }
  return out.sort((x, y) => x.assetNumber.localeCompare(y.assetNumber));
}

function makeRun(id: string, bookCode: string, p: string, lines: RunLine[], status: DepreciationRun["status"], createdAt: string, postedAt: string | null): DepreciationRun {
  const due = lines.filter((l) => l.status !== "Exception");
  return {
    id,
    legalEntityId: LEGAL_ENTITY.id,
    bookCode,
    period: p,
    currency: "USD",
    status,
    assetCount: lines.length,
    exceptionCount: lines.length - due.length,
    totalAmount: m(due.reduce((s, l) => s + c(l.amount), 0)),
    journalEntryId: status === "Posted" ? uid(K_RUN + 100, monthIndex(p.length === 4 ? `${p}-12` : p)) : null,
    version: status === "Posted" ? 2 : 1,
    createdAt,
    postedAt,
    lines: status === "Posted" ? lines.map((l) => (l.status === "Due" ? { ...l, status: "Posted" as const } : l)) : lines,
  };
}

/** Posted history: the IFRS runs of the three months before IFRS_POSTED and the TAX run of last year, rebuilt from the schedules. */
function seedPostedRun(n: number, bookCode: string, p: string, daysAgo: number) {
  const lines: RunLine[] = [];
  for (const a of assets.all()) {
    for (const b of a.books.filter((x) => x.bookCode === bookCode)) {
      if (b.inServiceDate > (p.length === 4 ? `${p}-12-31` : lastDay(p))) continue;
      const line = schedule(a, b).lines.find((l) => l.period === p);
      if (!line) continue;
      lines.push({ assetBookId: b.id, assetId: a.id, assetNumber: a.assetNumber, status: "Due", amount: line.amount, scheduledAmount: line.amount, exceptionCode: null, detail: null });
    }
  }
  lines.sort((x, y) => x.assetNumber.localeCompare(y.assetNumber));
  runs.insert(makeRun(uid(K_RUN, n), bookCode, p, lines, "Posted", at(-daysAgo - 0.2), at(-daysAgo)));
}

const dayOfMonth = new Date().getDate();
seedPostedRun(1, "IFRS", period(-4), dayOfMonth + 64);
seedPostedRun(2, "IFRS", period(-3), dayOfMonth + 34);
seedPostedRun(3, "IFRS", IFRS_POSTED, dayOfMonth + 4);
seedPostedRun(4, "TAX", TAX_POSTED, dayOfMonth + 280);
runs.insert(makeRun(uid(K_RUN, 5), "IFRS", period(-1), runLines("IFRS", period(-1)), "Proposed", at(-2), null));

// --- routes --------------------------------------------------------------------------------

const notFound = (what: string) => reply.problem(404, "COM-0404", `${what} not found`);

route("GET", `${BASE}/depreciation-runs`, () => ({
  items: runs
    .all()
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map(({ lines: _lines, ...rest }) => rest),
}));

route("POST", `${BASE}/depreciation-runs`, ({ body }) => {
  const input = (body ?? {}) as { legalEntityId?: string; bookCode?: string; period?: string };
  const bookCode = (input.bookCode ?? "").toUpperCase();
  const p = input.period ?? "";
  if (input.legalEntityId !== LEGAL_ENTITY.id) return reply.problem(400, "COM-0001", "request failed validation", { legalEntityId: `Use ${LEGAL_ENTITY.code}'s id, ${LEGAL_ENTITY.id}.` });
  if (!["IFRS", "TAX"].includes(bookCode)) return reply.problem(400, "COM-0001", "request failed validation", { bookCode: "No open books use this book code. Use IFRS or TAX." });
  if ((bookCode === "TAX") !== (p.length === 4)) return reply.problem(400, "COM-0001", "request failed validation", { period: bookCode === "TAX" ? "TAX books depreciate yearly: enter YYYY." : "IFRS books depreciate monthly: enter YYYY-MM." });
  const existing = runs.all().find((r) => r.bookCode === bookCode && r.period === p);
  if (existing?.status === "Posted") return reply.problem(409, "AST-0502", `${bookCode} ${p} is already posted`);
  const lines = runLines(bookCode, p);
  const run = makeRun(existing?.id ?? newId(), bookCode, p, lines, "Proposed", at(0), null);
  return reply.created(existing ? runs.update(existing.id, { ...run, version: existing.version + 1 }) : runs.insert(run));
});

route("GET", `${BASE}/depreciation-runs/{id}`, ({ params }) => {
  const run = runs.get(params.id);
  return run ? reply.versioned(run, run.version) : notFound("depreciation run");
});

route("POST", `${BASE}/depreciation-runs/{id}/post`, ({ params, headers }) => {
  const run = runs.get(params.id);
  if (!run) return notFound("depreciation run");
  if (headers.get("If-Match") && headers.get("If-Match") !== `"${run.version}"`) return reply.problem(412, "AST-0006", "the run was proposed again since you opened it");
  if (run.status === "Posted") return reply.problem(409, "AST-0502", `${run.bookCode} ${run.period} is already posted`);
  for (const line of run.lines ?? []) {
    if (line.status !== "Due") continue;
    assets.update(line.assetId, (a) => ({
      ...a,
      books: a.books.map((b) => {
        if (b.id !== line.assetBookId) return b;
        const acc = c(b.accumulatedDepreciation) + c(line.amount);
        return { ...b, accumulatedDepreciation: m(acc), nbv: m(c(b.cost) - acc), lastDepreciatedPeriod: run.period };
      }),
    }));
  }
  const posted = makeRun(run.id, run.bookCode, run.period, run.lines ?? [], "Posted", run.createdAt, at(0));
  const stored = runs.update(run.id, { ...posted, version: run.version + 1 })!;
  return reply.versioned(stored, stored.version);
});

route("GET", `${BASE}/index-series`, () => ({ items: [] }));

route("GET", BASE, ({ query }) => {
  const status = query.get("status");
  return {
    items: assets
      .all()
      .filter((a) => !status || a.status === status)
      .sort((a, b) => a.assetNumber.localeCompare(b.assetNumber))
      .map(plain),
  };
});

route("GET", `${BASE}/{id}/schedule`, ({ params, query }) => {
  const a = assets.get(params.id);
  const b = a?.books.find((x) => x.id === query.get("book"));
  return a && b ? schedule(a, b) : notFound("asset book");
});
route("GET", `${BASE}/{id}/transactions`, ({ params }) => {
  const a = assets.get(params.id);
  return a ? { items: transactions(a) } : notFound("asset");
});
route("GET", `${BASE}/{id}/additions`, ({ params }) => ({ items: additions.get(params.id) ?? [] }));
route("GET", `${BASE}/{id}/insurance-policies`, () => ({ items: [] }));
route("GET", `${BASE}/{id}/transfers`, () => ({ items: [] }));
route("GET", `${BASE}/{id}`, ({ params }) => {
  const a = assets.get(params.id);
  return a ? reply.versioned(detail(a), a.version) : notFound("asset");
});
