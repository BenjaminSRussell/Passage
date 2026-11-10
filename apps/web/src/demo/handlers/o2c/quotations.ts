// Sales quotations (/api/v1/sales/quotations) for the demo: a small pipeline from draft to converted, the workspace
// tiles that count it, and the actions (send, lose, cancel, change, convert, expire) that move a quotation along.
import type { Order, OrderLine, Quotation, QuotationLine, QuotationStatus, SalesSummary } from "../../../modules/sales/api/client";
import { CUSTOMERS, LEGAL_ENTITY, PLANT, WH_FG, itemByNumber } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, table, uid } from "../../store";
import { K, SALES_ACTOR, customerById, itemById, orderNumber, orderSeq, orders, priceFor, r2, recordVersion } from "./model";

const BASE = "/api/v1/sales";

let lineSeq = 0;
function qline(customer: number, lineNo: number, number: string, qty: number, shipIn: number, converted?: string): QuotationLine {
  const item = itemByNumber(number);
  const { unitPrice, discPct } = priceFor(CUSTOMERS[customer], number, qty);
  return {
    id: uid(K.quotation, 5000 + ++lineSeq),
    lineNo,
    productId: item.id,
    quantity: dec(qty, 0),
    unit: item.unit,
    siteId: PLANT.id,
    warehouseId: WH_FG.id,
    unitPrice: dec(unitPrice),
    discPct: dec(discPct),
    lineNet: dec(r2(unitPrice * qty * (1 - discPct / 100))),
    requestedShipDate: day(shipIn),
    lineStatus: converted ? "CONVERTED" : "OPEN",
    convertedOrderId: converted,
  };
}

const quotationNumber = (n: number) => `SQ-${String(4400 + n).padStart(6, "0")}`;
const quotationSeq = (q: Pick<Quotation, "quotationNumber">) => Number(q.quotationNumber.slice(3)) - 4400;

function quotation(n: number, customer: number, status: QuotationStatus, createdDaysAgo: number, validIn: number, probabilityPct: number, lines: QuotationLine[], extra: Partial<Quotation> = {}): Quotation {
  return {
    id: uid(K.quotation, n),
    legalEntityId: LEGAL_ENTITY.id,
    quotationNumber: quotationNumber(n),
    customerId: CUSTOMERS[customer].id,
    invoiceAccountId: CUSTOMERS[customer].id,
    currency: "USD",
    status,
    validUntil: day(validIn),
    probabilityPct,
    netTotal: dec(lines.reduce((s, l) => s + Number(l.lineNet), 0)),
    version: status === "DRAFT" ? 1 : 2,
    createdBy: SALES_ACTOR,
    createdAt: at(-createdDaysAgo),
    updatedAt: at(-createdDaysAgo / 2),
    lines,
    lineCount: lines.length,
    openLines: lines.filter((l) => l.lineStatus === "OPEN").length,
    ...extra,
  };
}

export const quotations = table<Quotation>([
  quotation(10, 0, "SENT", 5, 12, 70, [qline(0, 1, "CP-200", 8, 30)]),
  quotation(11, 4, "SENT", 8, 5, 60, [qline(4, 1, "SP-150", 40, 21)]),
  quotation(12, 3, "SENT", 9, 4, 55, [qline(3, 1, "BP-300", 3, 40), qline(3, 2, "CP-400", 2, 40)]),
  quotation(13, 5, "DRAFT", 1, 20, 50, [qline(5, 1, "DP-100", 6, 28)]),
  quotation(14, 1, "DRAFT", 2, 25, 40, [qline(1, 1, "SP-150", 60, 30), qline(1, 2, "SEAL-MK-125", 40, 30)]),
  quotation(15, 2, "CONVERTED", 22, -10, 80, [qline(2, 1, "CP-400", 3, 3, uid(K.order, 8)), qline(2, 2, "SA-IMP-06", 6, 3, uid(K.order, 8))]),
  quotation(16, 0, "LOST", 40, -20, 30, [qline(0, 1, "CP-200", 30, -5)], { lostReason: "Lost on price to a competing bid" }),
  quotation(17, 2, "SENT", 18, -3, 50, [qline(2, 1, "SA-IMP-06", 12, 14)]),
]);

const open = (q: Quotation) => q.status === "DRAFT" || q.status === "SENT";
const lapsed = (q: Quotation) => open(q) && q.validUntil < day(0);

function listed(q: Quotation): Quotation {
  return { ...q, lines: [] };
}

const notFound = () => reply.problem(404, "SLS-0404", "Quotation not found");
const stale = () => reply.problem(412, "SLS-0006", "The quotation changed since you opened it. Reload it and try again.");
const versionOf = (header: string | null): number | undefined => {
  const m = /(\d+)/.exec(header ?? "");
  return m ? Number(m[1]) : undefined;
};

