import { z } from "zod";

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

const defaultFetch: FetchLike = (input, init) => globalThis.fetch(input, init);

const BASE = "/api/v1/receiving";

export const RECEIPT_STATES = ["ARRIVED", "REGISTERED", "POSTED", "CORRECTED", "CANCELLED"] as const;
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

export interface ArrivalLineInput {
  poLineId: string;
  productId: string;
  orderedRemaining: number;
  overDeliveryPct: number;
  unitPrice: number;
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
  qty: number;
  batch?: string;
  serials?: string[];
  locationId: string;
  inventoryStatus: string;
}

export interface CorrectLineInput {
  lineNo: number;
  qtyDelta: number;
}

export class ReceivingApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "ReceivingApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

async function readProblem(res: Response): Promise<ReceivingApiError> {
  let body: { code?: unknown; detail?: unknown; errors?: unknown } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    // Non-JSON error bodies fall through to the generic error below.
  }
  const code = typeof body.code === "string" ? body.code : "COM-0500";
  const detail = typeof body.detail === "string" ? body.detail : `request failed with status ${res.status}`;
  const fields = z.record(z.string()).safeParse(body.errors ?? {});
  return new ReceivingApiError(res.status, code, detail, fields.success ? fields.data : {});
}

export function newKey(): string {
  return crypto.randomUUID();
}

async function request<T>(method: string, path: string, schema: z.ZodType<T>, fetchImpl: FetchLike, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetchImpl(`${BASE}${path}`, {
    method,
    headers: { Accept: "application/json", ...(body === undefined ? {} : { "Content-Type": "application/json" }), ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await readProblem(res);
  return schema.parse(await res.json());
}

export interface ReceiptQuery {
  state?: ReceiptState;
  cursor?: string;
  limit?: number;
}

/** GET /receipts: newest first, lines omitted from list items. */
export function fetchReceipts(q: ReceiptQuery = {}, fetchImpl: FetchLike = defaultFetch): Promise<ReceiptPage> {
  const params = new URLSearchParams();
  if (q.state) params.set("state", q.state);
  if (q.cursor) params.set("cursor", q.cursor);
  if (q.limit) params.set("limit", String(q.limit));
  const query = params.size > 0 ? `?${params}` : "";
  return request("GET", `/receipts${query}`, receiptPageSchema, fetchImpl);
}

export function fetchReceipt(id: string, fetchImpl: FetchLike = defaultFetch): Promise<Receipt> {
  return request("GET", `/receipts/${encodeURIComponent(id)}`, receiptSchema, fetchImpl);
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

export function correctReceipt(id: string, version: number, lines: CorrectLineInput[], idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<Receipt> {
  return command(id, "correct", version, idempotencyKey, fetchImpl, { lines });
}

export function cancelReceipt(id: string, version: number, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<Receipt> {
  return command(id, "cancel", version, idempotencyKey, fetchImpl);
}
