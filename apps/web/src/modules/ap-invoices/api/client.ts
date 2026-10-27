import { z } from "zod";

import { defaultFetch, request, type FetchLike } from "../../../shell/api/http";

export { ApiError, newKey } from "../../../shell/api/http";

export const GRIR_STATUSES = ["OPEN", "PARTIAL", "CLEARED", "WRITTEN_OFF"] as const;
export type GrirStatus = (typeof GRIR_STATUSES)[number];

export const grirItemSchema = z.object({
  id: z.string(),
  receiptId: z.string(),
  receiptNumber: z.string(),
  lineNo: z.number().int(),
  vendorId: z.string(),
  poLineId: z.string(),
  unitPrice: z.string(),
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
  qty: string;
  unitPrice: string;
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
  return request("GET", `/grir${query}`, grirPageSchema, fetchImpl);
}

/** POST /invoices:post. The caller keeps one Idempotency-Key per intended invoice so a retry replays. */
export async function postVendorInvoice(input: InvoiceInput, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<Invoice> {
  return request("POST", "/invoices:post", invoiceSchema, fetchImpl, { "Idempotency-Key": idempotencyKey }, input);
}
