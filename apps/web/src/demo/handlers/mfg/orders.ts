// The production orders of Cedar Ridge Pumps, shared by the production, scheduling and quality demo handlers.
// Ids are the order numbers: the production screens show ids raw, and "MO-24611" reads better than a UUID.
import type { Operation, OrderLine, ProductionOrder, ProductionStatus } from "../../../modules/production/api/client";
import { BOMS, LEGAL_ENTITY, PLANT, ROUTES, WH_WIP, itemByNumber } from "../../company";
import { at, day, dec } from "../../store";

export const LABOR_RATE = 38;
export const OVERHEAD_RATE = 24;

interface Spec {
  n: number;
  product: string;
  qty: number;
  status: ProductionStatus;
  /** Delivery date, days from today. */
  due: number;
  /** First start, days from today. */
  start: number;
  raf?: number;
  /** Operations finished so far (by operation index); the next one is started. */
  opsDone?: number;
  origin?: string;
}

const SPECS: Spec[] = [
  { n: 24618, product: "SA-IMP-06", qty: 60, status: "Started", due: -3, start: -9, raf: 24, opsDone: 1, origin: "Planned order" },
  { n: 24611, product: "CP-200", qty: 24, status: "Started", due: 3, start: -2, raf: 0, opsDone: 1, origin: "Sales order" },
  { n: 24605, product: "CP-400", qty: 4, status: "Released", due: -1, start: -1, origin: "Sales order" },
  { n: 24615, product: "CP-400", qty: 12, status: "Released", due: 6, start: 1, origin: "Planned order" },
  { n: 24627, product: "SA-MTR-05", qty: 30, status: "Released", due: 4, start: 0, origin: "Planned order" },
  { n: 24621, product: "CP-200", qty: 30, status: "Scheduled", due: 9, start: 3, origin: "Planned order" },
  { n: 24624, product: "SA-VOL-04", qty: 20, status: "Scheduled", due: 7, start: 2, origin: "Planned order" },
  { n: 24630, product: "CP-200", qty: 16, status: "Estimated", due: 14, start: 8, origin: "Manual" },
  { n: 24633, product: "CP-400", qty: 6, status: "Created", due: 20, start: 13, origin: "Sales order" },
  { n: 24608, product: "CP-200", qty: 40, status: "ReportedAsFinished", due: 0, start: -8, raf: 40, opsDone: 4, origin: "Sales order" },
  { n: 24592, product: "CP-400", qty: 8, status: "Ended", due: -6, start: -14, raf: 8, opsDone: 4, origin: "Sales order" },
  { n: 24580, product: "CP-200", qty: 25, status: "Ended", due: -12, start: -19, raf: 25, opsDone: 4, origin: "Planned order" },
  { n: 24575, product: "SA-IMP-06", qty: 40, status: "Ended", due: -15, start: -22, raf: 40, opsDone: 2, origin: "Planned order" },
];

const ORDINAL: ProductionStatus[] = ["Created", "Estimated", "Scheduled", "Released", "Started", "ReportedAsFinished", "Ended"];
export const rank = (s: ProductionStatus) => ORDINAL.indexOf(s);

