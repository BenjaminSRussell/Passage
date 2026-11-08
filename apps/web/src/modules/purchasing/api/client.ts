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
  /** The warehouse the line is wanted in; consolidation keeps different warehouses on different orders. */
  warehouseId?: string;
  preferredVendorId?: string;
  /** The procurement catalog entry the line was picked from; absent on a non-catalog line. */
  catalogEntryId?: string;
  /** The line this one was cut out of by a split (FR-17.3); absent on a line as entered. */
  splitFromLineId?: string;
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
  /** Levels of approverChain that have approved in this round. */
  approvedSteps?: number;
  /** The role that decides next while the requisition is IN_REVIEW. */
  nextApprover?: string;
  policyVersion?: string;
  version: number;
  lines: RequisitionLine[];
  /** The audit trail, oldest first. */
  history?: RequisitionHistory[];
  createdAt: string;
  updatedAt: string;
  warnings?: Warning[];
}

export type RequisitionHistoryAction = "SUBMITTED" | "APPROVED" | "REJECTED" | "REOPENED" | "CANCELLED";

export interface RequisitionHistory {
  id: string;
  action: RequisitionHistoryAction;
  stepNo?: number;
  role?: string;
  actorId?: string;
  comment?: string;
  fromStatus: string;
  toStatus: string;
  requisitionVersion: number;
  createdAt: string;
}

/** A requisition waiting for an approval level, as the approver's inbox lists it. */
export interface RequisitionApproval {
  id: string;
  number: string;
  legalEntityId: string;
  requesterId: string;
  currency: string;
  total: string;
  lineCount: number;
  approvedSteps: number;
  chainLength: number;
  nextApprover: string;
  version: number;
  submittedAt: string;
}

export type RequisitionAction = "approve" | "reject" | "reopen" | "cancel";

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
  warehouseId?: string;
  preferredVendorId?: string;
  /** Pick the line from a published catalog entry; the service fills the blanks from it. */
  catalogEntryId?: string;
}

export interface RequisitionInput {
  legalEntityId: string;
  operatingUnitId?: string;
  currency: string;
  lines: RequisitionLineInput[];
}

export interface PurchaseOrderLine {
  /** The line's stable id, the purchase_order_line_id a drop-ship names. Set by the service. */
  id?: string;
  lineNo: number;
  productId?: string;
  description?: string;
  /** Procurement category the category access rule checks (FR-17.20); fixed once the line is written. */
  category?: string;
  /** Where the line is delivered: its own, else the order's. */
  siteId?: string;
  warehouseId?: string;
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
  /** Written by receiving and payables (wave 18); a quantity cannot drop below them (PRC-PO-007). */
  receivedQty?: string;
  invoicedQty?: string;
  status: string;
  /** TRADE_AGREEMENT:<id> a reprice run last handed to payables for this invoiced line (FR-17.22). */
  invoicedRepriceSource?: string;
}

export interface PurchaseOrderCharge {
  seq: number;
  calc: ChargeCalc;
  value: string;
  amount: string;
  /** The charge code the charge was entered with, and the sides copied from it (FR-17.9). */
  chargeCodeId?: string;
  chargeCode?: string;
  debitType?: ChargeSide;
  creditType?: ChargeSide;
  allocationRule?: AllocationRule;
  /** True when an auto charge added the charge. */
  auto?: boolean;
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
  /** Charge codes auto charges have added to the order; saving with auto charges does not add them again (FR-17.9). */
  autoChargeCodes?: string[];
  createdAt: string;
  updatedAt: string;
  /** Returned on create and edit: PRC-AGR-004 when a release reaches an agreement threshold; PRC-CAT-002 / PRC-CAT-010 category access (FR-17.20). */
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
  /** On an edit, the existing line this entry changes; omitted for a new line. */
  lineNo?: number;
  productId?: string;
  description?: string;
  /** Required under HARD category access when there is no product; a requisition line's category wins. */
  category?: string;
  /** Blank on a new line goes where the order does; on an edit a line keeps its destination. */
  siteId?: string;
  warehouseId?: string;
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
  /** Give calc and value, or a chargeCodeId whose calc applies (a value given overrides the code's default). */
  charges?: { chargeCodeId?: string; calc?: ChargeCalc; value?: string }[];
  /** Adds the charges of the active auto charges that apply to the order (FR-17.9). */
  applyAutoCharges?: boolean;
}

/** PO custom methods (FR-17.7). Approve and reject stand in for the wave 4 approval task. */
export type PurchaseOrderAction = "submit" | "approve" | "reject" | "confirm" | "cancel";

/** One RFC 6902 operation of a version's change (FR-17.8). */
export interface PatchOp {
  op: "add" | "remove" | "replace";
  path: string;
  value?: unknown;
}