function save(q: Quotation, patch: Partial<Quotation>): Quotation {
  const lines = patch.lines ?? q.lines;
  const next: Quotation = {
    ...q,
    ...patch,
    lines,
    lineCount: lines.length,
    openLines: lines.filter((l) => l.lineStatus === "OPEN").length,
    netTotal: dec(lines.reduce((s, l) => s + Number(l.lineNet), 0)),
    version: q.version + 1,
    updatedAt: at(0),
  };
  return quotations.update(q.id, next) ?? next;
}

route("GET", `${BASE}/quotations`, ({ query }) => {
  const rows = quotations
    .all()
    .filter((q) => (!query.get("customerId") || q.customerId === query.get("customerId")) && (!query.get("status") || q.status === query.get("status")) && (!query.get("legalEntityId") || q.legalEntityId === query.get("legalEntityId")))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items: rows.map(listed) };
});

route("POST", `${BASE}/quotations`, ({ body }) => {
  const input = body as { customerId: string; invoiceAccountId?: string; currency: string; validUntil?: string; probabilityPct?: number; lines: { productId: string; quantity: string; unit: string; unitPrice: string; discPct?: string; requestedShipDate: string; siteId: string; warehouseId: string }[] };
  const errors: Record<string, string> = {};
  if (!customerById(input.customerId)) errors.customerId = "Choose a customer from the list.";
  const n = Math.max(...quotations.all().map(quotationSeq)) + 1;
  const lines: QuotationLine[] = (input.lines ?? []).map((l, i) => {
    if (!itemById(l.productId)) errors[`lines[${i}].productId`] = "Choose a product from the list.";
    const disc = Number(l.discPct ?? 0);
    return {
      id: uid(K.quotation, 5000 + ++lineSeq),
      lineNo: i + 1,
      productId: l.productId,
      quantity: l.quantity,
      unit: l.unit,
      siteId: PLANT.id,
      warehouseId: WH_FG.id,
      unitPrice: l.unitPrice,
      discPct: dec(disc),
      lineNet: dec(r2(Number(l.quantity) * Number(l.unitPrice) * (1 - disc / 100))),
      requestedShipDate: l.requestedShipDate,
      lineStatus: "OPEN",
    };
  });
  if (Object.keys(errors).length > 0) return reply.problem(422, "COM-0422", "The quotation has fields the service refuses.", errors);
  const created = quotations.insert(
    quotation(n, CUSTOMERS.findIndex((c) => c.id === input.customerId), "DRAFT", 0, 0, input.probabilityPct ?? 50, lines, {
      invoiceAccountId: input.invoiceAccountId || input.customerId,
      currency: input.currency,
      validUntil: input.validUntil || day(30),
      createdAt: at(0),
      updatedAt: at(0),
    }),
  );
  return reply.versioned(created, created.version, 201);
});

route("POST", `${BASE}/quotations:expire`, () => {
  const stale = quotations.all().filter(lapsed);
  const expired = stale.map((q) => {
    const previousStatus = q.status;
    const next = save(q, { status: "EXPIRED" });
    return { id: next.id, quotationNumber: next.quotationNumber, legalEntityId: next.legalEntityId, customerId: next.customerId, validUntil: next.validUntil, previousStatus, version: next.version };
  });
  return { count: expired.length, more: false, expired };
});

/** Looks up a quotation, checks the If-Match version, and runs the change. */
function act(id: string, headers: Headers, change: (q: Quotation) => unknown) {
  const q = quotations.get(id);
  if (!q) return notFound();
  const v = versionOf(headers.get("If-Match"));
  if (v !== undefined && v !== q.version) return stale();
  return change(q);
}

route("POST", `${BASE}/quotations/{id}:send`, ({ params, headers }) =>
  act(params.id, headers, (q) => {
    if (q.status !== "DRAFT") return reply.problem(409, "SLS-0005", "Only a draft quotation can be sent.");
    if (q.validUntil < day(0)) return reply.problem(409, "SLS-0005", "The quotation is past its valid-until date.");
    const next = save(q, { status: "SENT" });
    return reply.versioned(next, next.version);
  }),
);

route("POST", `${BASE}/quotations/{id}:lose`, ({ params, headers, body }) =>
  act(params.id, headers, (q) => {
    if (!open(q)) return reply.problem(409, "SLS-0005", "Only a draft or sent quotation can be marked lost.");
    const next = save(q, { status: "LOST", lostReason: (body as { reason?: string } | undefined)?.reason });
    return reply.versioned(next, next.version);
  }),
);

route("POST", `${BASE}/quotations/{id}:cancel`, ({ params, headers, body }) =>
  act(params.id, headers, (q) => {
    if (!open(q)) return reply.problem(409, "SLS-0005", "Only a draft or sent quotation can be cancelled.");
    const next = save(q, { status: "CANCELLED", cancelReason: (body as { reason?: string } | undefined)?.reason });
    return reply.versioned(next, next.version);
  }),
);

