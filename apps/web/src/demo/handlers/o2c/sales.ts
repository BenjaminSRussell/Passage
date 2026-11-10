// Sales service (/api/v1/sales) for the demo. Shapes are typed against the module's own client types, so a
// fixture that drifts from the real response fails `tsc` instead of failing in the browser.
import type {
  BackorderItem,
  CreditExposureRow,
  CreditProfile,
  Hold,
  HoldQueueItem,
  HoldType,
  NewOrder,
  Order,
  OrderLine,
  OrderSummary,
  SalesSummary,
  UtilizationStatus,
} from "../../../modules/sales/api/client";
import { CUSTOMERS, LEGAL_ENTITY, PLANT, WH_FG, itemByNumber } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, filterBy, uid } from "../../store";
import { K, SALES_ACTOR, balanceOf, creditProfiles, customerById, evaluateCredit, exposureOf, itemById, orderNumber, orderSeq, orders, progress, r2, recordVersion, scheduleLines, versions } from "./model";

const BASE = "/api/v1/sales";

const stale = () => reply.problem(412, "SLS-0006", "The order changed since you opened it. Reload it and try again.");
const notFound = () => reply.problem(404, "SLS-0404", "Sales order not found");
const versionOf = (header: string | null): number | undefined => {
  const m = /(\d+)/.exec(header ?? "");
  return m ? Number(m[1]) : undefined;
};

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

const sumNet = (lines: OrderLine[]) => dec(lines.filter((l) => l.lineStatus !== "CANCELLED").reduce((s, l) => s + Number(l.lineNet), 0));

/** Moves a checked order to CONFIRMED: lines get their confirmed dates and the version moves. */
function confirmed(o: Order, reason: string): Order {
  const lines = o.lines.map((l) => {
    if (l.lineStatus === "CANCELLED") return l;
    const ship = l.requestedShipDate < day(0) ? day(0) : l.requestedShipDate;
    return { ...l, lineStatus: "CONFIRMED", confirmedShipDate: ship, confirmedReceiptDate: new Date(Date.parse(ship) + 3 * 86_400_000).toISOString().slice(0, 10) };
  });
  const next: Order = { ...o, status: "CONFIRMED", lines, version: o.version + 1, updatedAt: at(0) };
  recordVersion(next, reason, [{ lineNo: 0, field: "status", old: o.status, new: "CONFIRMED" }]);
  return next;
}

// ---- Orders ----

route("GET", `${BASE}/orders`, ({ query }) => {
  const hold = query.get("hold");
  const rest = new URLSearchParams(query);
  rest.delete("hold");
  const limit = Number(query.get("limit") ?? 0);
  const rows = filterBy(orders.all(), rest)
    .filter((o) => !hold || o.holds.some((h) => h.status === "ACTIVE" && h.holdType === hold))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items: (limit > 0 ? rows.slice(0, limit) : rows).map(summary) };
});

route("POST", `${BASE}/orders`, ({ body }) => {
  const input = body as NewOrder;
  const errors: Record<string, string> = {};
  const customer = customerById(input.customerId);
  // Any legal entity, site or warehouse id is accepted and mapped to the demo company's, so only the customer and products need real ids.
  if (!customer) errors.customerId = `Unknown customer. Try ${CUSTOMERS[0].id} (${CUSTOMERS[0].name}), or copy one from an order's detail page.`;
  const lines: OrderLine[] = (input.lines ?? []).map((l, i) => {
    const item = itemById(l.productId);
    if (!item) errors[`lines[${i}].productId`] = `Unknown product. Try ${itemByNumber("CP-200").id} (CP-200), or copy one from an order's line table.`;
    const qty = Number(l.quantity);
    const disc = Number(l.discPct ?? 0);
    return {
      id: uid(K.line, 5000 + Math.floor(Math.random() * 1e6)),
      lineNo: i + 1,
      productId: l.productId,
      quantity: l.quantity,
      unit: l.unit,
      siteId: PLANT.id,
      warehouseId: WH_FG.id,
      unitPrice: l.unitPrice,
      discPct: dec(disc),
      lineNet: dec(r2(qty * Number(l.unitPrice) * (1 - disc / 100))),
      requestedShipDate: l.requestedShipDate,
      lineStatus: "OPEN",
      deliveredQty: "0",
      invoicedQty: "0",
    };
  });
  if (Object.keys(errors).length > 0) return reply.problem(422, "COM-0422", "The order has fields the service refuses.", errors);
  const next = Math.max(...orders.all().map(orderSeq)) + 1;
  const created = orders.insert({
    id: uid(K.order, next),
    legalEntityId: LEGAL_ENTITY.id,
    orderNumber: orderNumber(next),
    customerId: input.customerId,
    invoiceAccountId: input.invoiceAccountId || input.customerId,
    orderType: input.orderType ?? "ORDER",
    status: "DRAFT",
    currency: input.currency,
    requestedShipDate: input.requestedShipDate,
    channel: "MANUAL",
    externalRef: input.externalRef,
    netTotal: sumNet(lines),
    version: 1,
    lines,
    holds: [],
    createdAt: at(0),
    updatedAt: at(0),
  });
  recordVersion(created, "Order entered", []);
  return reply.versioned({ ...created, warnings: [] }, 1, 201);
});

