// Sales service (/api/v1/sales) for the demo. Shapes are typed against the module's own client types, so a
// fixture that drifts from the real response fails `tsc` instead of failing in the browser.
import type { Hold, HoldType, Order, OrderLine, OrderStatus, OrderSummary } from "../../../modules/sales/api/client";
import { CUSTOMERS, LEGAL_ENTITY, PLANT, WH_FG, itemByNumber } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, filterBy, table, uid } from "../../store";

const BASE = "/api/v1/sales";
const KIND_ORDER = 100;
const KIND_LINE = 101;
const KIND_HOLD = 102;

let lineSeq = 0;
function line(lineNo: number, number: string, qty: number, shipIn: number, status: string, delivered = 0): OrderLine {
  const it = itemByNumber(number);
  const price = it.price ?? it.cost * 2;
  return {
    id: uid(KIND_LINE, ++lineSeq),
    lineNo,
    productId: it.id,
    quantity: dec(qty, 0),
    unit: it.unit,
    siteId: PLANT.id,
    warehouseId: WH_FG.id,
    unitPrice: dec(price),
    discPct: "0.00",
    lineNet: dec(price * qty),
    requestedShipDate: day(shipIn),
    confirmedShipDate: status === "OPEN" ? undefined : day(shipIn),
    lineStatus: status,
    deliveredQty: dec(delivered, 0),
    invoicedQty: status === "INVOICED" ? dec(qty, 0) : "0",
  };
}

function order(n: number, customer: number, status: OrderStatus, createdDaysAgo: number, lines: OrderLine[], holds: Hold[] = []): Order {
  const net = lines.reduce((sum, l) => sum + Number(l.lineNet), 0);
  return {
    id: uid(KIND_ORDER, n),
    legalEntityId: LEGAL_ENTITY.id,
    orderNumber: `SO-${String(24180 + n).padStart(6, "0")}`,
    customerId: CUSTOMERS[customer].id,
    invoiceAccountId: CUSTOMERS[customer].id,
    orderType: "ORDER",
    status,
    currency: "USD",
    requestedShipDate: lines[0]?.requestedShipDate ?? day(7),
    channel: n % 3 === 0 ? "EDI" : "MANUAL",
    netTotal: dec(net),
    version: 1,
    lines,
    holds,
    createdAt: at(-createdDaysAgo),
    updatedAt: at(-createdDaysAgo / 2),
  };
}

const creditHold = (n: number, amount: number): Hold => ({
  id: uid(KIND_HOLD, n),
  holdType: "CREDIT",
  reasonCode: "CREDIT_LIMIT",
  status: "ACTIVE",
  orderAmount: dec(amount),
  appliedBy: "system",
  appliedAt: at(-1),
});

const orders = table<Order>([
  order(1, 0, "CONFIRMED", 6, [line(1, "CP-200", 12, 4, "CONFIRMED"), line(2, "SEAL-MK-125", 24, 4, "CONFIRMED")]),
  order(2, 1, "OPEN", 1, [line(1, "SP-150", 30, 9, "OPEN")]),
  order(3, 2, "PARTIALLY_SHIPPED", 12, [line(1, "CP-400", 6, -2, "PICKED", 4), line(2, "BRG-6206", 40, -2, "SHIPPED", 40)]),
  order(4, 3, "CONFIRMED", 3, [line(1, "BP-300", 2, 18, "CONFIRMED"), line(2, "CP-400", 4, 18, "CONFIRMED")]),
  order(5, 4, "OPEN", 0.5, [line(1, "SP-150", 50, 12, "OPEN"), line(2, "SEAL-MK-125", 50, 12, "OPEN")], [creditHold(1, 72750)]),
  order(6, 5, "SHIPPED", 20, [line(1, "DP-100", 8, -9, "SHIPPED", 8)]),
  order(7, 0, "INVOICED", 34, [line(1, "CP-200", 20, -25, "INVOICED", 20)]),
  order(8, 2, "CONFIRMED", 2, [line(1, "CP-400", 3, 10, "CONFIRMED"), line(2, "SA-IMP-06", 6, 10, "CONFIRMED")]),
  order(9, 1, "DRAFT", 0.2, [line(1, "CP-200", 4, 14, "OPEN")]),
  order(10, 3, "CLOSED", 60, [line(1, "CP-400", 10, -48, "INVOICED", 10)]),
]);

function summary(o: Order): OrderSummary {
  return {
    id: o.id,
    legalEntityId: o.legalEntityId,
    orderNumber: o.orderNumber,
    customerId: o.customerId,
    orderType: o.orderType,
    status: o.status,
    currency: o.currency,
    requestedShipDate: o.requestedShipDate,
    channel: o.channel,
    externalRef: o.externalRef,
    netTotal: o.netTotal,
    lineCount: o.lines.length,
    activeHolds: o.holds.filter((h) => h.status === "ACTIVE").map((h) => h.holdType as HoldType),
    version: o.version,
    createdAt: o.createdAt,
  };
}

route("GET", `${BASE}/orders`, ({ query }) => {
  const hold = query.get("hold");
  const rest = new URLSearchParams(query);
  rest.delete("hold");
  const rows = filterBy(orders.all(), rest)
    .filter((o) => !hold || o.holds.some((h) => h.status === "ACTIVE" && h.holdType === hold))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items: rows.map(summary) };
});

route("GET", `${BASE}/orders/{id}`, ({ params }) => {
  const o = orders.get(params.id);
  return o ? reply.versioned(o, o.version) : reply.problem(404, "SLS-0404", "Sales order not found");
});