route("POST", `${BASE}/quotations/{id}:convert`, ({ params, headers, body }) =>
  act(params.id, headers, (q) => {
    if (!open(q) || q.validUntil < day(0)) return reply.problem(409, "SLS-0005", "Only a valid, open quotation can be converted.");
    const wanted = (body as { lineNos?: number[] } | undefined)?.lineNos ?? [];
    const picked = q.lines.filter((l) => l.lineStatus === "OPEN" && (wanted.length === 0 || wanted.includes(l.lineNo)));
    if (picked.length === 0) return reply.problem(409, "SLS-0005", "There is no open line to convert.");
    const seq = Math.max(...orders.all().map(orderSeq)) + 1;
    const lines: OrderLine[] = picked.map((l, i) => ({ ...l, id: uid(K.line, 6000 + seq * 10 + i), lineNo: i + 1, lineStatus: "OPEN", deliveredQty: "0", invoicedQty: "0", convertedOrderId: undefined } as OrderLine));
    const order: Order = orders.insert({
      id: uid(K.order, seq),
      legalEntityId: q.legalEntityId,
      orderNumber: orderNumber(seq),
      customerId: q.customerId,
      invoiceAccountId: q.invoiceAccountId,
      orderType: "ORDER",
      status: "DRAFT",
      currency: q.currency,
      requestedShipDate: lines[0].requestedShipDate,
      channel: "QUOTATION",
      externalRef: q.quotationNumber,
      netTotal: dec(lines.reduce((s, l) => s + Number(l.lineNet), 0)),
      version: 1,
      lines,
      holds: [],
      createdAt: at(0),
      updatedAt: at(0),
    });
    recordVersion(order, `Converted from quotation ${q.quotationNumber}`, []);
    const nextLines = q.lines.map((l) => (picked.some((p) => p.lineNo === l.lineNo) ? { ...l, lineStatus: "CONVERTED" as const, convertedOrderId: order.id } : l));
    const next = save(q, { lines: nextLines, status: nextLines.every((l) => l.lineStatus === "CONVERTED") ? "CONVERTED" : q.status });
    return reply.versioned({ quotation: next, order: { ...order, warnings: [] } }, next.version);
  }),
);

route("PATCH", `${BASE}/quotations/{id}`, ({ params, headers, body }) =>
  act(params.id, headers, (q) => {
    if (!open(q)) return reply.problem(409, "SLS-0005", "Only a draft or sent quotation can be changed.");
    const input = body as { validUntil?: string; probabilityPct?: number; lines?: { productId: string; quantity: string; unit: string; unitPrice: string; discPct?: string; requestedShipDate: string }[] };
    const lines = input.lines
      ? input.lines.map((l, i): QuotationLine => ({
          id: uid(K.quotation, 5000 + ++lineSeq),
          lineNo: i + 1,
          productId: l.productId,
          quantity: l.quantity,
          unit: l.unit,
          siteId: PLANT.id,
          warehouseId: WH_FG.id,
          unitPrice: l.unitPrice,
          discPct: dec(Number(l.discPct ?? 0)),
          lineNet: dec(r2(Number(l.quantity) * Number(l.unitPrice) * (1 - Number(l.discPct ?? 0) / 100))),
          requestedShipDate: l.requestedShipDate,
          lineStatus: "OPEN",
        }))
      : undefined;
    const next = save(q, { ...(input.validUntil ? { validUntil: input.validUntil } : {}), ...(input.probabilityPct !== undefined ? { probabilityPct: input.probabilityPct } : {}), ...(lines ? { lines } : {}) });
    return reply.versioned(next, next.version);
  }),
);

route("GET", `${BASE}/quotations/{id}`, ({ params }) => {
  const q = quotations.get(params.id);
  return q ? reply.versioned(q, q.version) : notFound();
});

// ---- Workspace tiles, counted from the table above ----

route("GET", `${BASE}/summary`, ({ query }) => {
  const windowDays = Number(query.get("windowDays") ?? 90);
  const status = query.get("status");
  const all = quotations.all();
  const pool = all.filter((q) => open(q) && !lapsed(q) && (!status || q.status === status));
  const net = pool.reduce((s, q) => s + Number(q.netTotal), 0);
  const weighted = pool.reduce((s, q) => s + (Number(q.netTotal) * q.probabilityPct) / 100, 0);
  const horizon = day(7);
  const body: SalesSummary = {
    activeHolds: orders.all().reduce((n, o) => n + o.holds.filter((h) => h.status === "ACTIVE").length, 0),
    backorderLines: backorderCount(),
    quotationsExpiring: all.filter((q) => open(q) && !lapsed(q) && q.validUntil <= horizon).length,
    quotationsLapsed: all.filter(lapsed).length,
    openIntakeErrors: 0,
    pipeline: [{ currency: "USD", openCount: pool.length, openNet: dec(net), weightedNet: dec(weighted) }],
    windowDays,
    won: Math.round((windowDays / 90) * 11),
    lost: Math.round((windowDays / 90) * 4),
    expired: Math.round((windowDays / 90) * 3),
    conversionRatePct: "61.1",
  };
  return body;
});

let backorderCount = (): number => 0;
/** The sales handler supplies the live backorder count for the workspace tile. */
export function setBackorderCount(fn: () => number): void {
  backorderCount = fn;
}