export interface PurchaseOrderVersion {
  versionNo: number;
  /** Absent while the version waits for re-approval. */
  confirmedAt?: string;
  confirmedBy?: string;
  reapprovalRequired: boolean;
  createdAt: string;
  patch: PatchOp[];
  snapshot: { currency: string; total: string; lines: { lineNo: number; status: string }[] };
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
  /** The requisition's version: the If-Match value of a split. */
  requisitionVersion: number;
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
  method: "GET" | "POST" | "PATCH" | "PUT" | "DELETE",
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
  // 204 No Content (DELETE) has no body to parse.
  const body = res.status === 204 ? (undefined as T) : ((await res.json()) as T);
  return { body, etag: res.headers.get("ETag") };
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

/** Approve, reject (comment required), reopen or cancel a requisition; version is the ETag read when it was loaded. */
export function actOnRequisition(
  id: string,
  action: RequisitionAction,
  version: number,
  comment = "",
  idempotencyKey = newIdempotencyKey(),
) {
  return send<Requisition>("POST", `/requisitions/${encodeURIComponent(id)}:${action}`, {
    body: comment ? { comment } : undefined,
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

export function listRequisitionApprovals(filters: { role?: string; legalEntityId?: string } = {}, options: ListOptions = {}) {
  return list<RequisitionApproval>(
    "/requisition-approvals",
    { role: filters.role || undefined, legalEntityId: filters.legalEntityId || undefined },
    options,
  );
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

/**
 * Replaces the lines and charges of a PO (FR-17.8). Lines with lineNo change
 * that line, others are new, and live lines left out are cancelled. rowVersion
 * is the ETag read when the PO was loaded; a stale one is 412 PRC-PO-009.
 */
export function editPurchaseOrder(id: string, input: PurchaseOrderInput, rowVersion: number) {
  return send<PurchaseOrder>("PATCH", `/purchase-orders/${encodeURIComponent(id)}`, {
    body: input,
    headers: { "If-Match": `"${rowVersion}"` },
  });
}

/** Runs a PO custom method; rowVersion is the ETag read when the PO was loaded. */
export function actOnPurchaseOrder(id: string, action: PurchaseOrderAction, rowVersion: number, idempotencyKey = newIdempotencyKey()) {
  return send<PurchaseOrder>("POST", `/purchase-orders/${encodeURIComponent(id)}:${action}`, {
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${rowVersion}"` },
  });
}

/** Every stored version of a PO, oldest first, a pending change included. */
export async function listPurchaseOrderVersions(id: string, signal?: AbortSignal) {
  const { body } = await send<{ items: PurchaseOrderVersion[] }>("GET", `/purchase-orders/${encodeURIComponent(id)}/versions`, { signal });
  return body.items;
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

/**
 * Cuts one new OPEN line out of a requisition line for each quantity (FR-17.3). The original keeps what is left;
 * requisitionVersion is the If-Match value. Returns the requisition with all its lines.
 */
export function splitRequisitionLine(lineId: string, requisitionVersion: number, quantities: string[], idempotencyKey = newIdempotencyKey()) {
  return send<Requisition>("POST", `/requisition-lines/${encodeURIComponent(lineId)}:split`, {
    body: { quantities },
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${requisitionVersion}"` },
  });
}

/** Creates one DRAFT purchase order per vendor, currency, site, warehouse and delivery window. */
export function consolidateRequisitions(input: ConsolidationInput, idempotencyKey = newIdempotencyKey()) {
  return send<{ purchaseOrders: PurchaseOrder[]; warnings?: Warning[] }>("POST", "/requisitions:consolidate", {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

// --- Requisition release runs (FR-17.3, PRD 3.17.16) ---

export type ReleaseStatus = "EMPTY" | "COMPLETED" | "PARTIAL" | "FAILED";

/** A draft purchase order a release run created. */
export interface ReleasedOrder {
  purchaseOrderId: string;
  number: string;
  vendorId: string;
  currency: string;
  total: string;
  lineCount: number;
}

/** A requisition line a run left in the consolidation queue, with the problem that stopped it. */
export interface ReleaseSkip {
  requisitionLineId: string;
  requisitionNumber: string;
  lineNo: number;
  vendorId?: string;
  code: string;
  reason: string;
}

/** The buyer's record of one release run: linesReleased + linesSkipped = linesConsidered. */
export interface ReleaseRun {
  id: string;
  legalEntityId: string;
  status: ReleaseStatus;
  windowDays: number;
  maxLines: number;
  linesConsidered: number;
  linesReleased: number;
  linesSkipped: number;
  purchaseOrders: ReleasedOrder[];
  skipped: ReleaseSkip[];
  warnings: Warning[];
  requestedBy?: string;
  createdAt: string;
}

export interface RequisitionReleaseInput {
  legalEntityId: string;
  windowDays?: number;
  maxLines?: number;
}

export function listReleaseRuns(filters: { legalEntityId?: string; status?: ReleaseStatus | "" } = {}, options: ListOptions = {}) {
  return list<ReleaseRun>(
    "/requisition-releases",
    { legalEntityId: filters.legalEntityId || undefined, status: filters.status || undefined },
    options,
  );
}

export function getReleaseRun(id: string, signal?: AbortSignal) {
  return send<ReleaseRun>("GET", `/requisition-releases/${encodeURIComponent(id)}`, { signal });
}

/**
 * Releases the approved requisition lines of a legal entity that have a vendor into draft purchase orders (needs
 * procurement.purchase-order.maintain). Lines that could not be ordered stay in the consolidation queue and are
 * listed in the run's skipped lines.
 */
export function releaseRequisitions(input: RequisitionReleaseInput, idempotencyKey = newIdempotencyKey()) {
  return send<ReleaseRun>("POST", "/requisition-releases", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
}

// --- Open supply (FR-17.14, read by master planning) ---

/** A line of a confirmed purchase order with quantity still to receive. */
export interface OpenOrderLine {
  purchaseOrderId: string;
  number: string;
  lineNo: number;
  legalEntityId: string;
  vendorId: string;
  productId?: string;
  description?: string;
  siteId?: string;
  warehouseId?: string;
  unit: string;
  qty: string;
  receivedQty: string;
  openQty: string;
  deliveryDate: string;
  confirmedDate?: string;
}

export interface OpenSupplyFilters {
  warehouseId?: string;
  siteId?: string;
  productId?: string;
  dueFrom?: string;
  dueTo?: string;
}

export function listOpenOrderLines(filters: OpenSupplyFilters = {}, options: ListOptions = {}) {
  return list<OpenOrderLine>("/purchase-order-lines", { ...filters }, options);
}

// --- Inquiries (PRD 3.17.14) ---

/** Open CONFIRMED purchase orders of one vendor in one currency. */
export interface VendorOpenOrders {
  vendorId: string;
  currency: string;
  orderCount: number;
  lineCount: number;
  openValue: string;
  overdueLines: number;
  overdueValue: string;
  earliestDue: string;
}

/**
 * An open line past its due date. `deliveryDate` is the requested date; `dueDate` is the vendor's
 * confirmed date when there is one, else the requested date (with a delivery schedule, of the first
 * row the received quantity does not cover); `confirmedDate` is that row's confirmed date.
 */
export interface OverdueLine extends OpenOrderLine {
  dueDate: string;
  currency: string;
  daysLate: number;
  openValue: string;
}

/** An open line received in part: `openQty` is still owed; `daysLate` is 0 until the line is due. */
export type BackorderedLine = OverdueLine;

/** A priced line of an approved purchase order for one product. */
export interface PriceHistoryEntry {
  purchaseOrderId: string;
  number: string;
  lineNo: number;
  orderDate: string;
  vendorId: string;
  currency: string;
  unit: string;
  qty: string;
  unitPrice: string;
  priceUnit: string;
  discountPct: string;
  priceSource: string;
  agreementLineId?: string;
}

export interface InquiryFilters {
  /** Expiring agreements: look-ahead in days from `asOf` (default 30). */
  withinDays?: string;
  /** Near-limit agreements: release level in percent (default 90). */
  thresholdPct?: string;
  vendorId?: string;
  productId?: string;
  asOf?: string;
  from?: string;
  to?: string;
}

export function listOpenOrdersByVendor(filters: InquiryFilters = {}, options: ListOptions = {}) {
  return list<VendorOpenOrders>("/inquiries/open-orders-by-vendor", { ...filters }, options);
}

export function listOverdueLines(filters: InquiryFilters = {}, options: ListOptions = {}) {
  return list<OverdueLine>("/inquiries/overdue-lines", { ...filters }, options);
}

export function listBackorderedLines(filters: InquiryFilters = {}, options: ListOptions = {}) {
  return list<BackorderedLine>("/inquiries/backordered-lines", { ...filters }, options);
}

/** An agreement ending within the window, or past its end while still in force (`lapsed`). */
export interface ExpiringAgreement {
  id: string;
  number: string;
  legalEntityId: string;
  vendorId: string;
  currency: string;
  status: "EFFECTIVE" | "ON_HOLD";
  validTo: string;
  daysLeft: number;
  lapsed: boolean;
  lineCount: number;
  /** Quantity lines, not included in the amounts. */
  qtyLines: number;
  committedAmount: string;
  releasedAmount: string;
}

export function listExpiringAgreements(filters: InquiryFilters = {}, options: ListOptions = {}) {
  return list<ExpiringAgreement>("/inquiries/expiring-agreements", { ...filters }, options);
}

/** An agreement line released to the threshold of its commitment (FR-17.14). */
export interface NearLimitLine {
  agreementId: string;
  number: string;
  legalEntityId: string;
  vendorId: string;
  currency: string;
  validTo?: string;
  lineId: string;
  lineNo: number;
  commitmentType: "QUANTITY" | "VALUE" | "CATEGORY_VALUE" | "TOTAL_VALUE";
  productId?: string;
  category?: string;
  unit?: string;
  maxEnforced: "HARD" | "SOFT" | "NONE";
  /** Quantity in `unit` for a QUANTITY line, else an amount in `currency`. */
  committed: string;
  released: string;
  /** Negative after an overrun. */
  remaining: string;
  pctReleased: string;
  overrun: boolean;
}

export function listNearLimitAgreements(filters: InquiryFilters = {}, options: ListOptions = {}) {
  return list<NearLimitLine>("/inquiries/near-limit-agreements", { ...filters }, options);
}

/** A confirmed purchase order whose next version waits for re-approval (FR-17.8, FR-17.14). */
export interface ChangeOrder {
  purchaseOrderId: string;
  number: string;
  legalEntityId: string;
  vendorId: string;
  currency: string;
  confirmedVersion: number;
  pendingVersion: number;
  changeCount: number;
  confirmedTotal: string;
  proposedTotal: string;
  changedAt: string;
  changedBy?: string;
}

export function listChangeOrders(filters: InquiryFilters = {}, options: ListOptions = {}) {
  return list<ChangeOrder>("/inquiries/change-orders", { ...filters }, options);
}

export function listPriceHistory(filters: InquiryFilters = {}, options: ListOptions = {}) {
  return list<PriceHistoryEntry>("/inquiries/price-history", { ...filters }, options);
}

// --- Requests for quotation (FR-17.4, BR-17.11) ---

export type RFQStatus = "DRAFT" | "SENT" | "REPLIES_OPEN" | "CLOSED" | "AWARDED" | "CANCELLED";
export type RankingRule = "LOWEST_PRICE" | "LEAD_TIME" | "WEIGHTED_SCORE";
export type RFQAction = "send" | "close" | "cancel";

export interface RFQLine {
  id: string;
  lineNo: number;
  productId?: string;
  description?: string;
  qty: string;
  unit: string;
  requiredDate?: string;
  /** The requisition line the RFQ line was raised for; an award orders it. */
  requisitionLineId?: string;
}

export interface RFQInvitation {
  vendorId: string;
  status: "INVITED" | "REPLIED";
  sentAt: string;
}

export interface RFQReply {
  id: string;
  lineId: string;
  vendorId: string;
  unitPrice: string;
  priceUnit: string;
  currency: string;
  leadTimeDays: number;
  validUntil?: string;
  notes?: string;
  receivedAt: string;
  late: boolean;
  awarded: boolean;
  overrideReason?: string;
}

/** One weighted criterion of an RFQ's scoring group (FR-17.19). */
export interface ScoringCriterion {
  id: string;
  seq: number;
  name: string;
  weight: string;
}

/** The buyer's 1–10 score of one invited vendor on one criterion. */
export interface VendorScore {
  vendorId: string;
  criterionId: string;
  score: number;
}

export interface RFQ {
  id: string;
  number: string;
  legalEntityId: string;
  status: RFQStatus;
  replyDeadline: string;
  rankingRule: RankingRule;
  /** Replies stay unreadable until the deadline (BR-17.16). */
  sealed?: boolean;
  /** True while a sealed RFQ is before its deadline: replies and scores are then empty. */
  repliesSealed?: boolean;
  /** Reply lines recorded, counted even while sealed. */
  replyCount?: number;
  /** When and by whom the sealed replies were first shown after the deadline (AC-17.14). */
  openedAt?: string;
  openedBy?: string;
  version: number;
  lines: RFQLine[];
  invitations: RFQInvitation[];
  replies: RFQReply[];
  criteria?: ScoringCriterion[];
  scores?: VendorScore[];
  createdAt: string;
  updatedAt: string;
  /** Returned when replies are recorded: PRC-RFQ-003 for each late line. */
  warnings?: Warning[];
}

export interface RFQSummary {
  id: string;
  number: string;
  legalEntityId: string;
  status: RFQStatus;
  replyDeadline: string;
  rankingRule: RankingRule;
  sealed?: boolean;
  lineCount: number;
  vendorCount: number;
  replyCount: number;
  version: number;
  createdAt: string;
}

export interface RFQInput {
  legalEntityId: string;
  /** RFC 3339 instant, in the future. */
  replyDeadline: string;
  rankingRule?: RankingRule;
  sealed?: boolean;
  /** The scoring group; WEIGHTED_SCORE needs at least one. */
  criteria?: { name: string; weight: string }[];
  lines: { productId?: string; description?: string; qty: string; unit: string; requiredDate?: string }[];
}

export interface RFQReplyInput {
  vendorId: string;
  currency: string;
  replies: { lineId: string; unitPrice: string; priceUnit?: string; leadTimeDays: number; validUntil?: string; notes?: string }[];
}

export interface RFQAward {
  lineId: string;
  replyId: string;
  overrideReason?: string;
}

export interface RankedReply {
  rank: number;
  replyId: string;
  vendorId: string;
  unitPrice: string;
  currency: string;
  leadTimeDays: number;
  validUntil?: string;
  total: string;
  /** Σ(weight × score) ÷ Σ weight, when the vendor is scored. */
  weightedScore?: string;
}

export interface RFQComparisonLine {
  lineId: string;
  lineNo: number;
  productId?: string;
  description?: string;
  qty: string;
  unit: string;
  ranked: RankedReply[];
  excluded: { replyId: string; vendorId: string; reason: "LATE" }[];
}

export interface RFQComparison {
  rfqId: string;
  rankingRule: RankingRule;
  lines: RFQComparisonLine[];
}

export function listRFQs(filters: { statuses?: RFQStatus[] } = {}, options: ListOptions = {}) {
  const status = filters.statuses && filters.statuses.length > 0 ? filters.statuses.join(",") : undefined;
  return list<RFQSummary>("/rfqs", { status }, options);
}

export function getRFQ(id: string, signal?: AbortSignal) {
  return send<RFQ>("GET", `/rfqs/${encodeURIComponent(id)}`, { signal });
}

export function createRFQ(input: RFQInput, idempotencyKey = newIdempotencyKey()) {
  return send<RFQ>("POST", "/rfqs", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
}

/** Input of POST /rfqs:from-requisitions: OPEN lines of APPROVED requisitions of one legal entity (FR-17.3). */
export interface RFQFromRequisitionsInput {
  requisitionLineIds: string[];
  /** RFC 3339 instant, in the future. */
  replyDeadline: string;
  rankingRule?: RankingRule;
  sealed?: boolean;
  criteria?: { name: string; weight: string }[];
}

/** Starts a DRAFT RFQ from requisition lines that need sourcing; the lines leave the consolidation queue. */
export function createRFQFromRequisitions(input: RFQFromRequisitionsInput, idempotencyKey = newIdempotencyKey()) {
  return send<RFQ>("POST", "/rfqs:from-requisitions", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
}

/** Sends a DRAFT RFQ to vendors; version is the ETag read when it was loaded. */
export function sendRFQ(id: string, version: number, vendorIds: string[], idempotencyKey = newIdempotencyKey()) {
  return send<RFQ>("POST", `/rfqs/${encodeURIComponent(id)}:send`, {
    body: { vendorIds },
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

/** Closes or cancels an RFQ; version is the ETag read when it was loaded. */
export function changeRFQ(id: string, action: Exclude<RFQAction, "send">, version: number, idempotencyKey = newIdempotencyKey()) {
  return send<RFQ>("POST", `/rfqs/${encodeURIComponent(id)}:${action}`, {
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

/** Records one invited vendor's prices for some lines; late lines come back as PRC-RFQ-003 warnings. */
export function recordRFQReplies(id: string, input: RFQReplyInput, idempotencyKey = newIdempotencyKey()) {
  return send<RFQ>("POST", `/rfqs/${encodeURIComponent(id)}/replies`, {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

export function compareRFQ(id: string, rule?: RankingRule, signal?: AbortSignal) {
  return send<RFQComparison>("GET", `/rfqs/${encodeURIComponent(id)}/compare${query({ rule })}`, { signal });
}

/** Records the buyer's 1–10 scores of one invited vendor (duty procurement.rfq.award); version is the RFQ ETag. */
export function scoreRFQVendor(id: string, vendorId: string, version: number, scores: { criterionId: string; score: number }[]) {
  return send<RFQ>("PUT", `/rfqs/${encodeURIComponent(id)}/scores/${encodeURIComponent(vendorId)}`, {
    body: { scores },
    headers: { "If-Match": `"${version}"` },
  });
}

/** Awards lines to replies (duty procurement.rfq.award) and creates one DRAFT PO per vendor and currency. */
export function awardRFQ(id: string, version: number, awards: RFQAward[], idempotencyKey = newIdempotencyKey()) {
  return send<{ rfq: RFQ; purchaseOrders: PurchaseOrder[] }>("POST", `/rfqs/${encodeURIComponent(id)}:award`, {
    body: { awards },
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

// --- Invoicing plans on PO lines (FR-17.21, BR-17.17) ---

export type PlanKind = "PERIODIC" | "PARTIAL";
export type PlanFrequency = "MONTHLY" | "QUARTERLY";

export interface InvoicingPlanRow {
  seq: number;
  dueDate: string;
  amount: string;
  percent?: string;
  status: "OPEN" | "INVOICED";
}

export interface InvoicingPlan {
  purchaseOrderId: string;
  lineNo: number;
  kind: PlanKind;
  frequency?: PlanFrequency;
  billingDay?: number;
  settleWithoutReceipt: boolean;
  /** The line amount the rows sum to. */
  lineAmount: string;
  currency: string;
  rows: InvoicingPlanRow[];
  updatedAt: string;
}

/** A plan with the PO row version after the call: plan writes bump it. */
export interface InvoicingPlanResult extends InvoicingPlan {
  poRowVersion: number;
}

export type InvoicingPlanInput =
  | {
      kind: "PERIODIC";
      start: string;
      frequency: PlanFrequency;
      periods: number;
      billingDay?: number;
      amountPerPeriod: string;
      settleWithoutReceipt: boolean;
    }
  | {
      kind: "PARTIAL";
      milestones: { date: string; percent?: string; amount?: string }[];
      settleWithoutReceipt: boolean;
    };

function planPath(poId: string, lineNo: number) {
  return `/purchase-orders/${encodeURIComponent(poId)}/lines/${lineNo}/invoicing-plan`;
}

/** Every invoicing plan of a PO, in line order. */
export async function listInvoicingPlans(poId: string, signal?: AbortSignal) {
  const { body } = await send<{ items: InvoicingPlan[] }>("GET", `/purchase-orders/${encodeURIComponent(poId)}/invoicing-plans`, { signal });
  return body.items;
}

/** One line's plan; 404 PRC-IP-NOT-FOUND when the line has none. */
export function getInvoicingPlan(poId: string, lineNo: number, signal?: AbortSignal) {
  return send<InvoicingPlanResult>("GET", planPath(poId, lineNo), { signal });
}

/** Replaces the line's plan; rowVersion is the PO ETag (412 PRC-PO-009 when stale). */
export function putInvoicingPlan(poId: string, lineNo: number, rowVersion: number, input: InvoicingPlanInput) {
  return send<InvoicingPlanResult>("PUT", planPath(poId, lineNo), { body: input, headers: { "If-Match": `"${rowVersion}"` } });
}

/** Removes the line's plan; resolves to the new PO row version. */
export async function deleteInvoicingPlan(poId: string, lineNo: number, rowVersion: number) {
  const { etag } = await send<undefined>("DELETE", planPath(poId, lineNo), { headers: { "If-Match": `"${rowVersion}"` } });
  return etag === null ? rowVersion + 1 : Number(etag.replace(/"/g, ""));
}

// --- Delivery schedules on PO lines (FR-17.10) ---

export interface DeliveryScheduleRow {
  seq: number;
  qty: string;
  /** The date the PO asks for. */
  deliveryDate: string;
  /** The date the vendor confirmed, when it has. */
  confirmedDate?: string;
  confirmedBy?: string;
  confirmedAt?: string;
}

export interface DeliverySchedule {
  purchaseOrderId: string;
  lineNo: number;
  unit: string;
  /** The line quantity the rows add up to. */
  lineQty: string;
  rows: DeliveryScheduleRow[];
}

/** A schedule with the PO row version after the call: schedule writes bump it. */
export interface DeliveryScheduleResult extends DeliverySchedule {
  poRowVersion: number;
}

export interface DeliveryScheduleInput {
  rows: { qty: string; deliveryDate: string }[];
}

function schedulePath(poId: string, lineNo: number) {
  return `/purchase-orders/${encodeURIComponent(poId)}/lines/${lineNo}/delivery-schedule`;
}

/** Every delivery schedule of a PO, in line order. */
export async function listDeliverySchedules(poId: string, signal?: AbortSignal) {
  const { body } = await send<{ items: DeliverySchedule[] }>("GET", `/purchase-orders/${encodeURIComponent(poId)}/delivery-schedules`, { signal });
  return body.items;
}

/** One line's schedule; 404 PRC-DS-NOT-FOUND when the line has none. */
export function getDeliverySchedule(poId: string, lineNo: number, signal?: AbortSignal) {
  return send<DeliveryScheduleResult>("GET", schedulePath(poId, lineNo), { signal });
}

/** Replaces the line's schedule on a DRAFT or REJECTED PO; rowVersion is the PO ETag (412 PRC-PO-009 when stale). */
export function putDeliverySchedule(poId: string, lineNo: number, rowVersion: number, input: DeliveryScheduleInput) {
  return send<DeliveryScheduleResult>("PUT", schedulePath(poId, lineNo), { body: input, headers: { "If-Match": `"${rowVersion}"` } });
}

/** Removes the line's schedule; resolves to the new PO row version. */
export async function deleteDeliverySchedule(poId: string, lineNo: number, rowVersion: number) {
  const { etag } = await send<undefined>("DELETE", schedulePath(poId, lineNo), { headers: { "If-Match": `"${rowVersion}"` } });
  return etag === null ? rowVersion + 1 : Number(etag.replace(/"/g, ""));
}

/** Records the date the vendor confirmed for one row of a CONFIRMED PO. */
export function confirmDeliveryDate(poId: string, lineNo: number, seq: number, rowVersion: number, confirmedDate: string) {
  return send<DeliveryScheduleResult>("PUT", `${schedulePath(poId, lineNo)}/rows/${seq}/confirmation`, {
    body: { confirmedDate },
    headers: { "If-Match": `"${rowVersion}"` },
  });
}

// --- Subsequent price change (FR-17.22, BR-17.18). Wire shapes follow services/purchasing/api/openapi.yaml. ---

/** One line at its current and at the winning trade agreement price; amounts are for qty. */
export interface RepriceChange {
  lineNo: number;
  qty: string;
  oldPrice: string;
  oldPriceUnit: string;
  newPrice: string;
  newPriceUnit: string;
  oldAmount: string;
  newAmount: string;
  delta: string;
  oldPriceSource: string;
  newPriceSource: string;
}

export type RepriceSkipReason =
  | "MANUAL_PRICE"
  | "AGREEMENT_LINE"
  | "NOT_TRADE_AGREEMENT"
  | "NO_CURRENT_PRICE"
  | "UNCHANGED"
  | "INVOICED"
  | "HANDED_OVER";

export interface RepriceSkip {
  lineNo: number;
  reason: RepriceSkipReason;
}

export interface RepriceResult {
  purchaseOrderId: string;
  number: string;
  currency: string;
  applied: boolean;
  replayed?: boolean;
  versionNo: number;
  reapprovalRequired: boolean;
  pricingDate: string;
  totalDelta: string;
  /** Open, un-invoiced lines that move. */
  changes: RepriceChange[];
  /** Invoiced lines that keep the old price and go to payables for a subsequent credit or debit. */
  invoiced: RepriceChange[];
  skipped: RepriceSkip[];
  /** Set only in a run over many orders. */
  error?: { code: string; detail: string };
}

export interface RepriceRunInput {
  legalEntityId?: string;
  vendorId?: string;
  tradeAgreementId?: string;
  limit?: number;
  cursor?: string;
}

export interface RepriceRunResult {
  applied: boolean;
  visited: number;
  items: RepriceResult[];
  nextCursor?: string;
}

function repricePath(poId: string): string {
  return `/purchase-orders/${encodeURIComponent(poId)}:reprice`;
}

/** Read-only preview (procurement.purchase-order.inquire); the ETag is the PO row version to pass to applyReprice. */
export function previewReprice(poId: string, signal?: AbortSignal) {
  return send<RepriceResult>("POST", repricePath(poId), { body: { apply: false }, signal });
}

/** Applies the run as the next PO version (procurement.purchase-order.confirm); a stale rowVersion is 412 PRC-PO-009. */
export function applyReprice(poId: string, rowVersion: number, idempotencyKey = newIdempotencyKey()) {
  return send<RepriceResult>("POST", repricePath(poId), {
    body: { apply: true },
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${rowVersion}"` },
  });
}

/** One page of the run over many confirmed orders; apply writes each order in its own transaction. */
export async function runReprice(input: RepriceRunInput, apply: boolean, idempotencyKey = newIdempotencyKey()) {
  const body: Record<string, unknown> = { apply };
  for (const [k, v] of Object.entries(input)) {
    if (v !== undefined && String(v).trim() !== "") body[k] = typeof v === "string" ? v.trim() : v;
  }
  const { body: result } = await send<RepriceRunResult>("POST", "/purchase-orders:reprice", {
    body,
    headers: apply ? { "Idempotency-Key": idempotencyKey } : undefined,
  });
  return result;
}

// --- Scheduling agreements and releases (FR-17.15, BR-17.13) ---

export type ReleaseKind = "FRC" | "JIT";
export type ScheduleZone = "FIRM" | "TRADEOFF" | "FORECAST";
export type ReleaseFrequency = "DAILY" | "WEEKLY" | "MONTHLY";

export interface ReleaseProfile {
  frcHorizonDays: number;
  jitHorizonDays: number;
  firmZoneDays: number;
  tradeoffZoneDays: number;
  releaseFrequency: ReleaseFrequency;
}

export interface ScheduleLine {
  lineNo: number;
  deliveryDate: string;
  /** Zone on the agreement's zonesAsOf day. */
  zone: ScheduleZone;
  qty: string;
  receivedQty: string;
  openQty: string;
  reductionReason?: string;
}

export interface SchedulingAgreement {
  id: string;
  number: string;
  legalEntityId: string;
  vendorId: string;
  productId: string;
  siteId: string;
  currency: string;
  unit: string;
  unitPrice: string;
  targetQty: string;
  scheduledQty: string;
  receivedQty: string;
  validFrom: string;
  validTo?: string;
  profile: ReleaseProfile;
  zonesAsOf: string;
  version: number;
  lines: ScheduleLine[];
  /** After a schedule change: one per reduction inside the trade-off zone. */
  warnings?: string[];
  createdAt: string;
  updatedAt: string;
}

export interface SchedulingAgreementRow {
  id: string;
  number: string;
  legalEntityId: string;
  vendorId: string;
  productId: string;
  siteId: string;
  unit: string;
  validFrom: string;
  validTo?: string;
  releaseFrequency: ReleaseFrequency;
  scheduledQty: string;
  receivedQty: string;
  openQty: string;
  releaseCount: number;
  lastReleaseNumber?: string;
  lastReleaseKind?: ReleaseKind;
  lastRunDate?: string;
  nextReleaseDue?: string;
  version: number;
}

export interface ScheduleLineInput {
  /** Set to change an existing line; leave out to add one. */
  lineNo?: number;
  deliveryDate: string;
  qty: string;
  /** Required to reduce, move or remove a FIRM line (PRC-SA-001). */
  reason?: string;
}

export interface SchedulingAgreementInput {
  legalEntityId: string;
  vendorId: string;
  productId: string;
  siteId: string;
  currency: string;
  unit: string;
  unitPrice: string;
  targetQty?: string;
  validFrom: string;
  validTo?: string;
  profile?: Partial<ReleaseProfile>;
  lines: ScheduleLineInput[];
}

export interface SchedulingReleaseLine {
  lineNo: number;
  scheduleLineNo: number;
  deliveryDate: string;
  zone: ScheduleZone;
  inHorizon: boolean;
  qty: string;
  cumulativeReleased: string;
  cumulativeReceived: string;
}

export interface SchedulingRelease {
  id: string;
  agreementId: string;
  releaseNo: number;
  number: string;
  kind: ReleaseKind;
  runDate: string;
  agreementVersion: number;
  cumulativeReset: boolean;
  cumulativeReleased: string;
  cumulativeReceived: string;
  /** Absent in the release list. */
  lines?: SchedulingReleaseLine[];
}

export interface ReleaseRunInput {
  kind: ReleaseKind;
  runDate?: string;
  cumulativeReset?: boolean;
}

const saPath = (id: string) => `/scheduling-agreements/${encodeURIComponent(id)}`;

/** The release monitor: open cumulative quantity, last release and next release due. */
export function listSchedulingAgreements(filters: { vendorId?: string } = {}, options: ListOptions = {}) {
  return list<SchedulingAgreementRow>("/scheduling-agreements", { vendorId: filters.vendorId }, options);
}

export function getSchedulingAgreement(id: string, signal?: AbortSignal) {
  return send<SchedulingAgreement>("GET", saPath(id), { signal });
}

export function createSchedulingAgreement(input: SchedulingAgreementInput, idempotencyKey = newIdempotencyKey()) {
  return send<SchedulingAgreement>("POST", "/scheduling-agreements", {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

/**
 * Replaces the whole schedule: lines with lineNo change that line, others are
 * added, and lines left out are removed. version is the ETag read with the
 * agreement; a stale one is 409 PRC-VERSION-CONFLICT.
 */
export function replaceSchedule(id: string, lines: ScheduleLineInput[], version: number) {
  return send<SchedulingAgreement>("PUT", `${saPath(id)}/schedule`, {
    body: { lines },
    headers: { "If-Match": `"${version}"` },
  });
}

/** Runs a release; 201 for a new release, 200 when the schedule did not change since the same run. */
export function runSchedulingRelease(id: string, input: ReleaseRunInput, idempotencyKey = newIdempotencyKey()) {
  return send<SchedulingRelease>("POST", `${saPath(id)}:release`, {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

export async function listSchedulingReleases(id: string, signal?: AbortSignal) {
  const { body } = await send<{ items: SchedulingRelease[] }>("GET", `${saPath(id)}/releases`, { signal });
  return body.items;
}

export function getSchedulingRelease(id: string, releaseNo: number, signal?: AbortSignal) {
  return send<SchedulingRelease>("GET", `${saPath(id)}/releases/${releaseNo}`, { signal });
}

/** Purchasing policy of one legal entity (FR-17.13); version 0 means none is saved and the defaults apply. */
export interface PurchasingPolicy {
  legalEntityId: string;
  changeManagementDefault: boolean;
  poFourEyes: boolean;
  allowInvoiceBeforeReceipt: boolean;
  /** HARD refuses a vendor with no valid assignment on a restricted category, SOFT warns, OFF does not check (FR-17.20). */
  categoryAccessMode: CategoryAccessMode;
  version: number;
  updatedBy?: string;
  updatedAt?: string;
}

export type CategoryAccessMode = "HARD" | "SOFT" | "OFF";

export type PurchasingPolicyInput = Pick<
  PurchasingPolicy,
  "changeManagementDefault" | "poFourEyes" | "allowInvoiceBeforeReceipt" | "categoryAccessMode"
>;

export async function listPolicies(signal?: AbortSignal) {
  const { body } = await send<{ items: PurchasingPolicy[] }>("GET", "/purchasing-policies", { signal });
  return body.items;
}

export function getPolicy(legalEntityId: string, signal?: AbortSignal) {
  return send<PurchasingPolicy>("GET", `/purchasing-policies/${encodeURIComponent(legalEntityId)}`, { signal });
}

/** Saves the policy; version is the one read (0 creates it). PUT is idempotent, so no key is sent. */
export function savePolicy(legalEntityId: string, input: PurchasingPolicyInput, version: number) {
  return send<PurchasingPolicy>("PUT", `/purchasing-policies/${encodeURIComponent(legalEntityId)}`, {
    body: input,
    headers: { "If-Match": `"${version}"` },
  });
}

// --- Procurement catalogs (FR-17.18) ---

export type CatalogStatus = "DRAFT" | "PUBLISHED" | "RETIRED";
export type CatalogAction = "publish" | "retire";

export interface CatalogEntryInput {
  vendorId: string;
  vendorPartNo?: string;
  productId?: string;
  name: string;
  description?: string;
  category: string;
  unit: string;
  unitPrice: string;
  currency: string;
  leadTimeDays?: number;
  preferred?: boolean;
  imageUrl?: string;
}

export interface CatalogEntry extends CatalogEntryInput {
  id: string;
  entryNo: number;
}

export interface Catalog {
  id: string;
  number: string;
  legalEntityId: string;
  name: string;
  description?: string;
  status: CatalogStatus;
  validFrom: string;
  validTo?: string;
  orgUnitIds: string[];
  version: number;
  entryCount: number;
  /** The first 200 entries; entriesNextCursor pages on through listCatalogEntries. */
  entries: CatalogEntry[];
  entriesNextCursor?: string;
  createdAt: string;
  updatedAt: string;
}

export interface CatalogSummary {
  id: string;
  number: string;
  legalEntityId: string;
  name: string;
  status: CatalogStatus;
  validFrom: string;
  validTo?: string;
  entryCount: number;
  version: number;
  createdAt: string;
}

export interface CatalogInput {
  legalEntityId: string;
  name: string;
  description?: string;
  validFrom: string;
  validTo?: string;
  orgUnitIds?: string[];
  entries?: CatalogEntryInput[];
}

/** One requisition picker result: an entry with the catalog it belongs to. */
export interface CatalogHit extends CatalogEntry {
  catalogId: string;
  catalogNumber: string;
  catalogName: string;
}

export function listCatalogs(filters: { status?: CatalogStatus | "" } = {}, options: ListOptions = {}) {
  return list<CatalogSummary>("/catalogs", { status: filters.status || undefined }, options);
}

export function getCatalog(id: string, signal?: AbortSignal) {
  return send<Catalog>("GET", `/catalogs/${encodeURIComponent(id)}`, { signal });
}

export function createCatalog(input: CatalogInput, idempotencyKey = newIdempotencyKey()) {
  return send<Catalog>("POST", "/catalogs", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
}

/** Publishes or retires a catalog; version is the ETag read when it was loaded. */
export function actOnCatalog(id: string, action: CatalogAction, version: number, idempotencyKey = newIdempotencyKey()) {
  return send<Catalog>("POST", `/catalogs/${encodeURIComponent(id)}:${action}`, {
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

export function addCatalogEntries(id: string, entries: CatalogEntryInput[], version: number, idempotencyKey = newIdempotencyKey()) {
  return send<Catalog>("POST", `/catalogs/${encodeURIComponent(id)}/entries`, {
    body: { entries },
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

export function removeCatalogEntry(id: string, entryId: string, version: number) {
  return send<Catalog>("DELETE", `/catalogs/${encodeURIComponent(id)}/entries/${encodeURIComponent(entryId)}`, {
    headers: { "If-Match": `"${version}"` },
  });
}

/** A catalog's entries in entry order, a page at a time (up to 50,000 entries per catalog). */
export function listCatalogEntries(id: string, options: ListOptions = {}) {
  return list<CatalogEntry>(`/catalogs/${encodeURIComponent(id)}/entries`, {}, options);
}

/** The requisition picker: entries of catalogs published today to the legal entity and unit. */
export function searchCatalogEntries(
  filters: { legalEntityId: string; operatingUnitId?: string; q?: string; category?: string },
  options: ListOptions = {},
) {
  return list<CatalogHit>("/catalog-entries", { ...filters }, options);
}

// --- Vendor catalog import (FR-17.18, AC-17.13) ---

export type CatalogImportStatus = "STAGED" | "COMMITTED" | "CANCELLED";
export type ImportRowStatus = "ACCEPTABLE" | "ERROR" | "ACCEPTED" | "REJECTED";
export type CatalogImportAction = "commit" | "cancel";

export interface CatalogImport {
  id: string;
  catalogId: string;
  legalEntityId: string;
  vendorId: string;
  format: "CSV" | "JSON";
  fileName?: string;
  status: CatalogImportStatus;
  totalRows: number;
  acceptableRows: number;
  errorRows: number;
  acceptedRows: number;
  rejectedRows: number;
  entriesAdded: number;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ImportRow {
  rowNo: number;
  status: ImportRowStatus;
  reason?: string;
  raw: Record<string, string>;
  entry?: CatalogEntryInput;
}

export interface VendorImportInput {
  vendorId: string;
  format: "CSV";
  fileName?: string;
  csv: string;
}

export interface ImportDecisions {
  acceptAll?: boolean;
  accept?: number[];
  reject?: number[];
}

export function stageVendorImport(catalogId: string, input: VendorImportInput, idempotencyKey = newIdempotencyKey()) {
  return send<CatalogImport>("POST", `/catalogs/${encodeURIComponent(catalogId)}/vendor-import`, {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

export function listCatalogImports(filters: { catalogId?: string; status?: CatalogImportStatus | "" } = {}, options: ListOptions = {}) {
  return list<CatalogImport>("/catalog-imports", { catalogId: filters.catalogId, status: filters.status || undefined }, options);
}

export function getCatalogImport(id: string, signal?: AbortSignal) {
  return send<CatalogImport>("GET", `/catalog-imports/${encodeURIComponent(id)}`, { signal });
}

export function listImportRows(id: string, filters: { status?: ImportRowStatus | "" } = {}, options: ListOptions = {}) {
  return list<ImportRow>(`/catalog-imports/${encodeURIComponent(id)}/rows`, { status: filters.status || undefined }, options);
}

/** The buyer's accept and reject choices; version is the ETag read with the import. */
export function decideImportRows(id: string, decisions: ImportDecisions, version: number, idempotencyKey = newIdempotencyKey()) {
  return send<CatalogImport>("POST", `/catalog-imports/${encodeURIComponent(id)}/decisions`, {
    body: decisions,
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

export function actOnCatalogImport(id: string, action: CatalogImportAction, version: number, idempotencyKey = newIdempotencyKey()) {
  return send<CatalogImport>("POST", `/catalog-imports/${encodeURIComponent(id)}:${action}`, {
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

// --- Category vendor assignment and managers (FR-17.20) ---

export type AssignmentStatus = "APPROVED" | "PROVISIONAL" | "BLOCKED";

export interface CategoryVendorAssignment {
  id: string;
  legalEntityId: string;
  category: string;
  vendorId: string;
  status: AssignmentStatus;
  validFrom: string;
  validTo?: string;
  notes?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface CategoryVendorAssignmentInput {
  legalEntityId: string;
  category: string;
  vendorId: string;
  status: AssignmentStatus;
  validFrom?: string;
  validTo?: string;
  notes?: string;
}

export interface CategoryVendorAssignmentChange {
  status: AssignmentStatus;
  validFrom?: string;
  validTo?: string;
  notes?: string;
}

export interface CategoryManager {
  id: string;
  legalEntityId: string;
  category: string;
  workerId: string;
  createdAt: string;
}

export interface CategoryManagerInput {
  legalEntityId: string;
  category: string;
  workerId: string;
}

export function listCategoryVendors(
  filters: { status?: AssignmentStatus | ""; category?: string; legalEntityId?: string; vendorId?: string } = {},
  options: ListOptions = {},
) {
  return list<CategoryVendorAssignment>(
    "/category-vendors",
    { status: filters.status || undefined, category: filters.category, legalEntityId: filters.legalEntityId, vendorId: filters.vendorId },
    options,
  );
}

export function getCategoryVendor(id: string, signal?: AbortSignal) {
  return send<CategoryVendorAssignment>("GET", `/category-vendors/${encodeURIComponent(id)}`, { signal });
}

export function createCategoryVendor(input: CategoryVendorAssignmentInput, idempotencyKey = newIdempotencyKey()) {
  return send<CategoryVendorAssignment>("POST", "/category-vendors", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
}

/** Changes status, window and notes under the version read. PUT is idempotent, so no key is sent. */
export function changeCategoryVendor(id: string, input: CategoryVendorAssignmentChange, version: number) {
  return send<CategoryVendorAssignment>("PUT", `/category-vendors/${encodeURIComponent(id)}`, {
    body: input,
    headers: { "If-Match": `"${version}"` },
  });
}

export function listCategoryManagers(filters: { category?: string; legalEntityId?: string } = {}, options: ListOptions = {}) {
  return list<CategoryManager>("/category-managers", { category: filters.category, legalEntityId: filters.legalEntityId }, options);
}

export function createCategoryManager(input: CategoryManagerInput, idempotencyKey = newIdempotencyKey()) {
  return send<CategoryManager>("POST", "/category-managers", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
}

export function removeCategoryManager(id: string) {
  return send<void>("DELETE", `/category-managers/${encodeURIComponent(id)}`);
}

// --- Signing limits and their requests (FR-17.17) ---

export type SigningDocumentType = "REQUISITION" | "PURCHASE_ORDER" | "INVOICE" | "EXPENSE";
export type SigningRequestStatus = "PENDING" | "APPROVED" | "REJECTED";

export interface SigningLimit {
  id: string;
  legalEntityId: string;
  workerId: string;
  documentType: SigningDocumentType;
  currency: string;
  spendingLimit: string;
  approvalLimit: string;
  recertifyBy: string;
  lastRecertifiedOn?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface SigningLimitRequest {
  id: string;
  legalEntityId: string;
  workerId: string;
  documentType: SigningDocumentType;
  currency: string;
  spendingLimit: string;
  approvalLimit: string;
  reason?: string;
  status: SigningRequestStatus;
  requestedBy: string;
  requestedAt: string;
  decidedBy?: string;
  decidedAt?: string;
  decisionNote?: string;
  signingLimitId?: string;
  version: number;
}

export interface SigningLimitRequestInput {
  legalEntityId: string;
  workerId: string;
  documentType: SigningDocumentType;
  currency: string;
  spendingLimit: string;
  approvalLimit: string;
  reason?: string;
}

export function listSigningLimits(
  filters: { documentType?: SigningDocumentType | ""; workerId?: string; expired?: boolean } = {},
  options: ListOptions = {},
) {
  return list<SigningLimit>(
    "/signing-limits",
    { documentType: filters.documentType || undefined, workerId: filters.workerId, expired: filters.expired ? "true" : undefined },
    options,
  );
}

export function getSigningLimit(id: string, signal?: AbortSignal) {
  return send<SigningLimit>("GET", `/signing-limits/${encodeURIComponent(id)}`, { signal });
}

/** Moves the recertification date 12 months out; needs procurement.signing-limit.approve. */
export function recertifySigningLimit(id: string, version: number, idempotencyKey = newIdempotencyKey()) {
  return send<SigningLimit>("POST", `/signing-limits/${encodeURIComponent(id)}:recertify`, {
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

export function listSigningLimitRequests(filters: { status?: SigningRequestStatus | ""; workerId?: string } = {}, options: ListOptions = {}) {
  return list<SigningLimitRequest>("/signing-limit-requests", { status: filters.status || undefined, workerId: filters.workerId }, options);
}

export function getSigningLimitRequest(id: string, signal?: AbortSignal) {
  return send<SigningLimitRequest>("GET", `/signing-limit-requests/${encodeURIComponent(id)}`, { signal });
}

export function createSigningLimitRequest(input: SigningLimitRequestInput, idempotencyKey = newIdempotencyKey()) {
  return send<SigningLimitRequest>("POST", "/signing-limit-requests", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
}

/** Approves or rejects a pending request; needs procurement.signing-limit.approve. */
export function decideSigningLimitRequest(
  id: string,
  action: "approve" | "reject",
  version: number,
  note: string,
  idempotencyKey = newIdempotencyKey(),
) {
  return send<SigningLimitRequest>("POST", `/signing-limit-requests/${encodeURIComponent(id)}:${action}`, {
    body: note ? { note } : undefined,
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

// --- Charge codes and auto charges (FR-17.9) ---

export type ChargeSide = "ITEM" | "LEDGER" | "VENDOR";
export type AllocationRule = "QTY" | "AMOUNT" | "LINES";
export type ChargeStatus = "ACTIVE" | "INACTIVE";
export type AutoChargeKey = "VENDOR" | "GROUP" | "ALL";

export interface ChargeCode {
  id: string;
  legalEntityId: string;
  code: string;
  name: string;
  calc: ChargeCalc;
  defaultValue: string;
  debitType: ChargeSide;
  creditType: "LEDGER" | "VENDOR";
  debitAccountId?: string;
  creditAccountId?: string;
  vendorId?: string;
  allocationRule: AllocationRule;
  status: ChargeStatus;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ChargeCodeInput {
  legalEntityId: string;
  code: string;
  name: string;
  calc: ChargeCalc;
  defaultValue?: string;
  debitType?: ChargeSide;
  creditType?: "LEDGER" | "VENDOR";
  debitAccountId?: string;
  creditAccountId?: string;
  vendorId?: string;
  allocationRule?: AllocationRule;
  status?: ChargeStatus;
}

export interface AutoCharge {
  id: string;
  legalEntityId: string;
  chargeCodeId: string;
  accountCode: AutoChargeKey;
  accountId?: string;
  value?: string;
  minOrderAmount?: string;
  currency?: string;
  validFrom: string;
  validTo?: string;
  status: ChargeStatus;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface AutoChargeInput {
  legalEntityId: string;
  chargeCodeId: string;
  accountCode: AutoChargeKey;
  accountId?: string;
  value?: string;
  minOrderAmount?: string;
  currency?: string;
  validFrom?: string;
  validTo?: string;
  status?: ChargeStatus;
}

export function listChargeCodes(
  filters: { status?: ChargeStatus | ""; q?: string; legalEntityId?: string } = {},
  options: ListOptions = {},
) {
  return list<ChargeCode>("/charge-codes", { status: filters.status || undefined, q: filters.q, legalEntityId: filters.legalEntityId }, options);
}

export function getChargeCode(id: string, signal?: AbortSignal) {
  return send<ChargeCode>("GET", `/charge-codes/${encodeURIComponent(id)}`, { signal });
}

export function createChargeCode(input: ChargeCodeInput, idempotencyKey = newIdempotencyKey()) {
  return send<ChargeCode>("POST", "/charge-codes", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
}

/** Replaces everything but the code under the version read. PUT is idempotent, so no key is sent. */
export function changeChargeCode(id: string, input: ChargeCodeInput, version: number) {
  return send<ChargeCode>("PUT", `/charge-codes/${encodeURIComponent(id)}`, { body: input, headers: { "If-Match": `"${version}"` } });
}

export function listAutoCharges(
  filters: { status?: ChargeStatus | ""; chargeCodeId?: string; legalEntityId?: string } = {},
  options: ListOptions = {},
) {
  return list<AutoCharge>(
    "/auto-charges",
    { status: filters.status || undefined, chargeCodeId: filters.chargeCodeId, legalEntityId: filters.legalEntityId },
    options,
  );
}

export function getAutoCharge(id: string, signal?: AbortSignal) {
  return send<AutoCharge>("GET", `/auto-charges/${encodeURIComponent(id)}`, { signal });
}

export function createAutoCharge(input: AutoChargeInput, idempotencyKey = newIdempotencyKey()) {
  return send<AutoCharge>("POST", "/auto-charges", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
}

/** Replaces value, minimum, currency, window and status under the version read. */
export function changeAutoCharge(id: string, input: AutoChargeInput, version: number) {
  return send<AutoCharge>("PUT", `/auto-charges/${encodeURIComponent(id)}`, { body: input, headers: { "If-Match": `"${version}"` } });
}

// --- Service PO lines, outlines and model service specifications (FR-17.23, BR-17.19) ---

export type OutlineKind = "LEVEL" | "ITEM";

/** A stored outline node in depth-first order; a level's amount is the sum of its children. */
export interface OutlineRow {
  seq: number;
  parentSeq?: number;
  depth: number;
  kind: OutlineKind;
  description?: string;
  productId?: string;
  qty: string;
  unit?: string;
  unitPrice: string;
  amount: string;
}

/** A node of an outline in a request: a level with children, or an item. */
export interface OutlineNodeInput {
  kind: OutlineKind;
  description?: string;
  productId?: string;
  qty?: string;
  unit?: string;
  unitPrice?: string;
  /** The amount the caller expects a level to add up to (422 PRC-SRV-004 when it does not). */
  total?: string;
  children?: OutlineNodeInput[];
}

export interface ServiceSpec {
  purchaseOrderId: string;
  lineNo: number;
  currency: string;
  lineAmount: string;
  /** The limit for unplanned services. */
  valueLimit: string;
  expectedValue: string;
  /** The sum of the top-level outline amounts; never above the line amount. */
  plannedTotal: string;
  contractAgreementLineId?: string;
  /** The remaining value of the contract line now. */
  contractRemaining?: string;
  modelId?: string;
  outline: OutlineRow[];
  updatedBy?: string;
  updatedAt: string;
}

/** A specification with the PO row version after the call: writes bump it. */
export interface ServiceSpecResult extends ServiceSpec {
  poRowVersion: number;
}

export interface ServiceSpecInput {
  valueLimit: string;
  expectedValue?: string;
  contractAgreementLineId?: string;
  /** Copies the model's outline; give this or an outline, not both. */
  modelId?: string;
  outline?: OutlineNodeInput[];
}

export type ModelServiceStatus = "ACTIVE" | "INACTIVE";

export interface ModelServiceSpec {
  id: string;
  legalEntityId: string;
  code: string;
  name: string;
  currency: string;
  status: ModelServiceStatus;
  plannedTotal: string;
  /** Empty in a list. */
  outline: OutlineRow[];
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ModelServiceSpecInput {
  legalEntityId?: string;
  code?: string;
  name: string;
  currency: string;
  status?: ModelServiceStatus;
  outline?: OutlineNodeInput[];
}

function servicePath(poId: string, lineNo: number) {
  return `/purchase-orders/${encodeURIComponent(poId)}/lines/${lineNo}/service`;
}

/** The service lines of a PO with their outlines and limits, in line order. */
export async function listServiceSpecs(poId: string, signal?: AbortSignal) {
  const { body } = await send<{ items: ServiceSpec[] }>("GET", `/purchase-orders/${encodeURIComponent(poId)}/service-lines`, { signal });
  return body.items;
}

/** One line's specification; 404 PRC-SRV-NOT-FOUND when the line is a plain line. */
export function getServiceSpec(poId: string, lineNo: number, signal?: AbortSignal) {
  return send<ServiceSpecResult>("GET", servicePath(poId, lineNo), { signal });
}

/** Saves the line's specification on a DRAFT or REJECTED PO; rowVersion is the PO ETag (412 PRC-PO-009 when stale). */
export function putServiceSpec(poId: string, lineNo: number, rowVersion: number, input: ServiceSpecInput) {
  return send<ServiceSpecResult>("PUT", servicePath(poId, lineNo), { body: input, headers: { "If-Match": `"${rowVersion}"` } });
}

/** Removes the line's specification; resolves to the new PO row version. */
export async function deleteServiceSpec(poId: string, lineNo: number, rowVersion: number) {
  const { etag } = await send<undefined>("DELETE", servicePath(poId, lineNo), { headers: { "If-Match": `"${rowVersion}"` } });
  return etag === null ? rowVersion + 1 : Number(etag.replace(/"/g, ""));
}

export function listModelServiceSpecs(
  filters: { status?: ModelServiceStatus | ""; q?: string; legalEntityId?: string } = {},
  options: ListOptions = {},
) {
  return list<ModelServiceSpec>("/model-service-specs", { status: filters.status || undefined, q: filters.q, legalEntityId: filters.legalEntityId }, options);
}

export function getModelServiceSpec(id: string, signal?: AbortSignal) {
  return send<ModelServiceSpec>("GET", `/model-service-specs/${encodeURIComponent(id)}`, { signal });
}

export function createModelServiceSpec(input: ModelServiceSpecInput, idempotencyKey = newIdempotencyKey()) {
  return send<ModelServiceSpec>("POST", "/model-service-specs", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
}

/** Replaces everything but the code and legal entity under the version read. PUT is idempotent, so no key is sent. */
export function changeModelServiceSpec(id: string, input: ModelServiceSpecInput, version: number) {
  return send<ModelServiceSpec>("PUT", `/model-service-specs/${encodeURIComponent(id)}`, { body: input, headers: { "If-Match": `"${version}"` } });
}

/** The answer to an unplanned services check (BR-17.19). */
export interface ServiceLimitCheck {
  lineNo: number;
  currency: string;
  valueLimit: string;
  contractRemaining?: string;
  unplannedEntered: string;
  entry: string;
  /** The value limit less what is entered, never negative. */
  valueRemaining: string;
  /** What unplanned services may still add before this entry, under both limits. */
  remaining: string;
  allowed: boolean;
  /** PRC-SRV-003 when the entry does not fit. */
  code?: string;
  detail?: string;
}

/** Whether an entry of unplanned services fits the line's limits (BR-17.19); a refusal is allowed false with code PRC-SRV-003, not an error. Changes nothing. */
export function checkServiceLimit(poId: string, lineNo: number, unplanned: string, entry: string, signal?: AbortSignal) {
  return send<ServiceLimitCheck>("GET", `${servicePath(poId, lineNo)}/limit-check${query({ unplanned, entry })}`, { signal });
}

// --- Changes other services asked of purchase orders (slice 18) ---

export type InboundSource = "PLANNING_MESSAGE" | "DROPSHIP_CLOSED" | "DROPSHIP_CANCELLED";
export type InboundStatus = "APPLIED" | "REFUSED" | "IGNORED";
/** How a refused change was settled (slice 19); "NONE" in a filter lists the changes nobody has settled. */
export type InboundResolution = "ACKNOWLEDGED" | "RETRIED";

/**
 * One planning message a planner applied to a purchase order, or one closed drop-ship remainder, as the
 * purchasing consumer handled it. REFUSED rows are for a buyer to settle by hand.
 */
export interface InboundChange {
  id: string;
  legalEntityId: string;
  source: InboundSource;
  sourceKey: string;
  eventId: string;
  action: string;
  purchaseOrderId?: string;
  poNumber?: string;
  lineNo?: number;
  lineId?: string;
  productId?: string;
  status: InboundStatus;
  reasonCode?: string;
  reason?: string;
  requestedQty?: string;
  requestedDate?: string;
  qtyBefore?: string;
  qtyAfter?: string;
  dateBefore?: string;
  dateAfter?: string;
  versionNo?: number;
  reapprovalRequired: boolean;
  requestedBy?: string;
  note?: string;
  createdAt: string;
  warehouseId?: string;
  /** Runs of the request: the consumer's first and every retry. */
  attempts?: number;
  resolution?: InboundResolution;
  resolvedBy?: string;
  resolvedAt?: string;
  resolutionNote?: string;
}

export interface InboundFilters {
  source?: InboundSource | "";
  status?: InboundStatus | "";
  resolution?: InboundResolution | "NONE" | "";
  purchaseOrderId?: string;
}

export function listInboundChanges(filters: InboundFilters = {}, options: ListOptions = {}) {
  return list<InboundChange>(
    "/inbound-changes",
    {
      source: filters.source || undefined,
      status: filters.status || undefined,
      resolution: filters.resolution || undefined,
      purchaseOrderId: filters.purchaseOrderId || undefined,
    },
    options,
  );
}

export function getInboundChange(id: string, signal?: AbortSignal) {
  return send<InboundChange>("GET", `/inbound-changes/${encodeURIComponent(id)}`, { signal });
}

/**
 * Settles a refused change (slice 19; needs procurement.purchase-order.maintain): "acknowledge" says the buyer dealt with it by
 * hand, "retry" runs the request again against the order as it is now.
 */
export function settleInboundChange(id: string, action: "acknowledge" | "retry", note: string, idempotencyKey = newIdempotencyKey()) {
  return send<InboundChange>("POST", `/inbound-changes/${encodeURIComponent(id)}:${action}`, {
    body: note ? { note } : undefined,
    headers: { "Idempotency-Key": idempotencyKey },
  });
}
