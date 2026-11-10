// Purchasing service (/api/v1/procurement) for the demo: requisitions, purchase orders and the workspace lists.
// Typed against the module's own client types so shape drift fails `tsc`.
import type {
  ApprovalStatus,
  ChangeOrder,
  ExpiringAgreement,
  NearLimitLine,
  OpenOrderLine,
  OverdueLine,
  PurchaseAgreement,
  PurchaseAgreementSummary,
  PurchaseOrder,
  PurchaseOrderCharge,
  PurchaseOrderInput,
  PurchaseOrderLine,
  PurchaseOrderSummary,
  PurchaseOrderVersion,
  Requisition,
  RequisitionInput,
  RequisitionApproval,
  RequisitionHistory,
  RequisitionLine,
  RequisitionStatus,
  RequisitionSummary,
  SourcingLine,
  TradeAgreement,
  VendorOpenOrders,
} from "../../../modules/purchasing/api/client";
import { ITEMS, LEGAL_ENTITY, PLANT, VENDORS, WH_RAW, WORKERS, itemByNumber } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, table, uid } from "../../store";

const BASE = "/api/v1/procurement";
const K_REQ = 300;
const K_REQ_LINE = 301;
const K_PO = 302;
const K_PO_LINE = 303;
const K_HIST = 304;
const K_TA = 305;
const K_AGR = 306;

const CHAIN = ["BUYER_LEAD", "FINANCE"];
const requester = (n: number) => WORKERS[n % WORKERS.length].id;
const description = (number: string) => {
  const it = itemByNumber(number);
  return it.name;
};
const categoryOf = (number: string) => {
  if (number.startsWith("MTR")) return "Motors";
  if (number.startsWith("CST")) return "Castings";
  if (number.startsWith("SEAL")) return "Seals";
  if (number.startsWith("BRG")) return "Bearings";
  if (number.startsWith("FST")) return "Fasteners";
  return "Raw materials";
};
const vendorOf = (number: string) => VENDORS[itemByNumber(number).vendor ?? 0];
const etagOf = (version: number) => version;

// ---- Requisitions -------------------------------------------------------------------------------------------

let reqLineSeq = 0;
function rline(lineNo: number, number: string, qty: number, needIn: number, opts: { vendor?: boolean; status?: string; po?: { id: string; lineNo: number } } = {}): RequisitionLine {
  const it = itemByNumber(number);
  return {
    id: uid(K_REQ_LINE, ++reqLineSeq),
    lineNo,
    productId: it.id,
    description: description(number),
    category: categoryOf(number),
    qty: dec(qty, 0),
    unit: it.unit,
    estimatedUnitPrice: dec(it.cost),
    amount: dec(it.cost * qty),
    requestedDate: day(needIn),
    siteId: PLANT.id,
    warehouseId: WH_RAW.id,
    preferredVendorId: opts.vendor === false ? undefined : vendorOf(number).id,
    status: opts.status ?? "OPEN",
    purchaseOrderId: opts.po?.id,
    purchaseOrderLineNo: opts.po?.lineNo,
  };
}

function hist(reqN: number, i: number, action: RequisitionHistory["action"], from: string, to: string, ago: number, extra: Partial<RequisitionHistory> = {}): RequisitionHistory {
  return { id: uid(K_HIST, reqN * 10 + i), action, fromStatus: from, toStatus: to, requisitionVersion: i + 1, createdAt: at(-ago), actorId: requester(reqN), ...extra };
}

