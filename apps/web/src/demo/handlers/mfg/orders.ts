// The production orders of Cedar Ridge Pumps, shared by the production, scheduling and quality demo handlers.
// Order ids are the order numbers ("MO-24611" reads better in a link than a UUID); products, sites, warehouses and
// resources carry the real ids of company.ts, which the shell lookups turn into names.
import type { CreateOrderInput, Operation, OrderLine, ProductionOrder, ProductionStatus } from "../../../modules/production/api/client";
import { BOMS, ITEMS, LEGAL_ENTITY, PLANT, ROUTES, WH_WIP, itemByNumber, resourceByCode, type Item } from "../../company";
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

/** An item by id or number. */
export function findItem(idOrNumber: string): Item {
  return ITEMS.find((i) => i.id === idOrNumber) ?? itemByNumber(idOrNumber);
}

const ORDINAL: ProductionStatus[] = ["Created", "Estimated", "Scheduled", "Released", "Started", "ReportedAsFinished", "Ended"];
export const rank = (s: ProductionStatus) => ORDINAL.indexOf(s);

function build(s: Spec): ProductionOrder {
  const it = findItem(s.product);
  const key = it.number;
  const scheduled = rank(s.status) >= rank("Scheduled");
  const started = rank(s.status) >= rank("Started");
  const bom = BOMS[key] ?? [];
  const progress = s.status === "Started" ? Math.min(1, ((s.raf ?? 0) + s.qty * 0.35) / s.qty) : started ? 1 : 0;
  const lines: OrderLine[] = bom.map((b, i) => {
    const c = itemByNumber(b.component);
    const scrap = c.kind === "raw" ? 2 : 0;
    const required = b.qty * s.qty * (1 + scrap / 100);
    return {
      lineNo: String((i + 1) * 10),
      itemId: c.id,
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
  const route = ROUTES[key] ?? [];
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
      resourceId: resourceByCode(r.resource).id,
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
      op.qtyError = dec(op.status === "finished" && i === 0 && key === "SA-IMP-06" ? 2 : 0, 0);
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
    productId: it.id,
    siteId: PLANT.id,
    warehouseId: WH_WIP.id,
    currency: "USD",
    itemGroup: it.kind === "finished" ? "Finished pumps" : "Subassemblies",
    quantity: dec(s.qty, 0),
    qtyRaf: dec(s.raf ?? 0, 0),
    qtyError: dec(key === "SA-IMP-06" && started ? 2 : 0, 0),
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

/**
 * A Created order from the new-order form. Components and route come from the form; without components the
 * product's bill of material is exploded, as the service would for a released product.
 */
export function orderFromInput(input: CreateOrderInput): ProductionOrder {
  const it = findItem(input.productId);
  const qty = Number(input.quantity ?? "1") || 1;
  const due = input.deliveryDate ? Math.max(1, Math.round((Date.parse(input.deliveryDate) - Date.now()) / 86_400_000)) : 14;
  const order = build({ n: nextNumber++, product: it.id, qty, status: "Created", due, start: Math.max(1, due - 5), origin: "Manual" });
  order.siteId = input.siteId;
  order.warehouseId = input.warehouseId;
  order.currency = input.currency;
  order.itemGroup = input.itemGroup || order.itemGroup;
  order.deliveryDate = input.deliveryDate ?? order.deliveryDate;
  order.createdAt = at(0);
  order.updatedAt = at(0);
  if (input.bom.length > 0) {
    order.lines = input.bom.map((l, i) => {
      const c = findItem(l.itemId);
      const scrap = Number(l.scrapPct ?? "0");
      return {
        lineNo: l.lineNo || String((i + 1) * 10),
        itemId: c.id,
        qtyPer: dec(Number(l.qtyPer), 4),
        seriesSize: l.seriesSize ?? "1",
        scrapPct: dec(scrap),
        roundUp: Boolean(l.roundUp),
        qtyRequired: dec(Number(l.qtyPer) * qty * (1 + scrap / 100), 4),
        unitCost: dec(l.unitCost ? Number(l.unitCost) : c.cost, 4),
        flushing: l.flushing ?? "manual",
        qtyConsumed: "0.0000",
      };
    });
  }
  if (input.operations.length > 0) {
    const route = ROUTES[it.number] ?? [];
    order.operations = input.operations.map((o) => {
      const known = route.find((r) => resourceByCode(r.resource).id === o.resourceId);
      return {
        operNumber: o.operNumber,
        resourceId: o.resourceId,
        setupTimeH: dec(o.setupTimeH ? Number(o.setupTimeH) : (known?.setupHours ?? 0.5), 2),
        runTimeHPer: dec(o.runTimeHPer ? Number(o.runTimeHPer) : (known?.runHours ?? 0.5), 4),
        scrapPct: dec(Number(o.scrapPct ?? "0")),
        laborRate: dec(o.laborRate ? Number(o.laborRate) : LABOR_RATE),
        overheadRate: dec(o.overheadRate ? Number(o.overheadRate) : OVERHEAD_RATE),
        subcontract: false,
        subcontractPrice: "0.00",
        processQty: "1",
        queueBeforeH: dec(Number(o.queueBeforeH ?? "0"), 2),
        queueAfterH: dec(Number(o.queueAfterH ?? "0"), 2),
        transitH: dec(Number(o.transitH ?? "0"), 2),
      };
    });
  }
  order.costs = plannedRows(order);
  return order;
}