// Literal and colon actions before the {id} read.
route("POST", `${BASE}/orders/{id}:confirm`, ({ params, headers }) => {
  const o = orders.get(params.id);
  if (!o) return notFound();
  const v = versionOf(headers.get("If-Match"));
  if (v !== undefined && v !== o.version) return stale();
  if (o.status !== "DRAFT" && o.status !== "OPEN") return reply.problem(409, "SLS-0005", `Only a draft or open order can be confirmed; this one is ${o.status.toLowerCase().replace("_", " ")}.`);
  if (o.holds.some((h) => h.status === "ACTIVE" && (h.holdType === "CREDIT" || h.holdType === "FRAUD"))) return reply.problem(409, "SLS-0010", "An active credit or fraud hold blocks confirmation.");
  const check = evaluateCredit(o);
  if (check.outcome === "FAIL") {
    const failed = check.rules.find((r) => r.result === "FAIL");
    const hold: Hold = { id: uid(K.hold, 100 + Math.floor(Math.random() * 1e6)), holdType: "CREDIT", reasonCode: failed?.id === "CR-1" ? "CREDIT_LIMIT" : "CREDIT_RULE", status: "ACTIVE", orderAmount: o.netTotal, appliedBy: "system", appliedAt: at(0) };
    const next: Order = { ...o, status: "OPEN", holds: [...o.holds, hold], creditCheck: check, version: o.version + 1, updatedAt: at(0) };
    recordVersion(next, "Credit hold placed at confirmation", [{ lineNo: 0, field: "status", old: o.status, new: "OPEN" }], "system");
    orders.update(o.id, next);
    return reply.versioned({ ...next, confirmed: false }, next.version);
  }
  const next = { ...confirmed(o, "Confirmed after credit check"), creditCheck: check };
  orders.update(o.id, next);
  return reply.versioned({ ...next, confirmed: true }, next.version);
});

route("POST", `${BASE}/orders/{id}:cancel`, ({ params, headers, body }) => {
  const o = orders.get(params.id);
  if (!o) return notFound();
  const v = versionOf(headers.get("If-Match"));
  if (v !== undefined && v !== o.version) return stale();
  if (!["DRAFT", "OPEN", "CONFIRMED"].includes(o.status)) return reply.problem(409, "SLS-0005", "An order that has shipped can no longer be cancelled.");
  const next: Order = { ...o, status: "CANCELLED", lines: o.lines.map((l) => ({ ...l, lineStatus: "CANCELLED" })), version: o.version + 1, updatedAt: at(0) };
  recordVersion(next, (body as { reason?: string } | undefined)?.reason || "Cancelled", [{ lineNo: 0, field: "status", old: o.status, new: "CANCELLED" }]);
  orders.update(o.id, next);
  return reply.versioned(next, next.version);
});

route("POST", `${BASE}/orders/{id}/holds/{holdId}:release`, ({ params, headers }) => {
  const o = orders.get(params.id);
  if (!o) return notFound();
  const v = versionOf(headers.get("If-Match"));
  if (v !== undefined && v !== o.version) return stale();
  const hold = o.holds.find((h) => h.id === params.holdId && h.status === "ACTIVE");
  if (!hold) return reply.problem(404, "SLS-0404", "That hold is not active.");
  const released: Hold = { ...hold, status: "RELEASED", releasedBy: SALES_ACTOR, releasedUntil: at(7) };
  let next: Order = { ...o, holds: o.holds.map((h) => (h.id === hold.id ? released : h)), version: o.version + 1, updatedAt: at(0) };
  const blocked = next.holds.some((h) => h.status === "ACTIVE" && (h.holdType === "CREDIT" || h.holdType === "FRAUD"));
  const confirm = hold.holdType === "CREDIT" && (o.status === "DRAFT" || o.status === "OPEN") && !blocked;
  if (confirm) next = confirmed({ ...next, version: o.version }, "Credit hold released and order confirmed");
  else recordVersion(next, `${hold.holdType} hold released`, []);
  orders.update(o.id, next);
  return reply.versioned({ ...next, confirmed: confirm }, next.version);
});