function req(n: number, status: RequisitionStatus, ago: number, lines: RequisitionLine[], approvedSteps: number, history: RequisitionHistory[]): Requisition {
  const total = lines.reduce((s, l) => s + Number(l.amount), 0);
  return {
    id: uid(K_REQ, n),
    number: `REQ-${String(7100 + n).padStart(6, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    requesterId: requester(n),
    status,
    currency: "USD",
    total: dec(total),
    approverChain: CHAIN,
    approvedSteps: status === "IN_REVIEW" ? approvedSteps : status === "APPROVED" || status === "CLOSED" ? CHAIN.length : 0,
    nextApprover: status === "IN_REVIEW" ? CHAIN[approvedSteps] : undefined,
    policyVersion: "2026.1",
    version: history.length + 1,
    lines,
    history,
    createdAt: at(-ago),
    updatedAt: at(-ago + 0.2),
  };
}

const requisitions = table<Requisition>([
  req(
    1,
    "IN_REVIEW",
    1.5,
    [rline(1, "CST-IMP-06", 160, 24), rline(2, "MTR-15HP-TEFC", 6, 28)],
    0,
    [hist(1, 0, "SUBMITTED", "DRAFT", "IN_REVIEW", 1.4)],
  ),
  req(
    2,
    "IN_REVIEW",
    3,
    [rline(1, "SEAL-MK-125", 300, 14), rline(2, "BRG-6206", 800, 14)],
    1,
    [hist(2, 0, "SUBMITTED", "DRAFT", "IN_REVIEW", 2.9), hist(2, 1, "APPROVED", "IN_REVIEW", "IN_REVIEW", 2.2, { stepNo: 1, role: "BUYER_LEAD" })],
  ),
  req(
    3,
    "APPROVED",
    6,
    [rline(1, "CST-VOL-04", 50, 30, { vendor: false }), rline(2, "RM-EPOXY-BLU", 40, 12, { vendor: false })],
    2,
    [
      hist(3, 0, "SUBMITTED", "DRAFT", "IN_REVIEW", 5.9),
      hist(3, 1, "APPROVED", "IN_REVIEW", "IN_REVIEW", 5.2, { stepNo: 1, role: "BUYER_LEAD" }),
      hist(3, 2, "APPROVED", "IN_REVIEW", "APPROVED", 4.6, { stepNo: 2, role: "FINANCE" }),
    ],
  ),
  req(
    4,
    "APPROVED",
    9,
    [rline(1, "FST-KIT-A", 300, 10), rline(2, "RM-SS316-BAR", 600, 14)],
    2,
    [
      hist(4, 0, "SUBMITTED", "DRAFT", "IN_REVIEW", 8.9),
      hist(4, 1, "APPROVED", "IN_REVIEW", "IN_REVIEW", 8.1, { stepNo: 1, role: "BUYER_LEAD" }),
      hist(4, 2, "APPROVED", "IN_REVIEW", "APPROVED", 7.5, { stepNo: 2, role: "FINANCE" }),
    ],
  ),
  req(5, "DRAFT", 0.4, [rline(1, "MTR-5HP-TEFC", 20, 30)], 0, []),
  req(
    6,
    "REJECTED",
    12,
    [rline(1, "MTR-15HP-TEFC", 20, 20)],
    0,
    [hist(6, 0, "SUBMITTED", "DRAFT", "IN_REVIEW", 11.9), hist(6, 1, "REJECTED", "IN_REVIEW", "REJECTED", 10.5, { stepNo: 1, role: "BUYER_LEAD", comment: "Quantity is above the six-month need; resubmit for 8." })],
  ),
  req(
    7,
    "CLOSED",
    24,
    [rline(1, "MTR-5HP-TEFC", 24, -10, { status: "CONSOLIDATED", po: { id: uid(K_PO, 3), lineNo: 1 } })],
    2,
    [
      hist(7, 0, "SUBMITTED", "DRAFT", "IN_REVIEW", 23.9),
      hist(7, 1, "APPROVED", "IN_REVIEW", "IN_REVIEW", 23.2, { stepNo: 1, role: "BUYER_LEAD" }),
      hist(7, 2, "APPROVED", "IN_REVIEW", "APPROVED", 22.8, { stepNo: 2, role: "FINANCE" }),
    ],
  ),
]);

function reqSummary(r: Requisition): RequisitionSummary {
  return { id: r.id, number: r.number, legalEntityId: r.legalEntityId, requesterId: r.requesterId, status: r.status, currency: r.currency, total: r.total, lineCount: r.lines.length, version: r.version, createdAt: r.createdAt };
}

route("GET", `${BASE}/requisitions`, ({ query }) => {
  const status = query.get("status");
  const rows = requisitions
    .all()
    .filter((r) => !status || r.status === status)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items: rows.map(reqSummary) };
});

route("POST", `${BASE}/requisitions`, ({ body }) => {
  const input = body as RequisitionInput;
  const n = requisitions.all().length + 1;
  const lines: RequisitionLine[] = input.lines.map((l, i) => {
    const it = ITEMS.find((x) => x.id === l.productId);
    const price = l.estimatedUnitPrice ? Number(l.estimatedUnitPrice) : (it?.cost ?? 0);
    return {
      id: uid(K_REQ_LINE, 1000 + n * 20 + i),
      lineNo: i + 1,
      productId: l.productId,
      description: l.description || it?.name || "",
      category: l.category || (it ? categoryOf(it.number) : ""),
      qty: dec(Number(l.qty), 0),
      unit: l.unit,
      estimatedUnitPrice: dec(price),
      amount: dec(price * Number(l.qty)),
      requestedDate: l.requestedDate,
      siteId: l.siteId,
      warehouseId: l.warehouseId,
      preferredVendorId: l.preferredVendorId || (it?.vendor !== undefined ? VENDORS[it.vendor].id : undefined),
      status: "OPEN",
    };
  });
  const made = req(n, "DRAFT", 0, lines, 0, []);
  const created: Requisition = { ...made, legalEntityId: input.legalEntityId, currency: input.currency, requesterId: requester(0) };
  requisitions.insert(created);
  return reply.versioned(created, created.version, 201);
});

route("GET", `${BASE}/requisition-approvals`, () => {
  const items: RequisitionApproval[] = requisitions
    .all()
    .filter((r) => r.status === "IN_REVIEW")
    .map((r) => ({
      id: r.id,
      number: r.number,
      legalEntityId: r.legalEntityId,
      requesterId: r.requesterId,
      currency: r.currency,
      total: r.total,
      lineCount: r.lines.length,
      approvedSteps: r.approvedSteps ?? 0,
      chainLength: r.approverChain.length,
      nextApprover: r.nextApprover ?? "",
      version: r.version,
      submittedAt: r.history?.[0]?.createdAt ?? r.createdAt,
    }));
  return { items };
});

route("GET", `${BASE}/requisition-lines`, ({ query }) => {
  const sourcing = query.get("sourcing");
  const items: SourcingLine[] = [];
  for (const r of requisitions.all()) {
    if (r.status !== "APPROVED") continue;
    for (const l of r.lines) {
      if (l.status !== "OPEN") continue;
      if (sourcing === "NEEDS_SOURCING" && l.preferredVendorId) continue;
      if (sourcing === "HAS_VENDOR" && !l.preferredVendorId) continue;
      items.push({ ...l, requisitionId: r.id, requisitionNumber: r.number, requisitionStatus: r.status, legalEntityId: r.legalEntityId, currency: r.currency, requisitionVersion: r.version });
    }
  }
  return { items };
});

route("GET", `${BASE}/requisitions/{id}`, ({ params }) => {
  const r = requisitions.get(params.id);
  return r ? reply.versioned(r, r.version) : reply.problem(404, "PRC-REQ-NOT-FOUND", "Requisition not found");
});

route("POST", `${BASE}/requisitions/{id}:{action}`, ({ params, body }) => {
  const r = requisitions.get(params.id);
  if (!r) return reply.problem(404, "PRC-REQ-NOT-FOUND", "Requisition not found");
  const comment = (body as { comment?: string } | undefined)?.comment;
  const from = r.status;
  const entry = (action: RequisitionHistory["action"], to: string, extra: Partial<RequisitionHistory> = {}): RequisitionHistory => ({
    id: uid(K_HIST, 900 + (r.history?.length ?? 0) + r.version),
    action,
    fromStatus: from,
    toStatus: to,
    requisitionVersion: r.version + 1,
    createdAt: at(0),
    actorId: "dev",
    ...extra,
  });
  let next: Requisition;
  switch (params.action) {
    case "submit":
      if (from !== "DRAFT") return reply.problem(409, "PRC-REQ-004", "Only a draft can be submitted");
      next = { ...r, status: "IN_REVIEW", approvedSteps: 0, nextApprover: CHAIN[0], history: [...(r.history ?? []), entry("SUBMITTED", "IN_REVIEW")] };
      break;
    case "approve": {
      if (from !== "IN_REVIEW") return reply.problem(409, "PRC-REQ-004", "Only a requisition in review can be approved");
      const steps = (r.approvedSteps ?? 0) + 1;
      const done = steps >= r.approverChain.length;
      next = {
        ...r,
        status: done ? "APPROVED" : "IN_REVIEW",
        approvedSteps: steps,
        nextApprover: done ? undefined : r.approverChain[steps],
        history: [...(r.history ?? []), entry("APPROVED", done ? "APPROVED" : "IN_REVIEW", { stepNo: steps, role: r.approverChain[steps - 1], comment })],
      };
      break;
    }
    case "reject":
      if (!comment) return reply.problem(422, "PRC-REQ-005", "A comment is required to reject", { comment: "Say why the requisition is rejected." });
      next = { ...r, status: "REJECTED", nextApprover: undefined, history: [...(r.history ?? []), entry("REJECTED", "REJECTED", { comment })] };
      break;
    case "reopen":
    case "withdraw":
      next = { ...r, status: "DRAFT", approvedSteps: 0, nextApprover: undefined, history: [...(r.history ?? []), entry(params.action === "reopen" ? "REOPENED" : "WITHDRAWN", "DRAFT")] };
      break;
    case "cancel":
      next = { ...r, status: "CANCELLED", nextApprover: undefined, history: [...(r.history ?? []), entry("CANCELLED", "CANCELLED", { comment })] };
      break;
    default:
      return reply.problem(404, "PRC-REQ-404", `Unknown action ${params.action}`);
  }
  next.version = r.version + 1;
  next.updatedAt = at(0);
  requisitions.update(r.id, next);
  return reply.versioned(next, next.version);
});

// ---- Purchase orders ----------------------------------------------------------------------------------------

let poLineSeq = 0;
function pline(lineNo: number, number: string, qty: number, deliveryIn: number, received = 0, opts: { price?: number; cancelled?: boolean } = {}): PurchaseOrderLine {
  const it = itemByNumber(number);
  const price = opts.price ?? it.cost;
  const gross = price * qty;
  const status = opts.cancelled ? "CANCELLED" : received >= qty ? "RECEIVED" : received > 0 ? "PARTIALLY_RECEIVED" : "OPEN";
  return {
    id: uid(K_PO_LINE, ++poLineSeq),
    lineNo,
    productId: it.id,
    description: description(number),
    category: categoryOf(number),
    siteId: PLANT.id,
    warehouseId: WH_RAW.id,
    qty: dec(qty, 0),
    unit: it.unit,
    unitPrice: dec(price),
    priceUnit: "1",
    discountPct: "0.00",
    grossAmount: dec(gross),
    discountAmount: "0.00",
    lineAmount: dec(gross),
    taxAmount: "0.00",
    deliveryDate: day(deliveryIn),
    priceSource: "TRADE_AGREEMENT",
    receivedQty: dec(received, 0),
    invoicedQty: received >= qty ? dec(qty, 0) : "0",
    status,
  };
}

const freight = (seq: number, amount: number): PurchaseOrderCharge => ({ seq, calc: "FIXED", value: dec(amount), amount: dec(amount), chargeCode: "FREIGHT", debitType: "ITEM", creditType: "VENDOR", allocationRule: "AMOUNT" });

function version(po: { total: string; currency: string; lines: PurchaseOrderLine[] }, ago: number): PurchaseOrderVersion[] {
  return [
    {
      versionNo: 1,
      confirmedAt: at(-ago),
      confirmedBy: "dev",
      reapprovalRequired: false,
      createdAt: at(-ago),
      patch: [],
      snapshot: { currency: po.currency, total: po.total, lines: po.lines.map((l) => ({ lineNo: l.lineNo, status: l.status })) },
    },
  ];
}

interface PoRow extends PurchaseOrder {
  versions: PurchaseOrderVersion[];
}

function po(n: number, vendor: number, approval: ApprovalStatus, docStatus: string, ago: number, lines: PurchaseOrderLine[], charges: PurchaseOrderCharge[] = []): PoRow {
  const subtotal = lines.filter((l) => approval === "CANCELLED" || l.status !== "CANCELLED").reduce((s, l) => s + Number(l.lineAmount), 0);
  const chargesTotal = charges.reduce((s, c) => s + Number(c.amount), 0);
  const confirmed = approval === "CONFIRMED" || approval === "FINALIZED";
  const base = {
    id: uid(K_PO, n),
    number: `PO-${String(48200 + n).padStart(6, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    vendorId: VENDORS[vendor].id,
    siteId: PLANT.id,
    warehouseId: WH_RAW.id,
    currency: "USD",
    approvalStatus: approval,
    documentStatus: docStatus,
    versionNo: confirmed ? 1 : 0,
    changeManagement: true,
    subtotal: dec(subtotal),
    discountTotal: "0.00",
    chargesTotal: dec(chargesTotal),
    taxTotal: "0.00",
    total: dec(subtotal + chargesTotal),
    rowVersion: confirmed ? 4 : approval === "APPROVED" ? 3 : approval === "IN_REVIEW" ? 2 : 1,
    lines,
    charges,
    createdAt: at(-ago),
    updatedAt: at(-ago + 0.3),
  };
  return { ...base, versions: confirmed ? version(base, ago - 0.5) : [] };
}

const pos = table<PoRow>([
  po(1, 0, "CONFIRMED", "OPEN", 41, [pline(1, "CST-IMP-06", 200, -6), pline(2, "CST-VOL-04", 60, -6)], [freight(1, 640)]),
  po(2, 1, "CONFIRMED", "OPEN", 18, [pline(1, "MTR-15HP-TEFC", 8, 9)], [freight(1, 220)]),
  po(3, 1, "FINALIZED", "RECEIVED", 33, [pline(1, "MTR-5HP-TEFC", 24, -10, 24)]),
  po(4, 2, "CONFIRMED", "PARTIALLY_RECEIVED", 14, [pline(1, "SEAL-MK-125", 300, 3, 180)]),
  po(5, 5, "FINALIZED", "RECEIVED", 21, [pline(1, "BRG-6206", 1000, -4, 1000)]),
  po(6, 3, "DRAFT", "DRAFT", 0.5, [pline(1, "FST-KIT-A", 300, 12)]),
  po(7, 4, "IN_REVIEW", "DRAFT", 1.2, [pline(1, "RM-SS316-BAR", 800, 14), pline(2, "RM-EPOXY-BLU", 30, 14)]),
  po(8, 0, "APPROVED", "DRAFT", 2, [pline(1, "CST-VOL-04", 80, 26)], [freight(1, 380)]),
  po(9, 2, "CANCELLED", "CANCELLED", 28, [pline(1, "SEAL-MK-125", 60, -15, 0, { cancelled: true })]),
  po(10, 1, "CONFIRMED", "OPEN", 5, [pline(1, "MTR-5HP-TEFC", 20, 14)]),
  po(11, 4, "CONFIRMED", "OPEN", 10, [pline(1, "RM-SS316-BAR", 600, 5)]),
  po(12, 3, "FINALIZED", "RECEIVED", 45, [pline(1, "FST-KIT-A", 400, -30, 400)]),
]);

const stripVersions = ({ versions: _v, ...rest }: PoRow): PurchaseOrder => rest;

function poSummary(p: PoRow): PurchaseOrderSummary {
  return { id: p.id, number: p.number, legalEntityId: p.legalEntityId, vendorId: p.vendorId, currency: p.currency, approvalStatus: p.approvalStatus, documentStatus: p.documentStatus, total: p.total, lineCount: p.lines.length, rowVersion: p.rowVersion, createdAt: p.createdAt };
}

route("POST", `${BASE}/purchase-orders`, ({ body }) => {
  const input = body as PurchaseOrderInput;
  if (!input.vendorId) return reply.problem(422, "PRC-PO-VALIDATION", "Choose a vendor", { vendorId: "Choose a vendor." });
  const n = pos.all().length + 1;
  const lines: PurchaseOrderLine[] = input.lines.map((l, i) => {
    const it = ITEMS.find((x) => x.id === l.productId);
    const qty = Number(l.qty);
    const price = l.unitPrice ? Number(l.unitPrice) : (it?.cost ?? 0);
    const gross = price * qty;
    const discount = gross * (Number(l.discountPct || 0) / 100);
    return {
      id: uid(K_PO_LINE, 1000 + n * 20 + i),
      lineNo: i + 1,
      productId: l.productId,
      description: l.description || it?.name || "",
      category: l.category || (it ? categoryOf(it.number) : ""),
      siteId: l.siteId || input.siteId || PLANT.id,
      warehouseId: l.warehouseId || input.warehouseId || WH_RAW.id,
      qty: dec(qty, 0),
      unit: l.unit,
      unitPrice: dec(price),
      priceUnit: l.priceUnit || "1",
      discountPct: dec(Number(l.discountPct || 0)),
      grossAmount: dec(gross),
      discountAmount: dec(discount),
      lineAmount: dec(gross - discount),
      taxAmount: dec(Number(l.taxAmount || 0)),
      deliveryDate: l.deliveryDate,
      priceSource: l.unitPrice ? "MANUAL" : "TRADE_AGREEMENT",
      receivedQty: "0",
      invoicedQty: "0",
      status: "OPEN",
    };
  });
  const charges = (input.charges ?? []).map((c, i): PurchaseOrderCharge => ({ seq: i + 1, calc: c.calc ?? "FIXED", value: c.value ?? "0", amount: c.value ?? "0", chargeCode: "FREIGHT", debitType: "ITEM", creditType: "VENDOR", allocationRule: "AMOUNT" }));
  const vendor = Math.max(0, VENDORS.findIndex((v) => v.id === input.vendorId));
  const made = po(n, vendor, "DRAFT", "DRAFT", 0, lines, charges);
  const created: PoRow = { ...made, legalEntityId: input.legalEntityId, vendorId: input.vendorId, currency: input.currency, siteId: input.siteId ?? PLANT.id, warehouseId: input.warehouseId ?? WH_RAW.id };
  pos.insert(created);
  return reply.versioned(stripVersions(created), created.rowVersion, 201);
});

route("GET", `${BASE}/purchase-orders`, ({ query }) => {
  const status = query.get("approvalStatus");
  const vendor = query.get("vendorId");
  const rows = pos
    .all()
    .filter((p) => (!status || p.approvalStatus === status) && (!vendor || p.vendorId === vendor))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items: rows.map(poSummary) };
});

route("GET", `${BASE}/purchase-order-lines`, ({ query }) => ({ items: openLines(query.get("productId")) }));

route("GET", `${BASE}/purchase-orders/{id}`, ({ params }) => {
  const p = pos.get(params.id);
  return p ? reply.versioned(stripVersions(p), p.rowVersion) : reply.problem(404, "PRC-PO-NOT-FOUND", "Purchase order not found");
});

route("GET", `${BASE}/purchase-orders/{id}/versions`, ({ params }) => {
  const p = pos.get(params.id);
  return p ? { items: p.versions } : reply.problem(404, "PRC-PO-NOT-FOUND", "Purchase order not found");
});
route("GET", `${BASE}/purchase-orders/{id}/invoicing-plans`, () => ({ items: [] }));
route("GET", `${BASE}/purchase-orders/{id}/delivery-schedules`, () => ({ items: [] }));
route("GET", `${BASE}/purchase-orders/{id}/service-lines`, () => ({ items: [] }));

route("POST", `${BASE}/purchase-orders/{id}:{action}`, ({ params }) => {
  const p = pos.get(params.id);
  if (!p) return reply.problem(404, "PRC-PO-NOT-FOUND", "Purchase order not found");
  const next: PoRow = { ...p, rowVersion: p.rowVersion + 1, updatedAt: at(0) };
  switch (params.action) {
    case "submit":
      next.approvalStatus = "IN_REVIEW";
      break;
    case "approve":
      next.approvalStatus = "APPROVED";
      break;
    case "reject":
      next.approvalStatus = "REJECTED";
      break;
    case "confirm":
      next.approvalStatus = "CONFIRMED";
      next.documentStatus = "OPEN";
      next.versionNo = 1;
      next.versions = version(next, 0);
      break;
    case "cancel":
      next.approvalStatus = "CANCELLED";
      next.documentStatus = "CANCELLED";
      next.lines = next.lines.map((l) => ({ ...l, status: "CANCELLED" }));
      break;
    default:
      return reply.problem(404, "PRC-PO-404", `Unknown action ${params.action}`);
  }
  pos.update(p.id, next);
  return reply.versioned(stripVersions(next), next.rowVersion);
});

// ---- Open supply and inquiries (derived from the orders) ----------------------------------------------------

function openLines(productId?: string | null): (OpenOrderLine & { dueDate: string; currency: string; daysLate: number; openValue: string })[] {
  const today = day(0);
  const out: (OpenOrderLine & { dueDate: string; currency: string; daysLate: number; openValue: string })[] = [];
  for (const p of pos.all()) {
    if (p.approvalStatus !== "CONFIRMED") continue;
    for (const l of p.lines) {
      const open = Number(l.qty) - Number(l.receivedQty ?? 0);
      if (open <= 0 || l.status === "CANCELLED") continue;
      if (productId && l.productId !== productId) continue;
      const late = Math.max(0, Math.round((Date.parse(today) - Date.parse(l.deliveryDate)) / 86_400_000));
      out.push({
        purchaseOrderId: p.id,
        number: p.number,
        lineNo: l.lineNo,
        legalEntityId: p.legalEntityId,
        vendorId: p.vendorId,
        productId: l.productId,
        description: l.description,
        siteId: l.siteId,
        warehouseId: l.warehouseId,
        unit: l.unit,
        qty: l.qty,
        receivedQty: l.receivedQty ?? "0",
        openQty: dec(open, 0),
        deliveryDate: l.deliveryDate,
        dueDate: l.deliveryDate,
        currency: p.currency,
        daysLate: late,
        openValue: dec(open * Number(l.unitPrice)),
      });
    }
  }
  return out;
}

route("GET", `${BASE}/inquiries/open-orders-by-vendor`, () => {
  const today = day(0);
  const byVendor = new Map<string, VendorOpenOrders>();
  const orders = new Map<string, Set<string>>();
  for (const l of openLines()) {
    const v = byVendor.get(l.vendorId) ?? { vendorId: l.vendorId, currency: "USD", orderCount: 0, lineCount: 0, openValue: "0", overdueLines: 0, overdueValue: "0", earliestDue: l.deliveryDate };
    v.lineCount += 1;
    v.openValue = dec(Number(v.openValue) + Number(l.openValue));
    if (l.deliveryDate < today) {
      v.overdueLines += 1;
      v.overdueValue = dec(Number(v.overdueValue) + Number(l.openValue));
    }
    if (l.deliveryDate < v.earliestDue) v.earliestDue = l.deliveryDate;
    byVendor.set(l.vendorId, v);
    orders.set(l.vendorId, (orders.get(l.vendorId) ?? new Set()).add(l.purchaseOrderId));
  }
  return { items: [...byVendor.values()].map((v) => ({ ...v, orderCount: orders.get(v.vendorId)?.size ?? 0 })) };
});
route("GET", `${BASE}/inquiries/overdue-lines`, () => ({ items: openLines().filter((l) => l.daysLate > 0) as OverdueLine[] }));
route("GET", `${BASE}/inquiries/backordered-lines`, () => ({ items: openLines().filter((l) => Number(l.receivedQty) > 0) as OverdueLine[] }));

route("GET", `${BASE}/inquiries/expiring-agreements`, () => {
  const items: ExpiringAgreement[] = [
    { id: uid(K_AGR, 2), number: "PA-2026-004", legalEntityId: LEGAL_ENTITY.id, vendorId: VENDORS[3].id, currency: "USD", status: "EFFECTIVE", validTo: day(21), daysLeft: 21, lapsed: false, lineCount: 1, qtyLines: 1, committedAmount: "0.00", releasedAmount: "0.00" },
  ];
  return { items };
});
route("GET", `${BASE}/inquiries/near-limit-agreements`, () => {
  const items: NearLimitLine[] = [
    { agreementId: uid(K_AGR, 1), number: "PA-2026-002", legalEntityId: LEGAL_ENTITY.id, vendorId: VENDORS[0].id, currency: "USD", validTo: day(150), lineId: uid(K_AGR, 11), lineNo: 1, commitmentType: "VALUE", category: "Castings", maxEnforced: "SOFT", committed: "120000.00", released: "112800.00", remaining: "7200.00", pctReleased: "94.00", overrun: false },
  ];
  return { items };
});
route("GET", `${BASE}/inquiries/change-orders`, () => {
  const items: ChangeOrder[] = [];
  return { items };
});

const agreementSummaries = (): PurchaseAgreementSummary[] => [
  { id: uid(K_AGR, 1), number: "PA-2026-002", legalEntityId: LEGAL_ENTITY.id, vendorId: VENDORS[0].id, currency: "USD", validFrom: day(-200), validTo: day(150), status: "EFFECTIVE", lineCount: 2, maxPctReleased: "94.00", version: 3, createdAt: at(-200) },
  { id: uid(K_AGR, 2), number: "PA-2026-004", legalEntityId: LEGAL_ENTITY.id, vendorId: VENDORS[3].id, currency: "USD", validFrom: day(-340), validTo: day(21), status: "EFFECTIVE", lineCount: 1, maxPctReleased: "61.00", version: 2, createdAt: at(-340) },
  { id: uid(K_AGR, 3), number: "PA-2026-006", legalEntityId: LEGAL_ENTITY.id, vendorId: VENDORS[1].id, currency: "USD", validFrom: day(-90), validTo: day(275), status: "EFFECTIVE", lineCount: 2, maxPctReleased: "38.00", version: 1, createdAt: at(-90) },
];

route("GET", `${BASE}/agreements`, ({ query }) => {
  const status = query.get("status");
  return { items: agreementSummaries().filter((a) => !status || a.status === status) };
});

route("GET", `${BASE}/agreements/{id}`, ({ params }) => {
  const sum = agreementSummaries().find((a) => a.id === params.id);
  if (!sum) return reply.problem(404, "PRC-AGR-NOT-FOUND", "Agreement not found");
  const { lineCount: _l, maxPctReleased: _m, ...head } = sum;
  const products = sum.vendorId === VENDORS[0].id ? ["CST-IMP-06", "CST-VOL-04"] : sum.vendorId === VENDORS[3].id ? ["FST-KIT-A"] : ["MTR-5HP-TEFC", "MTR-15HP-TEFC"];
  const lines = products.slice(0, sum.lineCount).map((n, i): PurchaseAgreement["lines"][number] => {
    const it = itemByNumber(n);
    const committed = Math.round(Number(sum.maxPctReleased) > 90 ? 60000 : 40000);
    const released = (committed * Number(sum.maxPctReleased)) / 100;
    return {
      id: uid(K_AGR, 10 + i + 1),
      lineNo: i + 1,
      commitmentType: "VALUE",
      productId: it.id,
      category: categoryOf(n),
      unit: it.unit,
      committedQty: "0",
      committedAmount: dec(committed),
      unitPrice: dec(it.cost),
      discountPct: "0.00",
      maxEnforced: "SOFT",
      priceOverride: false,
      releasedQty: "0",
      releasedAmount: dec(released),
      pctReleased: dec(Number(sum.maxPctReleased)),
      remaining: dec(committed - released),
    };
  });
  const agreement: PurchaseAgreement = { ...head, lines, releases: [], updatedAt: sum.createdAt };
  return reply.versioned(agreement, agreement.version);
});

route("GET", `${BASE}/rfq-types`, () => ({ items: [] }));

const tradeAgreementRows = (): TradeAgreement[] =>
  ITEMS.filter((i) => i.vendor !== undefined).map((it, n) => ({
    id: uid(K_TA, n + 1),
    relation: "PRICE",
    accountCode: "VENDOR",
    accountId: VENDORS[it.vendor ?? 0].id,
    itemCode: "PRODUCT",
    itemId: it.id,
    currency: "USD",
    unit: it.unit,
    fromQty: "1",
    price: dec(it.cost),
    priceUnit: "1",
    validFrom: day(-120),
    validTo: day(245),
    consignment: false,
    status: "ACTIVE",
  }));

route("GET", `${BASE}/trade-agreements`, ({ query }) => {
  const status = query.get("status");
  return { items: tradeAgreementRows().filter((r) => !status || r.status === status) };
});

route("GET", `${BASE}/trade-agreements/{id}`, ({ params }) => {
  const row = tradeAgreementRows().find((r) => r.id === params.id);
  return row ? reply.versioned(row, 1) : reply.problem(404, "PRC-TA-NOT-FOUND", "Price row not found");
});

// Exported for the receiving handler: purchase orders are the source of what arrives.
export const purchaseOrders = pos;
export { po as newPurchaseOrder, pline as newPurchaseLine };
export { etagOf };
