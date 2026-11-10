// Requests for quotation (/api/v1/procurement/rfqs) for the demo: three RFQs at different stages, with vendor replies,
// so the RFQ list, the "RFQs awaiting replies" tile, the compare-and-award screen and the reply form all have data.
import type { RFQ, RFQComparison, RFQComparisonLine, RFQInvitation, RFQLine, RFQReply, RFQStatus, RFQSummary, RankedReply } from "../../../modules/purchasing/api/client";
import { ITEMS, LEGAL_ENTITY, VENDORS, itemByNumber } from "../../company";
import { reply, route } from "../../backend";
import { newPurchaseLine, newPurchaseOrder, purchaseOrders } from "./purchasing";
import { at, day, dec, table, uid } from "../../store";

const BASE = "/api/v1/procurement";
const K_RFQ = 340;
const K_RFQ_LINE = 341;
const K_RFQ_REPLY = 342;

let lineSeq = 0;
function line(lineNo: number, number: string, qty: number, requiredIn: number): RFQLine {
  const it = itemByNumber(number);
  return { id: uid(K_RFQ_LINE, ++lineSeq), lineNo, productId: it.id, description: it.name, qty: dec(qty, 0), unit: it.unit, requiredDate: day(requiredIn) };
}

let replySeq = 0;
function quote(l: RFQLine, vendor: number, unitPrice: number, leadTimeDays: number, ago: number, late = false, awarded = false): RFQReply {
  return {
    id: uid(K_RFQ_REPLY, ++replySeq),
    lineId: l.id,
    vendorId: VENDORS[vendor].id,
    unitPrice: dec(unitPrice),
    priceUnit: "1",
    currency: "USD",
    leadTimeDays,
    validUntil: day(30),
    receivedAt: at(-ago),
    late,
    awarded,
  };
}

function invitations(vendors: number[], replied: number[], ago: number): RFQInvitation[] {
  return vendors.map((v) => ({ vendorId: VENDORS[v].id, status: replied.includes(v) ? "REPLIED" : "INVITED", sentAt: at(-ago) }));
}

