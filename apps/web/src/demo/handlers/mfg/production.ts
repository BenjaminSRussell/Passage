// Production service (/api/v1/production) for the demo: orders, their status commands and the side panels of the detail page.
import type {
  AsBuiltRecord,
  CommandBody,
  CreateOrderInput,
  CostGroup,
  CostView,
  CreateJournalInput,
  EndCheck,
  Journal,
  JournalInquiryRow,
  JournalLine,
  MaterialItemRow,
  MaterialLineRow,
  OperationOverviewRow,
  OrderSummary,
  VarianceRow,
  VarianceType,
  WIPRow,
  ProductionCommand,
  ProductionOrder,
  ProductionStatus,
  StatusLogEntry,
  TriggerPoint,
} from "../../../modules/production/api/client";
import { reply, route } from "../../backend";
import { ITEMS, LEGAL_ENTITY, PLANT, RESOURCES, USER, WH_WIP } from "../../company";
import { at, day, dec, table } from "../../store";
import { SEED_ORDERS, actualShare, findItem, orderFromInput, plannedRows, plannedTotal, rank } from "./orders";

const BASE = "/api/v1/production";
export const orders = table<ProductionOrder>(SEED_ORDERS);
/** Status history per order, built from the seed status and extended by commands. */
const log = new Map<string, StatusLogEntry[]>();

const FLOW: { command: ProductionCommand; to: ProductionStatus }[] = [
  { command: "estimate", to: "Estimated" },
  { command: "schedule", to: "Scheduled" },
  { command: "release", to: "Released" },
  { command: "start", to: "Started" },
  { command: "report-finished", to: "ReportedAsFinished" },
  { command: "end", to: "Ended" },
];
const findItemSafe = (idOrNumber: string) => {
  try {
    return findItem(idOrNumber);
  } catch {
    return undefined;
  }
};
const PLANNERS = [USER.name, "Priya Natarajan", "Luis Ortega"];

function history(o: ProductionOrder): StatusLogEntry[] {
  const existing = log.get(o.id);
  if (existing) return existing;
  const entries: StatusLogEntry[] = [];
  let from: ProductionStatus = "Created";
  const steps = FLOW.filter((f) => rank(f.to) <= rank(o.status));
  steps.forEach((f, i) => {
    entries.push({
      command: f.command,
      from,
      to: f.to,
      actor: PLANNERS[i % PLANNERS.length],
      traceId: `4bf92f35${o.id.slice(3)}${String(i).padStart(2, "0")}`.padEnd(32, "0"),
      occurredAt: at(-(steps.length - i) * 1.3 - 0.2),
    });
    from = f.to;
  });
  log.set(o.id, entries);
  return entries;
}

function summary(o: ProductionOrder): OrderSummary {
  const share = actualShare(o);
  return {
    id: o.id,
    legalEntityId: o.legalEntityId,
    orderNumber: o.orderNumber,
    productId: o.productId,
    siteId: o.siteId,
    warehouseId: o.warehouseId,
    currency: o.currency,
    quantity: o.quantity,
    qtyRaf: o.qtyRaf,
    qtyError: o.qtyError,
    remaining: dec(Math.max(0, Number(o.quantity) - Number(o.qtyRaf)), 0),
    status: o.status,
    deliveryDate: o.deliveryDate,
    originType: o.originType,
    plannedCost: dec(plannedTotal(o)),
    actualCost: share > 0 ? dec(plannedTotal(o) * share) : undefined,
    version: o.version,
    createdAt: o.createdAt,
  };
}

function costs(o: ProductionOrder): CostView {
  const group = (rows: { group: CostGroup; amount: string }[], g: CostGroup, f = 1) => rows.filter((r) => r.group === g).reduce((n, r) => n + Number(r.amount) * f, 0);
  const share = actualShare(o);
  const planned = o.costs;
  const actualRows = planned.map((r) => ({ ...r, qty: dec(Number(r.qty) * share, 4), amount: dec(Number(r.amount) * share, 4) }));
  const totals: Partial<Record<CostGroup, string>> = {};
  const actualTotals: Partial<Record<CostGroup, string>> = {};
  for (const g of ["material", "labor", "overhead"] as CostGroup[]) {
    totals[g] = dec(group(planned, g));
    actualTotals[g] = dec(group(planned, g, share));
  }
  const total = plannedTotal(o);
  const ended = o.status === "Ended";
  const variance = ended ? total * 0.034 : 0;
  return {
    orderId: o.id,
    currency: o.currency,
    totals,
    total: dec(total),
    rows: planned,
    actualTotals: share > 0 ? actualTotals : {},
    actualTotal: dec(total * share),
    actualRows: share > 0 ? actualRows : [],
    wipBalance: dec(Math.max(0, wipOf(o).wip)),
    variances: ended ? variancesOf(o).map((v) => ({ type: v.type, costGroup: v.costGroup, amount: dec(variance * v.share) })) : [],
    varianceTotal: dec(variance),
  };
}

// Production journals: picking lists, route cards and reports as finished, seeded from the orders' own consumption,
// reported hours and finished quantities, so the journal inquiry, the WIP report and the order's cost tab add up.
const ACCOUNTS = { wip: "1410 Work in process", inventory: "1310 Raw materials and components", fg: "1350 Finished goods", labor: "5210 Labor absorbed", overhead: "5220 Overhead absorbed" };
let journalSeq = 0;
let entrySeq = 4810;

