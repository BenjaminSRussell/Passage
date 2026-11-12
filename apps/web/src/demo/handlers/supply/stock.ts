// Inventory service (/api/v1/inventory) for the demo, beyond the on-hand projection in inventory.ts: cycle count plans,
// count sheets and tags, holds, inventory journals with their approval workflow, transfer orders to the Dallas DC and
// the stock in transit. Quantities start from the on-hand figures in company.ts and dimension values are the codes
// the screens print (warehouse CR-RM, location RM-B-01), so the counts, holds and journals agree with on-hand.
import type {
  CountLine,
  CountPlan,
  CountPlanInput,
  CountSheet,
  CountSheetSummary,
  CountTag,
  Hold,
  HoldInput,
  InTransitItem,
  Journal,
  JournalLine,
  JournalStatus,
  JournalSummary,
  JournalType,
  LineInput,
  TransferInput,
  TransferLine,
  TransferOrder,
} from "../../../modules/inventory/api/client";
import { ITEMS, LEGAL_ENTITY, USER, WH_DAL, WH_FG, WH_RAW, WH_WIP, itemByNumber, type Item } from "../../company";
import { reply, route, type DemoRequest } from "../../backend";
import { at, day, dec, table, uid } from "../../store";
import { BIN_OF } from "./warehouse";

const BASE = "/api/v1/inventory";
const K_PLAN = 410;
const K_SHEET = 411;
const K_COUNT_LINE = 412;
const K_TAG = 413;
const K_HOLD = 414;
const K_JOURNAL = 415;
const K_JOURNAL_LINE = 416;
const K_TRANSFER = 417;
const K_TRANSFER_LINE = 418;

/** Transit warehouse of the plant-to-Dallas lane: stock on the truck. */
const TRANSIT = "TRN-DAL";

