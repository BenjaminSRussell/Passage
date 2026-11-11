import { z } from "zod";
import { defaultFetch, readProblem, request, type FetchLike } from "../../../shell/api/http";

export { ApiError, newKey } from "../../../shell/api/http";

export const RECEIPT_STATES = ["ARRIVED", "REGISTERED", "POSTED", "CANCELLED"] as const;
export type ReceiptState = (typeof RECEIPT_STATES)[number];

export const INVENTORY_STATUSES = ["AVAILABLE", "QUARANTINE", "BLOCKED"] as const;

export const receiptLineSchema = z.object({
  lineNo: z.number().int(),
  poLineId: z.string(),
  productId: z.string(),
  orderedRemaining: z.string(),
  overDeliveryPct: z.string(),
  unitPrice: z.string(),
  unit: z.string(),
  batchTracked: z.boolean(),
  serialTracked: z.boolean(),
  qty: z.string(),
  batch: z.string(),
  serials: z.array(z.string()),
  locationId: z.string(),
  inventoryStatus: z.string(),
  amount: z.string().nullable(),
  amountAcct: z.string().nullable(),
});
export type ReceiptLine = z.infer<typeof receiptLineSchema>;

export const receiptSchema = z.object({
  id: z.string(),
  receiptNumber: z.string(),
  state: z.enum(RECEIPT_STATES),
  version: z.number().int(),
  vendorId: z.string(),
  purchaseOrderId: z.string(),
  legalEntityId: z.string(),
  packingSlip: z.string(),
  carrier: z.string(),
  palletCount: z.number().int().optional(),
  receiptDate: z.string(),
  currency: z.string(),
  exchangeRate: z.string(),
  postingDate: z.string().optional(),
  ledgerEntryId: z.string().optional(),
  createdAt: z.string(),
  lines: z.array(receiptLineSchema),
});
export type Receipt = z.infer<typeof receiptSchema>;

const receiptPageSchema = z.object({ items: z.array(receiptSchema), nextCursor: z.string().optional() });
export type ReceiptPage = z.infer<typeof receiptPageSchema>;

/** The service reads the open quantity, net price and poLineId from this purchase order line. */
export interface ArrivalLineInput {
  poLineNo: number;
  productId: string;
  overDeliveryPct: string;
  unit: string;
  batchTracked: boolean;
  serialTracked: boolean;
}

export interface ArrivalInput {
  vendorId: string;
  purchaseOrderId: string;
  legalEntityId: string;
  packingSlip: string;
  carrier?: string;
  palletCount?: number;
  receiptDate: string;
  currency: string;
  lines: ArrivalLineInput[];
}

export interface RegisterLineInput {
  lineNo: number;
  qty: string;
  batch?: string;
  serials?: string[];
  locationId: string;
  inventoryStatus: string;
}

export interface CorrectLineInput {
  lineNo: number;
  qtyDelta: string;
}

export interface ReceiptQuery {
  state?: ReceiptState;
  vendorId?: string;
  /** Receipt date bounds, inclusive (YYYY-MM-DD). */
  from?: string;
  to?: string;
  cursor?: string;
  limit?: number;
}

/** GET /receipts: newest first, lines omitted from list items. */
export function fetchReceipts(q: ReceiptQuery = {}, fetchImpl: FetchLike = defaultFetch): Promise<ReceiptPage> {
  const params = new URLSearchParams();
  if (q.state) params.set("state", q.state);
  if (q.vendorId) params.set("vendorId", q.vendorId);
  if (q.from) params.set("from", q.from);
  if (q.to) params.set("to", q.to);
  if (q.cursor) params.set("cursor", q.cursor);
  if (q.limit) params.set("limit", String(q.limit));
  const query = params.size > 0 ? `?${params}` : "";
  return request("GET", `/receipts${query}`, receiptPageSchema, fetchImpl);
}

export function fetchReceipt(id: string, fetchImpl: FetchLike = defaultFetch): Promise<Receipt> {
  return request("GET", `/receipts/${encodeURIComponent(id)}`, receiptSchema, fetchImpl);
}

const purchaseOrderLineSchema = z.object({
  lineNo: z.number().int(),
  productId: z.string().optional(),
  description: z.string().optional(),
  qty: z.string(),
  unit: z.string(),
  status: z.string(),
});
export type PurchaseOrderLine = z.infer<typeof purchaseOrderLineSchema>;