function postingsOf(type: Journal["type"], lines: JournalLine[]): Journal["postings"] {
  const total = (kinds: JournalLine["kind"][], f: (l: JournalLine) => number) => lines.filter((l) => kinds.includes(l.kind)).reduce((n, l) => n + f(l), 0);
  if (type === "report-as-finished") {
    const v = total(["receipt"], (l) => Number(l.amount));
    return [
      { postingType: "finished-goods", account: ACCOUNTS.fg, dr: dec(v), cr: "0.00", wip: false },
      { postingType: "wip-relief", account: ACCOUNTS.wip, dr: "0.00", cr: dec(v), wip: true },
    ];
  }
  const material = total(["issue"], (l) => Number(l.amount));
  const labor = total(["hours"], (l) => Number(l.amount));
  const overhead = total(["hours"], (l) => Number(l.overheadAmount));
  const out: Journal["postings"] = [];
  if (material > 0) {
    out.push({ postingType: "material-issue", account: ACCOUNTS.wip, dr: dec(material), cr: "0.00", wip: true });
    out.push({ postingType: "inventory", account: ACCOUNTS.inventory, dr: "0.00", cr: dec(material), wip: false });
  }
  if (labor > 0) {
    out.push({ postingType: "labor", account: ACCOUNTS.wip, dr: dec(labor + overhead), cr: "0.00", wip: true });
    out.push({ postingType: "labor-absorption", account: ACCOUNTS.labor, dr: "0.00", cr: dec(labor), wip: false });
    out.push({ postingType: "overhead-absorption", account: ACCOUNTS.overhead, dr: "0.00", cr: dec(overhead), wip: false });
  }
  return out;
}

export const journals = table<Journal>([]);

function makeJournal(o: ProductionOrder, type: Journal["type"], lines: JournalLine[], daysAgo: number, posted: boolean, qtyGood = 0, qtyError = 0): Journal {
  journalSeq += 1;
  const n = journals.all().filter((j) => j.orderId === o.id).length + 1;
  const by = PLANNERS[journalSeq % 3];
  return {
    id: `${o.id}-J${n}`,
    orderId: o.id,
    type,
    journalNumber: `PJ-${o.id.slice(3)}-${n}`,
    postingDate: at(-daysAgo).slice(0, 10),
    posted,
    postedAt: posted ? at(-daysAgo) : undefined,
    postedBy: posted ? by : undefined,
    qtyGood: dec(qtyGood, 0),
    qtyError: dec(qtyError, 0),
    journalEntryId: posted ? `JE-${String((entrySeq += 3)).padStart(6, "0")}` : undefined,
    createdBy: by,
    createdAt: at(-daysAgo - 0.02),
    lines,
    postings: posted ? postingsOf(type, lines) : [],
  };
}

const issueLines = (o: ProductionOrder, share = 1): JournalLine[] =>
  o.lines
    .filter((l) => Number(l.qtyConsumed ?? 0) > 0)
    .map((l) => {
      const qty = Number(l.qtyConsumed ?? 0) * share;
      return { kind: "issue" as const, lineNo: l.lineNo, itemId: l.itemId, qty: dec(qty, 4), qtyGood: "0", qtyError: "0", finished: false, unitCost: l.unitCost, amount: dec(qty * Number(l.unitCost), 4), overheadAmount: "0.0000" };
    });

const hoursLine = (op: ProductionOrder["operations"][number], hours: number, good: number, error: number, finished: boolean): JournalLine => ({
  kind: "hours",
  operNumber: op.operNumber,
  qty: dec(hours, 2),
  qtyGood: dec(good, 0),
  qtyError: dec(error, 0),
  finished,
  unitCost: op.laborRate,
  amount: dec(hours * Number(op.laborRate), 4),
  overheadAmount: dec(hours * Number(op.overheadRate), 4),
});

const unitCostOf = (o: ProductionOrder) => plannedTotal(o) / Math.max(1, Number(o.quantity));

const receiptLine = (o: ProductionOrder, good: number, error: number): JournalLine => ({
  kind: "receipt",
  itemId: o.productId,
  qty: dec(good, 4),
  qtyGood: dec(good, 0),
  qtyError: dec(error, 0),
  finished: true,
  unitCost: dec(unitCostOf(o), 4),
  amount: dec(good * unitCostOf(o), 4),
  overheadAmount: "0.0000",
});

/** The journals an order's progress implies: one picking list, a route card per reported operation, a report as finished. */
function seedJournals(o: ProductionOrder) {
  if (rank(o.status) < rank("Started")) return;
  const age = Math.max(2, Math.round((Date.now() - Date.parse(o.createdAt)) / 86_400_000) - 6);
  journals.insert(makeJournal(o, "picking-list", issueLines(o), age, true));
  o.operations.forEach((op, i) => {
    const hours = Number(op.hoursReported ?? 0);
    if (hours <= 0) return;
    const good = Number(op.qtyGood ?? 0);
    const error = Number(op.qtyError ?? 0);
    journals.insert(makeJournal(o, "route-card", [hoursLine(op, hours, good, error, op.status === "finished")], Math.max(0.3, age - 1 - i), true, good, error));
  });
  if (Number(o.qtyRaf) > 0) journals.insert(makeJournal(o, "report-as-finished", [receiptLine(o, Number(o.qtyRaf), 0)], Math.max(0.2, age - 4), true, Number(o.qtyRaf)));
}
for (const o of SEED_ORDERS) seedJournals(o);
// Drafts waiting to be posted: the test stand's hours on the running pump order and part of the impeller batch.
{
  const pump = SEED_ORDERS.find((o) => o.id === "MO-24611");
  const test = pump?.operations.find((op) => op.operNumber === 20);
  if (pump && test) journals.insert(makeJournal(pump, "route-card", [hoursLine(test, 6.5, 10, 1, false)], 0.1, false, 10, 1));
  const imp = SEED_ORDERS.find((o) => o.id === "MO-24618");
  if (imp) journals.insert(makeJournal(imp, "report-as-finished", [receiptLine(imp, 12, 0)], 0, false, 12));
}

const orderJournals = (orderId: string) => journals.all().filter((j) => j.orderId === orderId).sort((a, b) => a.createdAt.localeCompare(b.createdAt));
const lineValue = (j: Journal, kinds: JournalLine["kind"][]) => j.lines.filter((l) => kinds.includes(l.kind)).reduce((n, l) => n + Number(l.amount) + Number(l.overheadAmount), 0);