function build(s: Spec): ProductionOrder {
  const it = itemByNumber(s.product);
  const scheduled = rank(s.status) >= rank("Scheduled");
  const started = rank(s.status) >= rank("Started");
  const bom = BOMS[s.product] ?? [];
  const progress = s.status === "Started" ? Math.min(1, ((s.raf ?? 0) + s.qty * 0.35) / s.qty) : started ? 1 : 0;
  const lines: OrderLine[] = bom.map((b, i) => {
    const c = itemByNumber(b.component);
    const scrap = c.kind === "raw" ? 2 : 0;
    const required = b.qty * s.qty * (1 + scrap / 100);
    return {
      lineNo: String((i + 1) * 10),
      itemId: c.number,
      qtyPer: dec(b.qty, 4),
      seriesSize: "1",
      scrapPct: dec(scrap),
      roundUp: false,
      qtyRequired: dec(required, 4),
      unitCost: dec(c.cost, 4),
      flushing: c.kind === "raw" ? "finish" : "start",
      qtyConsumed: dec(required * progress, 4),
    };
  });
  const route = ROUTES[s.product] ?? [];
  let cursor = Date.parse(at(s.start));
  const operations: Operation[] = route.map((r, i) => {
    const hours = r.setupHours + r.runHours * s.qty;
    const begin = new Date(cursor);
    const end = new Date(cursor + Math.max(hours, 2) * 3_600_000 * 1.6);
    cursor = end.getTime();
    const done = i < (s.opsDone ?? 0);
    const running = s.status === "Started" && i === (s.opsDone ?? 0);
    const op: Operation = {
      operNumber: r.op,
      resourceId: r.resource,
      setupTimeH: dec(r.setupHours, 2),
      runTimeHPer: dec(r.runHours, 4),
      scrapPct: "0.00",
      laborRate: dec(LABOR_RATE),
      overheadRate: dec(OVERHEAD_RATE),
      subcontract: false,
      subcontractPrice: "0.00",
      processQty: "1",
      queueBeforeH: "2.00",
      queueAfterH: "0.00",
      transitH: i === route.length - 1 ? "0.00" : "1.00",
    };
    if (started) {
      op.status = done || s.status !== "Started" ? "finished" : running ? "started" : "open";
      op.hoursReported = dec(op.status === "finished" ? hours * 1.04 : running ? hours * 0.4 : 0, 2);
      op.qtyGood = dec(op.status === "finished" ? s.qty : running ? (s.raf ?? 0) : 0, 0);
      op.qtyError = dec(op.status === "finished" && i === 0 && s.product === "SA-IMP-06" ? 2 : 0, 0);
    }
    if (scheduled) {
      op.plannedStart = begin.toISOString();
      op.plannedEnd = end.toISOString();
    }
    return op;
  });
  const order: ProductionOrder = {
    id: `MO-${s.n}`,
    legalEntityId: LEGAL_ENTITY.id,
    orderNumber: `MO-${s.n}`,
    productId: it.number,
    siteId: PLANT.code,
    warehouseId: WH_WIP.code,
    currency: "USD",
    itemGroup: it.kind === "finished" ? "Finished pumps" : "Subassemblies",
    quantity: dec(s.qty, 0),
    qtyRaf: dec(s.raf ?? 0, 0),
    qtyError: dec(s.product === "SA-IMP-06" && started ? 2 : 0, 0),
    status: s.status,
    deliveryDate: day(s.due),
    originType: s.origin ?? "Manual",
    version: 1 + rank(s.status),
    lines,
    operations,
    costs: [],
    createdAt: at(s.start - 6),
    updatedAt: at(Math.min(s.start + 1, 0)),
  };
  if (scheduled) {
    order.startDate = operations[0]?.plannedStart?.slice(0, 10) ?? day(s.start);
    order.endDate = operations.at(-1)?.plannedEnd?.slice(0, 10) ?? day(s.due);
    order.schedulingMethod = "jobs";
    order.scheduleBasis = "infinite";
  }
  order.costs = plannedRows(order);
  return order;
}

export function plannedRows(o: ProductionOrder): ProductionOrder["costs"] {
  const qty = Number(o.quantity);
  const rows: ProductionOrder["costs"] = [];
  for (const l of o.lines) rows.push({ group: "material", key: l.itemId, qty: dec(Number(l.qtyRequired), 4), amount: dec(Number(l.qtyRequired) * Number(l.unitCost), 4), fixed: "0.0000" });
  for (const op of o.operations) {
    const hours = Number(op.setupTimeH) + Number(op.runTimeHPer) * qty;
    rows.push({ group: "labor", key: `${op.operNumber} ${op.resourceId}`, qty: dec(hours, 4), amount: dec(hours * Number(op.laborRate), 4), fixed: dec(Number(op.setupTimeH) * Number(op.laborRate), 4) });
    rows.push({ group: "overhead", key: `${op.operNumber} ${op.resourceId}`, qty: dec(hours, 4), amount: dec(hours * Number(op.overheadRate), 4), fixed: dec(Number(op.setupTimeH) * Number(op.overheadRate), 4) });
  }
  return rows;
}

export const plannedTotal = (o: ProductionOrder): number => o.costs.reduce((n, r) => n + Number(r.amount), 0);

/** Share of the planned cost booked as actual: none before Started, all (and a little over) at the end. */
export function actualShare(o: ProductionOrder): number {
  switch (o.status) {
    case "Started":
      return Math.min(0.9, 0.25 + (Number(o.qtyRaf) / Number(o.quantity)) * 0.6);
    case "ReportedAsFinished":
      return 0.98;
    case "Ended":
      return 1.034;
    default:
      return 0;
  }
}

export const SEED_ORDERS: ProductionOrder[] = SPECS.map(build);

let nextNumber = 24640;
/** A new Created production order, for a planned order that was firmed in the demo. */
export function newOrder(product: string, qty: number, dueInDays: number): ProductionOrder {
  return build({ n: nextNumber++, product, qty, status: "Created", due: dueInDays, start: Math.max(1, dueInDays - 5), origin: "Planned order" });
}
