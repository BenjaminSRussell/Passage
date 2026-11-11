// Receiving service (/api/v1/receiving) for the demo: deliveries from the dock to posting, against the purchase orders.
import type { Receipt, ReceiptLine } from "../../../modules/receiving/api/client";
import { ITEMS, LEGAL_ENTITY, VENDORS, itemByNumber } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, table, uid } from "../../store";
import { adjustOnHand } from "./inventory";
import { purchaseOrders } from "./purchasing";
import { locationId } from "./warehouse";

const BASE = "/api/v1/receiving";
const K_RECEIPT = 320;

const CARRIERS = ["Estes Express", "XPO Logistics", "Old Dominion", "FedEx Freight", "Saia LTL"];

function rline(lineNo: number, poN: number, number: string, orderedRemaining: number, qty: number, lot = ""): ReceiptLine {
  const it = itemByNumber(number);
  const po = purchaseOrders.get(uid(302, poN));
  const poLine = po?.lines.find((l) => l.productId === it.id);
  return {
    lineNo,
    poLineId: poLine?.id ?? uid(303, lineNo),
    productId: it.id,
    orderedRemaining: dec(orderedRemaining, 0),
    overDeliveryPct: "5.00",
    unitPrice: dec(it.cost),
    unit: it.unit,
    batchTracked: lot !== "",
    serialTracked: false,
    qty: dec(qty, 0),
    batch: lot,
    serials: [],
    locationId: qty > 0 ? locationId(number) : "",
    inventoryStatus: "AVAILABLE",
    amount: qty > 0 ? dec(qty * it.cost) : null,
    amountAcct: qty > 0 ? dec(qty * it.cost) : null,
  };
}

function receipt(n: number, state: Receipt["state"], poN: number, slip: string, ago: number, lines: ReceiptLine[]): Receipt {
  const po = purchaseOrders.get(uid(302, poN));
  return {
    id: uid(K_RECEIPT, n),
    receiptNumber: `RCV-${String(10400 + n).padStart(6, "0")}`,
    state,
    version: state === "ARRIVED" ? 1 : state === "REGISTERED" ? 2 : 3,
    vendorId: po?.vendorId ?? VENDORS[0].id,
    purchaseOrderId: uid(302, poN),
    legalEntityId: LEGAL_ENTITY.id,
    packingSlip: slip,
    carrier: CARRIERS[n % CARRIERS.length],
    palletCount: 1 + (n % 4),
    receiptDate: day(-ago),
    currency: "USD",
    exchangeRate: "1.000000",
    postingDate: state === "POSTED" ? day(-ago) : undefined,
    createdAt: at(-ago),
    lines,
  };
}

const receipts = table<Receipt>([
  receipt(1, "ARRIVED", 4, "KSG-558213", 0, [rline(1, 4, "SEAL-MK-125", 120, 0)]),
  receipt(2, "REGISTERED", 11, "RBS-90417", 1, [rline(1, 11, "RM-SS316-BAR", 350, 250, "HT-24-8841")]),
  receipt(3, "POSTED", 4, "KSG-557640", 2, [rline(1, 4, "SEAL-MK-125", 300, 180)]),
  receipt(4, "POSTED", 5, "PBC-33018", 4, [rline(1, 5, "BRG-6206", 1000, 1000, "PB-2611")]),
  receipt(5, "POSTED", 3, "VMW-120771", 10, [rline(1, 3, "MTR-5HP-TEFC", 24, 24, "VM-5H-0924")]),
  receipt(6, "POSTED", 12, "AFS-74402", 30, [rline(1, 12, "FST-KIT-A", 400, 400)]),
]);

route("GET", `${BASE}/receipts`, ({ query }) => {
  const state = query.get("state");
  const vendorId = query.get("vendorId");
  const from = query.get("from");
  const to = query.get("to");
  const items = receipts
    .all()
    .filter((r) => (!state || r.state === state) && (!vendorId || r.vendorId === vendorId) && (!from || r.receiptDate >= from) && (!to || r.receiptDate <= to))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.receiptNumber.localeCompare(a.receiptNumber))
    .map((r) => ({ ...r, lines: [] }));
  return { items };
});

route("GET", `${BASE}/receipts/{id}`, ({ params }) => {
  const r = receipts.get(params.id);
  return r ? reply.versioned(r, r.version) : reply.problem(404, "RCV-0404", "Receipt not found");
});

route("POST", `${BASE}/arrivals`, ({ body }) => {
  const input = body as { vendorId: string; purchaseOrderId: string; legalEntityId: string; packingSlip: string; carrier?: string; palletCount?: number; receiptDate: string; currency: string; lines: { poLineNo: number; productId: string; overDeliveryPct: string; unit: string; batchTracked: boolean; serialTracked: boolean }[] };
  const po = purchaseOrders.get(input.purchaseOrderId);
  const n = receipts.all().length + 1;
  const created: Receipt = {
    id: uid(K_RECEIPT, n),
    receiptNumber: `RCV-${String(10400 + n).padStart(6, "0")}`,
    state: "ARRIVED",
    version: 1,
    vendorId: input.vendorId,
    purchaseOrderId: input.purchaseOrderId,
    legalEntityId: input.legalEntityId,
    packingSlip: input.packingSlip,
    carrier: input.carrier ?? "",
    palletCount: input.palletCount,
    receiptDate: input.receiptDate,
    currency: input.currency,
    exchangeRate: "1.000000",
    createdAt: at(0),
    lines: input.lines.map((l, i) => {
      const pl = po?.lines.find((x) => x.lineNo === l.poLineNo);
      return {
        lineNo: i + 1,
        poLineId: pl?.id ?? "",
        productId: l.productId,
        orderedRemaining: dec(Number(pl?.qty ?? 0) - Number(pl?.receivedQty ?? 0), 0),
        overDeliveryPct: l.overDeliveryPct,
        unitPrice: pl?.unitPrice ?? "0.00",
        unit: l.unit,
        batchTracked: l.batchTracked,
        serialTracked: l.serialTracked,
        qty: "0",
        batch: "",
        serials: [],
        locationId: "",
        inventoryStatus: "AVAILABLE",
        amount: null,
        amountAcct: null,
      };
    }),
  };
  receipts.insert(created);
  return reply.created(created, { ETag: '"1"' });
});

