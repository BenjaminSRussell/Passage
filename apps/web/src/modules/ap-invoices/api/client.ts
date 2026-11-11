import { z } from "zod";

import { ApiError, defaultFetch, newKey, request, type FetchLike } from "../../../shell/api/http";

export { ApiError, newKey };

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

export const INVOICE_STATUSES = ["POSTED", "AUDIT_BLOCKED", "AUDIT_REJECTED"] as const;
export type InvoiceStatus = (typeof INVOICE_STATUSES)[number];

export const MATCH_STATUSES = ["MATCHED", "PRICE_VARIANCE", "QTY_VARIANCE", "NO_RECEIPT", "WAITING_RECEIPT"] as const;
export type MatchStatus = (typeof MATCH_STATUSES)[number];

const matchSchema = z.object({
  status: z.enum(MATCH_STATUSES),
  withinTolerance: z.boolean(),
  lines: z.array(
    z.object({
      lineNo: z.number().int(),
      status: z.enum(MATCH_STATUSES),
      receivedQty: z.string().optional(),
      priceVariance: z.object({ amount: z.string(), currency: z.string() }),
      currencyMismatch: z.boolean().optional(),
    }),
  ),
});

export const invoiceSchema = z.object({
  match: matchSchema.optional(),
  status: z.enum(INVOICE_STATUSES),
  source: z.enum(["MANUAL", "ERS"]).optional(),
  auditReason: z.string().optional(),
  releasedBy: z.string().optional(),
  releasedAt: z.string().optional(),
  rejectedBy: z.string().optional(),
  rejectedAt: z.string().optional(),
  rejectReason: z.string().optional(),
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

export const CAPTURE_STATUSES = ["CAPTURED", "NEEDS_REVIEW"] as const;
export type CaptureStatus = (typeof CAPTURE_STATUSES)[number];
export const REVIEW_REASONS = ["LOW_CONFIDENCE", "TOTAL_MISMATCH", "NO_VENDOR_TAX_ID"] as const;

export const captureSchema = z.object({
  id: z.string(),
  legalEntityId: z.string(),
  status: z.enum(CAPTURE_STATUSES),
  reviewReasons: z.array(z.enum(REVIEW_REASONS)),
  vendorTaxId: z.string(),
  invoiceNumber: z.string(),
  invoiceDate: z.string(),
  currency: z.string(),
  total: z.string(),
  createdAt: z.string(),
});
export type Capture = z.infer<typeof captureSchema>;

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

const invoicePageSchema = z.object({ items: z.array(invoiceSchema), nextCursor: z.string().optional() });
export type InvoicePage = z.infer<typeof invoicePageSchema>;

export interface InvoiceQuery {
  vendorId?: string;
  status?: InvoiceStatus;
  cursor?: string;
  limit?: number;
}

/** GET /invoices: vendor invoices newest first with their lines. */
export async function fetchInvoices(q: InvoiceQuery = {}, fetchImpl: FetchLike = defaultFetch): Promise<InvoicePage> {
  const params = new URLSearchParams();
  if (q.vendorId) params.set("vendorId", q.vendorId);
  if (q.status) params.set("status", q.status);
  if (q.cursor) params.set("cursor", q.cursor);
  if (q.limit) params.set("limit", String(q.limit));
  const query = params.size > 0 ? `?${params}` : "";
  return request("GET", `/invoices${query}`, invoicePageSchema, fetchImpl);
}

/** GET /invoices/{id}: one invoice with its lines; 404 outside the caller's legal entities. */
export async function fetchInvoice(id: string, fetchImpl: FetchLike = defaultFetch): Promise<Invoice> {
  return request("GET", `/invoices/${encodeURIComponent(id)}`, invoiceSchema, fetchImpl);
}

/** POST /invoices:post. The caller keeps one Idempotency-Key per intended invoice so a retry replays. */
export async function postVendorInvoice(input: InvoiceInput, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<Invoice> {
  return request("POST", "/invoices:post", invoiceSchema, fetchImpl, { "Idempotency-Key": idempotencyKey }, input);
}

const capturePageSchema = z.object({ items: z.array(captureSchema), nextCursor: z.string().optional() });

export interface CaptureQuery {
  status?: CaptureStatus;
  cursor?: string;
  limit?: number;
}

/** GET /invoice-captures: captured vendor invoice documents newest first; none of them is posted yet. */
export async function fetchCaptures(q: CaptureQuery = {}, fetchImpl: FetchLike = defaultFetch): Promise<z.infer<typeof capturePageSchema>> {
  const params = new URLSearchParams();
  if (q.status) params.set("status", q.status);
  if (q.cursor) params.set("cursor", q.cursor);
  if (q.limit) params.set("limit", String(q.limit));
  const query = params.size > 0 ? `?${params}` : "";
  return request("GET", `/invoice-captures${query}`, capturePageSchema, fetchImpl);
}

/** A vendor's invoice submitted against received lines in the supplier portal, waiting for accounts payable (#511). */
export const SUBMISSION_STATUSES = ["SUBMITTED", "WITHDRAWN", "REJECTED", "POSTED"] as const;
export type SubmissionStatus = (typeof SUBMISSION_STATUSES)[number];

export const submissionSchema = z.object({
  id: z.string(),
  legalEntityId: z.string(),
  vendorId: z.string().optional(),
  invoiceNumber: z.string(),
  invoiceDate: z.string(),
  currency: z.string(),
  total: z.string(),
  note: z.string().optional(),
  status: z.enum(SUBMISSION_STATUSES),
  lines: z.array(z.object({ receiptId: z.string(), receiptLineNo: z.number().int(), qty: z.string(), unitPrice: z.string(), amount: z.string() })),
  createdAt: z.string(),
  decidedAt: z.string().optional(),
  decisionNote: z.string().optional(),
  invoiceId: z.string().optional(),
});
export type Submission = z.infer<typeof submissionSchema>;

const submissionPageSchema = z.object({ items: z.array(submissionSchema), nextCursor: z.string().optional() });
export type SubmissionPage = z.infer<typeof submissionPageSchema>;

export interface SubmissionQuery {
  vendorId?: string;
  status?: SubmissionStatus;
  cursor?: string;
  limit?: number;
}

/** GET /invoice-submissions: vendors' submissions newest first, in the caller's legal entities. */
export async function fetchSubmissions(q: SubmissionQuery = {}, fetchImpl: FetchLike = defaultFetch): Promise<SubmissionPage> {
  const params = new URLSearchParams();
  if (q.vendorId) params.set("vendorId", q.vendorId);
  if (q.status) params.set("status", q.status);
  if (q.cursor) params.set("cursor", q.cursor);
  if (q.limit) params.set("limit", String(q.limit));
  const query = params.size > 0 ? `?${params}` : "";
  return request("GET", `/invoice-submissions${query}`, submissionPageSchema, fetchImpl);
}

export async function fetchSubmission(id: string, fetchImpl: FetchLike = defaultFetch): Promise<Submission> {
  return request("GET", `/invoice-submissions/${encodeURIComponent(id)}`, submissionSchema, fetchImpl);
}

/** POST /invoice-submissions/{id}:reject closes a submission with a reason the vendor reads. */
export async function rejectSubmission(id: string, reason: string, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<Submission> {
  return request("POST", `/invoice-submissions/${encodeURIComponent(id)}:reject`, submissionSchema, fetchImpl, { "Idempotency-Key": idempotencyKey }, { reason });
}

/** POST /invoice-submissions/{id}:resolve closes a submission with the vendor invoice that was posted from it. */
export async function resolveSubmission(id: string, invoiceId: string, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<Submission> {
  return request("POST", `/invoice-submissions/${encodeURIComponent(id)}:resolve`, submissionSchema, fetchImpl, { "Idempotency-Key": idempotencyKey }, { invoiceId });
}