route("POST", `${BASE}/orders/{id}/holds/{holdId}:reject`, ({ params, headers, body }) => {
  const o = orders.get(params.id);
  if (!o) return notFound();
  const v = versionOf(headers.get("If-Match"));
  if (v !== undefined && v !== o.version) return stale();
  const comment = (body as { comment?: string } | undefined)?.comment ?? "";
  const next: Order = {
    ...o,
    status: "CANCELLED",
    lines: o.lines.map((l) => ({ ...l, lineStatus: "CANCELLED" })),
    holds: o.holds.map((h) => (h.id === params.holdId ? { ...h, status: "REJECTED", rejectedBy: SALES_ACTOR, rejectedAt: at(0), rejectionComment: comment } : h)),
    version: o.version + 1,
    updatedAt: at(0),
  };
  recordVersion(next, "Credit hold rejected, order cancelled", [{ lineNo: 0, field: "status", old: o.status, new: "CANCELLED" }]);
  orders.update(o.id, next);
  return reply.versioned(next, next.version);
});

route("POST", `${BASE}/orders/{id}/holds`, ({ params, headers, body }) => {
  const o = orders.get(params.id);
  if (!o) return notFound();
  const v = versionOf(headers.get("If-Match"));
  if (v !== undefined && v !== o.version) return stale();
  const input = body as { holdType: HoldType; reasonCode: string };
  const hold: Hold = { id: uid(K.hold, 100 + Math.floor(Math.random() * 1e6)), holdType: input.holdType, reasonCode: input.reasonCode, status: "ACTIVE", orderAmount: o.netTotal, appliedBy: SALES_ACTOR, appliedAt: at(0) };
  const next: Order = { ...o, holds: [...o.holds, hold], version: o.version + 1, updatedAt: at(0) };
  recordVersion(next, `${input.holdType} hold applied`, []);
  orders.update(o.id, next);
  return reply.versioned(next, next.version, 201);
});

route("PATCH", `${BASE}/orders/{id}`, ({ params, headers, body }) => {
  const o = orders.get(params.id);
  if (!o) return notFound();
  const v = versionOf(headers.get("If-Match"));
  if (v !== undefined && v !== o.version) return stale();
  const input = body as { reason?: string; requestedShipDate?: string; lines?: { lineNo: number; quantity?: string; unitPrice?: string; discPct?: string; requestedShipDate?: string }[]; removeLines?: number[] };
  const diff: { lineNo: number; field: string; old: string; new: string }[] = [];
  if (input.requestedShipDate && input.requestedShipDate !== o.requestedShipDate) diff.push({ lineNo: 0, field: "requestedShipDate", old: o.requestedShipDate, new: input.requestedShipDate });
  const lines = o.lines.map((l) => {
    let next = { ...l };
    const change = input.lines?.find((c) => c.lineNo === l.lineNo);
    if (change) {
      for (const field of ["quantity", "unitPrice", "discPct", "requestedShipDate"] as const) {
        const value = change[field];
        if (value !== undefined && value !== l[field]) {
          diff.push({ lineNo: l.lineNo, field, old: String(l[field]), new: value });
          next = { ...next, [field]: value };
        }
      }
      next.lineNet = dec(r2(Number(next.quantity) * Number(next.unitPrice) * (1 - Number(next.discPct) / 100)));
    }
    if (input.removeLines?.includes(l.lineNo)) {
      diff.push({ lineNo: l.lineNo, field: "lineStatus", old: l.lineStatus, new: "CANCELLED" });
      next = { ...next, lineStatus: "CANCELLED" };
    }
    return next;
  });
  const next: Order = { ...o, requestedShipDate: input.requestedShipDate ?? o.requestedShipDate, lines, netTotal: sumNet(lines), version: o.version + 1, updatedAt: at(0) };
  recordVersion(next, input.reason || "Order changed", diff);
  orders.update(o.id, next);
  return reply.versioned(next, next.version);
});