function rfq(n: number, status: RFQStatus, ago: number, deadlineIn: number, lines: RFQLine[], invites: RFQInvitation[], replies: RFQReply[], extra: Partial<RFQ> = {}): RFQ {
  return {
    id: uid(K_RFQ, n),
    number: `RFQ-${String(2600 + n).padStart(5, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    status,
    replyDeadline: at(deadlineIn),
    rankingRule: "LOWEST_PRICE",
    sealed: false,
    repliesSealed: false,
    replyCount: replies.length,
    version: 2 + replies.length,
    amendmentsAllowed: true,
    lines,
    invitations: invites,
    replies,
    createdAt: at(-ago),
    updatedAt: at(-ago + 0.5),
    ...extra,
  };
}

const castingLines = [line(1, "CST-IMP-06", 160, 28), line(2, "CST-VOL-04", 50, 28)];
const motorLines = [line(1, "MTR-15HP-TEFC", 12, 35)];
const sealLines = [line(1, "SEAL-MK-125", 400, 14), line(2, "BRG-6206", 1200, 14)];

const rfqs = table<RFQ>([
  // Two of three vendors have quoted the castings; the third is still invited.
  rfq(
    1,
    "REPLIES_OPEN",
    4,
    2,
    castingLines,
    invitations([0, 4, 3], [0, 4], 4),
    [quote(castingLines[0], 0, 94.5, 30, 2), quote(castingLines[1], 0, 166, 32, 2), quote(castingLines[0], 4, 99, 21, 1), quote(castingLines[1], 4, 158.75, 24, 1)],
  ),
  // Sent yesterday; nothing back yet.
  rfq(2, "SENT", 1, 6, motorLines, invitations([1, 5], [], 1), []),
  // Closed after the deadline with quotes from both vendors, ready to award.
  rfq(
    3,
    "CLOSED",
    12,
    -2,
    sealLines,
    invitations([2, 5], [2, 5], 12),
    [quote(sealLines[0], 2, 47.9, 10, 7), quote(sealLines[1], 2, 7.9, 12, 7), quote(sealLines[0], 5, 49.25, 8, 6), quote(sealLines[1], 5, 7.25, 9, 6)],
  ),
]);

const itemOf = (productId?: string): string => ITEMS.find((i) => i.id === productId)?.number ?? "CST-IMP-06";

const summary = (r: RFQ): RFQSummary => ({
  id: r.id,
  number: r.number,
  legalEntityId: r.legalEntityId,
  status: r.status,
  replyDeadline: r.replyDeadline,
  rankingRule: r.rankingRule,
  sealed: r.sealed,
  lineCount: r.lines.length,
  vendorCount: r.invitations.length,
  replyCount: r.replies.length,
  version: r.version,
  createdAt: r.createdAt,
});

route("GET", `${BASE}/rfqs`, ({ query }) => {
  const statuses = (query.get("status") ?? "").split(",").filter(Boolean);
  const rows = rfqs
    .all()
    .filter((r) => statuses.length === 0 || statuses.includes(r.status))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items: rows.map(summary) };
});

route("GET", `${BASE}/rfqs/{id}`, ({ params }) => {
  const r = rfqs.get(params.id);
  return r ? reply.versioned(r, r.version) : reply.problem(404, "PRC-RFQ-NOT-FOUND", "RFQ not found");
});

route("GET", `${BASE}/rfqs/{id}/compare`, ({ params }) => {
  const r = rfqs.get(params.id);
  if (!r) return reply.problem(404, "PRC-RFQ-NOT-FOUND", "RFQ not found");
  const lines: RFQComparisonLine[] = r.lines.map((l) => {
    const onTime = r.replies.filter((x) => x.lineId === l.id && !x.late).sort((a, b) => Number(a.unitPrice) - Number(b.unitPrice) || a.leadTimeDays - b.leadTimeDays);
    const ranked: RankedReply[] = onTime.map((x, i) => ({
      rank: i + 1,
      replyId: x.id,
      vendorId: x.vendorId,
      unitPrice: x.unitPrice,
      currency: x.currency,
      leadTimeDays: x.leadTimeDays,
      validUntil: x.validUntil,
      total: dec(Number(x.unitPrice) * Number(l.qty)),
    }));
    return { lineId: l.id, lineNo: l.lineNo, productId: l.productId, description: l.description, qty: l.qty, unit: l.unit, ranked, excluded: [] };
  });
  const body: RFQComparison = { rfqId: r.id, rankingRule: r.rankingRule, lines };
  return body;
});

route("POST", `${BASE}/rfqs/{id}:{action}`, ({ params, body }) => {
  const r = rfqs.get(params.id);
  if (!r) return reply.problem(404, "PRC-RFQ-NOT-FOUND", "RFQ not found");
  let status: RFQStatus;
  switch (params.action) {
    case "send": {
      const ids = (body as { vendorIds?: string[] } | undefined)?.vendorIds ?? [];
      const invites: RFQInvitation[] = ids.map((vendorId) => ({ vendorId, status: "INVITED", sentAt: at(0) }));
      const next = rfqs.update(r.id, { status: "SENT", invitations: invites, version: r.version + 1, updatedAt: at(0) });
      return next ? reply.versioned(next, next.version) : reply.problem(404, "PRC-RFQ-NOT-FOUND", "RFQ not found");
    }
    case "close":
      status = "CLOSED";
      break;
    case "cancel":
      status = "CANCELLED";
      break;
    default:
      return reply.problem(404, "PRC-RFQ-404", `Unknown action ${params.action}`);
  }
  const next = rfqs.update(r.id, { status, version: r.version + 1, updatedAt: at(0) });
  return next ? reply.versioned(next, next.version) : reply.problem(404, "PRC-RFQ-NOT-FOUND", "RFQ not found");
});

route("POST", `${BASE}/rfqs/{id}:award`, ({ params, body }) => {
  const r = rfqs.get(params.id);
  if (!r) return reply.problem(404, "PRC-RFQ-NOT-FOUND", "RFQ not found");
  const awards = (body as { awards: { lineId: string; replyId: string }[] }).awards;
  const won = awards.map((a) => ({ line: r.lines.find((l) => l.id === a.lineId), reply: r.replies.find((x) => x.id === a.replyId) })).filter((x): x is { line: RFQLine; reply: RFQReply } => Boolean(x.line && x.reply));
  const orders = [];
  let seq = purchaseOrders.all().length;
  for (const vendorId of new Set(won.map((w) => w.reply.vendorId))) {
    const mine = won.filter((w) => w.reply.vendorId === vendorId);
    const vendor = VENDORS.findIndex((v) => v.id === vendorId);
    const lines = mine.map((w, i) => newPurchaseLine(i + 1, itemOf(w.line.productId), Number(w.line.qty), 14, 0, { price: Number(w.reply.unitPrice) }));
    const { versions: _v, ...order } = newPurchaseOrder(++seq, vendor < 0 ? 0 : vendor, "DRAFT", "DRAFT", 0, lines);
    purchaseOrders.insert({ ...order, versions: [] });
    orders.push(order);
  }
  const ids = new Set(awards.map((a) => a.replyId));
  const next = rfqs.update(r.id, { status: "AWARDED", replies: r.replies.map((x) => ({ ...x, awarded: ids.has(x.id) })), version: r.version + 1, updatedAt: at(0) });
  return next ? reply.versioned({ rfq: next, purchaseOrders: orders }, next.version) : reply.problem(404, "PRC-RFQ-NOT-FOUND", "RFQ not found");
});

route("POST", `${BASE}/rfqs/{id}/replies`, ({ params, body }) => {
  const r = rfqs.get(params.id);
  if (!r) return reply.problem(404, "PRC-RFQ-NOT-FOUND", "RFQ not found");
  const input = body as { vendorId: string; currency: string; replies: { lineId: string; unitPrice: string; priceUnit?: string; leadTimeDays: number; validUntil?: string; notes?: string }[] };
  const added: RFQReply[] = input.replies.map((x) => ({
    id: uid(K_RFQ_REPLY, ++replySeq),
    lineId: x.lineId,
    vendorId: input.vendorId,
    unitPrice: x.unitPrice,
    priceUnit: x.priceUnit ?? "1",
    currency: input.currency,
    leadTimeDays: x.leadTimeDays,
    validUntil: x.validUntil,
    notes: x.notes,
    receivedAt: at(0),
    late: false,
    awarded: false,
  }));
  const replies = [...r.replies, ...added];
  const invites = r.invitations.map((i): RFQInvitation => (i.vendorId === input.vendorId ? { ...i, status: "REPLIED" } : i));
  const next = rfqs.update(r.id, { replies, invitations: invites, replyCount: replies.length, status: r.status === "SENT" ? "REPLIES_OPEN" : r.status, version: r.version + 1, updatedAt: at(0) });
  return next ? reply.versioned(next, next.version) : reply.problem(404, "PRC-RFQ-NOT-FOUND", "RFQ not found");
});
