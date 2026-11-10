import { apiFetch } from "../../../shell/api/http";
// Typed clients for the billing and revrec services (services/billing/api/openapi.yaml,
// services/revrec/api/openapi.yaml). The browser calls /api/v1/...; the BFF rewrites to /v1/....

export const billingBase = "/api/v1/billing";
export const revrecBase = "/api/v1/revrec";

export const INVOICE_STATUSES = ["PROPOSAL", "POSTED", "PARTIALLY_SETTLED", "SETTLED", "REVERSED"] as const;
export type InvoiceStatus = (typeof INVOICE_STATUSES)[number];
export const INVOICE_TYPES = ["SHIPMENT", "CONSOLIDATED", "FREE_TEXT", "SUBSCRIPTION", "MILESTONE", "CREDIT_NOTE"] as const;
export type InvoiceType = (typeof INVOICE_TYPES)[number];

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
  /** On a credit note: the invoice it credits. */
  originalInvoiceId?: string;
  originalNumber?: string;
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
  /** On a credit note: the credited line of the original. */
  originalLineNo?: number;
  /** On a creditable invoice: the quantity posted credit notes took. */
  creditedQty?: string;
  /** On a shipment or consolidated invoice: the documents the line came from (FR-29.1). */
  packingSlipId?: string;
  packingSlipLineId?: string;
  orderId?: string;
  orderLineId?: string;
  productId?: string;
  priceExplainRef?: string;
}

/** One open item of the installment plan (FR-29.18). */
export interface Installment {
  no: number;
  percent: string;
  offsetDays: number;
  amount: string;
  dueDate: string;
}

export interface Invoice extends InvoiceSummary {
  lines: InvoiceLine[];
  installments: Installment[];
  /** On a credit note: why it was issued. */
  reason?: string;
  /** On a posted invoice that can be credited: what credit notes took and what is left. */
  credited?: { gross: string; remaining: string };
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
  installmentTerms?: { percent: string; offsetDays: number }[];
  lines: FreeTextLineInput[];
}

/** One posted packing slip line offered for billing: the shipped quantity with the order line's price. */
export interface PackingSlipLineInput {
  id: string;
  orderLineId: string;
  productId: string;
  description: string;
  qty: string;
  unitPrice: string;
  discountPct?: string;
  revenueAccount: string;
  taxCode: string;
  priceExplainRef?: string;
}

export interface PackingSlipInput {
  id: string;
  orderId: string;
  /** The slip's posting date; the invoice date may not precede it (BIL-0006). */
  date: string;
  chargeAmount?: string;
  lines: PackingSlipLineInput[];
}

/** POST /proposals: invoices from posted packing slips of one invoice account, one per order or consolidated. */
export interface ShipmentProposalInput {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  currency: string;
  invoiceDate: string;
  paymentTermDays: number;
  consolidate: boolean;
  slips: PackingSlipInput[];
}

export interface TraceLine {
  lineNo: number;
  description: string;
  qty: string;
  net: string;
  revenueAccount: string;
  originalLineNo?: number;
  packingSlipId?: string;
  packingSlipLineId?: string;
  orderId?: string;
  orderLineId?: string;
  productId?: string;
  priceExplainRef?: string;
}

/** GET /invoices/{id}/trace (FR-29.12): invoice → packing slips → order lines → price explanations. */
export interface InvoiceTrace {
  invoice: InvoiceSummary;
  /** On a credit note: the invoice it credits, through whose lines the trace resolves. */
  original?: InvoiceSummary;
  packingSlips: { id: string; date: string; orderId: string }[];
  lines: TraceLine[];
}

/** A credit note request: without lines the whole remainder of the original is credited. */
export interface CreditNoteInput {
  originalInvoiceId: string;
  creditDate: string;
  reason: string;
  chargeAmount?: string;
  lines?: { lineNo: number; qty: string }[];
}

export const SSP_SOURCES = ["EXPLICIT", "LIST", "PCT_OF_LIST", "RESIDUAL"] as const;
export type SspSource = (typeof SSP_SOURCES)[number];
export const METHODS = ["POINT_IN_TIME", "RATABLE", "MILESTONE", "USAGE"] as const;
export type RecognitionMethod = (typeof METHODS)[number];
export const RESIDUAL_RULES = ["LARGEST", "FIRST", "LAST"] as const;
export type ResidualRule = (typeof RESIDUAL_RULES)[number];
/** How a RATABLE obligation spreads over its term (§5.29.13 ratable_basis). */
export const RATABLE_BASES = ["DAILY", "MONTHLY"] as const;
export type RatableBasis = (typeof RATABLE_BASES)[number];