route("GET", `${BASE}/orders/{id}/versions`, ({ params }) => ({ items: versions.get(params.id) ?? [] }));

route("GET", `${BASE}/orders/{id}/schedule-lines`, ({ params }) => {
  const o = orders.get(params.id);
  return { items: o ? scheduleLines(o) : [] };
});

route("GET", `${BASE}/orders/{id}`, ({ params }) => {
  const o = orders.get(params.id);
  return o ? reply.versioned(o, o.version) : notFound();
});

route("GET", `${BASE}/order-progress`, ({ query }) => {
  const rows = filterBy(progress.all(), query).sort((a, b) => b.recordedAt.localeCompare(a.recordedAt));
  return { items: rows };
});

route("GET", `${BASE}/delivery-tolerances:resolve`, () => ({ source: "DEFAULT", overdeliveryPct: "5", underdeliveryPct: "5", partialDelivery: "ALLOWED" }));

// ---- Credit ----

function holdQueue(): HoldQueueItem[] {
  return orders
    .all()
    .flatMap((o) =>
      o.holds
        .filter((h) => h.status === "ACTIVE")
        .map((h): HoldQueueItem => {
          const profile = creditProfiles.find((p) => p.customerId === o.customerId);
          return {
            holdId: h.id,
            legalEntityId: o.legalEntityId,
            orderId: o.id,
            orderNumber: o.orderNumber,
            orderVersion: o.version,
            orderStatus: o.status,
            customerId: o.customerId,
            holdType: h.holdType,
            reasonCode: h.reasonCode,
            amount: h.orderAmount,
            currency: o.currency,
            appliedBy: h.appliedBy,
            appliedAt: h.appliedAt,
            slaDueAt: new Date(Date.parse(h.appliedAt) + 2 * 86_400_000).toISOString(),
            failedRule: o.creditCheck?.rules.find((r) => r.result === "FAIL"),
            creditLimit: profile?.limit,
            limitCurrency: profile?.currency,
          };
        }),
    )
    .sort((a, b) => a.appliedAt.localeCompare(b.appliedAt));
}

route("GET", `${BASE}/holds`, ({ query }) => {
  const type = query.get("holdType");
  const le = query.get("legalEntityId");
  return { items: holdQueue().filter((h) => (!type || h.holdType === type) && (!le || h.legalEntityId === le)) };
});

function exposureRows(): CreditExposureRow[] {
  return creditProfiles
    .map((p): CreditExposureRow => {
      const e = exposureOf(p.customerId);
      const balance = balanceOf(p.limitType, e);
      const limit = Number(p.limit);
      const threshold = r2(limit * (1 + Number(p.tolerancePct) / 100));
      const status: UtilizationStatus = limit === 0 ? "NO_LIMIT" : balance <= limit ? "WITHIN" : balance <= threshold ? "OVER_LIMIT" : "OVER_TOLERANCE";
      return {
        legalEntityId: p.legalEntityId,
        customerId: p.customerId,
        currency: p.currency,
        limitType: p.limitType,
        limit: p.limit,
        threshold: dec(threshold),
        exposure: {
          openAr: dec(e.openAr),
          unapplied: dec(e.unapplied),
          deliveredNotInvoiced: dec(e.deliveredNotInvoiced),
          openOrders: dec(e.openOrders),
          overdueAr: dec(e.overdueAr),
          oldestItemDays: e.oldestItemDays,
          dunningLevel: e.dunningLevel,
        },
        balance: dec(balance),
        utilizationPct: limit === 0 ? undefined : dec((balance / limit) * 100),
        status,
        understated: false,
        reviewDate: p.reviewDate,
      };
    })
    .sort((a, b) => Number(b.utilizationPct ?? 0) - Number(a.utilizationPct ?? 0));
}

route("GET", `${BASE}/credit-exposure`, ({ query }) => {
  const min = Number(query.get("minUtilizationPct") ?? 0);
  const rows = exposureRows().filter(
    (r) =>
      (!query.get("customerId") || r.customerId === query.get("customerId")) &&
      (!query.get("legalEntityId") || r.legalEntityId === query.get("legalEntityId")) &&
      (!query.get("status") || r.status === query.get("status")) &&
      Number(r.utilizationPct ?? 0) >= min,
  );
  return { items: rows, total: rows.length, truncated: false };
});

