// Sales service (/api/v1/sales) for the demo, beside the orders in sales.ts: sales agreements with the orders
// released against them, change requests of customers that need change approval, channel orders refused at
// intake, and the product allow and deny lists. Agreements, requests and intake errors point at the orders and
// customers of model.ts, and an approved change or a released agreement order lands in the order table.
import type { Agreement, AgreementRelease, ChangeRequest, FieldChange, IntakeError, IntakeReplayResult, Order, OrderLine, ProductListing, ProductListingInput } from "../../../modules/sales/api/client";
import { CUSTOMERS, LEGAL_ENTITY, PLANT, USER, WH_FG, itemByNumber } from "../../company";
import { reply, route, type DemoRequest } from "../../backend";
import { at, day, dec, table, uid } from "../../store";
import { K, SALES_ACTOR, orderNumber, orderSeq, orders, r2, recordVersion } from "./model";

const BASE = "/api/v1/sales";
const K_AGREEMENT = 432;
const K_RELEASE = 433;
const K_CHANGE = 434;
const K_INTAKE = 435;
const K_LISTING = 436;

const orderId = (n: number) => uid(K.order, n);
const versionOf = (req: DemoRequest): number | undefined => {
  const m = /(\d+)/.exec(req.headers.get("If-Match") ?? "");
  return m ? Number(m[1]) : undefined;
};
const stale = () => reply.problem(412, "SLS-0006", "This changed since you opened it. Reload it and try again.");

// ---- Sales agreements ---------------------------------------------------------------------------------------

interface AgreementSeed {
  n: number;
  customer: number;
  item: string;
  price: number;
  /** Units, or the amount for an AMOUNT agreement. */
  commitment: number;
  amount?: boolean;
  /** Orders released against it: order number in model.ts and the line of the agreement's product. */
  released: number[];
  from: number;
  to: number;
  status?: Agreement["status"];
  close?: { reason: string; by: string; ago: number };
}

const SEEDS: AgreementSeed[] = [
  { n: 1, customer: 0, item: "CP-200", price: 2390, commitment: 60, released: [7, 1], from: -200, to: 165 },
  { n: 2, customer: 1, item: "SP-150", price: 1225, commitment: 250000, amount: true, released: [11, 16], from: -150, to: 215 },
  { n: 3, customer: 2, item: "CP-400", price: 5650, commitment: 24, released: [3, 8], from: -120, to: 245 },
  { n: 4, customer: 3, item: "BP-300", price: 18200, commitment: 6, released: [4], from: -60, to: 305 },
  { n: 5, customer: 3, item: "CP-400", price: 5750, commitment: 10, released: [10], from: -300, to: 65, status: "EXHAUSTED" },
  { n: 6, customer: 4, item: "SP-150", price: 1240, commitment: 80, released: [12, 5], from: -347, to: 18 },
  { n: 7, customer: 5, item: "DP-100", price: 1690, commitment: 30, released: [14, 6], from: -260, to: 105, status: "CLOSED", close: { reason: "Metro moved its chilled water loops to a different pump line; the rest of the commitment is released.", by: "dana.whitfield", ago: 4 } },
];

function releasesOf(seed: AgreementSeed): AgreementRelease[] {
  const it = itemByNumber(seed.item);
  return seed.released
    .map((n, i): AgreementRelease | undefined => {
      const o = orders.get(orderId(n));
      const line = o?.lines.find((l) => l.productId === it.id);
      if (!o || !line) return undefined;
      const qty = Number(line.quantity);
      return {
        id: uid(K_RELEASE, seed.n * 10 + i + 1),
        quantity: line.quantity,
        units: seed.amount ? dec(qty * seed.price) : line.quantity,
        drawnQty: "0",
        returnedQty: "0",
        salesOrderId: o.id,
        orderNumber: o.orderNumber,
        actor: SALES_ACTOR,
        releasedAt: o.createdAt,
      };
    })
    .filter((r): r is AgreementRelease => r !== undefined)
    .sort((a, b) => b.releasedAt.localeCompare(a.releasedAt));
}

