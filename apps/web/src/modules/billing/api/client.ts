// Typed clients for the billing and revrec services (services/billing/api/openapi.yaml,
// services/revrec/api/openapi.yaml). The browser calls /api/v1/...; the BFF rewrites to /v1/....

export const billingBase = "/api/v1/billing";
export const revrecBase = "/api/v1/revrec";

export const INVOICE_STATUSES = ["PROPOSAL", "POSTED", "PARTIALLY_SETTLED", "SETTLED", "REVERSED"] as const;
export type InvoiceStatus = (typeof INVOICE_STATUSES)[number];

export interface InvoiceTotals {
  net: string;
  charges: string;
  tax: string;
  gross: string;
}

/** An invoice header as the list returns it (OpenAPI InvoiceSummary). */
export interface InvoiceSummary {
  id: string;
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  type: string;
  status: InvoiceStatus;
  number?: string;
  currency: string;
  invoiceDate: string;
  dueDate: string;
  postingDate?: string;
  paymentTermDays: number;
  totals: InvoiceTotals;
  version: number;
}

export interface InvoiceLine {
  lineNo: number;
  description: string;
  qty: string;
  unitPrice: string;
  discountPct: string;
  net: string;
  tax: string;
  revenueAccount: string;
  taxCode: string;
}

export interface Invoice extends InvoiceSummary {
  lines: InvoiceLine[];
}

export interface FreeTextLineInput {
  description: string;
  qty: string;
  unitPrice: string;
  discountPct?: string;
  revenueAccount: string;
  taxCode: string;
}

export interface FreeTextInvoiceInput {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  currency: string;
  invoiceDate: string;
  paymentTermDays: number;
  chargeAmount?: string;
  lines: FreeTextLineInput[];
}

export const SSP_SOURCES = ["EXPLICIT", "LIST", "PCT_OF_LIST", "RESIDUAL"] as const;
export type SspSource = (typeof SSP_SOURCES)[number];
export const METHODS = ["POINT_IN_TIME", "RATABLE", "MILESTONE", "USAGE"] as const;
export type RecognitionMethod = (typeof METHODS)[number];
export const RESIDUAL_RULES = ["LARGEST", "FIRST", "LAST"] as const;
export type ResidualRule = (typeof RESIDUAL_RULES)[number];

export interface ObligationInput {
  id: string;
  ssp: string;
  sspSource: SspSource;
  method: RecognitionMethod;
  termStart?: string;
  termEnd?: string;
}

export interface AllocationPreviewInput {
  currency: string;
  transactionPrice: string;
  residualRule: ResidualRule;
  obligations: ObligationInput[];
}

export interface SchedulePeriod {
  start: string;
  end: string;
  amount: string;
}

export interface AllocatedObligation {
  id: string;
  ssp: string;
  sspSource: SspSource;
  method: RecognitionMethod;
  allocated: string;
  schedule?: SchedulePeriod[];
}

export interface AllocationPreview {
  currency: string;
  transactionPrice: string;
  residualRule: ResidualRule;
  obligations: AllocatedObligation[];
}

/** What a page shows for a failed call: the stable code, the message and any field errors. */
export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

/** A non-2xx answer carrying the RFC 9457 problem document. */
export class BillingApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string>,
  ) {
    super(message);
    this.name = "BillingApiError";
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof BillingApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "NETWORK", message: "The billing service could not be reached. Check your connection and try again.", fields: {} };
}

/** A fresh Idempotency-Key for one user action; retries of the same action reuse it. */
export function newKey(): string {
  return crypto.randomUUID();
}

async function toError(res: Response): Promise<BillingApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new BillingApiError(res.status, code, message, fields);
}

async function send<T>(url: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(url, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

export interface InvoiceFilter {
  status?: InvoiceStatus | "";
  customerId?: string;
  limit?: number;
}

/** GET /invoices: headers only, newest first, at most `limit` (default 100). */
export async function listInvoices(filter: InvoiceFilter = {}, signal?: AbortSignal): Promise<InvoiceSummary[]> {
  const q = new URLSearchParams();
  for (const [name, value] of Object.entries(filter)) {
    if (value !== undefined && value !== "") q.set(name, String(value));
  }
  const query = q.toString();
  const page = await send<{ items: InvoiceSummary[] }>(`${billingBase}/invoices${query ? `?${query}` : ""}`, {}, signal);
  return page.items;
}

/** GET /invoices/{id}: the invoice with its lines. */
export function getInvoice(id: string, signal?: AbortSignal): Promise<Invoice> {
  return send<Invoice>(`${billingBase}/invoices/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /invoices/{id}:post with If-Match on the version the user reviewed. */
export function postInvoice(id: string, version: number): Promise<Invoice> {
  return send<Invoice>(`${billingBase}/invoices/${encodeURIComponent(id)}:post`, {
    method: "POST",
    headers: { "If-Match": `"${version}"` },
  });
}

/** POST /free-text-invoices: a proposal with tax quoted per line; the key makes retries return the same proposal. */
export function createFreeTextInvoice(input: FreeTextInvoiceInput, idempotencyKey: string): Promise<Invoice> {
  return send<Invoice>(`${billingBase}/free-text-invoices`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** POST /revrec/allocations:preview: SSP allocation and ratable schedules; stores nothing. */
export function previewAllocation(input: AllocationPreviewInput): Promise<AllocationPreview> {
  return send<AllocationPreview>(`${revrecBase}/allocations:preview`, { method: "POST", body: JSON.stringify(input) });
}