/** Posted consumption and receipts of an order, for the WIP report and the order's cost tab. */
function wipOf(o: ProductionOrder): { consumption: number; rafValue: number; settled: number; wip: number } {
  const posted = orderJournals(o.id).filter((j) => j.posted);
  const consumption = posted.reduce((n, j) => n + lineValue(j, ["issue", "hours"]), 0);
  const rafValue = posted.reduce((n, j) => n + lineValue(j, ["receipt", "coproduct"]), 0);
  const settled = o.status === "Ended" ? consumption - rafValue : 0;
  return { consumption, rafValue, settled, wip: consumption - rafValue - settled };
}

/** A draft journal from the journal form: the proposal fills the lines the way the service would. */
function draftJournal(o: ProductionOrder, input: CreateJournalInput): Journal | string {
  const qty = Number(o.quantity);
  switch (input.type) {
    case "picking-list": {
      const lines: JournalLine[] = input.issues?.length
        ? input.issues.map((i) => {
            const l = o.lines.find((x) => x.lineNo === i.lineNo);
            const unit = i.unitCost ?? l?.unitCost ?? "0";
            return { kind: "issue", lineNo: i.lineNo, itemId: l?.itemId, qty: dec(Number(i.quantity), 4), qtyGood: "0", qtyError: "0", finished: false, unitCost: unit, amount: dec(Number(i.quantity) * Number(unit), 4), overheadAmount: "0.0000" };
          })
        : input.proposal === "none"
          ? []
          : o.lines
              .map((l) => ({ l, open: Math.max(0, Number(l.qtyRequired) - Number(l.qtyConsumed ?? 0)) }))
              .filter((x) => x.open > 0)
              .map(({ l, open }) => ({ kind: "issue" as const, lineNo: l.lineNo, itemId: l.itemId, qty: dec(open, 4), qtyGood: "0", qtyError: "0", finished: false, unitCost: l.unitCost, amount: dec(open * Number(l.unitCost), 4), overheadAmount: "0.0000" }));
      if (lines.length === 0) return "Nothing is left to issue on this order.";
      return makeJournal(o, "picking-list", lines, 0, false);
    }
    case "route-card":
    case "job-card": {
      const lines = input.hours?.length
        ? input.hours.map((h) => {
            const op = o.operations.find((x) => x.operNumber === h.operNumber) ?? o.operations[0];
            return hoursLine(op, Number(h.hours), Number(h.qtyGood ?? 0), Number(h.qtyError ?? 0), Boolean(h.finished));
          })
        : o.operations
            .filter((op) => op.status !== "finished")
            .slice(0, 1)
            .map((op) => hoursLine(op, Math.max(0.5, Number(op.setupTimeH) + Number(op.runTimeHPer) * qty - Number(op.hoursReported ?? 0)), Math.max(0, qty - Number(op.qtyGood ?? 0)), 0, true));
      if (lines.length === 0) return "Every operation of this order is finished.";
      const good = lines.reduce((n, l) => n + Number(l.qtyGood), 0);
      return makeJournal(o, input.type, lines, 0, false, good);
    }
    case "report-as-finished": {
      const good = Number(input.quantity ?? Math.max(0, qty - Number(o.qtyRaf)));
      if (!(good > 0)) return "Enter a positive quantity.";
      return makeJournal(o, "report-as-finished", [receiptLine(o, good, Number(input.errorQuantity ?? 0))], 0, false, good, Number(input.errorQuantity ?? 0));
    }
    default:
      return "Choose a journal type.";
  }
}

function endCheck(o: ProductionOrder): EndCheck {
  const blockers = [] as EndCheck["blockers"];
  const warnings = [] as EndCheck["warnings"];
  if (o.status === "Started") blockers.push({ code: "PROD-0012", message: "The order has not been reported as finished.", count: 1 });
  if (o.status === "ReportedAsFinished" && o.id === "MO-24608") warnings.push({ code: "PROD-0021", message: "Material issued is 2.1 % above the BOM requirement.", count: 1 });
  return { orderId: o.id, canEnd: blockers.length === 0, blockers, warnings };
}

function apply(o: ProductionOrder, command: ProductionCommand, body: CommandBody): ProductionOrder | string {
  const step = FLOW.find((f) => f.command === command);
  const next = o.status;
  if (command === "cancel") return { ...o, status: "Cancelled", version: o.version + 1, updatedAt: at(0) };
  if (command === "reset-status") return { ...o, status: body.status ?? "Created", version: o.version + 1, updatedAt: at(0) };
  if (!step) return `Unknown command ${command}.`;
  if (command === "report-finished") {
    const q = Number(body.quantity ?? 0);
    const err = Number(body.errorQuantity ?? 0);
    if (q <= 0) return "Enter a positive quantity.";
    return { ...o, status: step.to, qtyRaf: dec(Number(o.qtyRaf) + q, 0), qtyError: dec(Number(o.qtyError) + err, 0), version: o.version + 1, updatedAt: at(0) };
  }
  const updated: ProductionOrder = { ...o, status: step.to, version: o.version + 1, updatedAt: at(0) };
  if (command === "schedule" && !updated.startDate) {
    updated.startDate = at(1).slice(0, 10);
    updated.endDate = o.deliveryDate;
    updated.schedulingMethod = body.method ?? "jobs";
    updated.scheduleBasis = "infinite";
  }
  if (command === "start") updated.operations = updated.operations.map((op, i) => ({ ...op, status: i === 0 ? "started" : "open", hoursReported: "0.00", qtyGood: "0", qtyError: "0" }));
  void next;
  return updated;
}