function agreement(seed: AgreementSeed): Agreement {
  const it = itemByNumber(seed.item);
  const customer = CUSTOMERS[seed.customer];
  const releases = releasesOf(seed);
  const released = releases.reduce((s, r) => s + Number(r.units ?? r.quantity), 0);
  const remaining = Math.max(0, seed.commitment - released);
  const status = seed.status ?? (remaining === 0 ? "EXHAUSTED" : "ACTIVE");
  return {
    id: uid(K_AGREEMENT, seed.n),
    agreementNumber: `SAG-${String(26000 + seed.n * 7).padStart(6, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    customerId: customer.id,
    invoiceAccountId: customer.id,
    productId: it.id,
    unit: it.unit,
    unitPrice: dec(seed.price),
    currency: "USD",
    commitmentType: seed.amount ? "AMOUNT" : "QUANTITY",
    commitmentUnit: seed.amount ? "USD" : it.unit,
    commitmentQty: seed.amount ? dec(seed.commitment) : dec(seed.commitment, 0),
    releasedQty: seed.amount ? dec(released) : dec(released, 0),
    remainingQty: seed.amount ? dec(remaining) : dec(remaining, 0),
    usedPct: dec((released / seed.commitment) * 100),
    status,
    validFrom: day(seed.from),
    validTo: day(seed.to),
    cumTolerance: "0",
    overdeliveryPct: "5",
    ceilingQty: dec(seed.commitment * 1.05, seed.amount ? 2 : 0),
    closeReason: seed.close?.reason,
    closedBy: seed.close?.by,
    closedAt: seed.close ? at(-seed.close.ago) : undefined,
    version: 2 + releases.length + (seed.close ? 1 : 0),
    createdBy: "dana.whitfield",
    createdAt: at(seed.from - 5),
    updatedAt: releases[0]?.releasedAt ?? at(seed.from),
    releases,
  };
}

const agreements = table<Agreement>(SEEDS.map(agreement));

route("GET", `${BASE}/agreements`, ({ query }) => {
  const customerId = query.get("customerId");
  const status = query.get("status");
  const expiringBy = query.get("expiringBy");
  const minUsedPct = query.get("minUsedPct");
  let items = agreements
    .all()
    .filter((a) => (!customerId || a.customerId === customerId) && (!status || a.status === status) && (!expiringBy || (a.validTo ?? "9999") <= expiringBy) && (!minUsedPct || Number(a.usedPct ?? 0) >= Number(minUsedPct)))
    .map(({ releases: _r, ...a }) => a);
  items = expiringBy ? items.sort((a, b) => (a.validTo ?? "").localeCompare(b.validTo ?? "")) : items.sort((a, b) => b.agreementNumber.localeCompare(a.agreementNumber));
  return { items };
});

route("GET", `${BASE}/agreements/{id}/versions`, () => ({ items: [] }));

route("GET", `${BASE}/agreements/{id}`, ({ params }) => {
  const a = agreements.get(params.id);
  return a ? reply.versioned(a, a.version) : reply.problem(404, "SLS-0404", "Sales agreement not found");
});

route("POST", `${BASE}/agreements/{id}:close`, (req) => {
  const a = agreements.get(req.params.id);
  if (!a) return reply.problem(404, "SLS-0404", "Sales agreement not found");
  const v = versionOf(req);
  if (v !== undefined && v !== a.version) return stale();
  if (a.status === "CLOSED") return reply.problem(409, "SLS-0130", "The agreement is already closed");
  const reason = String((req.body as { reason?: string } | undefined)?.reason ?? "");
  const next = agreements.update(a.id, { status: "CLOSED", closeReason: reason, closedBy: SALES_ACTOR, closedAt: at(0), version: a.version + 1, updatedAt: at(0) })!;
  return reply.versioned(next, next.version);
});

/** Draws from the commitment and creates a DRAFT order at the agreement price. */
route("POST", `${BASE}/agreements/{id}:release-order`, ({ params, body }) => {
  const a = agreements.get(params.id);
  if (!a) return reply.problem(404, "SLS-0404", "Sales agreement not found");
  if (a.status !== "ACTIVE") return reply.problem(409, "SLS-0131", "Only an active agreement takes releases");
  const input = body as { quantity: string; siteId: string; warehouseId: string; requestedShipDate: string };
  const qty = Number(input.quantity);
  const units = a.commitmentType === "AMOUNT" ? qty * Number(a.unitPrice) : qty;
  const ceiling = Number(a.ceilingQty ?? a.commitmentQty);
  if (Number(a.releasedQty) + units > ceiling) return reply.problem(422, "SLS-0132", `Only ${a.remainingQty} ${a.commitmentUnit ?? a.unit} is left on the agreement`, { quantity: "More than the agreement has left." });
  const seq = Math.max(...orders.all().map(orderSeq)) + 1;
  const line: OrderLine = {
    id: uid(K.line, 7000 + seq),
    lineNo: 1,
    productId: a.productId,
    quantity: dec(qty, 0),
    unit: a.unit,
    siteId: input.siteId || PLANT.id,
    warehouseId: input.warehouseId || WH_FG.id,
    unitPrice: a.unitPrice,
    discPct: "0.00",
    lineNet: dec(r2(qty * Number(a.unitPrice))),
    requestedShipDate: input.requestedShipDate,
    lineStatus: "OPEN",
    deliveredQty: "0",
    invoicedQty: "0",
  };
  const order: Order = orders.insert({
    id: uid(K.order, seq),
    legalEntityId: a.legalEntityId,
    orderNumber: orderNumber(seq),
    customerId: a.customerId,
    invoiceAccountId: a.invoiceAccountId,
    orderType: "ORDER",
    status: "DRAFT",
    currency: a.currency,
    requestedShipDate: input.requestedShipDate,
    channel: "MANUAL",
    externalRef: a.agreementNumber,
    netTotal: line.lineNet,
    version: 1,
    lines: [line],
    holds: [],
    createdAt: at(0),
    updatedAt: at(0),
  });
  recordVersion(order, `Released from sales agreement ${a.agreementNumber}`, []);
  const released = Number(a.releasedQty) + units;
  const commitment = Number(a.commitmentQty);
  const places = a.commitmentType === "AMOUNT" ? 2 : 0;
  const release: AgreementRelease = { id: uid(K_RELEASE, 900 + seq), quantity: line.quantity, units: dec(units, places), drawnQty: "0", returnedQty: "0", salesOrderId: order.id, orderNumber: order.orderNumber, actor: SALES_ACTOR, releasedAt: at(0) };
  const next = agreements.update(a.id, {
    releasedQty: dec(released, places),
    remainingQty: dec(Math.max(0, commitment - released), places),
    usedPct: dec((released / commitment) * 100),
    status: released >= commitment ? "EXHAUSTED" : "ACTIVE",
    releases: [release, ...(a.releases ?? [])],
    version: a.version + 1,
    updatedAt: at(0),
  })!;
  return reply.created({ agreement: next, order });
});

// ---- Change requests ----------------------------------------------------------------------------------------

function change(n: number, order: number, status: ChangeRequest["status"], ago: number, reason: string, diff: FieldChange[], extra: Partial<ChangeRequest> = {}): ChangeRequest {
  const o = orders.get(orderId(order))!;
  const header = diff.find((d) => d.lineNo === 0 && d.field === "requestedShipDate");
  const lineChanges = diff.filter((d) => d.lineNo > 0).map((d) => ({ lineNo: d.lineNo, [d.field]: d.new }));
  return {
    id: uid(K_CHANGE, n),
    legalEntityId: o.legalEntityId,
    orderId: o.id,
    orderNumber: o.orderNumber,
    customerId: o.customerId,
    kind: "CHANGE",
    status,
    baseVersion: o.version,
    reason,
    change: { reason, ...(header ? { requestedShipDate: header.new } : {}), ...(lineChanges.length ? { lines: lineChanges } : {}) },
    diff,
    requestedBy: "dana.whitfield",
    requestedAt: at(-ago),
    decisionComment: "",
    version: status === "PENDING" ? 1 : 2,
    ...extra,
  };
}

const o4 = orders.get(orderId(4))!;
const o8 = orders.get(orderId(8))!;
const o16 = orders.get(orderId(16))!;
const later = (date: string, days: number) => new Date(Date.parse(date) + days * 86_400_000).toISOString().slice(0, 10);

const changes = table<ChangeRequest>([
  change(5, 4, "PENDING", 0.3, "Skid foundations at the Corpus Christi plant are two weeks behind", [{ lineNo: 0, field: "requestedShipDate", old: o4.requestedShipDate, new: later(o4.requestedShipDate, 14) }]),
  change(4, 8, "PENDING", 0.8, "Second dewatering pit opens early; customer needs two more pumps", [{ lineNo: 1, field: "quantity", old: o8.lines[0].quantity, new: "5" }], { requestedBy: "jordan.avery" }),
  change(3, 16, "PENDING", 1.6, "Co-op members cancelled ten of the units", [{ lineNo: 1, field: "quantity", old: o16.lines[0].quantity, new: "60" }]),
  change(2, 3, "REJECTED", 6, "Add two CP-400 to the order", [{ lineNo: 1, field: "quantity", old: "6", new: "8" }], {
    decidedBy: "morgan.lee",
    decidedAt: at(-5.6),
    decisionComment: "The order is partly shipped; enter the extra pumps as a new order against the agreement.",
  }),
  change(1, 1, "APPROVED", 4.4, "Customer asked for a later ship date", [{ lineNo: 1, field: "requestedShipDate", old: later(orders.get(orderId(1))!.lines[0].requestedShipDate, -5), new: orders.get(orderId(1))!.lines[0].requestedShipDate }], {
    decidedBy: "morgan.lee",
    decidedAt: at(-4.2),
    decisionComment: "Fine, stock is reserved either way.",
    appliedVersion: 3,
    baseVersion: 2,
  }),
]);

route("GET", `${BASE}/change-requests`, ({ query }) => {
  const status = query.get("status");
  const order = query.get("orderId");
  return { items: changes.all().filter((c) => (!status || c.status === status) && (!order || c.orderId === order)).sort((a, b) => b.requestedAt.localeCompare(a.requestedAt)) };
});

route("GET", `${BASE}/change-requests/{id}`, ({ params }) => {
  const c = changes.get(params.id);
  return c ? reply.versioned(c, c.version) : reply.problem(404, "SLS-0404", "Change request not found");
});

route("POST", `${BASE}/change-requests/{id}:{action}`, (req) => {
  const c = changes.get(req.params.id);
  if (!c) return reply.problem(404, "SLS-0404", "Change request not found");
  const v = versionOf(req);
  if (v !== undefined && v !== c.version) return stale();
  if (c.status !== "PENDING") return reply.problem(409, "SLS-0112", "The request is already decided");
  const comment = String((req.body as { comment?: string } | undefined)?.comment ?? "");
  const decided = { decidedBy: SALES_ACTOR, decidedAt: at(0), decisionComment: comment, version: c.version + 1 };
  if (req.params.action === "reject" || req.params.action === "withdraw") {
    const next = changes.update(c.id, { ...decided, status: req.params.action === "reject" ? "REJECTED" : "WITHDRAWN" })!;
    return reply.versioned(next, next.version);
  }
  if (req.params.action !== "approve") return reply.problem(404, "SLS-0404", `Unknown action ${req.params.action}`);
  const o = orders.get(c.orderId);
  if (!o) return reply.problem(404, "SLS-0404", "Sales order not found");
  let requestedShipDate = o.requestedShipDate;
  const lines = o.lines.map((l) => {
    let next = { ...l };
    for (const d of c.diff) {
      if (d.lineNo === 0 && d.field === "requestedShipDate") {
        requestedShipDate = d.new;
        next = { ...next, requestedShipDate: d.new, confirmedShipDate: next.confirmedShipDate ? d.new : undefined };
      } else if (d.lineNo === l.lineNo && d.field === "quantity") {
        next = { ...next, quantity: d.new, lineNet: dec(r2(Number(d.new) * Number(l.unitPrice) * (1 - Number(l.discPct) / 100))) };
      } else if (d.lineNo === l.lineNo && d.field === "requestedShipDate") {
        next = { ...next, requestedShipDate: d.new };
      }
    }
    return next;
  });
  const netTotal = dec(lines.filter((l) => l.lineStatus !== "CANCELLED").reduce((s, l) => s + Number(l.lineNet), 0));
  const order = orders.update(o.id, { lines, requestedShipDate, netTotal, version: o.version + 1, updatedAt: at(0) })!;
  recordVersion(order, `${c.reason} (change request approved)`, c.diff);
  const request = changes.update(c.id, { ...decided, status: "APPROVED", appliedVersion: order.version })!;
  return reply.versioned({ request, order }, request.version);
});

// ---- Intake errors ------------------------------------------------------------------------------------------

function intake(n: number, channel: string, ref: string, code: string, message: string, attempts: number, ago: number, extra: Partial<IntakeError> = {}): IntakeError {
  return {
    id: uid(K_INTAKE, n),
    legalEntityId: LEGAL_ENTITY.id,
    channel,
    externalRef: ref,
    requestHash: `sha256:${(0x9e3779b9 * (n + 7)).toString(16).slice(0, 8)}${(n * 2654435761).toString(16).padStart(8, "0").slice(0, 8)}`,
    errorCode: code,
    message,
    status: "OPEN",
    attempts,
    firstSeenAt: at(-ago),
    lastSeenAt: at(-ago + Math.min(ago, 0.05 * attempts)),
    replayable: true,
    replayCount: 0,
    version: attempts,
    ...extra,
  };
}

const intakeErrors = table<IntakeError>([
  intake(6, "EDI", "PO-88111", "SLS-0099", "Order intake timed out after 30 seconds before it answered the EDI gateway.", 1, 0.2),
  intake(5, "EDI", "PO-88412", "SLS-0021", "Product CP-250 is not a sellable product; Summit Mining Supply's item cross-reference has no match.", 3, 0.6),
  intake(4, "PORTAL", "WEB-55120", "SLS-0032", "Prairie Irrigation Systems is on credit hold: an invoice is 18 days overdue.", 1, 1.1),
  intake(3, "EMAIL", "MFG-7781-B", "SLS-0014", "The requested ship date is in the past.", 2, 2.3, { replayable: false }),
  intake(2, "EDI", "PO-88160", "SLS-0009", "Unit of measure CS is not set up for BP-300 (booster pump skid).", 2, 3.4),
  intake(1, "EDI", "PO-88222", "SLS-0040", "An order with this external reference already exists for Metro Facilities Group.", 1, 20.4, {
    status: "RESOLVED",
    resolvedBy: SALES_ACTOR,
    resolvedAt: at(-20.2),
    resolutionNote: "Duplicate send from the customer's EDI provider; already stored as SO-024186.",
    replayable: false,
  }),
]);

route("GET", `${BASE}/intake-errors`, ({ query }) => {
  const status = query.get("status");
  const channel = query.get("channel");
  const code = query.get("errorCode");
  return { items: intakeErrors.all().filter((e) => (!status || e.status === status) && (!channel || e.channel === channel) && (!code || e.errorCode === code)).sort((a, b) => b.lastSeenAt.localeCompare(a.lastSeenAt)) };
});

route("GET", `${BASE}/intake-errors/{id}`, ({ params }) => {
  const e = intakeErrors.get(params.id);
  return e ? reply.versioned(e, e.version) : reply.problem(404, "SLS-0404", "Intake error not found");
});

/** The EDI order that timed out had in fact been stored (SO-024183); every other replay is refused again. */
function replay(e: IntakeError): IntakeReplayResult {
  if (e.status !== "OPEN" || !e.replayable) return { intakeError: e, replayed: false, failure: { code: "SLS-0118", message: "This intake error cannot be replayed; resolve it with a note instead." } };
  if (e.errorCode === "SLS-0099") {
    const found = orders.get(orderId(3))!;
    const next = intakeErrors.update(e.id, { status: "RESOLVED", resolvedBy: SALES_ACTOR, resolvedAt: at(0), resolutionNote: `Replayed: the order was already stored as ${found.orderNumber}.`, replayCount: e.replayCount + 1, replayedOrderId: found.id, attempts: e.attempts + 1, version: e.version + 1 })!;
    return { intakeError: next, replayed: true, orderId: found.id, orderNumber: found.orderNumber };
  }
  const next = intakeErrors.update(e.id, { replayCount: e.replayCount + 1, attempts: e.attempts + 1, lastSeenAt: at(0), version: e.version + 1 })!;
  return { intakeError: next, replayed: false, failure: { code: e.errorCode, message: e.message } };
}

route("POST", `${BASE}/intake-errors:replay`, ({ query }) => {
  const channel = query.get("channel");
  const code = query.get("errorCode");
  const open = intakeErrors.all().filter((e) => e.status === "OPEN" && (!channel || e.channel === channel) && (!code || e.errorCode === code));
  const skipped = open.filter((e) => e.attempts >= 10).length;
  const items = open.filter((e) => e.replayable && e.attempts < 10).sort((a, b) => a.firstSeenAt.localeCompare(b.firstSeenAt)).map(replay);
  return { attempted: items.length, replayed: items.filter((i) => i.replayed).length, failed: items.filter((i) => !i.replayed).length, skipped, items };
});

route("POST", `${BASE}/intake-errors/{id}:replay`, ({ params }) => {
  const e = intakeErrors.get(params.id);
  if (!e) return reply.problem(404, "SLS-0404", "Intake error not found");
  const out = replay(e);
  return reply.versioned(out, out.intakeError.version);
});

route("POST", `${BASE}/intake-errors/{id}:resolve`, (req) => {
  const e = intakeErrors.get(req.params.id);
  if (!e) return reply.problem(404, "SLS-0404", "Intake error not found");
  const v = versionOf(req);
  if (v !== undefined && v !== e.version) return stale();
  if (e.status !== "OPEN") return reply.problem(409, "SLS-0117", "The intake error is already resolved");
  const note = String((req.body as { note?: string } | undefined)?.note ?? "");
  const next = intakeErrors.update(e.id, { status: "RESOLVED", resolvedBy: SALES_ACTOR, resolvedAt: at(0), resolutionNote: note, version: e.version + 1 })!;
  return reply.versioned(next, next.version);
});

// ---- Product listings ---------------------------------------------------------------------------------------

let listingSeq = 0;
function listing(scope: "CUSTOMER" | "GROUP", key: string, item: string, type: "ALLOW" | "DENY", ago: number, from?: number, to?: number): ProductListing {
  return {
    id: uid(K_LISTING, ++listingSeq),
    legalEntityId: LEGAL_ENTITY.id,
    scopeType: scope,
    scopeKey: key,
    productId: itemByNumber(item).id,
    listingType: type,
    validFrom: from === undefined ? undefined : day(from),
    validTo: to === undefined ? undefined : day(to),
    createdBy: ago > 30 ? "dana.whitfield" : SALES_ACTOR,
    createdAt: at(-ago),
  };
}

const listings = table<ProductListing>([
  listing("CUSTOMER", CUSTOMERS[4].id, "BP-300", "DENY", 3, -3),
  listing("CUSTOMER", CUSTOMERS[3].id, "DP-100", "DENY", 41, -41),
  listing("GROUP", "MUNICIPAL", "CP-400", "ALLOW", 64, -64),
  listing("GROUP", "MUNICIPAL", "CP-200", "ALLOW", 64, -64),
  listing("GROUP", "INDUSTRIAL", "BP-300", "ALLOW", 90, -90),
  listing("GROUP", "AGRICULTURE", "SP-150", "ALLOW", 120, -120),
  listing("CUSTOMER", CUSTOMERS[2].id, "SA-IMP-06", "ALLOW", 18, -18, 347),
  listing("GROUP", "DEALER", "SEAL-MK-125", "DENY", 9, 21, 202),
]);

route("GET", `${BASE}/product-listings`, ({ query }) => {
  const productId = query.get("productId");
  const scopeKey = query.get("scopeKey");
  const limit = Number(query.get("limit") ?? 200) || 200;
  return { items: listings.all().filter((l) => (!productId || l.productId === productId) && (!scopeKey || l.scopeKey === scopeKey)).sort((a, b) => b.createdAt.localeCompare(a.createdAt)).slice(0, limit) };
});

route("POST", `${BASE}/product-listings`, ({ body }) => {
  const input = body as ProductListingInput;
  if (listings.all().some((l) => l.scopeType === input.scopeType && l.scopeKey === input.scopeKey && l.productId === input.productId && l.listingType === input.listingType)) {
    return reply.problem(409, "SLS-0072", "The same entry already exists");
  }
  return reply.created(listings.insert({ ...input, id: uid(K_LISTING, 100 + listings.all().length), createdBy: USER.name, createdAt: at(0) }));
});

route("DELETE", `${BASE}/product-listings/{id}`, ({ params }) => (listings.remove(params.id) ? reply.noContent() : reply.problem(404, "SLS-0404", "Listing entry not found")));
