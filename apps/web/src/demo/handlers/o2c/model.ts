// Shared order to cash model for the demo: pricing conditions, sales orders, and the shipments, packing slips and
// invoices that follow from them. The sales, fulfillment, billing and pricing handlers all read these tables, so a
// shipped order in the sales list has its shipment and its invoice, and prices on order lines match the price list.
import type { ConditionRecord } from "../../../modules/pricing/api/client";
import type { Container, PackingSlip, Shipment, ShipmentLine, ShipmentStatus } from "../../../modules/fulfillment/api/client";
import type { Invoice, InvoiceLine } from "../../../modules/billing/api/client";
import type { CreditEvaluation, CreditProfile, Hold, LimitType, Order, OrderLine, OrderProgress, OrderStatus, OrderVersion, ScheduleLine } from "../../../modules/sales/api/client";
import { CUSTOMERS, ITEMS, LEGAL_ENTITY, PLANT, WH_FG, itemByNumber, type Party } from "../../company";
import { at, day, dec, table, uid } from "../../store";

// uid() kinds claimed by this area (100-102 are the sales order, line and hold ids).
export const K = { order: 100, line: 101, hold: 102, schedule: 103, progress: 104, shipment: 105, shipLine: 106, container: 107, slip: 108, invoice: 109, condition: 110, change: 111, quotation: 112 } as const;

export const r2 = (n: number): number => Math.round(n * 100) / 100;
export const customerById = (id: string): Party | undefined => CUSTOMERS.find((c) => c.id === id);
export const customerByKey = (key: string): Party | undefined => CUSTOMERS.find((c) => c.id === key || c.number === key);
export const itemById = (id: string) => ITEMS.find((i) => i.id === id);
export const itemByKey = (key: string) => ITEMS.find((i) => i.id === key || i.number === key);
const termDays = (c: Party): number => Number(/\d+/.exec(c.paymentTerms.replace(/^2%\s*10\s*/, ""))?.[0] ?? 30);

// ---- Pricing conditions ----