function transition(id: string, command: ProductionCommand, body: CommandBody) {
  const o = orders.get(id);
  if (!o) return reply.problem(404, "PROD-0404", `Production order ${id} was not found.`);
  const result = apply(o, command, body);
  if (typeof result === "string") return reply.problem(422, "PROD-0001", result, { quantity: result });
  const entries = history(o);
  entries.push({ command, from: o.status, to: result.status, actor: USER.name, traceId: crypto.randomUUID().replace(/-/g, ""), occurredAt: at(0) });
  // Reporting as finished from the order posts its journal, so the journals, WIP and cost views follow.
  if (command === "report-finished") {
    const good = Number(result.qtyRaf) - Number(o.qtyRaf);
    const j = makeJournal(o, "report-as-finished", [receiptLine(o, good, Number(body.errorQuantity ?? 0))], 0, true, good, Number(body.errorQuantity ?? 0));
    journals.insert({ ...j, postedBy: USER.name, createdBy: USER.name });
  }
  return reply.ok(orders.update(id, result));
}

route("GET", `${BASE}/orders`, (req) => {
  const status = req.query.get("status");
  const origin = req.query.get("originType");
  const reworkOf = req.query.get("reworkOf");
  let rows = orders.all();
  if (status) rows = rows.filter((o) => o.status === status);
  if (origin) rows = rows.filter((o) => o.originType === origin);
  if (reworkOf) rows = [];
  // Open orders first by delivery date, then the finished ones, newest first.
  const closed = (o: ProductionOrder) => o.status === "Ended" || o.status === "Cancelled";
  rows.sort((a, b) => Number(closed(a)) - Number(closed(b)) || (closed(a) ? -1 : 1) * (a.deliveryDate ?? "").localeCompare(b.deliveryDate ?? ""));
  return { items: rows.map(summary) };
});

route("POST", `${BASE}/orders`, (req) => {
  const input = req.body as CreateOrderInput;
  if (!findItemSafe(input.productId)) return reply.problem(422, "PROD-0001", "Choose a product.", { productId: "Choose a product." });
  if (!(Number(input.quantity) > 0)) return reply.problem(422, "PROD-0001", "Enter a positive quantity.", { quantity: "Enter a positive quantity." });
  const created = orders.insert(orderFromInput(input));
  return reply.created(created, { ETag: `"${created.version}"` });
});

route("POST", `${BASE}/orders:bulk-status`, (req) => {
  const { command, orderIds } = req.body as { command: ProductionCommand; orderIds: string[] };
  const results = orderIds.map((orderId) => {
    const res = transition(orderId, command, {});
    if (res.status === 200) {
      const o = res.body as ProductionOrder;
      return { orderId, ok: true, status: o.status, version: o.version };
    }
    return { orderId, ok: false, code: "PROD-0001", detail: (res.body as { detail: string }).detail };
  });
  const succeeded = results.filter((r) => r.ok).length;
  return { command, succeeded, failed: results.length - succeeded, results };
});

route("GET", `${BASE}/orders/{id}/status-log`, (req) => {
  const o = orders.get(req.params.id);
  return o ? { items: history(o) } : reply.problem(404, "PROD-0404", "Production order not found.");
});
route("GET", `${BASE}/orders/{id}/end-check`, (req) => {
  const o = orders.get(req.params.id);
  return o ? endCheck(o) : reply.problem(404, "PROD-0404", "Production order not found.");
});
route("GET", `${BASE}/orders/{id}/costs`, (req) => {
  const o = orders.get(req.params.id);
  return o ? costs(o) : reply.problem(404, "PROD-0404", "Production order not found.");
});
route("GET", `${BASE}/orders/{id}/journals`, (req) => {
  const o = orders.get(req.params.id);
  return o ? { items: orderJournals(o.id) } : reply.problem(404, "PROD-0404", "Production order not found.");
});
route("POST", `${BASE}/orders/{id}/journals`, (req) => {
  const o = orders.get(req.params.id);
  if (!o) return reply.problem(404, "PROD-0404", "Production order not found.");
  if (rank(o.status) < rank("Released")) return reply.problem(409, "PROD-0010", "Journals can be created once the order is released.");
  const draft = draftJournal(o, (req.body ?? {}) as CreateJournalInput);
  if (typeof draft === "string") return reply.problem(422, "PROD-0001", draft);
  return reply.created(journals.insert(draft));
});
route("POST", `${BASE}/journals/{id}:post`, (req) => {
  const j = journals.get(req.params.id);
  if (!j) return reply.problem(404, "PROD-0404", "Journal not found.");
  if (j.posted) return reply.ok(j);
  const posted = journals.update(j.id, { posted: true, postedAt: at(0), postedBy: USER.name, postingDate: day(0), journalEntryId: `JE-${String((entrySeq += 3)).padStart(6, "0")}`, postings: postingsOf(j.type, j.lines) })!;
  // A posted report as finished books the quantity on the order, as the service does.
  const o = orders.get(j.orderId);
  if (o && j.type === "report-as-finished") {
    const raf = Number(o.qtyRaf) + Number(j.qtyGood);
    orders.update(o.id, { qtyRaf: dec(raf, 0), status: raf >= Number(o.quantity) ? "ReportedAsFinished" : o.status, version: o.version + 1, updatedAt: at(0) });
  }
  return reply.ok(posted);
});
route("DELETE", `${BASE}/journals/{id}`, (req) => {
  const j = journals.get(req.params.id);
  if (!j) return reply.problem(404, "PROD-0404", "Journal not found.");
  if (j.posted) return reply.problem(409, "PROD-0011", "A posted journal cannot be deleted; reverse it instead.");
  journals.remove(j.id);
  return reply.noContent();
});

const resourceId = (code: string) => RESOURCES.find((r) => r.code === code)?.id ?? code;