const homeWarehouse = (it: Item) => (it.kind === "finished" ? WH_FG : it.kind === "subassembly" ? WH_WIP : WH_RAW);
/** The dimension codes of an item's home bin at the plant. */
function dims(number: string, extra: Record<string, string> = {}): Record<string, string> {
  const it = itemByNumber(number);
  return { site: "CR", warehouse: homeWarehouse(it).code, location: BIN_OF[number] ?? "", ...extra };
}
const ifMatch = (req: DemoRequest): number | undefined => {
  const raw = req.headers.get("If-Match");
  return raw ? Number(raw.replace(/"/g, "")) : undefined;
};
const stale = () => reply.problem(409, "conflict_version", "Someone changed this since you opened it. Reload and try again.");

// ---- Count plans, sheets and tags ---------------------------------------------------------------------------

function plan(n: number, name: string, extra: Partial<CountPlan>): CountPlan {
  return { id: uid(K_PLAN, n), legalEntityId: LEGAL_ENTITY.id, name, currency: "USD", unit: "ea", tolerancePct: "2.00", toleranceValue: "500.00", blind: false, freezeStock: false, fourEyes: false, createdAt: at(-180 + n * 11), ...extra };
}

const plans = table<CountPlan>([
  plan(1, "Weekly A-class cycle count, raw materials", { warehouse: WH_RAW.code, abcClass: "A", blind: true, fourEyes: true, tolerancePct: "1.50", toleranceValue: "400.00" }),
  plan(2, "Small parts bins RM-B", { warehouse: WH_RAW.code, locationFrom: "RM-B-01", locationTo: "RM-B-04", tolerancePct: "5.00", toleranceValue: "250.00" }),
  plan(3, "Finished pumps, month end", { warehouse: WH_FG.code, freezeStock: true, fourEyes: true, tolerancePct: "0.00", toleranceValue: "1000.00" }),
  plan(4, "Dallas DC quarterly tag count", { warehouse: WH_DAL.code, minDaysSinceCount: 80, tolerancePct: "1.00", toleranceValue: "750.00" }),
]);

/** Plan 1 counts motors and castings, plan 2 small parts, plan 3 finished pumps, plan 4 the Dallas racks. */
const PLAN_ITEMS: Record<number, string[]> = {
  1: ["MTR-5HP-TEFC", "MTR-15HP-TEFC", "CST-VOL-04", "CST-IMP-06"],
  2: ["SEAL-MK-125", "BRG-6206", "FST-KIT-A"],
  3: ["CP-200", "CP-400", "SP-150", "DP-100", "BP-300"],
  4: ["CP-200", "SP-150", "CP-400", "DP-100"],
};
const DALLAS: Record<string, number> = { "CP-200": 15, "SP-150": 30, "CP-400": 3, "DP-100": 2 };
const planNo = (planId: string) => Number(Object.keys(PLAN_ITEMS).find((k) => uid(K_PLAN, Number(k)) === planId) ?? 0);

let countLineSeq = 0;
function cline(lineNo: number, number: string, planN: number, counted?: number, ago = 0.2, extra: Partial<CountLine> = {}): CountLine {
  const it = itemByNumber(number);
  const expected = planN === 4 ? (DALLAS[number] ?? 0) : it.onHand;
  const d = planN === 4 ? { site: "DAL", warehouse: WH_DAL.code, location: `DAL-A-0${lineNo}` } : dims(number);
  return {
    id: uid(K_COUNT_LINE, ++countLineSeq),
    lineNo,
    productId: it.id,
    dimensions: d,
    expectedQty: dec(expected, 0),
    countedQty: counted === undefined ? null : dec(counted, 0),
    countedBy: counted === undefined ? undefined : "Luis Ortega",
    countedAt: counted === undefined ? undefined : at(-ago),
    postedVariance: null,
    recountRequested: false,
    recountRounds: 0,
    ...extra,
  };
}

function sheet(n: number, planN: number, status: CountSheet["status"], ago: number, lines: CountLine[], extra: Partial<CountSheet> = {}): CountSheet {
  const p = plans.get(uid(K_PLAN, planN))!;
  return {
    id: uid(K_SHEET, n),
    legalEntityId: LEGAL_ENTITY.id,
    planId: p.id,
    planName: p.name,
    number: `CS-${String(300 + n).padStart(6, "0")}`,
    status,
    blind: p.blind,
    fourEyes: p.fourEyes,
    freezeStock: p.freezeStock,
    recountLines: lines.filter((l) => l.recountRequested).length,
    version: 1 + lines.filter((l) => l.countedQty !== null).length,
    lineCount: lines.length,
    countedCount: lines.filter((l) => l.countedQty !== null).length,
    createdAt: at(-ago),
    lines,
    ...extra,
  };
}

const posted = (l: CountLine): CountLine => ({ ...l, postedVariance: dec(Number(l.countedQty ?? 0) - Number(l.expectedQty ?? 0), 0) });

const sheets = table<CountSheet>([
  sheet(18, 1, "OPEN", 0.3, [cline(1, "MTR-5HP-TEFC", 1, 22, 0.1), cline(2, "MTR-15HP-TEFC", 1, 4, 0.08), cline(3, "CST-VOL-04", 1), cline(4, "CST-IMP-06", 1)]),
  sheet(19, 2, "OPEN", 0.1, [cline(1, "SEAL-MK-125", 2), cline(2, "BRG-6206", 2), cline(3, "FST-KIT-A", 2)]),
  sheet(17, 3, "COUNTED", 1.2, [cline(1, "CP-200", 3, 42, 1), cline(2, "CP-400", 3, 11, 1), cline(3, "SP-150", 3, 87, 1), cline(4, "DP-100", 3, 5, 1), cline(5, "BP-300", 3, 2, 1)], { submittedBy: "Luis Ortega" }),
  sheet(15, 1, "POSTED", 7.3, [cline(1, "MTR-5HP-TEFC", 1, 24, 7.1), cline(2, "MTR-15HP-TEFC", 1, 4, 7.1), cline(3, "CST-VOL-04", 1, 30, 7.1), cline(4, "CST-IMP-06", 1, 12, 7.1)].map(posted), {
    journalId: uid(K_JOURNAL, 9),
    postedAt: at(-7),
    approvedBy: "Dana Whitfield",
    submittedBy: "Luis Ortega",
  }),
  sheet(12, 2, "POSTED", 21.4, [cline(1, "SEAL-MK-125", 2, 141, 21.2), cline(2, "BRG-6206", 2, 606, 21.2), cline(3, "FST-KIT-A", 2, 220, 21.2)].map(posted), {
    journalId: uid(K_JOURNAL, 4),
    postedAt: at(-21),
    submittedBy: "Marcus Bell",
  }),
]);

const summary = ({ lines: _lines, ...s }: CountSheet): CountSheetSummary => s;

function recount(s: CountSheet): CountSheet {
  const counted = s.lines.filter((l) => l.countedQty !== null).length;
  return { ...s, countedCount: counted, lineCount: s.lines.length, recountLines: s.lines.filter((l) => l.recountRequested).length };
}

route("GET", `${BASE}/count-plans`, () => ({ items: plans.all().sort((a, b) => a.name.localeCompare(b.name)) }));

route("POST", `${BASE}/count-plans`, ({ body }) => {
  const input = body as CountPlanInput;
  const n = plans.all().length + 1;
  const created: CountPlan = {
    ...plan(n, input.name, {}),
    warehouse: input.warehouse || undefined,
    currency: input.currency,
    unit: input.unit,
    tolerancePct: input.tolerancePct || "0.00",
    toleranceValue: input.toleranceValue || "0.00",
    blind: input.blind,
    freezeStock: input.freezeStock ?? false,
    fourEyes: input.fourEyes ?? false,
    locationFrom: input.locationFrom || undefined,
    locationTo: input.locationTo || undefined,
    abcClass: input.abcClass || undefined,
    samplePct: input.samplePct || undefined,
    minDaysSinceCount: input.minDaysSinceCount,
    includeVendorOwned: input.includeVendorOwned,
    createdAt: at(0),
  };
  PLAN_ITEMS[n] = ITEMS.filter((i) => !created.warehouse || homeWarehouse(i).code === created.warehouse).map((i) => i.number);
  return reply.created(plans.insert(created));
});

route("POST", `${BASE}/count-plans/{id}:generate`, ({ params }) => {
  const p = plans.get(params.id);
  if (!p) return reply.problem(404, "INV-CNT-004", "Count plan not found");
  const n = 100 + sheets.all().length;
  const planN = planNo(p.id) || 1;
  const lines = (PLAN_ITEMS[planN] ?? []).map((number, i) => cline(i + 1, number, planN));
  const made = sheet(n, planN, "OPEN", 0, lines);
  return reply.created(sheets.insert({ ...made, planId: p.id, planName: p.name, version: 1 }));
});

// Tags: the Dallas plan counts with pre-numbered tags; the others have none yet.
let tagSeq = 0;
function tag(n: number, status: CountTag["status"], number?: string, counted?: number): CountTag {
  const it = number ? itemByNumber(number) : undefined;
  return {
    id: uid(K_TAG, ++tagSeq),
    tagNumber: `T-4${String(n).padStart(4, "0")}`,
    planId: uid(K_PLAN, 4),
    status,
    productId: it?.id,
    dimensions: it ? { site: "DAL", warehouse: WH_DAL.code, location: `DAL-A-0${(n % 4) + 1}` } : {},
    countedQty: counted === undefined ? null : dec(counted, 0),
    countedBy: counted === undefined ? undefined : "Samir Haddad",
    version: status === "OPEN" ? 1 : 2,
  };
}
const tags = table<CountTag>([tag(1, "USED", "CP-200", 15), tag(2, "USED", "SP-150", 30), tag(3, "USED", "CP-400", 3), tag(4, "OPEN"), tag(5, "OPEN"), tag(6, "VOID")]);

route("GET", `${BASE}/count-plans/{id}/tags`, ({ params }) => {
  if (!plans.get(params.id)) return reply.problem(404, "INV-CNT-004", "Count plan not found");
  return { items: tags.all().filter((t) => t.planId === params.id) };
});
route("POST", `${BASE}/count-plans/{id}:generate-tags`, ({ params, body }) => {
  const count = Math.min(50, Number((body as { count?: number } | undefined)?.count ?? 1));
  const start = tags.all().filter((t) => t.planId === params.id).length;
  const made = Array.from({ length: count }, (_, i) => tags.insert({ ...tag(start + i + 1, "OPEN"), planId: params.id }));
  return reply.created({ items: made });
});
route("POST", `${BASE}/count-tags/{id}:use`, ({ params, body }) => {
  const input = body as { productId: string; dimensions: Record<string, string>; countedQty: string };
  const t = tags.update(params.id, (row) => ({ ...row, status: "USED", productId: input.productId, dimensions: input.dimensions, countedQty: input.countedQty, countedBy: USER.name, version: row.version + 1 }));
  return t ?? reply.problem(404, "INV-CNT-010", "Tag not found");
});
route("POST", `${BASE}/count-tags/{id}:void`, ({ params }) => tags.update(params.id, (row) => ({ ...row, status: "VOID", version: row.version + 1 })) ?? reply.problem(404, "INV-CNT-010", "Tag not found"));

route("GET", `${BASE}/count-sheets`, ({ query }) => {
  const status = query.get("status");
  const planId = query.get("planId");
  const items = sheets
    .all()
    .filter((s) => (!status || s.status === status) && (!planId || s.planId === planId))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map(summary);
  return { items };
});

route("GET", `${BASE}/count-sheets/{id}`, ({ params }) => sheets.get(params.id) ?? reply.problem(404, "INV-CNT-005", "Count sheet not found"));

route("PUT", `${BASE}/count-sheets/{id}/lines/{lineId}`, (req) => {
  const s = sheets.get(req.params.id);
  if (!s) return reply.problem(404, "INV-CNT-005", "Count sheet not found");
  if (s.status !== "OPEN") return reply.problem(409, "INV-CNT-006", "Only an open sheet takes counts");
  const qty = String((req.body as { countedQty?: string } | undefined)?.countedQty ?? "");
  const lines = s.lines.map((l) => (l.id === req.params.lineId ? { ...l, countedQty: qty, countedBy: USER.name, countedAt: at(0), recountRequested: false } : l));
  return sheets.update(s.id, recount({ ...s, lines, version: s.version + 1 }));
});

route("POST", `${BASE}/count-sheets/{id}/lines/{lineId}:recount`, ({ params, body }) => {
  const s = sheets.get(params.id);
  if (!s) return reply.problem(404, "INV-CNT-005", "Count sheet not found");
  const reason = String((body as { reason?: string } | undefined)?.reason ?? "");
  const lines = s.lines.map((l) => (l.id === params.lineId ? { ...l, recountRequested: true, recountReason: reason, recountRounds: l.recountRounds + 1, recountSource: "SUPERVISOR" as const } : l));
  return sheets.update(s.id, recount({ ...s, lines, status: "OPEN", version: s.version + 1 }));
});

route("POST", `${BASE}/count-sheets/{id}:{action}`, ({ params }) => {
  const s = sheets.get(params.id);
  if (!s) return reply.problem(404, "INV-CNT-005", "Count sheet not found");
  switch (params.action) {
    case "submit":
      if (s.lines.some((l) => l.countedQty === null)) return reply.problem(422, "INV-CNT-007", "Count every line before closing the count");
      return sheets.update(s.id, { status: "COUNTED", submittedBy: USER.name, version: s.version + 1 });
    case "approve":
      if (s.status !== "COUNTED") return reply.problem(409, "INV-CNT-008", "Only a counted sheet can be approved");
      return sheets.update(s.id, { status: "APPROVED", approvedBy: USER.name, version: s.version + 1 });
    case "cancel":
      return sheets.update(s.id, { status: "CANCELLED", version: s.version + 1 });
    case "post": {
      if (s.fourEyes && s.status !== "APPROVED") return reply.problem(409, "INV-CNT-009", "This plan needs a supervisor's approval before posting");
      if (s.lines.some((l) => l.countedQty === null)) return reply.problem(422, "INV-CNT-007", "Count every line before posting");
      const lines = s.lines.map(posted);
      const variances = lines.filter((l) => Number(l.postedVariance) !== 0);
      const j = countJournal(variances);
      return sheets.update(s.id, { status: "POSTED", lines, journalId: j.id, postedAt: at(0), version: s.version + 1 });
    }
    default:
      return reply.problem(404, "INV-0404", `Unknown action ${params.action}`);
  }
});

// ---- Holds --------------------------------------------------------------------------------------------------

function hold(n: number, number: string, qty: number, released: number, reason: string, source: [string, string], extra: Partial<Hold> = {}): Hold {
  const it = itemByNumber(number);
  return {
    id: uid(K_HOLD, n),
    legalEntityId: LEGAL_ENTITY.id,
    productId: it.id,
    dimensions: dims(number),
    qty: dec(qty, 0),
    releasedQty: dec(released, 0),
    heldQty: dec(qty - released, 0),
    wholeLot: false,
    reason,
    sourceType: source[0],
    sourceRef: source[1],
    state: released >= qty ? "RELEASED" : "ACTIVE",
    version: released > 0 ? 2 : 1,
    ...extra,
  };
}

const holds = table<Hold>([
  hold(1, "CST-IMP-06", 12, 0, "QUALITY", ["QUALITY_ORDER", "QO-24006"], { wholeLot: true, dimensions: dims("CST-IMP-06", { batch: "IR-9931" }), note: "Receiving inspection of the Ironbridge lot is still running" }),
  hold(2, "CP-200", 4, 0, "QUALITY", ["QUALITY_ORDER", "QO-24007"], { dimensions: dims("CP-200", { batch: "B24608" }), note: "One sample failed the flow test; usage decision pending" }),
  hold(3, "MTR-5HP-TEFC", 2, 0, "DAMAGE", ["RECEIPT", "RCV-010405"], { note: "Fan covers dented in transit, claim open with Volta Motor Works" }),
  hold(4, "SEAL-MK-125", 24, 0, "OTHER", ["SALES_ORDER", "SO-024181"], { note: "Spare seal kits kept for Bluewater's commissioning visit" }),
  hold(5, "BRG-6206", 40, 0, "RECALL", ["NONCONFORMANCE", "NC-24033"], { dimensions: dims("BRG-6206", { batch: "PB-2588" }), note: "Supplier advisory on lot PB-2588 grease fill" }),
  hold(6, "SA-IMP-06", 8, 8, "QUALITY", ["QUALITY_ORDER", "QO-24003"], { dimensions: dims("SA-IMP-06", { batch: "B24575" }), note: "Rebalanced and released" }),
  hold(7, "CP-400", 2, 2, "LEGAL", ["SALES_ORDER", "SO-024175"], { note: "Export screening cleared" }),
]);

route("GET", `${BASE}/holds`, ({ query }) => {
  const state = query.get("state");
  const product = query.get("product");
  const sourceType = query.get("sourceType");
  const sourceRef = query.get("sourceRef");
  const limit = Number(query.get("limit") ?? 100) || 100;
  const items = holds
    .all()
    .filter((h) => (!state || h.state === state) && (!product || h.productId === product) && (!sourceType || h.sourceType === sourceType) && (!sourceRef || h.sourceRef === sourceRef))
    .slice(0, limit);
  return { items };
});

route("POST", `${BASE}/holds`, ({ body }) => {
  const input = body as HoldInput;
  const it = ITEMS.find((i) => i.id === input.productId);
  if (!it) return reply.problem(422, "INV-HLD-001", "Choose a product", { productId: "Choose a product." });
  const qty = input.wholeLot || !input.qty ? it.onHand : Number(input.qty);
  const n = holds.all().length + 1;
  const made: Hold = {
    ...hold(n, it.number, qty, 0, input.reason, [input.sourceType, input.sourceRef], { note: input.note, wholeLot: input.wholeLot ?? false }),
    dimensions: input.dimensions,
  };
  return reply.created({ items: [holds.insert(made)], shortfall: "0" });
});

route("POST", `${BASE}/holds/{id}:release`, (req) => {
  const h = holds.get(req.params.id);
  if (!h) return reply.problem(404, "INV-HLD-404", "Hold not found");
  const version = ifMatch(req);
  if (version !== undefined && version !== h.version) return stale();
  const asked = (req.body as { qty?: string } | undefined)?.qty;
  const release = asked ? Number(asked) : Number(h.heldQty);
  const releasedQty = Number(h.releasedQty) + release;
  const held = Math.max(0, Number(h.qty) - releasedQty);
  return holds.update(h.id, { releasedQty: dec(releasedQty, 0), heldQty: dec(held, 0), state: held === 0 ? "RELEASED" : "ACTIVE", version: h.version + 1 });
});

// ---- Journals -----------------------------------------------------------------------------------------------

let jLineSeq = 0;
function jline(lineNo: number, number: string, qty: number, extra: Partial<JournalLine> = {}): JournalLine {
  const it = itemByNumber(number);
  return { id: uid(K_JOURNAL_LINE, ++jLineSeq), lineNo, productId: it.id, dimensions: dims(number), qty: dec(qty, 0), enteredQty: dec(qty, 0), enteredUnit: it.unit, costPrice: dec(it.cost), ...extra };
}

/** Inventory 1400, adjustments 5150 (or the journal's offset account), opening balance equity 3900. */
function glOf(type: JournalType, lines: JournalLine[], offset = "5150"): Journal["glEntry"] {
  if (type === "TRANSFER" || type === "OWNERSHIP_CHANGE") return { currency: "USD", lines: [], ledgerEntryId: null };
  const value = lines.reduce((s, l) => s + Number(l.qty) * Number(l.costPrice), 0);
  const against = type === "OPENING_BALANCE" ? "3900" : offset;
  const debitInventory = value >= 0;
  const amount = dec(Math.abs(value));
  return {
    currency: "USD",
    ledgerEntryId: `GJ-${String(Math.round(Math.abs(value)) % 9000 + 41000)}`,
    lines: [
      { account: "1400", debit: debitInventory ? amount : "0.00", credit: debitInventory ? "0.00" : amount },
      { account: against, debit: debitInventory ? "0.00" : amount, credit: debitInventory ? amount : "0.00" },
    ],
  };
}

function journal(n: number, type: JournalType, status: JournalStatus, ago: number, lines: JournalLine[], extra: Partial<Journal> = {}): Journal {
  const isPosted = status === "POSTED";
  return {
    id: uid(K_JOURNAL, n),
    number: `IJ-${String(n).padStart(6, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    journalType: type,
    status,
    postingDate: day(-Math.floor(ago)),
    offsetAccountId: type === "ADJUSTMENT" ? "5150" : undefined,
    currency: "USD",
    version: status === "DRAFT" ? 1 + (lines.length > 0 ? 1 : 0) : status === "SUBMITTED" ? 3 : 5,
    lineCount: lines.length,
    createdAt: at(-ago - 0.1),
    postedAt: isPosted ? at(-ago) : undefined,
    lines,
    glEntry: isPosted ? glOf(type, lines) : undefined,
    ...extra,
  };
}

const journals = table<Journal>([
  journal(1, "OPENING_BALANCE", "POSTED", 200, [jline(1, "CP-200", 30), jline(2, "CP-400", 8), jline(3, "SP-150", 60), jline(4, "DP-100", 10), jline(5, "BP-300", 2)]),
  journal(2, "ADJUSTMENT", "POSTED", 34, [jline(1, "RM-EPOXY-BLU", -2, { dimensions: dims("RM-EPOXY-BLU", { batch: "EP-2604" }) })]),
  journal(3, "MOVEMENT", "POSTED", 26, [jline(1, "SA-IMP-06", 24, { dimensions: dims("SA-IMP-06", { batch: "B24575" }) })], { offsetAccountId: "5200" }),
  journal(4, "COUNT", "POSTED", 21, [jline(1, "SEAL-MK-125", 1), jline(2, "BRG-6206", -4)]),
  journal(5, "TRANSFER", "POSTED", 15, [jline(1, "CP-200", 6, { toDimensions: { site: "CR", warehouse: WH_FG.code, location: "FG-A-06" } })]),
  journal(6, "ADJUSTMENT", "REJECTED", 12, [jline(1, "CST-IMP-06", -6, { dimensions: dims("CST-IMP-06", { batch: "IR-9902" }) })]),
  journal(9, "COUNT", "POSTED", 7, [jline(1, "MTR-5HP-TEFC", 2), jline(2, "CST-VOL-04", -1)]),
  journal(10, "ADJUSTMENT", "SUBMITTED", 1.2, [jline(1, "DP-100", -1)]),
  journal(11, "MOVEMENT", "SUBMITTED", 0.9, [jline(1, "RM-SS316-BAR", -18, { dimensions: dims("RM-SS316-BAR", { batch: "HT-24-8841" }) }), jline(2, "CST-IMP-06", -12, { dimensions: dims("CST-IMP-06", { batch: "IR-9931" }) })], { offsetAccountId: "5200" }),
  journal(12, "ADJUSTMENT", "DRAFT", 0.4, [jline(1, "SEAL-MK-125", -3), jline(2, "FST-KIT-A", 10)]),
  journal(13, "TRANSFER", "DRAFT", 0.2, [jline(1, "SP-150", 10, { toDimensions: { site: "CR", warehouse: WH_FG.code, location: "FG-A-06" } })]),
  journal(14, "MOVEMENT", "DRAFT", 0.05, []),
]);

function countJournal(variances: CountLine[]): Journal {
  const n = 100 + journals.all().length;
  const lines = variances.map((l, i): JournalLine => {
    const it = ITEMS.find((x) => x.id === l.productId)!;
    return { id: uid(K_JOURNAL_LINE, 5000 + n * 20 + i), lineNo: i + 1, productId: it.id, dimensions: l.dimensions, qty: l.postedVariance ?? "0", enteredQty: l.postedVariance ?? "0", enteredUnit: it.unit, costPrice: dec(it.cost) };
  });
  return journals.insert(journal(n, "COUNT", "POSTED", 0, lines, { createdAt: at(0), postedAt: at(0), postingDate: day(0), version: 2, glEntry: glOf("COUNT", lines) }));
}

const jsummary = ({ lines: _l, glEntry: _g, ...j }: Journal): JournalSummary => j;

route("GET", `${BASE}/journals`, ({ query }) => {
  const status = query.get("status");
  const type = query.get("type");
  const items = journals
    .all()
    .filter((j) => (!status || j.status === status) && (!type || j.journalType === type))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map(jsummary);
  return { items };
});

route("POST", `${BASE}/journals`, ({ body }) => {
  const input = body as { journalType: JournalType; postingDate: string; currency: string; offsetAccountId?: string };
  const n = 200 + journals.all().length;
  const made = journal(n, input.journalType, "DRAFT", 0, [], { postingDate: input.postingDate, currency: input.currency, offsetAccountId: input.offsetAccountId, createdAt: at(0), version: 1 });
  return reply.created(journals.insert(made));
});

route("GET", `${BASE}/journals/{id}`, ({ params }) => journals.get(params.id) ?? reply.problem(404, "INV-JRN-404", "Journal not found"));

route("POST", `${BASE}/journals/{id}/lines:bulk`, ({ params, body }) => {
  const j = journals.get(params.id);
  if (!j) return reply.problem(404, "INV-JRN-404", "Journal not found");
  if (j.status !== "DRAFT") return reply.problem(409, "INV-JRN-003", "Lines can only be added to a draft journal");
  const input = (body as { lines: LineInput[] }).lines;
  const added = input.map((l, i): JournalLine => {
    const it = ITEMS.find((x) => x.id === l.productId);
    return {
      id: uid(K_JOURNAL_LINE, 9000 + j.lines.length + i + Math.round(Math.random() * 1000) * 10),
      lineNo: j.lines.length + i + 1,
      clientKey: l.clientKey,
      productId: l.productId,
      dimensions: l.dimensions,
      toDimensions: l.toDimensions,
      qty: l.qty,
      enteredQty: l.qty,
      enteredUnit: l.enteredUnit,
      costPrice: l.costPrice || dec(it?.cost ?? 0),
    };
  });
  const lines = [...j.lines, ...added];
  return journals.update(j.id, { lines, lineCount: lines.length, version: j.version + 1 });
});

route("POST", `${BASE}/journals/{id}:validate`, ({ params }) => {
  const j = journals.get(params.id);
  if (!j) return reply.problem(404, "INV-JRN-404", "Journal not found");
  if (j.lines.length === 0) return reply.problem(422, "INV-JRN-005", "The journal has no lines to post");
  return { valid: true, journalId: j.id, status: j.status, version: j.version, lineCount: j.lines.length };
});

route("POST", `${BASE}/journals/{id}:reverse`, ({ params, body }) => {
  const j = journals.get(params.id);
  if (!j || j.status !== "POSTED") return reply.problem(409, "INV-JRN-007", "Only a posted journal can be reversed");
  if (j.reversedByJournalId) return journals.get(j.reversedByJournalId);
  const n = 300 + journals.all().length;
  const lines = j.lines.map((l, i) => ({ ...l, id: uid(K_JOURNAL_LINE, 7000 + n * 20 + i), reversesLineId: l.id, qty: dec(-Number(l.qty), 0), enteredQty: dec(-Number(l.enteredQty), 0) }));
  const postingDate = String((body as { postingDate?: string } | undefined)?.postingDate ?? day(0));
  const rev = journals.insert(journal(n, j.journalType, "DRAFT", 0, lines, { postingDate, reversesJournalId: j.id, offsetAccountId: j.offsetAccountId, createdAt: at(0), version: 1 }));
  journals.update(j.id, { reversedByJournalId: rev.id });
  return reply.created(rev);
});

route("POST", `${BASE}/journals/{id}:copy`, ({ params, body }) => {
  const j = journals.get(params.id);
  if (!j) return reply.problem(404, "INV-JRN-404", "Journal not found");
  const n = 400 + journals.all().length;
  const postingDate = (body as { postingDate?: string } | undefined)?.postingDate || j.postingDate;
  const lines = j.lines.map((l, i) => ({ ...l, id: uid(K_JOURNAL_LINE, 8000 + n * 20 + i), reversesLineId: undefined }));
  const type = j.journalType === "COUNT" ? "ADJUSTMENT" : j.journalType;
  return reply.created(journals.insert(journal(n, type, "DRAFT", 0, lines, { postingDate, offsetAccountId: j.offsetAccountId, createdAt: at(0), version: 1 })));
});

route("POST", `${BASE}/journals/{id}:{step}`, (req) => {
  const j = journals.get(req.params.id);
  if (!j) return reply.problem(404, "INV-JRN-404", "Journal not found");
  const version = ifMatch(req);
  if (version !== undefined && version !== j.version) return stale();
  const next = (status: JournalStatus, extra: Partial<Journal> = {}) => journals.update(j.id, { status, version: j.version + 1, ...extra });
  switch (req.params.step) {
    case "submit":
      if (j.status !== "DRAFT") return reply.problem(409, "INV-JRN-004", "Only a draft journal can be submitted");
      if (j.lines.length === 0) return reply.problem(422, "INV-JRN-005", "Add a line before submitting the journal");
      return next("SUBMITTED");
    case "approve":
      if (j.status !== "SUBMITTED") return reply.problem(409, "INV-JRN-004", "Only a submitted journal can be approved");
      return next("APPROVED");
    case "reject":
      return next("REJECTED");
    case "reopen":
      return next("DRAFT");
    case "cancel":
      return next("CANCELLED");
    case "post":
      if (j.lines.length === 0) return reply.problem(422, "INV-JRN-005", "The journal has no lines to post");
      if (j.status !== "DRAFT" && j.status !== "APPROVED") return reply.problem(409, "INV-JRN-006", "Submit the journal for approval before posting it");
      return next("POSTED", { postedAt: at(0), glEntry: glOf(j.journalType, j.lines, j.offsetAccountId) });
    default:
      return reply.problem(404, "INV-0404", `Unknown step ${req.params.step}`);
  }
});

// ---- Transfer orders and stock in transit --------------------------------------------------------------------

let tLineSeq = 0;
function tline(lineNo: number, number: string, qty: number, shipped: number, received: number, from = WH_FG.code): TransferLine {
  const it = itemByNumber(number);
  return {
    id: uid(K_TRANSFER_LINE, ++tLineSeq),
    lineNo,
    productId: it.id,
    dimensions: { site: "CR", warehouse: from, location: BIN_OF[number] ?? "" },
    unit: it.unit,
    qty: dec(qty, 0),
    shippedQty: dec(shipped, 0),
    receivedQty: dec(received, 0),
    inTransit: dec(shipped - received, 0),
    underdeliveryPct: "0.00",
    overdeliveryPct: "5.00",
    unshippedQty: dec(shipped > 0 ? 0 : qty, 0),
    closed: received >= qty,
  };
}

function transfer(n: number, status: TransferOrder["status"], shipIn: number, lines: TransferLine[], extra: Partial<TransferOrder> = {}): TransferOrder {
  return {
    id: uid(K_TRANSFER, n),
    legalEntityId: LEGAL_ENTITY.id,
    number: `TO-${String(210 + n).padStart(6, "0")}`,
    status,
    fromWarehouseId: WH_FG.code,
    toWarehouseId: WH_DAL.code,
    toSiteId: "DAL",
    transitWarehouseId: TRANSIT,
    useTransit: true,
    shipDate: day(shipIn),
    plannedReceiptDate: day(shipIn + 2),
    deliveryTerms: "FCA",
    modeOfDelivery: "ROAD",
    receiptDate: status === "RECEIVED" ? day(shipIn + 2) : undefined,
    version: status === "DRAFT" ? 1 : status === "SHIPPED" ? 3 : 4,
    createdAt: at(shipIn - 1.5),
    lines,
    ...extra,
  };
}

const transfers = table<TransferOrder>([
  transfer(1, "RECEIVED", -62, [tline(1, "CP-200", 12, 12, 12), tline(2, "SP-150", 24, 24, 24)]),
  transfer(2, "RECEIVED", -41, [tline(1, "SP-150", 20, 20, 20), tline(2, "DP-100", 4, 4, 4)]),
  transfer(3, "RECEIVED", -27, [tline(1, "CP-400", 3, 3, 3)]),
  transfer(4, "RECEIVED", -12, [tline(1, "SP-150", 18, 18, 18), tline(2, "DP-100", 2, 2, 2)]),
  transfer(5, "SHIPPED", -2, [tline(1, "CP-200", 15, 15, 0), tline(2, "SEAL-MK-125", 24, 24, 0, WH_RAW.code)], { plannedReceiptDate: day(0) }),
  transfer(6, "DRAFT", 5, [tline(1, "SP-150", 30, 0, 0), tline(2, "CP-200", 8, 0, 0)]),
]);

route("GET", `${BASE}/transfer-orders`, ({ query }) => {
  const status = query.get("status");
  return { items: transfers.all().filter((t) => !status || t.status === status).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
});

route("POST", `${BASE}/transfer-orders`, ({ body }) => {
  const input = body as TransferInput;
  const n = transfers.all().length + 1;
  const lines = input.lines.map((l, i): TransferLine => {
    const it = ITEMS.find((x) => x.id === l.productId);
    return {
      id: uid(K_TRANSFER_LINE, 500 + n * 20 + i),
      lineNo: i + 1,
      productId: l.productId,
      dimensions: l.dimensions,
      unit: l.unit || it?.unit || "ea",
      qty: l.qty,
      shippedQty: "0",
      receivedQty: "0",
      inTransit: "0",
      underdeliveryPct: l.underdeliveryPct || "0.00",
      overdeliveryPct: l.overdeliveryPct || "0.00",
      unshippedQty: l.qty,
      closed: false,
    };
  });
  const made = transfer(n, input.draft ? "DRAFT" : "CREATED", 0, lines, {
    fromWarehouseId: input.fromWarehouseId,
    toWarehouseId: input.toWarehouseId,
    toSiteId: input.toSiteId,
    useTransit: input.useTransit,
    transitWarehouseId: input.useTransit ? input.transitWarehouseId || TRANSIT : undefined,
    shipDate: input.shipDate,
    plannedReceiptDate: input.plannedReceiptDate,
    deliveryTerms: input.deliveryTerms,
    modeOfDelivery: input.modeOfDelivery,
    createdAt: at(0),
    version: 1,
  });
  return reply.created(transfers.insert(made));
});

route("GET", `${BASE}/transfer-orders/{id}`, ({ params }) => transfers.get(params.id) ?? reply.problem(404, "INV-TRF-404", "Transfer order not found"));

route("DELETE", `${BASE}/transfer-orders/{id}`, ({ params }) => {
  const t = transfers.get(params.id);
  if (!t) return reply.problem(404, "INV-TRF-404", "Transfer order not found");
  if (Number(t.lines.reduce((s, l) => s + Number(l.shippedQty), 0)) > 0) return reply.problem(409, "INV-TRF-006", "A transfer order that shipped cannot be deleted");
  transfers.remove(t.id);
  return reply.noContent();
});

route("POST", `${BASE}/transfer-orders/{id}:{action}`, (req) => {
  const t = transfers.get(req.params.id);
  if (!t) return reply.problem(404, "INV-TRF-404", "Transfer order not found");
  const version = ifMatch(req);
  if (version !== undefined && version !== t.version) return stale();
  const body = (req.body ?? {}) as { shipDate?: string; receiptDate?: string; lines?: { lineId: string; qty: string; closeShort?: boolean }[] };
  switch (req.params.action) {
    case "confirm":
      return transfers.update(t.id, { status: "CREATED", version: t.version + 1 });
    case "cancel":
      return transfers.update(t.id, { status: "CANCELLED", version: t.version + 1 });
    case "ship": {
      if (t.status !== "CREATED" && t.status !== "DRAFT") return reply.problem(409, "INV-TRF-003", "Only a created transfer order can ship");
      const lines = t.lines.map((l) => {
        const asked = body.lines?.find((x) => x.lineId === l.id);
        const qty = asked ? Number(asked.qty) : Number(l.unshippedQty ?? l.qty);
        const shipped = Number(l.shippedQty) + qty;
        return { ...l, shippedQty: dec(shipped, 0), inTransit: dec(t.useTransit ? shipped - Number(l.receivedQty) : 0, 0), receivedQty: t.useTransit ? l.receivedQty : dec(shipped, 0), unshippedQty: dec(Math.max(0, Number(l.qty) - shipped), 0), closed: !t.useTransit && shipped >= Number(l.qty) };
      });
      return transfers.update(t.id, { status: t.useTransit ? "SHIPPED" : "RECEIVED", shipDate: body.shipDate ?? day(0), receiptDate: t.useTransit ? undefined : (body.shipDate ?? day(0)), lines, version: t.version + 1 });
    }
    case "receive": {
      const lines = t.lines.map((l) => {
        const asked = body.lines?.find((x) => x.lineId === l.id);
        if (!asked) return l;
        const received = Number(l.receivedQty) + Number(asked.qty);
        const closed = asked.closeShort === true || received >= Number(l.qty);
        return { ...l, receivedQty: dec(received, 0), inTransit: dec(Math.max(0, Number(l.shippedQty) - received), 0), closed };
      });
      const done = lines.every((l) => l.closed);
      return transfers.update(t.id, { status: done ? "RECEIVED" : "PARTIALLY_RECEIVED", receiptDate: body.receiptDate ?? day(0), lines, version: t.version + 1 });
    }
    default:
      return reply.problem(404, "INV-0404", `Unknown action ${req.params.action}`);
  }
});

route("GET", `${BASE}/transfers/in-transit`, () => {
  const today = Date.parse(day(0));
  const items: InTransitItem[] = transfers
    .all()
    .filter((t) => t.useTransit && (t.status === "SHIPPED" || t.status === "PARTIALLY_RECEIVED"))
    .flatMap((t) =>
      t.lines
        .filter((l) => Number(l.inTransit) > 0)
        .map((l) => ({
          orderId: t.id,
          number: t.number,
          lineId: l.id,
          productId: l.productId,
          dimensions: l.dimensions,
          unit: l.unit,
          fromWarehouseId: t.fromWarehouseId,
          toWarehouseId: t.toWarehouseId,
          transitWarehouseId: t.transitWarehouseId ?? TRANSIT,
          shipDate: t.shipDate,
          daysInTransit: Math.max(0, Math.round((today - Date.parse(t.shipDate)) / 86_400_000)),
          inTransitQty: l.inTransit,
          closedShort: l.closed,
        })),
    )
    .sort((a, b) => a.shipDate.localeCompare(b.shipDate));
  return { items };
});
