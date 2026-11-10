// Production service (/api/v1/production) for the demo: orders, their status commands and the side panels of the detail page.
import type {
  CommandBody,
  CostGroup,
  CostView,
  EndCheck,
  Journal,
  OrderSummary,
  ProductionCommand,
  ProductionOrder,
  ProductionStatus,
  StatusLogEntry,
} from "../../../modules/production/api/client";
import { reply, route } from "../../backend";
import { USER } from "../../company";
import { at, dec, table } from "../../store";
import { SEED_ORDERS, actualShare, plannedRows, plannedTotal, rank } from "./orders";

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
route("GET", `${BASE}/orders/{id}/trigger-points`, () => ({ items: [] }));
route("GET", `${BASE}/orders/{id}/as-built`, () => ({ items: [] }));
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

/** Used by the quality handlers to show what production order a finished-goods inspection came from. */
export const productionOrderNumbers = (): string[] => orders.all().map((o) => o.orderNumber);