export interface ObligationInput {
  id: string;
  /** Omitted for RESIDUAL: the service derives it as the price less the other SSPs. */
  ssp?: string;
  sspSource: SspSource;
  method: RecognitionMethod;
  termStart?: string;
  termEnd?: string;
}

export interface AllocationPreviewInput {
  currency: string;
  transactionPrice: string;
  residualRule: ResidualRule;
  ratableBasis: RatableBasis;
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
  ratableBasis: RatableBasis;
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
  const res = await apiFetch(url, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

export interface InvoiceFilter {
  status?: InvoiceStatus | "";
  type?: InvoiceType | "";
  customerId?: string;
  originalInvoiceId?: string;
  /** Only invoices with a line billed from this sales order or packing slip (the document flow). */
  orderId?: string;
  packingSlipId?: string;
  limit?: number;
  /** The nextCursor of the previous page. */
  cursor?: string;
}

/** One page of a list; nextCursor is absent on the last page. */
export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

/** GET /invoices: one page of headers, newest first, at most `limit` (default 100). */
export function listInvoicePage(filter: InvoiceFilter = {}, signal?: AbortSignal): Promise<Page<InvoiceSummary>> {
  return send<Page<InvoiceSummary>>(`${billingBase}/invoices${query({ ...filter })}`, {}, signal);
}

/** GET /invoices: the first page's headers only. */
export async function listInvoices(filter: InvoiceFilter = {}, signal?: AbortSignal): Promise<InvoiceSummary[]> {
  return (await listInvoicePage(filter, signal)).items;
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

/** POST /proposals: one or more invoice proposals from packing slips, stored together; the key makes retries return the same invoices. */
export async function createShipmentProposals(input: ShipmentProposalInput, idempotencyKey: string): Promise<Invoice[]> {
  const batch = await send<{ items: Invoice[] }>(`${billingBase}/proposals`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
  return batch.items;
}

/** GET /invoices/{id}/trace: the documents behind each invoice line. */
export function getInvoiceTrace(id: string, signal?: AbortSignal): Promise<InvoiceTrace> {
  return send<InvoiceTrace>(`${billingBase}/invoices/${encodeURIComponent(id)}/trace`, {}, signal);
}

/** POST /credit-notes: a credit note proposal against a posted invoice; the key makes retries return the same proposal. */
export function createCreditNote(input: CreditNoteInput, idempotencyKey: string): Promise<Invoice> {
  return send<Invoice>(`${billingBase}/credit-notes`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** POST /credit-notes/{id}:post with If-Match on the version the user reviewed. */
export function postCreditNote(id: string, version: number): Promise<Invoice> {
  return send<Invoice>(`${billingBase}/credit-notes/${encodeURIComponent(id)}:post`, {
    method: "POST",
    headers: { "If-Match": `"${version}"` },
  });
}

/** POST /revrec/allocations:preview: SSP allocation and ratable schedules; stores nothing. */
export function previewAllocation(input: AllocationPreviewInput): Promise<AllocationPreview> {
  return send<AllocationPreview>(`${revrecBase}/allocations:preview`, { method: "POST", body: JSON.stringify(input) });
}

// Revenue contracts, milestones, recognition runs and the roll-forward (revrec slice 2).

export const CONTRACT_STATUSES = ["OPEN", "COMPLETE", "CANCELLED"] as const;
export type ContractStatus = (typeof CONTRACT_STATUSES)[number];
export type ScheduleStatus = "SCHEDULED" | "RECOGNIZED" | "REVERSED";

/** A revenue contract header as the list returns it (OpenAPI ContractSummary). */
export interface RevenueContractSummary {
  id: string;
  legalEntityId: string;
  orderId: string;
  customerId: string;
  currency: string;
  transactionPrice: string;
  recognized: string;
  remaining: string;
  residualRule: ResidualRule;
  ratableBasis: RatableBasis;
  contractDate: string;
  status: ContractStatus;
  version: number;
}

export interface ScheduleLine {
  id: string;
  seq: number;
  date?: string;
  amount: string;
  status: ScheduleStatus;
  recognizedOn?: string;
}

export interface ContractObligation {
  id: string;
  lineNo: number;
  productId: string;
  ssp: string;
  sspSource: SspSource;
  allocated: string;
  recognized: string;
  method: RecognitionMethod;
  termStart?: string;
  termEnd?: string;
  satisfiedOn?: string;
  schedule: ScheduleLine[];
}

export interface RevenueContract extends RevenueContractSummary {
  obligations: ContractObligation[];
  warnings: { code: string; detail: string }[];
}

export interface ContractObligationInput {
  productId: string;
  /** Omitted for RESIDUAL: the service derives it as the price less the other SSPs. */
  ssp?: string;
  sspSource: SspSource;
  method: RecognitionMethod;
  termStart?: string;
  termEnd?: string;
  satisfiedOn?: string;
}

export interface RevenueContractInput {
  legalEntityId: string;
  orderId: string;
  customerId: string;
  currency: string;
  transactionPrice: string;
  residualRule: ResidualRule;
  ratableBasis: RatableBasis;
  contractDate: string;
  obligations: ContractObligationInput[];
}

export interface Satisfaction {
  contractId: string;
  contractVersion: number;
  obligationId: string;
  scheduleId: string;
  amount: string;
  recognizedOn: string;
}

export interface RecognitionRun {
  id: string;
  legalEntityId: string;
  periodEnd: string;
  lineCount: number;
  createdAt: string;
}

export interface RollForward {
  legalEntityId: string;
  /** Present on the drill-down to one contract. */
  contractId?: string;
  currency: string;
  period: string;
  start: string;
  end: string;
  opening: string;
  additions: string;
  recognized: string;
  adjustments: string;
  closing: string;
}

export interface ContractFilter {
  status?: ContractStatus | "";
  customerId?: string;
  legalEntityId?: string;
  /** Contracts with at least one obligation of this method. */
  method?: RecognitionMethod | "";
  limit?: number;
  /** The nextCursor of the previous page. */
  cursor?: string;
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [name, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(name, String(value));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

/** GET /revrec/contracts: one page of headers with recognized and remaining, newest first. */
export function listContracts(filter: ContractFilter = {}, signal?: AbortSignal): Promise<Page<RevenueContractSummary>> {
  return send<Page<RevenueContractSummary>>(`${revrecBase}/contracts${query({ ...filter })}`, {}, signal);
}

/** GET /revrec/contracts/{id}: obligations with their schedule lines. */
export function getContract(id: string, signal?: AbortSignal): Promise<RevenueContract> {
  return send<RevenueContract>(`${revrecBase}/contracts/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /revrec/contracts: allocate and plan a contract; the key makes retries return the same contract. */
export function createContract(input: RevenueContractInput, idempotencyKey: string): Promise<RevenueContract> {
  return send<RevenueContract>(`${revrecBase}/contracts`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** POST /revrec/milestones/{obligationId}:satisfy with If-Match on the contract version the user reviewed. */
export function satisfyMilestone(obligationId: string, satisfiedOn: string, contractVersion: number): Promise<Satisfaction> {
  return send<Satisfaction>(`${revrecBase}/milestones/${encodeURIComponent(obligationId)}:satisfy`, {
    method: "POST",
    headers: { "If-Match": `"${contractVersion}"` },
    body: JSON.stringify({ satisfiedOn }),
  });
}

/** POST /revrec/recognition-runs: recognize due ratable lines; a rerun of the period returns the recorded run. */
export function runRecognition(legalEntityId: string, periodEnd: string): Promise<RecognitionRun> {
  return send<RecognitionRun>(`${revrecBase}/recognition-runs`, { method: "POST", body: JSON.stringify({ legalEntityId, periodEnd }) });
}

/** GET /revrec/recognition-runs: latest period first. */
export async function listRuns(legalEntityId: string, signal?: AbortSignal): Promise<RecognitionRun[]> {
  const page = await send<{ items: RecognitionRun[] }>(`${revrecBase}/recognition-runs${query({ legalEntityId, limit: 24 })}`, {}, signal);
  return page.items;
}

/** GET /revrec/roll-forward: one entity, currency and month. */
export function getRollForward(legalEntityId: string, currency: string, period: string, signal?: AbortSignal): Promise<RollForward> {
  return send<RollForward>(`${revrecBase}/roll-forward${query({ legalEntityId, currency, period })}`, {}, signal);
}

/** GET /revrec/roll-forward?contractId=: one contract's month, in the contract's currency (drill-down). */
export function getContractRollForward(contractId: string, period: string, signal?: AbortSignal): Promise<RollForward> {
  return send<RollForward>(`${revrecBase}/roll-forward${query({ contractId, period })}`, {}, signal);
}

/** The last day of a YYYY-MM month as YYYY-MM-DD. */
export function periodEndOf(period: string): string {
  const [year, month] = period.split("-").map(Number);
  const last = new Date(Date.UTC(year, month, 0)).getUTCDate();
  return `${period}-${String(last).padStart(2, "0")}`;
}
