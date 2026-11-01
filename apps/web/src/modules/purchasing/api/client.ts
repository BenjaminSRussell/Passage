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
  /** The procurement catalog entry the line was picked from; absent on a non-catalog line. */
  catalogEntryId?: string;
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
  /** On an edit, the existing line this entry changes; omitted for a new line. */
  lineNo?: number;
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

/** Creates one DRAFT purchase order per vendor, currency, site and delivery window. */
export function consolidateRequisitions(input: ConsolidationInput, idempotencyKey = newIdempotencyKey()) {
  return send<{ purchaseOrders: PurchaseOrder[] }>("POST", "/requisitions:consolidate", {
    body: input,
    headers: { "Idempotency-Key": idempotencyKey },
  });
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

/** Records the buyer's 1–10 scores of one invited vendor (duty RFQAward); version is the RFQ ETag. */
export function scoreRFQVendor(id: string, vendorId: string, version: number, scores: { criterionId: string; score: number }[]) {
  return send<RFQ>("PUT", `/rfqs/${encodeURIComponent(id)}/scores/${encodeURIComponent(vendorId)}`, {
    body: { scores },
    headers: { "If-Match": `"${version}"` },
  });
}

/** Awards lines to replies (duty RFQAward) and creates one DRAFT PO per vendor and currency. */
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

/** Read-only preview (PurchaseOrderInquire); the ETag is the PO row version to pass to applyReprice. */
export function previewReprice(poId: string, signal?: AbortSignal) {
  return send<RepriceResult>("POST", repricePath(poId), { body: { apply: false }, signal });
}

/** Applies the run as the next PO version (PurchaseOrderConfirm); a stale rowVersion is 412 PRC-PO-009. */
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
  version: number;
  updatedBy?: string;
  updatedAt?: string;
}

export type PurchasingPolicyInput = Pick<PurchasingPolicy, "changeManagementDefault" | "poFourEyes" | "allowInvoiceBeforeReceipt">;

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
