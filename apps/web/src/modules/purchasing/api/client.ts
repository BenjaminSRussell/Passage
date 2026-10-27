// Typed client for the purchasing API (services/purchasing/api/openapi.yaml).
// The browser calls /api/v1/procurement; the web BFF rewrites it to /v1/procurement.
// Money and quantities stay decimal strings end to end; the client never parses them to numbers.

export const PROCUREMENT_BASE = "/api/v1/procurement";

/** RFC 9457 problem details as returned by the service. */
export interface Problem {
  type: string;
  title: string;
  status: number;
  detail?: string;
  code?: string;
  errors?: Record<string, string>;
}

export class ProcurementApiError extends Error {
  readonly problem: Problem;

  constructor(problem: Problem) {
    super(problem.detail ?? problem.title);
    this.name = "ProcurementApiError";
    this.problem = problem;
  }
}

/** A message and code for any failure, for alerts. */
export function describeProblem(error: unknown): { message: string; code: string; errors?: Record<string, string> } {
  if (error instanceof ProcurementApiError) {
    return { message: error.message, code: error.problem.code ?? String(error.problem.status), errors: error.problem.errors };
  }
  if (error instanceof DOMException && error.name === "AbortError") {
    return { message: "The request was cancelled.", code: "ABORTED" };
  }
  return { message: "The purchasing service could not be reached.", code: "NETWORK" };
}

export type RequisitionStatus = "DRAFT" | "IN_REVIEW" | "APPROVED" | "REJECTED" | "CLOSED" | "CANCELLED";
export type ApprovalStatus = "DRAFT" | "IN_REVIEW" | "APPROVED" | "REJECTED" | "CONFIRMED" | "FINALIZED" | "CANCELLED";

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export interface ListOptions {
  limit?: number;
  cursor?: string;
  signal?: AbortSignal;
}

export interface Warning {
  code: string;
  lineNo: number;
  detail: string;
}

export interface RequisitionLine {
  id: string;
  lineNo: number;
  productId?: string;
  description?: string;
  category?: string;
  qty: string;
  unit: string;
  estimatedUnitPrice: string;
  amount: string;
  requestedDate: string;
  siteId?: string;
  preferredVendorId?: string;
  status: string;
  /** Set when CONSOLIDATED: the purchase order line that orders this line. */
  purchaseOrderId?: string;
  purchaseOrderLineNo?: number;
}

export interface Requisition {
  id: string;
  number: string;
  legalEntityId: string;
  operatingUnitId?: string;
  requesterId: string;
  status: RequisitionStatus;
  currency: string;
  total: string;
  approverChain: string[];
  policyVersion?: string;
  version: number;
  lines: RequisitionLine[];
  createdAt: string;
  updatedAt: string;
  warnings?: Warning[];
}

export interface RequisitionSummary {
  id: string;
  number: string;
  legalEntityId: string;
  requesterId: string;
  status: RequisitionStatus;
  currency: string;
  total: string;
  lineCount: number;
  version: number;
  createdAt: string;
}

export interface RequisitionLineInput {
  productId?: string;
  description?: string;
  category?: string;
  qty: string;
  unit: string;
  estimatedUnitPrice?: string;
  requestedDate: string;
  siteId?: string;
  preferredVendorId?: string;
}

export interface RequisitionInput {
  legalEntityId: string;
  operatingUnitId?: string;
  currency: string;
  lines: RequisitionLineInput[];
}

export interface PurchaseOrderLine {
  lineNo: number;
  productId?: string;
  description?: string;
  qty: string;
  unit: string;
  unitPrice: string;
  priceUnit: string;
  discountPct: string;
  grossAmount: string;
  discountAmount: string;
  lineAmount: string;
  taxAmount: string;
  deliveryDate: string;
  priceSource: string;
  requisitionLineId?: string;
  agreementLineId?: string;
  status: string;
}

export interface PurchaseOrderCharge {
  seq: number;
  calc: ChargeCalc;
  value: string;
  amount: string;
}