/** Quality triggers on the route: balancing that finds scrap inserts a rework step, a failed test raises a rework order. */
function triggerPoints(o: ProductionOrder): TriggerPoint[] {
  const key = findItem(o.productId).number;
  const specs: Record<string, { oper: number; condition: string; fn: TriggerPoint["function"]; ops?: string[] }[]> = {
    "SA-IMP-06": [{ oper: 20, condition: "error_pct > 3", fn: "insert-operations", ops: ["MCH-01"] }],
    "CP-200": [
      { oper: 20, condition: "qty_error > 0", fn: "create-rework-order" },
      { oper: 30, condition: "hours > 6", fn: "notify" },
    ],
    "CP-400": [{ oper: 20, condition: "qty_error > 0", fn: "create-rework-order" }],
  };
  const started = rank(o.status) >= rank("Started");
  return (specs[key] ?? []).map((t, i) => {
    const fired = started && key === "SA-IMP-06";
    const id = `${o.id}-TP${i + 1}`;
    return {
      id,
      orderId: o.id,
      operNumber: t.oper,
      sequence: i + 1,
      condition: t.condition,
      event: "feedback" as const,
      function: t.fn,
      operations: (t.ops ?? []).map((c) => ({ resourceId: resourceId(c), setupTimeH: "0.25", runTimeHPer: "0.1000" })),
      repeatable: t.fn !== "create-rework-order",
      firedCount: fired ? 1 : 0,
      lastFiredAt: fired ? at(-3.2) : undefined,
      createdBy: PLANNERS[1],
      createdAt: at(-12),
      firings: fired
        ? [
            {
              id: `${id}-F1`,
              triggerPointId: id,
              event: "feedback" as const,
              journalId: null,
              operNumber: t.oper,
              function: t.fn,
              status: "fired" as const,
              feedback: { hours: "3.50", qtyGood: "22", qtyError: "2" },
              insertedOperations: [25],
              actor: PLANNERS[2],
              firedAt: at(-3.2),
            },
          ]
        : [],
    };
  });
}
route("GET", `${BASE}/orders/{id}/trigger-points`, (req) => {
  const o = orders.get(req.params.id);
  return { items: o ? triggerPoints(o) : [] };
});

/** As-built genealogy of the pumps: which motor and impeller serials or batches went into each unit. */
function asBuilt(o: ProductionOrder): AsBuiltRecord[] {
  const key = findItem(o.productId).number;
  if (rank(o.status) < rank("Started") || !(key === "CP-200" || key === "CP-400")) return [];
  const units = Math.min(3, Math.max(Number(o.qtyRaf), 2));
  const motor = ITEMS.find((i) => i.number === (key === "CP-400" ? "MTR-15HP-TEFC" : "SA-MTR-05"));
  const impeller = ITEMS.find((i) => i.number === "SA-IMP-06");
  const seal = ITEMS.find((i) => i.number === "SEAL-MK-125");
  const rows: AsBuiltRecord[] = [];
  for (let u = 1; u <= units; u++) {
    const serial = `${key}-${o.id.slice(3)}-${String(u).padStart(3, "0")}`;
    const mk = (n: number, item: typeof motor, tracking: AsBuiltRecord["tracking"], component: string, qty = "1"): AsBuiltRecord => ({
      id: `${o.id}-AB${u}${n}`,
      orderId: o.id,
      orderNumber: o.orderNumber,
      finishedItemId: o.productId,
      finishedSerialOrBatch: serial,
      componentItemId: item?.id ?? "",
      tracking,
      componentSerialOrBatch: component,
      qty,
      assignedBy: PLANNERS[(u + n) % 3],
      assignedAt: at(-2 + u * 0.05),
    });
    rows.push(mk(1, motor, "serial", `VM-${4400 + u * 3 + Number(o.id.slice(-2))}`));
    rows.push(mk(2, impeller, "batch", `IMP-B${o.id.slice(-3)}`));
    rows.push(mk(3, seal, "batch", "KS-7720"));
  }
  return rows;
}
route("GET", `${BASE}/orders/{id}/as-built`, (req) => {
  const o = orders.get(req.params.id);
  return { items: o ? asBuilt(o) : [] };
});
route("GET", `${BASE}/orders/{id}/bom-edits`, () => ({ items: [] }));
route("GET", `${BASE}/orders/{id}/jobs`, (req) => {
  const o = orders.get(req.params.id);
  if (!o || rank(o.status) < rank("Scheduled")) return { items: [] };
  return {
    items: o.operations.map((op, i) => ({
      id: `${o.id}-JOB${i + 1}`,
      orderId: o.id,
      orderNumber: o.orderNumber,
      orderStatus: o.status,
      legalEntityId: o.legalEntityId,
      siteId: o.siteId,
      productId: o.productId,
      operNumber: op.operNumber,
      seq: i + 1,
      jobType: "process",
      resourceId: op.resourceId,
      plannedStart: op.plannedStart ?? at(0),
      plannedEnd: op.plannedEnd ?? at(1),
      plannedHours: dec(Number(op.setupTimeH) + Number(op.runTimeHPer) * Number(o.quantity), 2),
      hoursReported: op.hoursReported ?? "0.00",
      qtyGood: op.qtyGood ?? "0",
      qtyError: op.qtyError ?? "0",
      status: op.status === "finished" ? "finished" : op.status === "started" ? "started" : "planned",
    })),
  };
});
route("GET", `${BASE}/orders/{id}`, (req) => {
  const o = orders.get(req.params.id);
  return o ? reply.versioned(o, o.version) : reply.problem(404, "PROD-0404", `Production order ${req.params.id} was not found.`);
});

for (const f of [...FLOW.map((s) => s.command), "cancel", "reset-status"] as ProductionCommand[]) {
  route("POST", `${BASE}/orders/{id}:${f}`, (req) => transition(req.params.id, f, (req.body ?? {}) as CommandBody));
}

route("POST", `${BASE}/orders/{id}/operations/{oper}:finish`, (req) => {
  const o = orders.get(req.params.id);
  if (!o) return reply.problem(404, "PROD-0404", "Production order not found.");
  const n = Number(req.params.oper);
  const ops = o.operations.map((op, i) => {
    if (op.operNumber === n) return { ...op, status: "finished" as const };
    const prev = o.operations[i - 1];
    return prev?.operNumber === n && op.status === "open" ? { ...op, status: "started" as const } : op;
  });
  return reply.ok(orders.update(o.id, { operations: ops, version: o.version + 1, costs: plannedRows(o) }));
});
route("POST", `${BASE}/orders/{id}/operations/{oper}:reopen`, (req) => {
  const o = orders.get(req.params.id);
  if (!o) return reply.problem(404, "PROD-0404", "Production order not found.");
  const n = Number(req.params.oper);
  return reply.ok(orders.update(o.id, { operations: o.operations.map((op) => (op.operNumber === n ? { ...op, status: "started" as const } : op)), version: o.version + 1 }));
});