const purchaseOrderSchema = z.object({ lines: z.array(purchaseOrderLineSchema) });

/** Lines of a purchase order in services/purchasing, so an arrival line can name its poLineNo. */
export async function fetchPurchaseOrderLines(purchaseOrderId: string, fetchImpl: FetchLike = defaultFetch): Promise<PurchaseOrderLine[]> {
  const res = await fetchImpl(`/api/v1/procurement/purchase-orders/${encodeURIComponent(purchaseOrderId)}`, { method: "GET", headers: { Accept: "application/json" } });
  if (!res.ok) throw await readProblem(res);
  return purchaseOrderSchema.parse(await res.json()).lines;
}

const purchaseOrderRefSchema = z.object({ id: z.string(), number: z.string(), vendorId: z.string().optional(), documentStatus: z.string().optional() });
export type PurchaseOrderRef = z.infer<typeof purchaseOrderRefSchema>;

/** The number of a purchase order, so a receipt can name its order. */
export async function fetchPurchaseOrderRef(purchaseOrderId: string, fetchImpl: FetchLike = defaultFetch): Promise<PurchaseOrderRef> {
  const res = await fetchImpl(`/api/v1/procurement/purchase-orders/${encodeURIComponent(purchaseOrderId)}`, { method: "GET", headers: { Accept: "application/json" } });
  if (!res.ok) throw await readProblem(res);
  return purchaseOrderRefSchema.parse(await res.json());
}

/** Confirmed purchase orders of one vendor, the orders a delivery can arrive against. */
export async function fetchOpenPurchaseOrders(vendorId: string, fetchImpl: FetchLike = defaultFetch): Promise<PurchaseOrderRef[]> {
  const query = new URLSearchParams({ vendorId, approvalStatus: "CONFIRMED" });
  const res = await fetchImpl(`/api/v1/procurement/purchase-orders?${query}`, { method: "GET", headers: { Accept: "application/json" } });
  if (!res.ok) throw await readProblem(res);
  return z.object({ items: z.array(purchaseOrderRefSchema) }).parse(await res.json()).items;
}

/** The bin locations of a warehouse, by name, for choosing where received goods are put. */
export async function fetchLocations(warehouseId: string, fetchImpl: FetchLike = defaultFetch): Promise<{ id: string; name: string; zone: string }[]> {
  const res = await fetchImpl(`/api/v1/warehouse/locations?${new URLSearchParams({ warehouseId, limit: "200" })}`, { method: "GET", headers: { Accept: "application/json" } });
  if (!res.ok) throw await readProblem(res);
  return z.object({ items: z.array(z.object({ id: z.string(), name: z.string(), zone: z.string().default("") })) }).parse(await res.json()).items;
}

/** POST /arrivals: records a delivery at the dock in state ARRIVED. */
export function createArrival(input: ArrivalInput, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<Receipt> {
  return request("POST", "/arrivals", receiptSchema, fetchImpl, { "Idempotency-Key": idempotencyKey }, input);
}

function command(id: string, verb: string, version: number, idempotencyKey: string, fetchImpl: FetchLike, body?: unknown): Promise<Receipt> {
  return request("POST", `/receipts/${encodeURIComponent(id)}:${verb}`, receiptSchema, fetchImpl, { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` }, body);
}

export function registerReceipt(id: string, version: number, lines: RegisterLineInput[], idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<Receipt> {
  return command(id, "register", version, idempotencyKey, fetchImpl, { lines });
}

export function postReceipt(id: string, version: number, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<Receipt> {
  return command(id, "post", version, idempotencyKey, fetchImpl);
}

export function correctReceipt(id: string, version: number, lines: CorrectLineInput[], idempotencyKey: string, postingDate?: string, fetchImpl: FetchLike = defaultFetch): Promise<Receipt> {
  return command(id, "correct", version, idempotencyKey, fetchImpl, { lines, ...(postingDate ? { postingDate } : {}) });
}

export function cancelReceipt(id: string, version: number, idempotencyKey: string, postingDate?: string, fetchImpl: FetchLike = defaultFetch): Promise<Receipt> {
  return command(id, "cancel", version, idempotencyKey, fetchImpl, postingDate ? { postingDate } : undefined);
}