export interface PurchaseOrder {
  id: string;
  number: string;
  legalEntityId: string;
  vendorId: string;
  siteId?: string;
  warehouseId?: string;
  currency: string;
  approvalStatus: ApprovalStatus;
  documentStatus: string;
  versionNo: number;
  changeManagement: boolean;
  subtotal: string;
  discountTotal: string;
  chargesTotal: string;
  taxTotal: string;
  total: string;
  rowVersion: number;
  lines: PurchaseOrderLine[];
  charges: PurchaseOrderCharge[];
  createdAt: string;
  updatedAt: string;
  /** Returned on create only: PRC-AGR-004 when a release reaches an agreement threshold. */
  warnings?: Warning[];
}

export interface PurchaseOrderSummary {
  id: string;
  number: string;
  legalEntityId: string;
  vendorId: string;
  currency: string;
  approvalStatus: ApprovalStatus;
  documentStatus: string;
  total: string;
  lineCount: number;
  rowVersion: number;
  createdAt: string;
}

export type ChargeCalc = "FIXED" | "PER_UNIT" | "PERCENT";

export interface PurchaseOrderLineInput {
  productId?: string;
  description?: string;
  qty: string;
  unit: string;
  unitPrice?: string;
  priceUnit?: string;
  discountPct?: string;
  taxAmount?: string;
  deliveryDate: string;
  requisitionLineId?: string;
  agreementLineId?: string;
}

export interface PurchaseOrderInput {
  legalEntityId: string;
  vendorId: string;
  siteId?: string;
  warehouseId?: string;
  currency: string;
  changeManagement?: boolean;
  lines: PurchaseOrderLineInput[];
  charges?: { calc: ChargeCalc; value: string }[];
}

export type AccountCode = "VENDOR" | "GROUP" | "ALL";
export type ItemCode = "PRODUCT" | "GROUP" | "ALL";

export interface TradeAgreement {
  id: string;
  relation: string;
  accountCode: AccountCode;
  accountId?: string;
  itemCode: ItemCode;
  itemId?: string;
  currency: string;
  unit: string;
  fromQty: string;
  toQty?: string;
  price: string;
  priceUnit: string;
  validFrom: string;
  validTo?: string;
  consignment: boolean;
  status: string;
}

export interface TradeAgreementInput {
  relation: "PRICE";
  accountCode: AccountCode;
  accountId?: string;
  itemCode: ItemCode;
  itemId?: string;
  currency: string;
  unit: string;
  fromQty?: string;
  toQty?: string;
  price: string;
  priceUnit?: string;
  validFrom: string;
  validTo?: string;
  consignment?: boolean;
}

export interface Candidate {
  agreementId: string;
  specificity: number;
  matched: boolean;
  rank: number;
  reason: string;
}

export interface PriceLookup {
  winner: TradeAgreement | null;
  candidates: Candidate[];
}

export type TieRule = "LATEST_FROM_DATE" | "LOWEST_PRICE";

export interface LookupParams {
  vendor: string;
  product: string;
  qty: string;
  date: string;
  currency: string;
  unit: string;
  tie?: TieRule | "";
  vendorGroup?: string;
  productGroup?: string;
}

export type AgreementStatus = "DRAFT" | "EFFECTIVE" | "ON_HOLD" | "CLOSED";
export type CommitmentType = "QUANTITY" | "VALUE" | "CATEGORY_VALUE" | "TOTAL_VALUE";
export type MaxEnforced = "HARD" | "SOFT" | "NONE";
export type AgreementAction = "activate" | "hold" | "close";

export interface PurchaseAgreementLine {
  id: string;
  lineNo: number;
  commitmentType: CommitmentType;
  productId?: string;
  category?: string;
  unit?: string;
  committedQty: string;
  committedAmount: string;
  unitPrice: string;
  discountPct: string;
  maxEnforced: MaxEnforced;
  priceOverride: boolean;
  releasedQty: string;
  releasedAmount: string;
  pctReleased: string;
  remaining: string;
}