route("POST", `${BASE}/receipts/{id}:register`, ({ params, body }) => {
  const r = receipts.get(params.id);
  if (!r) return reply.problem(404, "RCV-0404", "Receipt not found");
  const input = (body as { lines: { lineNo: number; qty: string; batch?: string; serials?: string[]; locationId: string; inventoryStatus: string }[] }).lines;
  const lines = r.lines.map((l) => {
    const m = input.find((x) => x.lineNo === l.lineNo);
    if (!m) return l;
    const cost = Number(l.unitPrice) * Number(m.qty);
    return { ...l, qty: m.qty, batch: m.batch ?? "", serials: m.serials ?? [], locationId: m.locationId, inventoryStatus: m.inventoryStatus, amount: dec(cost), amountAcct: dec(cost) };
  });
  const next = receipts.update(r.id, { state: "REGISTERED", lines, version: r.version + 1 });
  return next ? reply.versioned(next, next.version) : reply.problem(404, "RCV-0404", "Receipt not found");
});

route("POST", `${BASE}/receipts/{id}:post`, ({ params }) => {
  const r = receipts.get(params.id);
  if (!r) return reply.problem(404, "RCV-0404", "Receipt not found");
  const po = purchaseOrders.get(r.purchaseOrderId);
  if (po) {
    const lines = po.lines.map((pl) => {
      const rl = r.lines.find((l) => l.poLineId === pl.id);
      if (!rl) return pl;
      const got = Number(pl.receivedQty ?? 0) + Number(rl.qty);
      return { ...pl, receivedQty: dec(got, 0), status: got >= Number(pl.qty) ? "RECEIVED" : "PARTIALLY_RECEIVED" };
    });
    const all = lines.every((l) => l.status === "RECEIVED" || l.status === "CANCELLED");
    purchaseOrders.update(po.id, { lines, documentStatus: all ? "RECEIVED" : "PARTIALLY_RECEIVED", rowVersion: po.rowVersion + 1 });
  }
  for (const l of r.lines) {
    const it = ITEMS.find((i) => i.id === l.productId);
    if (it) adjustOnHand(it.id, Number(l.qty));
  }
  const next = receipts.update(r.id, { state: "POSTED", postingDate: day(0), version: r.version + 1 });
  return next ? reply.versioned(next, next.version) : reply.problem(404, "RCV-0404", "Receipt not found");
});

route("POST", `${BASE}/receipts/{id}:cancel`, ({ params }) => {
  const r = receipts.get(params.id);
  if (!r) return reply.problem(404, "RCV-0404", "Receipt not found");
  const next = receipts.update(r.id, { state: "CANCELLED", version: r.version + 1 });
  return next ? reply.versioned(next, next.version) : reply.problem(404, "RCV-0404", "Receipt not found");
});

// The document flow query: every receipt of the purchase order that the asked-about document belongs to.
// The demo has no vendor invoices in receiving, so the invoice list is empty.
route("GET", `${BASE}/related-documents`, ({ query }) => {
  const receiptId = query.get("receiptId");
  const poId = query.get("purchaseOrderId") ?? receipts.get(receiptId ?? "")?.purchaseOrderId;
  if (!poId) return reply.problem(400, "COM-0001", "give exactly one of purchaseOrderId, receiptId or invoiceId");
  const list = receipts
    .all()
    .filter((r) => r.purchaseOrderId === poId)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map((r) => ({ id: r.id, receiptNumber: r.receiptNumber, state: r.state, receiptDate: r.receiptDate }));
  return { purchaseOrderId: poId, receipts: list, invoices: [], truncated: false };
});

// Goods received, not invoiced: every posted receipt line waits for its vendor invoice, since the demo has none yet.
route("GET", `${BASE}/grir`, ({ query }) => {
  const status = query.get("status");
  const items = receipts
    .all()
    .filter((r) => r.state === "POSTED")
    .flatMap((r) =>
      r.lines.map((l) => ({
        id: `${r.id}-${l.lineNo}`,
        receiptId: r.id,
        receiptNumber: r.receiptNumber,
        lineNo: l.lineNo,
        vendorId: r.vendorId,
        poLineId: l.poLineId,
        unitPrice: l.unitPrice,
        receivedQty: l.qty,
        invoicedQty: "0",
        receivedAmount: l.amount ?? "0.00",
        clearedAmount: "0.00",
        openAmount: l.amount ?? "0.00",
        currency: r.currency,
        status: "OPEN" as const,
        ageDays: Math.max(0, Math.round((Date.parse(day(0)) - Date.parse(r.postingDate ?? r.receiptDate)) / 86_400_000)),
        createdAt: r.createdAt,
      })),
    )
    .filter((g) => !status || g.status === status)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  const open = items.reduce((sum, g) => sum + Number(g.openAmount), 0);
  return { items, totals: { count: items.length, openAmount: [{ amount: dec(open), currency: "USD" }] } };
});
