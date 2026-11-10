// Production service (/api/v1/production) for the demo: orders, their status commands and the side panels of the detail page.
import type {
  AsBuiltRecord,
  CommandBody,
  CreateOrderInput,
  CostGroup,
  CostView,
  EndCheck,
  Journal,
  OrderSummary,
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
    wipBalance: dec(share > 0 && !ended ? total * share : 0),
    variances: ended
      ? [
          { type: "production-quantity", costGroup: "material", amount: dec(variance * 0.55) },
          { type: "production-price", costGroup: "labor", amount: dec(variance * 0.45) },
        ]
      : [],
    varianceTotal: dec(variance),
  };
}

function journals(o: ProductionOrder): Journal[] {
  if (rank(o.status) < rank("Started")) return [];
  const mk = (n: number, type: Journal["type"], daysAgo: number, qtyGood: number): Journal => ({
    id: `${o.id}-J${n}`,
    orderId: o.id,
    type,
    journalNumber: `PJ-${o.id.slice(3)}-${n}`,
    postingDate: at(-daysAgo).slice(0, 10),
    posted: true,
    postedAt: at(-daysAgo),
    postedBy: PLANNERS[n % 3],
    qtyGood: dec(qtyGood, 0),
    qtyError: "0",
    createdBy: PLANNERS[n % 3],
    createdAt: at(-daysAgo - 0.01),
    lines:
      type === "picking-list"
        ? o.lines.slice(0, 4).map((l) => ({
            kind: "issue" as const,
            lineNo: l.lineNo,
            itemId: l.itemId,
            qty: l.qtyConsumed ?? l.qtyRequired,
            qtyGood: "0",
            qtyError: "0",
            finished: false,
            unitCost: l.unitCost,
            amount: dec(Number(l.qtyConsumed ?? 0) * Number(l.unitCost), 4),
            overheadAmount: "0.0000",
          }))
        : [],
    postings: [],
  });
  const out = [mk(1, "picking-list", 4, 0)];
  if (Number(o.qtyRaf) > 0) out.push(mk(2, "report-as-finished", 1, Number(o.qtyRaf)));
  return out;
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
  return o ? { items: journals(o) } : reply.problem(404, "PROD-0404", "Production order not found.");
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