export interface AgreementRelease {
  agreementLineId: string;
  purchaseOrderId: string;
  purchaseOrderNumber: string;
  lineNo: number;
  qty: string;
  unit: string;
  lineAmount: string;
  deliveryDate: string;
  approvalStatus: ApprovalStatus;
  createdAt: string;
}

export interface PurchaseAgreement {
  id: string;
  number: string;
  legalEntityId: string;
  vendorId: string;
  currency: string;
  status: AgreementStatus;
  validFrom: string;
  validTo?: string;
  version: number;
  lines: PurchaseAgreementLine[];
  releases: AgreementRelease[];
  createdAt: string;
  updatedAt: string;
}

export interface PurchaseAgreementSummary {
  id: string;
  number: string;
  legalEntityId: string;
  vendorId: string;
  currency: string;
  validFrom: string;
  validTo?: string;
  status: AgreementStatus;
  lineCount: number;
  maxPctReleased: string;
  version: number;
  createdAt: string;
}

export interface PurchaseAgreementLineInput {
  commitmentType: CommitmentType;
  productId?: string;
  category?: string;
  unit?: string;
  committedQty?: string;
  committedAmount?: string;
  unitPrice?: string;
  discountPct?: string;
  maxEnforced?: MaxEnforced;
  priceOverride?: boolean;
}

export interface PurchaseAgreementInput {
  legalEntityId: string;
  vendorId: string;
  currency: string;
  status?: "DRAFT" | "EFFECTIVE";
  validFrom: string;
  validTo?: string;
  lines: PurchaseAgreementLineInput[];
}

export type SourcingFilter = "NEEDS_SOURCING" | "HAS_VENDOR";

/** An OPEN line of an APPROVED requisition waiting to be ordered. */
export interface SourcingLine extends RequisitionLine {
  requisitionId: string;
  requisitionNumber: string;
  requisitionStatus: RequisitionStatus;
  legalEntityId: string;
  currency: string;
}

export interface ConsolidationInput {
  windowDays?: number;
  lines: { requisitionLineId: string; vendorId?: string; agreementLineId?: string }[];
}

/** Fresh key for one logical command; reuse it when retrying the same command. */
export function newIdempotencyKey(): string {
  return globalThis.crypto.randomUUID();
}