// Planned orders that planning firmed and a planner has yet to convert into production orders.
interface Intake {
  id: string;
  legalEntityId: string | null;
  plannedOrderId: string;
  planVersionId: string;
  batchId: string;
  targetId: string;
  productId: string;
  siteId: string;
  warehouseId: string | null;
  quantity: string;
  orderDate: string;
  deliveryDate: string;
  quotaArrangementId: string | null;
  quotaSourceId: string | null;
  status: "open" | "converted" | "dismissed";
  orderId: string | null;
  dismissReason: string | null;
  version: number;
  receivedAt: string;
}
const intakeSpecs: { n: number; item: string; qty: number; due: number; status?: Intake["status"]; order?: string; reason?: string }[] = [
  { n: 30426, item: "CP-200", qty: 40, due: 12 },
  { n: 30428, item: "SA-IMP-06", qty: 60, due: 9 },
  { n: 30431, item: "BP-300", qty: 2, due: 21 },
  { n: 30422, item: "CP-200", qty: 30, due: 9, status: "converted", order: "MO-24621" },
  { n: 30409, item: "DP-100", qty: 25, due: 6, status: "dismissed", reason: "Dallas will buy these in; the line is full." },
];
const intakes = table<Intake>(
  intakeSpecs.map((s, i) => ({
    id: `PI-${s.n}`,
    legalEntityId: LEGAL_ENTITY.id,
    plannedOrderId: `PL-${s.n}`,
    planVersionId: "MAIN-3",
    batchId: `FIRM-${40 + i}`,
    targetId: s.order ?? `MO-${24640 + i}`,
    productId: findItem(s.item).id,
    siteId: PLANT.id,
    warehouseId: s.item === "BP-300" ? null : WH_WIP.id,
    quantity: dec(s.qty, 0),
    orderDate: day(s.due - 6),
    deliveryDate: day(s.due),
    quotaArrangementId: null,
    quotaSourceId: null,
    status: s.status ?? "open",
    orderId: s.order ?? null,
    dismissReason: s.reason ?? null,
    version: 1,
    receivedAt: at(-1 - i * 0.4),
  })),
);
route("GET", `${BASE.replace(/\/orders$/, "")}/planned-intake`, (req) => {
  const status = req.query.get("status");
  const rows = intakes.all().filter((r) => !status || r.status === status);
  return { items: rows.sort((a, b) => a.deliveryDate.localeCompare(b.deliveryDate)) };
});
route("GET", `${BASE.replace(/\/orders$/, "")}/planned-intake/{id}`, (req) => intakes.get(req.params.id) ?? reply.problem(404, "PROD-0404", "Planned order not found."));
route("POST", `${BASE.replace(/\/orders$/, "")}/planned-intake/{id}:dismiss`, (req) => {
  const row = intakes.get(req.params.id);
  if (!row) return reply.problem(404, "PROD-0404", "Planned order not found.");
  const reason = (req.body as { reason?: string } | undefined)?.reason ?? "Dismissed by the planner.";
  return reply.ok(intakes.update(row.id, { status: "dismissed", dismissReason: reason, version: row.version + 1 }));
});
route("POST", `${BASE.replace(/\/orders$/, "")}/planned-intake/{id}:convert`, (req) => {
  const row = intakes.get(req.params.id);
  if (!row) return reply.problem(404, "PROD-0404", "Planned order not found.");
  const created = orders.insert(orderFromInput({ ...(req.body as CreateOrderInput), productId: row.productId, siteId: row.siteId }));
  intakes.update(row.id, { status: "converted", orderId: created.id, version: row.version + 1 });
  return reply.created(created);
});

/** Used by the quality handlers to show what production order a finished-goods inspection came from. */
export const productionOrderNumbers = (): string[] => orders.all().map((o) => o.orderNumber);

// Inquiries across orders (4.21.14): journals, WIP, variances, operations and material requirements. Every row is
// computed from the orders and journals above, so the numbers agree with each order's own tabs.

/** How an ended order's overrun splits by variance type, as shares of the total variance. */
function variancesOf(o: ProductionOrder): { type: VarianceType; costGroup: CostGroup; share: number }[] {
  // The impeller batches lose castings to porosity and re-machine them; the 25-unit pump batch carried a full setup.
  if (findItem(o.productId).number === "SA-IMP-06") {
    return [
      { type: "production-quantity", costGroup: "material", share: 0.7 },
      { type: "production-quantity", costGroup: "labor", share: 0.3 },
    ];
  }
  if (o.id === "MO-24580") {
    return [
      { type: "lot-size", costGroup: "labor", share: 0.35 },
      { type: "production-quantity", costGroup: "material", share: 0.65 },
    ];
  }
  return [
    { type: "production-quantity", costGroup: "material", share: 0.55 },
    { type: "production-price", costGroup: "labor", share: 0.45 },
  ];
}

const page = <T>(rows: T[], query: URLSearchParams): { items: T[]; nextCursor?: string } => {
  const limit = Number(query.get("limit") ?? 50) || 50;
  const offset = Number(query.get("cursor") ?? 0) || 0;
  const next = offset + limit < rows.length ? String(offset + limit) : undefined;
  return { items: rows.slice(offset, offset + limit), nextCursor: next };
};
const plusDays = (date: string, n: number) => new Date(Date.parse(`${date}T00:00:00Z`) + n * 86_400_000).toISOString().slice(0, 10);
const plannedFor = (o: ProductionOrder, g: CostGroup) => o.costs.filter((r) => r.group === g).reduce((n, r) => n + Number(r.amount), 0);