function profileOf(customerId: string): CreditProfile | undefined {
  const p = creditProfiles.find((x) => x.customerId === customerId);
  if (!p) return undefined;
  const e = exposureOf(customerId);
  return { ...p, exposure: { openAr: dec(e.openAr), unapplied: dec(e.unapplied), deliveredNotInvoiced: dec(e.deliveredNotInvoiced), openOrders: dec(e.openOrders), overdueAr: dec(e.overdueAr), oldestItemDays: e.oldestItemDays, dunningLevel: e.dunningLevel } };
}

route("GET", `${BASE}/credit-profiles`, ({ query }) => {
  const due = query.get("reviewDueBy");
  const items = creditProfiles.map((p) => profileOf(p.customerId)!).filter((p) => !due || (p.reviewDate ?? "9999") <= due);
  return { items };
});
route("GET", `${BASE}/credit-profiles/{customerId}`, ({ params }) => {
  const p = profileOf(params.customerId);
  return p ? reply.versioned(p, p.version) : reply.problem(404, "SLS-0404", "No credit profile for this customer");
});

// ---- Workspace ----

/** Open quotations are fixed sample figures; the other tiles count the live tables. */
let lapsed = 1;
route("GET", `${BASE}/summary`, ({ query }) => {
  const windowDays = Number(query.get("windowDays") ?? 90);
  const status = query.get("status");
  const pipeline = status === "SENT" ? { openCount: 3, openNet: "152880.00", weightedNet: "91728.00" } : status === "DRAFT" ? { openCount: 2, openNet: "58640.00", weightedNet: "23456.00" } : { openCount: 5, openNet: "211520.00", weightedNet: "115184.00" };
  const body: SalesSummary = {
    activeHolds: orders.all().reduce((n, o) => n + o.holds.filter((h) => h.status === "ACTIVE").length, 0),
    backorderLines: backorders().length,
    quotationsExpiring: 2,
    quotationsLapsed: lapsed,
    openIntakeErrors: 0,
    pipeline: [{ currency: "USD", ...pipeline }],
    windowDays,
    won: Math.round((windowDays / 90) * 11),
    lost: Math.round((windowDays / 90) * 4),
    expired: Math.round((windowDays / 90) * 3),
    conversionRatePct: "61.1",
  };
  return body;
});

route("POST", `${BASE}/quotations:expire`, () => {
  const count = lapsed;
  lapsed = 0;
  return {
    count,
    more: false,
    expired: count > 0 ? [{ id: uid(112, 7), quotationNumber: "SQ-004417", legalEntityId: LEGAL_ENTITY.id, customerId: CUSTOMERS[2].id, validUntil: day(-3), previousStatus: "SENT", version: 2 }] : [],
  };
});

function backorders(): BackorderItem[] {
  return orders
    .all()
    .filter((o) => o.status === "CONFIRMED")
    .flatMap((o) =>
      scheduleLines(o)
        .filter((s) => s.status === "BACKORDER")
        .map((s): BackorderItem => {
          const l = o.lines.find((x) => x.lineNo === s.lineNo)!;
          return {
            scheduleLineId: s.id,
            legalEntityId: o.legalEntityId,
            orderId: o.id,
            orderNumber: o.orderNumber,
            orderVersion: o.version,
            customerId: o.customerId,
            lineId: l.id,
            lineNo: l.lineNo,
            productId: l.productId,
            siteId: l.siteId,
            warehouseId: l.warehouseId,
            quantity: s.quantity,
            requestedShipDate: l.requestedShipDate,
            promisedDate: s.deliveryDate,
            daysLate: Math.round((Date.parse(s.deliveryDate ?? l.requestedShipDate) - Date.parse(l.requestedShipDate)) / 86_400_000),
            status: "BACKORDER",
          };
        }),
    );
}
route("GET", `${BASE}/backorders`, () => ({ items: backorders() }));

// ---- Order entry helpers ----

route("POST", `${BASE}/product-proposals:evaluate`, () => ({ items: [] }));
route("POST", `${BASE}/product-proposals:respond`, ({ body }) => ({ id: uid(K.change, 1), ...(body as object), orderedProductId: itemByNumber("CP-200").id, proposedProductId: itemByNumber("SEAL-MK-125").id, reason: "CROSS_SELL", respondedBy: SALES_ACTOR, respondedAt: at(0) }));