async function send<T>(
  method: "GET" | "POST",
  path: string,
  options: { body?: unknown; headers?: Record<string, string>; signal?: AbortSignal } = {},
): Promise<{ body: T; etag: string | null }> {
  const headers: Record<string, string> = { Accept: "application/json", ...options.headers };
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  const res = await fetch(PROCUREMENT_BASE + path, {
    method,
    headers,
    credentials: "same-origin",
    signal: options.signal,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  if (!res.ok) {
    let problem: Problem;
    try {
      problem = (await res.json()) as Problem;
    } catch {
      problem = { type: "", title: res.statusText || "Request failed", status: res.status };
    }
    throw new ProcurementApiError({ ...problem, status: problem.status ?? res.status });
  }
  return { body: (await res.json()) as T, etag: res.headers.get("ETag") };
}

/** Builds a query string from defined, non-blank values. */
export function query(params: Record<string, string | number | undefined>): string {
  const entries = Object.entries(params)
    .filter(([, v]) => v !== undefined && String(v).trim() !== "")
    .map(([k, v]) => [k, String(v).trim()]);
  const qs = new URLSearchParams(entries).toString();
  return qs === "" ? "" : `?${qs}`;
}

async function list<T>(path: string, filters: Record<string, string | undefined>, options: ListOptions): Promise<Page<T>> {
  const { body } = await send<Page<T>>("GET", path + query({ ...filters, limit: options.limit, cursor: options.cursor }), {
    signal: options.signal,
  });
  return body;
}

export function listRequisitions(filters: { status?: RequisitionStatus | ""; mine?: boolean } = {}, options: ListOptions = {}) {
  return list<RequisitionSummary>(
    "/requisitions",
    { status: filters.status || undefined, mine: filters.mine ? "true" : undefined },
    options,
  );
}

export async function getRequisition(id: string, signal?: AbortSignal) {
  return send<Requisition>("GET", `/requisitions/${encodeURIComponent(id)}`, { signal });
}

export function createRequisition(input: RequisitionInput, idempotencyKey = newIdempotencyKey()) {
  return send<Requisition>("POST", "/requisitions", {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

/** Submits a DRAFT requisition; version is the ETag read when it was loaded. */
export function submitRequisition(id: string, version: number, idempotencyKey = newIdempotencyKey()) {
  return send<Requisition>("POST", `/requisitions/${encodeURIComponent(id)}:submit`, {
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

export function listPurchaseOrders(filters: { approvalStatus?: ApprovalStatus | ""; vendorId?: string } = {}, options: ListOptions = {}) {
  return list<PurchaseOrderSummary>(
    "/purchase-orders",
    { approvalStatus: filters.approvalStatus || undefined, vendorId: filters.vendorId },
    options,
  );
}

export function getPurchaseOrder(id: string, signal?: AbortSignal) {
  return send<PurchaseOrder>("GET", `/purchase-orders/${encodeURIComponent(id)}`, { signal });
}

export function createPurchaseOrder(input: PurchaseOrderInput, idempotencyKey = newIdempotencyKey()) {
  return send<PurchaseOrder>("POST", "/purchase-orders", {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

export function listTradeAgreements(filters: { accountId?: string; itemId?: string; status?: string } = {}, options: ListOptions = {}) {
  return list<TradeAgreement>("/trade-agreements", filters, options);
}

export function getTradeAgreement(id: string, signal?: AbortSignal) {
  return send<TradeAgreement>("GET", `/trade-agreements/${encodeURIComponent(id)}`, { signal });
}

export function createTradeAgreement(input: TradeAgreementInput, idempotencyKey = newIdempotencyKey()) {
  return send<TradeAgreement>("POST", "/trade-agreements", {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

export function lookupPrice(params: LookupParams, signal?: AbortSignal) {
  return send<PriceLookup>("GET", `/pricing/lookup${query({ ...params })}`, { signal });
}

export function listPurchaseAgreements(filters: { status?: AgreementStatus | ""; vendorId?: string } = {}, options: ListOptions = {}) {
  return list<PurchaseAgreementSummary>("/agreements", { status: filters.status || undefined, vendorId: filters.vendorId }, options);
}

export function getPurchaseAgreement(id: string, signal?: AbortSignal) {
  return send<PurchaseAgreement>("GET", `/agreements/${encodeURIComponent(id)}`, { signal });
}

export function createPurchaseAgreement(input: PurchaseAgreementInput, idempotencyKey = newIdempotencyKey()) {
  return send<PurchaseAgreement>("POST", "/agreements", {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

/** Activates, holds or closes an agreement; version is the ETag read when it was loaded. */
export function changePurchaseAgreement(id: string, action: AgreementAction, version: number, idempotencyKey = newIdempotencyKey()) {
  return send<PurchaseAgreement>("POST", `/agreements/${encodeURIComponent(id)}:${action}`, {
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

export function listOpenRequisitionLines(filters: { sourcing?: SourcingFilter | ""; vendorId?: string } = {}, options: ListOptions = {}) {
  return list<SourcingLine>("/requisition-lines", { sourcing: filters.sourcing || undefined, vendorId: filters.vendorId }, options);
}

/** Creates one DRAFT purchase order per vendor, currency, site and delivery window. */
export function consolidateRequisitions(input: ConsolidationInput, idempotencyKey = newIdempotencyKey()) {
  return send<{ purchaseOrders: PurchaseOrder[] }>("POST", "/requisitions:consolidate", {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}
