import { z } from "zod";

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

const defaultFetch: FetchLike = (input, init) => globalThis.fetch(input, init);

const BASE = "/api/v1/receiving";

export const GRIR_STATUSES = ["OPEN", "PARTIAL", "CLEARED", "WRITTEN_OFF"] as const;
export type GrirStatus = (typeof GRIR_STATUSES)[number];

export const grirItemSchema = z.object({
  id: z.string(),
  receiptId: z.string(),
  receiptNumber: z.string(),
  lineNo: z.number().int(),
  vendorId: z.string(),
  poLineId: z.string(),
  receivedQty: z.string(),
  invoicedQty: z.string(),
  receivedAmount: z.string(),
  clearedAmount: z.string(),
  openAmount: z.string(),
  currency: z.string(),
  status: z.enum(GRIR_STATUSES),
  ageDays: z.number().int(),
  createdAt: z.string(),
});
export type GrirItem = z.infer<typeof grirItemSchema>;

const grirPageSchema = z.object({
  items: z.array(grirItemSchema),
  totals: z.object({ count: z.number().int(), openAmount: z.array(z.object({ amount: z.string(), currency: z.string() })) }),
  nextCursor: z.string().optional(),
});
export type GrirPage = z.infer<typeof grirPageSchema>;

export const invoiceSchema = z.object({
  id: z.string(),
  vendorId: z.string(),
  legalEntityId: z.string(),
  invoiceNumber: z.string(),
  invoiceDate: z.string(),
  postingDate: z.string(),
  currency: z.string(),
  ledgerId: z.string(),
  fiscalPeriodId: z.string(),
  total: z.string(),
  ledgerEntryId: z.string(),
  createdAt: z.string(),
  lines: z.array(
    z.object({
      lineNo: z.number().int(),
      receiptId: z.string(),
      receiptLineNo: z.number().int(),
      qty: z.string(),
      unitPrice: z.string(),
      amount: z.string(),
      standardCost: z.boolean(),
    }),
  ),
});
export type Invoice = z.infer<typeof invoiceSchema>;

export interface InvoiceLineInput {
  receiptId: string;
  receiptLineNo: number;
  qty: number;
  unitPrice: number;
  standardCost: boolean;
}

export interface InvoiceInput {
  vendorId: string;
  legalEntityId: string;
  invoiceNumber: string;
  invoiceDate: string;
  postingDate: string;
  currency: string;
  ledgerId: string;
  fiscalPeriodId: string;
  lines: InvoiceLineInput[];
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

async function readProblem(res: Response): Promise<ApiError> {
  let body: { code?: unknown; detail?: unknown; errors?: unknown } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    // Non-JSON error bodies fall through to the generic error below.
  }
  const code = typeof body.code === "string" ? body.code : "COM-0500";
  const detail = typeof body.detail === "string" ? body.detail : `request failed with status ${res.status}`;
  const fields = z.record(z.string()).safeParse(body.errors ?? {});
  return new ApiError(res.status, code, detail, fields.success ? fields.data : {});
}

export function newKey(): string {
  return crypto.randomUUID();
}

export interface GrirQuery {
  vendorId?: string;
  status?: GrirStatus;
  ageGt?: number;
  cursor?: string;
  limit?: number;
}

/** GET /grir: open items newest first with per-currency open totals over the whole filter. */
export async function fetchGrir(q: GrirQuery = {}, fetchImpl: FetchLike = defaultFetch): Promise<GrirPage> {
  const params = new URLSearchParams();
  if (q.vendorId) params.set("vendorId", q.vendorId);
  if (q.status) params.set("status", q.status);
  if (q.ageGt !== undefined) params.set("ageGt", String(q.ageGt));
  if (q.cursor) params.set("cursor", q.cursor);
  if (q.limit) params.set("limit", String(q.limit));
  const query = params.size > 0 ? `?${params}` : "";
  const res = await fetchImpl(`${BASE}/grir${query}`, { headers: { Accept: "application/json" } });
  if (!res.ok) throw await readProblem(res);
  return grirPageSchema.parse(await res.json());
}

/** POST /invoices:post. The caller keeps one Idempotency-Key per intended invoice so a retry replays. */
export async function postVendorInvoice(input: InvoiceInput, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<Invoice> {
  const res = await fetchImpl(`${BASE}/invoices:post`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw await readProblem(res);
  return invoiceSchema.parse(await res.json());
}