route("GET", `${BASE}/journals`, (req) => {
  const q = req.query;
  const byId = new Map(orders.all().map((o) => [o.id, o]));
  let rows: JournalInquiryRow[] = journals.all().map((j) => {
    const o = byId.get(j.orderId);
    return {
      id: j.id,
      orderId: j.orderId,
      orderNumber: o?.orderNumber ?? j.orderId,
      legalEntityId: LEGAL_ENTITY.id,
      type: j.type,
      journalNumber: j.journalNumber,
      postingDate: j.postingDate,
      posted: j.posted,
      postedAt: j.postedAt ?? null,
      qtyGood: j.qtyGood,
      qtyError: j.qtyError,
      reversalOf: j.reversalOf ?? null,
      journalEntryId: j.journalEntryId ?? null,
      currency: o?.currency ?? "USD",
      lines: j.lines.length,
      consumed: dec(lineValue(j, ["issue", "hours"])),
      received: dec(lineValue(j, ["receipt", "coproduct"])),
    };
  });
  const type = q.get("type");
  const posted = q.get("posted");
  if (type) rows = rows.filter((r) => r.type === type);
  if (posted) rows = rows.filter((r) => String(r.posted) === posted);
  if (q.get("orderId")) rows = rows.filter((r) => r.orderId === q.get("orderId"));
  if (q.get("orderNumber")) rows = rows.filter((r) => r.orderNumber.toLowerCase() === q.get("orderNumber")!.toLowerCase());
  if (q.get("from")) rows = rows.filter((r) => r.postingDate >= q.get("from")!);
  if (q.get("to")) rows = rows.filter((r) => r.postingDate <= q.get("to")!);
  rows.sort((a, b) => Number(a.posted) - Number(b.posted) || b.postingDate.localeCompare(a.postingDate) || b.journalNumber.localeCompare(a.journalNumber));
  return { ...page(rows, q), total: rows.length };
});

route("GET", `${BASE}/wip-balances`, (req) => {
  const q = req.query;
  let list = orders.all().filter((o) => rank(o.status) >= rank("Started") && o.status !== "Cancelled");
  if (q.get("status")) list = list.filter((o) => o.status === q.get("status"));
  if (q.get("siteId")) list = list.filter((o) => o.siteId === q.get("siteId"));
  const rows: WIPRow[] = list
    .map((o) => {
      const w = wipOf(o);
      return {
        orderId: o.id,
        orderNumber: o.orderNumber,
        legalEntityId: o.legalEntityId,
        siteId: o.siteId,
        productId: o.productId,
        status: o.status,
        currency: o.currency,
        consumption: dec(w.consumption),
        rafValue: dec(w.rafValue),
        settled: dec(w.settled),
        wipBalance: dec(w.wip),
      };
    })
    .sort((a, b) => Number(b.wipBalance) - Number(a.wipBalance));
  const sum = (k: "consumption" | "rafValue" | "settled" | "wipBalance") => dec(rows.reduce((n, r) => n + Number(r[k]), 0));
  const totals = rows.length ? [{ currency: "USD", orders: rows.length, consumption: sum("consumption"), rafValue: sum("rafValue"), settled: sum("settled"), wipBalance: sum("wipBalance") }] : [];
  return { asOf: q.get("asOf") ?? day(0), ...page(rows, q), totals };
});

route("GET", `${BASE}/variances`, (req) => {
  const q = req.query;
  let rows: VarianceRow[] = orders
    .all()
    .filter((o) => o.status === "Ended")
    .flatMap((o) => {
      const total = plannedTotal(o) * (actualShare(o) - 1);
      const postedOn = o.deliveryDate ? plusDays(o.deliveryDate, 1) : day(0);
      return variancesOf(o).map((v, i) => {
        const amount = total * v.share;
        const planned = plannedFor(o, v.costGroup);
        return {
          id: `${o.id}-V${i + 1}`,
          orderId: o.id,
          orderNumber: o.orderNumber,
          siteId: o.siteId,
          itemGroup: o.itemGroup,
          currency: o.currency,
          type: v.type,
          costGroup: v.costGroup,
          amount: dec(amount),
          planned: dec(planned),
          pctOfPlanned: planned > 0 ? dec((amount / planned) * 100) : null,
          postedOn: postedOn > day(0) ? day(0) : postedOn,
        };
      });
    });
  if (q.get("varianceType")) rows = rows.filter((r) => r.type === q.get("varianceType"));
  if (q.get("siteId")) rows = rows.filter((r) => r.siteId === q.get("siteId"));
  if (q.get("itemGroup")) rows = rows.filter((r) => (r.itemGroup ?? "").toLowerCase() === q.get("itemGroup")!.toLowerCase());
  if (q.get("orderId")) rows = rows.filter((r) => r.orderId === q.get("orderId"));
  if (q.get("from")) rows = rows.filter((r) => r.postedOn >= q.get("from")!);
  if (q.get("to")) rows = rows.filter((r) => r.postedOn <= q.get("to")!);
  rows.sort((a, b) => b.postedOn.localeCompare(a.postedOn) || a.id.localeCompare(b.id));
  const types = [...new Set(rows.map((r) => r.type))];
  const totals = types.map((type) => ({ currency: "USD", type, amount: dec(rows.filter((r) => r.type === type).reduce((n, r) => n + Number(r.amount), 0)) }));
  const net = rows.length ? [{ currency: "USD", amount: dec(rows.reduce((n, r) => n + Number(r.amount), 0)) }] : [];
  return { ...page(rows, q), totals, net };
});