let condSeq = 0;
function cond(partial: Partial<ConditionRecord> & Pick<ConditionRecord, "type">): ConditionRecord {
  const n = ++condSeq;
  return {
    id: `PRC-${String(n).padStart(4, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    customerKind: "all",
    customerKey: "",
    productKind: "product",
    productKey: "",
    variant: "",
    currency: "USD",
    unit: "ea",
    fromQuantity: "1",
    amount: "0.00",
    discPct1: "0",
    discPct2: "0",
    validFrom: day(-120),
    validTo: day(245),
    searchNext: false,
    exclusive: false,
    status: "ACTIVE",
    version: 1,
    ...partial,
  };
}
const priceOf = (number: string) => dec(itemByNumber(number).price ?? 0);
const listPrice = (number: string, extra: Partial<ConditionRecord> = {}) => cond({ type: "PRICE", productKey: number, amount: priceOf(number), unit: itemByNumber(number).unit, ...extra });
const forCustomer = (index: number): Partial<ConditionRecord> => ({ customerKind: "customer", customerKey: CUSTOMERS[index].id });

export const conditions = table<ConditionRecord>([
  listPrice("CP-200"),
  listPrice("CP-400"),
  listPrice("SP-150"),
  listPrice("DP-100"),
  listPrice("BP-300"),
  listPrice("SEAL-MK-125"),
  listPrice("BRG-6206"),
  listPrice("CP-200", { fromQuantity: "10", amount: "2380.00", searchNext: true }),
  listPrice("CP-200", { fromQuantity: "25", amount: "2310.00", searchNext: true }),
  listPrice("SP-150", { fromQuantity: "50", amount: "1225.00", searchNext: true }),
  listPrice("CP-200", { ...forCustomer(0), amount: "2390.00", validTo: day(200) }),
  listPrice("CP-400", { ...forCustomer(2), amount: "5650.00" }),
  listPrice("BP-300", { ...forCustomer(3), amount: "18200.00", validFrom: day(-60), validTo: day(305) }),
  listPrice("CP-400", { ...forCustomer(3), amount: "5750.00" }),
  listPrice("SP-150", { ...forCustomer(4), fromQuantity: "20", amount: "1240.00" }),
  cond({ type: "LINE_DISC", ...forCustomer(1), productKey: "SP-150", fromQuantity: "25", discPct1: "5", validTo: day(150) }),
  cond({ type: "TOTAL_DISC", ...forCustomer(2), productKind: "all", unit: "", discPct1: "2", validFrom: day(20), validTo: day(385) }),
  cond({ type: "LINE_DISC", productKind: "all", unit: "", fromQuantity: "20", discPct1: "3", status: "DRAFT", validFrom: day(10) }),
  cond({ type: "PRICE", productKey: "CP-400", amount: "6150.00", validFrom: day(45), validTo: day(410), status: "DRAFT" }),
  cond({ type: "PRICE", productKey: "SEAL-MK-125", amount: "119.00", validFrom: day(-400), validTo: day(-41), status: "EXPIRED" }),
  cond({ type: "SURCHARGE", productKind: "all", unit: "", fromQuantity: "0", amount: "45.00", status: "WITHDRAWN", validTo: day(60) }),
]);

const inWindow = (r: ConditionRecord, date: string) => r.status === "ACTIVE" && r.validFrom <= date && r.validTo >= date;
const matches = (r: ConditionRecord, c: Party, number: string, qty: number) =>
  (r.customerKind === "all" || r.customerKey === c.number || r.customerKey === c.id) && (r.productKind === "all" || r.productKey === number) && Number(r.fromQuantity) <= qty;

/** The list price and line discount the active records give a customer for a quantity of an item. */
export function priceFor(c: Party, number: string, qty: number, date = day(0)): { unitPrice: number; discPct: number; priceRecord?: ConditionRecord; discRecord?: ConditionRecord } {
  const all = conditions.all().filter((r) => inWindow(r, date) && matches(r, c, number, qty));
  const prices = all.filter((r) => r.type === "PRICE").sort((a, b) => Number(r2sort(b, a)));
  const priceRecord = prices[0];
  const discRecord = all.filter((r) => r.type === "LINE_DISC").sort((a, b) => Number(b.fromQuantity) - Number(a.fromQuantity))[0];
  const fallback = itemByNumber(number);
  return { unitPrice: priceRecord ? Number(priceRecord.amount) : (fallback.price ?? fallback.cost * 2), discPct: discRecord ? Number(discRecord.discPct1) : 0, priceRecord, discRecord };
}
/** Orders price records: the customer's own first, then the highest quantity break. */
function r2sort(a: ConditionRecord, b: ConditionRecord): number {
  const own = (r: ConditionRecord) => (r.customerKind === "all" ? 0 : 1);
  return own(a) - own(b) || Number(a.fromQuantity) - Number(b.fromQuantity);
}

// ---- Sales orders ----

const NOW_SALES_ENTRY = "jordan.avery";
let lineSeq = 0;
function line(customer: number, lineNo: number, number: string, qty: number, shipIn: number, status: string, delivered = 0): OrderLine {
  const it = itemByNumber(number);
  const { unitPrice, discPct } = priceFor(CUSTOMERS[customer], number, qty);
  const net = r2(unitPrice * qty * (1 - discPct / 100));
  const confirmed = status !== "OPEN";
  return {
    id: uid(K.line, ++lineSeq),
    lineNo,
    productId: it.id,
    quantity: dec(qty, 0),
    unit: it.unit,
    siteId: PLANT.id,
    warehouseId: WH_FG.id,
    unitPrice: dec(unitPrice),
    discPct: dec(discPct),
    lineNet: dec(net),
    requestedShipDate: day(shipIn),
    confirmedShipDate: confirmed ? day(shipIn) : undefined,
    confirmedReceiptDate: confirmed ? day(shipIn + 3) : undefined,
    lineStatus: status,
    deliveredQty: dec(delivered, 0),
    invoicedQty: status === "INVOICED" ? dec(qty, 0) : "0",
  };
}

export const orderNumber = (n: number): string => `SO-${String(24180 + n).padStart(6, "0")}`;
export const orderSeq = (o: Pick<Order, "orderNumber">): number => Number(o.orderNumber.slice(3)) - 24180;

function order(n: number, customer: number, status: OrderStatus, createdDaysAgo: number, lines: OrderLine[], extra: Partial<Order> = {}): Order {
  const net = lines.reduce((sum, l) => sum + Number(l.lineNet), 0);
  return {
    id: uid(K.order, n),
    legalEntityId: LEGAL_ENTITY.id,
    orderNumber: orderNumber(n),
    customerId: CUSTOMERS[customer].id,
    invoiceAccountId: CUSTOMERS[customer].id,
    orderType: "ORDER",
    status,
    currency: "USD",
    requestedShipDate: lines[0]?.requestedShipDate ?? day(7),
    channel: n % 3 === 0 ? "EDI" : "MANUAL",
    externalRef: n % 3 === 0 ? `PO-${88000 + n * 37}` : undefined,
    netTotal: dec(net),
    version: status === "DRAFT" || status === "OPEN" ? 1 : 2,
    lines,
    holds: [],
    createdAt: at(-createdDaysAgo),
    updatedAt: at(-createdDaysAgo / 2),
    ...extra,
  };
}

const seedOrders: Order[] = [
  order(1, 0, "CONFIRMED", 6, [line(0, 1, "CP-200", 12, 4, "CONFIRMED"), line(0, 2, "SEAL-MK-125", 24, 4, "CONFIRMED")], { version: 3 }),
  order(2, 1, "OPEN", 1, [line(1, 1, "SP-150", 30, 9, "OPEN")]),
  order(3, 2, "PARTIALLY_SHIPPED", 12, [line(2, 1, "CP-400", 6, -2, "PICKED", 4), line(2, 2, "BRG-6206", 40, -2, "SHIPPED", 40)]),
  order(4, 3, "CONFIRMED", 3, [line(3, 1, "BP-300", 2, 18, "CONFIRMED"), line(3, 2, "CP-400", 4, 18, "CONFIRMED")], { version: 3 }),
  order(5, 4, "OPEN", 0.5, [line(4, 1, "SP-150", 50, 12, "OPEN"), line(4, 2, "SEAL-MK-125", 50, 12, "OPEN")]),
  order(6, 5, "SHIPPED", 20, [line(5, 1, "DP-100", 8, -9, "SHIPPED", 8)]),
  order(7, 0, "INVOICED", 34, [line(0, 1, "CP-200", 20, -25, "INVOICED", 20)]),
  order(8, 2, "CONFIRMED", 2, [line(2, 1, "CP-400", 3, 3, "CONFIRMED"), line(2, 2, "SA-IMP-06", 6, 3, "CONFIRMED")]),
  order(9, 1, "DRAFT", 0.2, [line(1, 1, "CP-200", 4, 14, "OPEN")]),
  order(10, 3, "CLOSED", 60, [line(3, 1, "CP-400", 10, -48, "INVOICED", 10)]),
  order(11, 1, "INVOICED", 44, [line(1, 1, "SP-150", 40, -37, "INVOICED", 40)]),
  order(12, 4, "INVOICED", 52, [line(4, 1, "SP-150", 25, -44, "INVOICED", 25), line(4, 2, "SEAL-MK-125", 20, -44, "INVOICED", 20)]),
  order(13, 2, "CLOSED", 82, [line(2, 1, "CP-200", 16, -73, "INVOICED", 16), line(2, 2, "BRG-6206", 80, -73, "INVOICED", 80)]),
  order(14, 5, "CLOSED", 96, [line(5, 1, "DP-100", 12, -88, "INVOICED", 12)]),
  order(15, 0, "CANCELLED", 14, [line(0, 1, "CP-400", 2, 20, "CANCELLED")]),
  order(16, 1, "CONFIRMED", 4, [line(1, 1, "SP-150", 70, 2, "CONFIRMED")]),
];

/** Orders by id, newest first when listed. */
export const orders = table<Order>(seedOrders);

/** Order change history, newest first. Orders confirmed in the seed get an entry and a creation entry. */
export const versions = new Map<string, OrderVersion[]>();
function recordVersion(o: Order, reason: string, diff: OrderVersion["diff"], actor = NOW_SALES_ENTRY, when = at(0)): void {
  const list = versions.get(o.id) ?? [];
  list.unshift({ version: o.version, actor, reason, diff, changedAt: when });
  versions.set(o.id, list);
}
for (const o of seedOrders) {
  const created = new Date(o.createdAt).getTime();
  const iso = (offsetDays: number) => new Date(created + offsetDays * 86_400_000).toISOString();
  versions.set(o.id, [{ version: 1, actor: o.channel === "EDI" ? "edi-intake" : NOW_SALES_ENTRY, reason: "Order entered", diff: [], changedAt: o.createdAt }]);
  if (o.version >= 2) {
    const cancelled = o.status === "CANCELLED";
    const reason = cancelled ? "Cancelled at the customer's request" : o.status === "CLOSED" || o.status === "INVOICED" ? "Confirmed" : "Confirmed after credit check";
    const list = versions.get(o.id)!;
    list.unshift({ version: 2, actor: NOW_SALES_ENTRY, reason, diff: [{ lineNo: 0, field: "status", old: cancelled ? "CONFIRMED" : "OPEN", new: cancelled ? "CANCELLED" : "CONFIRMED" }], changedAt: iso(0.2) });
  }
  if (o.version >= 3) {
    const list = versions.get(o.id)!;
    const earlier = new Date(Date.parse(o.lines[0].requestedShipDate) - 5 * 86_400_000).toISOString().slice(0, 10);
    list.unshift({
      version: 3,
      actor: "dana.whitfield",
      reason: "Customer asked for a later ship date",
      diff: [{ lineNo: 1, field: "requestedShipDate", old: earlier, new: o.lines[0].requestedShipDate }],
      changedAt: iso(1.5),
    });
  }
}
export { recordVersion };

/** One schedule line per open order line, dated by the confirmed ship date. */
export function scheduleLines(o: Order): ScheduleLine[] {
  return o.lines
    .filter((l) => l.lineStatus !== "CANCELLED" && l.lineStatus !== "OPEN")
    .flatMap((l): ScheduleLine[] => {
      const base = { lineNo: l.lineNo, deliveryDate: l.confirmedShipDate ?? l.requestedShipDate, status: "CONFIRMED" as const, source: "LINE" as const };
      const id = (seq: number) => uid(K.schedule, orderSeq(o) * 1000 + l.lineNo * 10 + seq);
      // Booster skids are built to order: only one unit is in stock, the second is promised two weeks late.
      if (o.status === "CONFIRMED" && itemById(l.productId)?.number === "BP-300" && Number(l.quantity) >= 2) {
        const late = new Date(Date.parse(l.requestedShipDate) + 14 * 86_400_000).toISOString().slice(0, 10);
        return [
          { ...base, id: id(1), seq: 1, quantity: "1" },
          { ...base, id: id(2), seq: 2, quantity: String(Number(l.quantity) - 1), deliveryDate: late, status: "BACKORDER", source: "ATP", bucketDate: late },
        ];
      }
      return [{ ...base, id: id(1), seq: 1, quantity: l.quantity }];
    });
}

// ---- Credit ----

const LIMIT_TYPES: LimitType[] = ["BALANCE_ALL", "BALANCE_ALL", "BALANCE_PACKING_SLIP", "BALANCE_ALL", "BALANCE_ALL", "BALANCE_ALL"];
const UNAPPLIED = [0, 0, 4200, 0, 0, 1850];
const lineNetPerUnit = (l: OrderLine) => Number(l.lineNet) / Number(l.quantity);

// Invoices are built below; the exposure functions read them lazily.
export interface InvoiceRow extends Invoice {
  /** Share of the gross already settled. */
  settled: number;
  orderId?: string;
}
export const invoices = table<InvoiceRow>([]);

const invoiceOpen = (inv: InvoiceRow): number => (inv.status === "POSTED" || inv.status === "PARTIALLY_SETTLED" ? r2(Number(inv.totals.gross) * (1 - inv.settled)) : 0);

export interface Exposure {
  openAr: number;
  unapplied: number;
  deliveredNotInvoiced: number;
  openOrders: number;
  overdueAr: number;
  oldestItemDays: number;
  dunningLevel: number;
}
export function exposureOf(customerId: string): Exposure {
  const index = CUSTOMERS.findIndex((c) => c.id === customerId);
  const mine = invoices.all().filter((i) => i.customerId === customerId);
  const open = mine.filter((i) => invoiceOpen(i) > 0);
  const overdue = open.filter((i) => i.dueDate < day(0));
  let dni = 0;
  let openOrders = 0;
  for (const o of orders.all().filter((x) => x.customerId === customerId)) {
    for (const l of o.lines) {
      if (l.lineStatus === "CANCELLED") continue;
      const delivered = Number(l.deliveredQty ?? 0);
      const invoiced = Number(l.invoicedQty ?? 0);
      dni += Math.max(0, delivered - invoiced) * lineNetPerUnit(l);
      if (o.status === "CONFIRMED" || o.status === "PARTIALLY_SHIPPED") openOrders += Math.max(0, Number(l.quantity) - delivered) * lineNetPerUnit(l);
    }
  }
  const today = Date.parse(day(0));
  return {
    openAr: r2(open.reduce((s, i) => s + invoiceOpen(i), 0)),
    unapplied: UNAPPLIED[index] ?? 0,
    deliveredNotInvoiced: r2(dni),
    openOrders: r2(openOrders),
    overdueAr: r2(overdue.reduce((s, i) => s + invoiceOpen(i), 0)),
    oldestItemDays: Math.max(0, ...open.map((i) => Math.round((today - Date.parse(i.invoiceDate)) / 86_400_000))),
    dunningLevel: overdue.length > 0 ? 1 : 0,
  };
}

export const creditProfiles = CUSTOMERS.map((c, i): Omit<CreditProfile, "exposure"> => ({
  legalEntityId: LEGAL_ENTITY.id,
  customerId: c.id,
  limit: dec(c.creditLimit ?? 0),
  currency: "USD",
  limitType: LIMIT_TYPES[i],
  tolerancePct: "5.00",
  toleranceAmount: "0.00",
  oldestItemDaysMax: 60,
  maxDunningLevel: 2,
  reviewDate: day(40 + i * 23),
  customerSince: day(-(400 + i * 310)),
  mandatoryCheck: true,
  version: 1,
  updatedAt: at(-30 - i * 7),
}));

export const balanceOf = (type: LimitType, e: Exposure): number => {
  const ar = e.openAr - e.unapplied;
  if (type === "NONE") return 0;
  if (type === "BALANCE") return r2(ar);
  if (type === "BALANCE_PACKING_SLIP") return r2(ar + e.deliveredNotInvoiced);
  return r2(ar + e.deliveredNotInvoiced + e.openOrders);
};

/** The credit rules for an order that is about to be confirmed: CR-1 exposure with the order, CR-2 oldest item, CR-3 dunning, CR-5 overdue share. */
export function evaluateCredit(o: Order): CreditEvaluation {
  const profile = creditProfiles.find((p) => p.customerId === o.customerId);
  const e = exposureOf(o.customerId);
  const counted = o.status === "CONFIRMED" || o.status === "PARTIALLY_SHIPPED";
  const net = Number(o.netTotal);
  const withOrder = balanceOf(profile?.limitType ?? "BALANCE_ALL", e) + (counted ? 0 : net);
  const limit = Number(profile?.limit ?? 0);
  const threshold = r2(limit * (1 + Number(profile?.tolerancePct ?? 0) / 100) + Number(profile?.toleranceAmount ?? 0));
  const cr1 = limit === 0 || withOrder <= threshold ? "PASS" : "FAIL";
  const maxDays = profile?.oldestItemDaysMax ?? 60;
  const share = e.openAr > 0 ? r2((e.overdueAr / e.openAr) * 100) : 0;
  const rules: CreditEvaluation["rules"] = [
    { id: "CR-1", result: cr1, value: dec(withOrder), threshold: dec(threshold), note: cr1 === "FAIL" ? "Open AR, delivered goods, open orders and this order exceed the limit with its tolerance" : "Exposure with this order is within the limit" },
    { id: "CR-2", result: e.oldestItemDays > maxDays ? "WARN" : "PASS", value: String(e.oldestItemDays), threshold: String(maxDays), note: "Days since the oldest open item" },
    { id: "CR-3", result: e.dunningLevel > (profile?.maxDunningLevel ?? 2) ? "FAIL" : "PASS", value: String(e.dunningLevel), threshold: String(profile?.maxDunningLevel ?? 2) },
    { id: "CR-5", result: share > 25 ? "WARN" : "PASS", value: dec(share), threshold: "25.00", note: "Overdue share of open AR, percent" },
  ];
  const outcome = rules.some((r) => r.result === "FAIL") ? "FAIL" : rules.some((r) => r.result === "WARN") ? "WARN" : "PASS";
  return { outcome, rules };
}

// ---- Shipments, packing slips, invoices ----

const TAX: Record<string, number> = { MN: 6.875, IA: 6, NV: 6.85, TX: 6.25, NE: 5.5, IL: 6.25 };

function sscc(n: number): string {
  const body = `0${"0614141"}${String(n).padStart(9, "0")}`;
  let sum = 0;
  for (let i = 0; i < body.length; i++) sum += Number(body[body.length - 1 - i]) * (i % 2 === 0 ? 3 : 1);
  return `${body}${(10 - (sum % 10)) % 10}`;
}

interface ShipSpec {
  status: ShipmentStatus;
  planned: number;
  carrier: string;
  /** Per line: picked, packed (default the shipped or planned quantity). */
  picked?: number[];
  pickConfirmed?: boolean[];
}
const SHIP_SPECS: Record<number, ShipSpec> = {
  1: { status: "RELEASED", planned: 4, carrier: "ODFL" },
  3: { status: "PARTIALLY_SHIPPED", planned: -2, carrier: "XPO" },
  6: { status: "SHIPPED", planned: -9, carrier: "SAIA" },
  7: { status: "DELIVERED", planned: -25, carrier: "ODFL" },
  8: { status: "PICKING", planned: 3, carrier: "XPO", picked: [2, 6], pickConfirmed: [false, true] },
  10: { status: "DELIVERED", planned: -48, carrier: "FEDEX-FRT" },
  11: { status: "DELIVERED", planned: -37, carrier: "SAIA" },
  12: { status: "DELIVERED", planned: -44, carrier: "SAIA" },
  13: { status: "DELIVERED", planned: -73, carrier: "ODFL" },
  14: { status: "DELIVERED", planned: -88, carrier: "XPO" },
  16: { status: "PACKED", planned: 2, carrier: "SAIA" },
};

export const shipments = table<Shipment>([]);
export const slips = table<PackingSlip & { shipmentId: string }>([]);
export const slipNumbers = new Map<string, string>();
export const progress = table<OrderProgress>([]);

interface InvoiceSpec {
  status: InvoiceRow["status"];
  /** Days since the order shipped when it was invoiced. */
  after: number;
  settled: number;
}
const INVOICE_SPECS: Record<number, InvoiceSpec> = {
  6: { status: "PROPOSAL", after: 8, settled: 0 },
  7: { status: "POSTED", after: 1, settled: 0 },
  10: { status: "SETTLED", after: 1, settled: 1 },
  11: { status: "PARTIALLY_SETTLED", after: 1, settled: 0.6 },
  12: { status: "POSTED", after: 1, settled: 0 },
  13: { status: "SETTLED", after: 1, settled: 1 },
  14: { status: "SETTLED", after: 2, settled: 1 },
};

function buildFlows(): void {
  let slipSeq = 700;
  let invoiceSeq = 0;
  const invoiceRows: InvoiceRow[] = [];
  const shipOrder = Object.keys(SHIP_SPECS).map(Number).sort((a, b) => SHIP_SPECS[a].planned - SHIP_SPECS[b].planned);
  for (const n of shipOrder) {
    const spec = SHIP_SPECS[n];
    const o = seedOrders[n - 1];
    const shipId = uid(K.shipment, n);
    const shipped = spec.status === "PARTIALLY_SHIPPED" || spec.status === "SHIPPED" || spec.status === "DELIVERED";
    const lines: ShipmentLine[] = o.lines.map((l, i) => {
      const planned = Number(l.quantity);
      const delivered = Number(l.deliveredQty ?? 0);
      const pickedQty = spec.picked ? spec.picked[i] : shipped ? delivered : spec.status === "PACKED" ? planned : 0;
      const packedQty = spec.status === "PACKED" || shipped ? (shipped ? delivered : pickedQty) : 0;
      return {
        id: uid(K.shipLine, n * 10 + i + 1),
        orderLineId: l.id,
        productId: l.productId,
        unit: l.unit,
        qtyPlanned: dec(planned, 0),
        qtyPicked: dec(pickedQty, 0),
        qtyPacked: dec(packedQty, 0),
        qtyShipped: dec(shipped ? delivered : 0, 0),
        pickConfirmed: spec.pickConfirmed ? spec.pickConfirmed[i] : pickedQty > 0,
        zone: i === 0 ? "A-FINISHED" : "B-SPARES",
      };
    });
    const containers: Container[] = [];
    if (spec.status === "PACKED" || shipped) {
      const packed = lines.filter((l) => Number(l.qtyPacked) > 0);
      packed.forEach((l, i) => {
        containers.push({
          id: uid(K.container, n * 10 + i + 1),
          containerType: l.unit === "ea" && Number(l.qtyPacked) > 20 ? "PALLET-STD" : "CRATE-L",
          sscc: sscc(n * 10 + i + 1),
          status: shipped ? "SHIPPED" : "CLOSED",
          tareKg: "22.00",
          grossKg: dec(22 + Number(l.qtyPacked) * 38, 2),
          returnable: true,
          packagingMaterial: "WOOD",
          packingSlipId: null,
          lines: [{ shipmentLineId: l.id, qty: l.qtyPacked }],
        });
      });
    }
    const slipId = uid(K.slip, n);
    if (shipped) {
      const number = `PS-${String(++slipSeq).padStart(6, "0")}`;
      slipNumbers.set(slipId, number);
      containers.forEach((c) => (c.packingSlipId = slipId));
      const invoiced = INVOICE_SPECS[n] !== undefined && INVOICE_SPECS[n].status !== "PROPOSAL";
      const shipDay = spec.planned;
      const slipLines = lines.filter((l) => Number(l.qtyShipped) > 0).map((l) => ({ orderLineId: l.orderLineId, shipmentLineId: l.id, qty: l.qtyShipped, costAmount: dec(Number(l.qtyShipped) * (itemById(l.productId)?.cost ?? 0)), costSnapshotId: uid(K.slip, 5000 + n) }));
      const total = slipLines.reduce((s, l) => s + Number(l.costAmount), 0);
      slips.insert({
        id: slipId,
        packingSlipNumber: number,
        shipmentId: shipId,
        orderId: o.id,
        status: invoiced ? "INVOICED" : "POSTED",
        postingDate: day(shipDay),
        currency: "USD",
        totalCost: dec(total),
        reversalOfId: null,
        lines: slipLines,
        journal: slipLines.flatMap((l) => [
          { account: "1405", amount: l.costAmount, currency: "USD", orderLineId: l.orderLineId },
          { account: "1400", amount: `-${l.costAmount}`, currency: "USD", orderLineId: l.orderLineId },
        ]),
        packagingFees: [],
      });
      progress.insert({
        id: uid(K.progress, n * 2),
        legalEntityId: LEGAL_ENTITY.id,
        orderId: o.id,
        orderNumber: o.orderNumber,
        kind: "DELIVERED",
        sourceRef: number,
        lines: o.lines
          .filter((l) => Number(l.deliveredQty ?? 0) > 0)
          .map((l) => ({ lineId: l.id, lineNo: l.lineNo, quantity: l.deliveredQty ?? "0", deliveredQty: l.deliveredQty ?? "0", invoicedQty: l.invoicedQty ?? "0", lineStatus: l.lineStatus })),
        orderStatus: o.status,
        orderVersion: o.version,
        recordedBy: "fulfillment",
        recordedAt: at(shipDay + 0.4),
      });
    }
    shipments.insert({
      id: shipId,
      shipmentNumber: `SHP-${String(4100 + n * 3).padStart(6, "0")}`,
      legalEntityId: LEGAL_ENTITY.id,
      warehouseId: WH_FG.id,
      orderId: o.id,
      customerId: o.customerId,
      status: spec.status,
      plannedShipDate: day(spec.planned),
      actualShipDate: shipped ? at(spec.planned + 0.3) : null,
      carrierCode: spec.carrier,
      version: spec.status === "RELEASED" ? 1 : shipped ? 6 : 3,
      lines,
      containers,
      orderType: "ORDER",
      priority: n % 4 === 0 ? 1 : 2,
    });

    const inv = INVOICE_SPECS[n];
    if (inv) {
      const customer = customerById(o.customerId)!;
      const rate = TAX[customer.state] ?? 6;
      const invLines: InvoiceLine[] = o.lines.map((l, i) => {
        const it = itemById(l.productId)!;
        const gross = Number(l.deliveredQty ?? l.quantity) * Number(l.unitPrice);
        const net = r2(gross * (1 - Number(l.discPct) / 100));
        return {
          lineNo: i + 1,
          description: `${it.number} ${it.name}`,
          qty: dec(Number(l.deliveredQty ?? l.quantity), 0),
          unitPrice: l.unitPrice,
          discountPct: l.discPct,
          net: dec(net),
          tax: dec(r2((net * rate) / 100)),
          revenueAccount: "4000",
          taxCode: `TX-${customer.state}`,
          packingSlipId: slipId,
          orderId: o.id,
          orderLineId: l.id,
          productId: l.productId,
        };
      });
      const net = r2(invLines.reduce((s, l) => s + Number(l.net), 0));
      const tax = r2(invLines.reduce((s, l) => s + Number(l.tax), 0));
      const invoiceDate = day(spec.planned + inv.after);
      const terms = termDays(customer);
      const gross = r2(net + tax);
      invoiceRows.push({
        id: uid(K.invoice, n),
        legalEntityId: LEGAL_ENTITY.id,
        customerId: o.customerId,
        invoiceAccountId: o.customerId,
        type: "SHIPMENT",
        status: inv.status,
        number: undefined,
        currency: "USD",
        invoiceDate,
        dueDate: new Date(Date.parse(invoiceDate) + terms * 86_400_000).toISOString().slice(0, 10),
        postingDate: inv.status === "PROPOSAL" ? undefined : invoiceDate,
        paymentTermDays: terms,
        totals: { net: dec(net), charges: "0.00", tax: dec(tax), gross: dec(gross) },
        version: inv.status === "PROPOSAL" ? 1 : 3,
        lines: invLines,
        installments: [{ no: 1, percent: "100", offsetDays: terms, amount: dec(gross), dueDate: "" }],
        credited: inv.status === "PROPOSAL" ? undefined : { gross: "0.00", remaining: dec(gross) },
        settled: inv.settled,
        orderId: o.id,
      });
    }
  }

  // A free-text invoice for services that did not ship from the warehouse.
  const metro = CUSTOMERS[5];
  const services = 2 * 1400;
  const serviceTax = r2((services * (TAX[metro.state] ?? 6)) / 100);
  invoiceRows.push({
    id: uid(K.invoice, 40),
    legalEntityId: LEGAL_ENTITY.id,
    customerId: metro.id,
    invoiceAccountId: metro.id,
    type: "FREE_TEXT",
    status: "POSTED",
    currency: "USD",
    invoiceDate: day(-12),
    dueDate: day(18),
    postingDate: day(-12),
    paymentTermDays: 30,
    totals: { net: dec(services), charges: "0.00", tax: dec(serviceTax), gross: dec(services + serviceTax) },
    version: 2,
    lines: [{ lineNo: 1, description: "Field commissioning, booster skid (technician days)", qty: "2", unitPrice: "1400.00", discountPct: "0", net: dec(services), tax: dec(serviceTax), revenueAccount: "4200", taxCode: `TX-${metro.state}` }],
    installments: [{ no: 1, percent: "100", offsetDays: 30, amount: dec(services + serviceTax), dueDate: day(18) }],
    credited: { gross: "0.00", remaining: dec(services + serviceTax) },
    settled: 0,
  });

  // Number posted invoices in date order; proposals stay unnumbered.
  const year = day(0).slice(2, 4);
  invoiceRows
    .sort((a, b) => a.invoiceDate.localeCompare(b.invoiceDate))
    .forEach((row) => {
      if (row.status !== "PROPOSAL") row.number = `INV-${year}-${String(2400 + ++invoiceSeq * 3).padStart(5, "0")}`;
      row.installments.forEach((i) => (i.dueDate = row.dueDate));
      invoices.insert(row);
    });
  // Invoice progress against the sales orders.
  for (const row of invoiceRows) {
    if (row.status === "PROPOSAL" || row.orderId === undefined) continue;
    const o = orders.get(row.orderId)!;
    progress.insert({
      id: uid(K.progress, orderSeq(o) * 2 + 1),
      legalEntityId: LEGAL_ENTITY.id,
      orderId: o.id,
      orderNumber: o.orderNumber,
      kind: "INVOICED",
      sourceRef: row.number ?? "",
      lines: o.lines.map((l) => ({ lineId: l.id, lineNo: l.lineNo, quantity: l.quantity, deliveredQty: l.deliveredQty ?? "0", invoicedQty: l.invoicedQty ?? "0", lineStatus: l.lineStatus })),
      orderStatus: o.status,
      orderVersion: o.version,
      recordedBy: "billing",
      recordedAt: at(day(0) > row.invoiceDate ? -(Date.parse(day(0)) - Date.parse(row.invoiceDate)) / 86_400_000 + 0.2 : 0),
    });
  }
}
buildFlows();

// Credit checks of the seeded confirmed and later orders, and the credit hold of SO-024185, once invoices exist.
for (const o of seedOrders) {
  const fresh = orders.get(o.id)!;
  if (o.status === "CONFIRMED" || o.status === "PARTIALLY_SHIPPED" || o.status === "SHIPPED" || o.status === "INVOICED" || o.status === "CLOSED") {
    orders.update(o.id, { creditCheck: evaluateCredit(fresh) });
  }
}
// Orders 2 (Harlan, near its limit) and 5 (Prairie, with an overdue invoice) sit on credit hold.
[
  { n: 5, hold: 1, ago: 0.4 },
  { n: 2, hold: 2, ago: 1.1 },
].forEach(({ n, hold: h, ago }) => {
  const held = orders.get(uid(K.order, n))!;
  const check = evaluateCredit(held);
  const hold: Hold = {
    id: uid(K.hold, h),
    holdType: "CREDIT",
    reasonCode: "CREDIT_LIMIT",
    status: "ACTIVE",
    orderAmount: held.netTotal,
    appliedBy: "system",
    appliedAt: at(-ago),
  };
  orders.update(held.id, { holds: [hold], creditCheck: check, updatedAt: hold.appliedAt });
});

export const SALES_ACTOR = NOW_SALES_ENTRY;