route("GET", `${BASE}/operations`, (req) => {
  const q = req.query;
  let rows: OperationOverviewRow[] = orders
    .all()
    .filter((o) => o.status !== "Cancelled")
    .flatMap((o) =>
      o.operations.map((op) => ({
        orderId: o.id,
        orderNumber: o.orderNumber,
        orderStatus: o.status,
        legalEntityId: o.legalEntityId,
        siteId: o.siteId,
        productId: o.productId,
        dueDate: op.plannedEnd?.slice(0, 10) ?? o.deliveryDate ?? null,
        operNumber: op.operNumber,
        resourceId: op.resourceId,
        subcontract: op.subcontract,
        processQty: o.quantity,
        plannedHours: dec(Number(op.setupTimeH) + Number(op.runTimeHPer) * Number(o.quantity), 2),
        hoursReported: op.hoursReported ?? "0.00",
        qtyGood: op.qtyGood ?? "0",
        qtyError: op.qtyError ?? "0",
        operStatus: op.status ?? (o.status === "Ended" || o.status === "ReportedAsFinished" ? "finished" : "open"),
      })),
    );
  if (q.get("status")) rows = rows.filter((r) => r.operStatus === q.get("status"));
  if (q.get("orderStatus")) rows = rows.filter((r) => r.orderStatus === q.get("orderStatus"));
  if (q.get("resourceId")) rows = rows.filter((r) => r.resourceId === q.get("resourceId"));
  if (q.get("siteId")) rows = rows.filter((r) => r.siteId === q.get("siteId"));
  if (q.get("orderId")) rows = rows.filter((r) => r.orderId === q.get("orderId"));
  if (q.get("dueFrom")) rows = rows.filter((r) => (r.dueDate ?? "") >= q.get("dueFrom")!);
  if (q.get("dueTo")) rows = rows.filter((r) => (r.dueDate ?? "9999") <= q.get("dueTo")!);
  const statusRank = { started: 0, open: 1, finished: 2 } as const;
  rows.sort((a, b) => statusRank[a.operStatus] - statusRank[b.operStatus] || (a.dueDate ?? "").localeCompare(b.dueDate ?? "") || a.orderNumber.localeCompare(b.orderNumber) || a.operNumber - b.operNumber);
  const count = (s: string) => rows.filter((r) => r.operStatus === s).length;
  const add = (k: "plannedHours" | "hoursReported" | "qtyGood" | "qtyError", places: number) => dec(rows.reduce((n, r) => n + Number(r[k]), 0), places);
  const totals = { operations: rows.length, open: count("open"), started: count("started"), finished: count("finished"), plannedHours: add("plannedHours", 2), hoursReported: add("hoursReported", 2), qtyGood: add("qtyGood", 0), qtyError: add("qtyError", 0) };
  return { ...page(rows, q), totals };
});

/** The BOM lines of the open orders with what is still to issue. */
function materialLines(q: URLSearchParams): MaterialLineRow[] {
  const open = (s: ProductionStatus) => rank(s) >= rank("Created") && rank(s) <= rank("Started");
  let rows: MaterialLineRow[] = orders
    .all()
    .filter((o) => (q.get("all") === "true" ? o.status !== "Cancelled" : open(o.status)))
    .flatMap((o) =>
      o.lines.map((l) => {
        const required = Number(l.qtyRequired);
        const issued = Number(l.qtyConsumed ?? 0);
        return {
          orderId: o.id,
          orderNumber: o.orderNumber,
          orderStatus: o.status,
          legalEntityId: o.legalEntityId,
          siteId: o.siteId,
          productId: o.productId,
          dueDate: o.deliveryDate ?? null,
          lineNumber: l.lineNo,
          itemId: l.itemId,
          qtyRequired: dec(required, 4),
          qtyIssued: dec(issued, 4),
          qtyOutstanding: dec(Math.max(0, required - issued), 4),
        };
      }),
    );
  if (q.get("all") !== "true") rows = rows.filter((r) => Number(r.qtyOutstanding) > 0);
  if (q.get("itemId")) rows = rows.filter((r) => r.itemId === q.get("itemId"));
  if (q.get("siteId")) rows = rows.filter((r) => r.siteId === q.get("siteId"));
  if (q.get("orderId")) rows = rows.filter((r) => r.orderId === q.get("orderId"));
  if (q.get("orderStatus")) rows = rows.filter((r) => r.orderStatus === q.get("orderStatus"));
  if (q.get("dueFrom")) rows = rows.filter((r) => (r.dueDate ?? "") >= q.get("dueFrom")!);
  if (q.get("dueTo")) rows = rows.filter((r) => (r.dueDate ?? "9999") <= q.get("dueTo")!);
  return rows.sort((a, b) => (a.dueDate ?? "").localeCompare(b.dueDate ?? "") || a.orderNumber.localeCompare(b.orderNumber) || Number(a.lineNumber) - Number(b.lineNumber));
}

route("GET", `${BASE}/material-requirements/by-item`, (req) => {
  const lines = materialLines(req.query);
  const byItem = new Map<string, MaterialLineRow[]>();
  for (const l of lines) byItem.set(l.itemId, [...(byItem.get(l.itemId) ?? []), l]);
  const rows: MaterialItemRow[] = [...byItem].map(([itemId, ls]) => {
    const sum = (k: "qtyRequired" | "qtyIssued" | "qtyOutstanding") => dec(ls.reduce((n, l) => n + Number(l[k]), 0), 4);
    const waiting = ls.filter((l) => Number(l.qtyOutstanding) > 0 && l.dueDate).map((l) => l.dueDate as string).sort();
    return { itemId, orders: new Set(ls.map((l) => l.orderId)).size, lines: ls.length, qtyRequired: sum("qtyRequired"), qtyIssued: sum("qtyIssued"), qtyOutstanding: sum("qtyOutstanding"), earliestDue: waiting[0] ?? null };
  });
  rows.sort((a, b) => (a.earliestDue ?? "9999").localeCompare(b.earliestDue ?? "9999") || Number(b.qtyOutstanding) - Number(a.qtyOutstanding));
  return page(rows, req.query);
});

route("GET", `${BASE}/material-requirements`, (req) => {
  const lines = materialLines(req.query);
  return { ...page(lines, req.query), totals: { lines: lines.length, items: new Set(lines.map((l) => l.itemId)).size, orders: new Set(lines.map((l) => l.orderId)).size } };
});
