// Client for the processmfg service (services/processmfg/api/openapi.yaml).
// Browser calls use /api/v1/production/..., which the web BFF routes to the
// service. Decimals travel as strings so no quantity passes through a float.

export const base = "/api/v1/production";

export const LINE_TYPES = ["ingredient", "active-ingredient", "phantom", "co-product", "by-product"] as const;
export type LineType = (typeof LINE_TYPES)[number];

export const ORDER_STATUSES = ["planned", "released", "started", "reported", "ended", "closed"] as const;
export type OrderStatus = (typeof ORDER_STATUSES)[number];

export interface FormulaLine {
  lineNumber: number;
  itemId: string;
  type: LineType;
  qty: string;
  percent: string;
  scalable: boolean;
  percentControlled: boolean;
}

export interface Formula {
  id: string;
  code: string;
  name: string;
  itemId: string;
  bomVersionId: string | null;
  size: string;
  unit: string;
  version: number;
  lineCount: number;
  lines?: FormulaLine[];
  createdAt: string;
}

export interface NewFormula {
  code: string;
  name: string;
  itemId: string;
  size: string;
  unit: string;
  lines: FormulaLine[];
}

export interface ScaledLine {
  lineNumber: number;
  itemId: string;
  type: LineType;
  formulaQty: string;
  qty: string;
}

export interface ScalePreview {
  batchSize: string;
  scaleFactor: string;
  lines: ScaledLine[];
}

export interface OrderLine extends FormulaLine {
  formulaQty: string;
  /** Quantity reported as finished of a co- or by-product line, once the order is reported. */
  rafQty?: string;
}

export type ShareRole = "main" | "co-product" | "by-product-burden";

export interface AllocationShare {
  role: ShareRole;
  lineNumber: number;
  itemId: string;
  rafQty: string;
  amount: string;
  percent: string;
}

/** The cost split an ended order carries; the amounts add up to batchCost exactly. */
export interface OrderAllocation {
  method: AllocationMethod;
  currency: string;
  batchCost: string;
  shares: AllocationShare[];
}

export interface BatchOrder {
  id: string;
  legalEntityId: string;
  orderNumber: string;
  formulaId: string;
  formulaCode: string;
  formulaVersion: number;
  itemId: string;
  unit: string;
  formulaSize: string;
  batchSize: string;
  scaleFactor: string;
  status: OrderStatus;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  releasedAt?: string;
  startedAt?: string;
  reportedAt?: string;
  endedAt?: string;
  actualQty?: string;
  lines?: OrderLine[];
  allocation?: OrderAllocation;
}

export interface PotencyRequest {
  planned: string;
  standardPotency: string;
  batchPotency: string;
  compensatingPlanned: string;
}

export interface PotencyResult {
  qty: string;
  compensatingQty: string;
}

export const ALLOCATION_METHODS = ["tca", "quantity", "manual", "none"] as const;
export type AllocationMethod = (typeof ALLOCATION_METHODS)[number];

export const BY_PRODUCT_COST_TYPES = ["per-quantity", "per-series", "percent", "recycled"] as const;
export type ByProductCostType = (typeof BY_PRODUCT_COST_TYPES)[number];

export interface CostItem {
  itemId: string;
  standardCost: string;
  rafQty: string;
  percent: string;
}

export interface ByProductItem {
  itemId: string;
  costType: ByProductCostType;
  qty: string;
  rate: string;
}

export interface AllocateRequest {
  batchCost: string;
  currency: string;
  method: AllocationMethod;
  main: CostItem;
  coProducts: CostItem[];
  byProducts: ByProductItem[];
}

export interface Allocated {
  itemId: string;
  amount: string;
  percent: string;
}

export interface AllocateResult {
  currency: string;
  main: Allocated;
  coProducts: Allocated[];
  byProductBurdens: Allocated[];
  total: string;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class ProcessApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ProcessApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ProcessApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<ProcessApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new ProcessApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new ProcessApiError(0, "COM-0001", "The response has no items");
  }
  return body.items;
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(key, String(value));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

export function newKey(): string {
  return crypto.randomUUID();
}

function post<T>(path: string, body: unknown, key?: string): Promise<T> {
  const headers: Record<string, string> = {};
  if (key) headers["Idempotency-Key"] = key;
  return send<T>(path, { method: "POST", headers, body: JSON.stringify(body) });
}

export function listFormulas(signal?: AbortSignal): Promise<Formula[]> {
  return items<Formula>(`/formulas${query({ limit: 200 })}`, signal);
}

export function getFormula(id: string, signal?: AbortSignal): Promise<Formula> {
  return send<Formula>(`/formulas/${encodeURIComponent(id)}`, {}, signal);
}

/** Creates a formula; reuse one key per form so a retried submit cannot create two. */
export function createFormula(input: NewFormula, key: string = newKey()): Promise<Formula> {
  return post<Formula>("/formulas", input, key);
}

/** Scales a stored formula to a batch size without writing (BR-24.1). */
export function previewScale(formulaId: string, batchSize: string): Promise<ScalePreview> {
  return post<ScalePreview>(`/formulas/${encodeURIComponent(formulaId)}:scale`, { batchSize });
}

export function listBatchOrders(filter: { status?: OrderStatus | ""; formulaId?: string } = {}, signal?: AbortSignal): Promise<BatchOrder[]> {
  return items<BatchOrder>(`/batch-orders${query({ status: filter.status, formulaId: filter.formulaId })}`, signal);
}

export function getBatchOrder(id: string, signal?: AbortSignal): Promise<BatchOrder> {
  return send<BatchOrder>(`/batch-orders/${encodeURIComponent(id)}`, {}, signal);
}

/** Creates a batch order in the caller's legal entity (FR-24.2). */
export function createBatchOrder(input: { formulaId: string; batchSize: string }, key: string = newKey()): Promise<BatchOrder> {
  return post<BatchOrder>("/batch-orders", input, key);
}

/** Changes the batch size of a planned or released order; version is the one the page read. */
export function rescaleBatchOrder(id: string, input: { batchSize: string; version: number }, key: string = newKey()): Promise<BatchOrder> {
  return post<BatchOrder>(`/batch-orders/${encodeURIComponent(id)}:rescale`, input, key);
}

/** Release (planned) or start (released) an order; version is the one the page read. */
export function advanceBatchOrder(id: string, command: "release" | "start", version: number, key: string = newKey()): Promise<BatchOrder> {
  return post<BatchOrder>(`/batch-orders/${encodeURIComponent(id)}:${command}`, { version }, key);
}

export interface ReportInput {
  version: number;
  mainQty: string;
  outputs: { lineNumber: number; qty: string }[];
}

/** Records the quantities reported as finished and moves a started order to reported. */
export function reportBatchOrder(id: string, input: ReportInput, key: string = newKey()): Promise<BatchOrder> {
  return post<BatchOrder>(`/batch-orders/${encodeURIComponent(id)}:report-finished`, input, key);
}

export interface EndInput {
  version: number;
  method: AllocationMethod;
  currency: string;
  batchCost: string;
  main: { standardCost?: string; percent?: string };
  coProducts: { lineNumber: number; standardCost?: string; percent?: string }[];
  byProducts: { lineNumber: number; costType: ByProductCostType; rate: string }[];
}

/** Ends a reported order and allocates its cost (BR-24.3). */
export function endBatchOrder(id: string, input: EndInput, key: string = newKey()): Promise<BatchOrder> {
  return post<BatchOrder>(`/batch-orders/${encodeURIComponent(id)}:end`, input, key);
}

export function consumePotency(req: PotencyRequest): Promise<PotencyResult> {
  return post<PotencyResult>("/potency:consume", req);
}

export function allocateCoProducts(req: AllocateRequest): Promise<AllocateResult> {
  return post<AllocateResult>("/co-products:allocate", req);
}

// Slice 2: batch rules as previews over batches the caller passes in (FR-24.15
// to FR-24.21). Nothing is stored; see services/processmfg/NOT-IMPLEMENTED.md.

/** One attribute value: at most one of num, text and date (yyyy-mm-dd); {} is a missing value. */
export interface AttributeValue {
  num?: string;
  text?: string;
  date?: string;
}

export const AGGREGATIONS = ["weighted-average", "min", "max", "copy", "earliest-date", "latest-date", "fixed"] as const;
export type Aggregation = (typeof AGGREGATIONS)[number];

export interface DeriveRequest {
  rule: { attribute: string; aggregation: Aggregation; decimals?: number; fixed?: AttributeValue };
  senders: { batchId: string; value: AttributeValue; qty: string }[];
}

export interface DeriveResult {
  attribute: string;
  value: AttributeValue;
  missingSenders: string[];
  warningCode?: string;
}

export const DISPOSITIONS = ["available", "on-hold", "rejected", "quarantine"] as const;
export type Disposition = (typeof DISPOSITIONS)[number];

export interface BatchInput {
  id: string;
  itemId?: string;
  warehouseId?: string;
  cwUnit?: string;
  disposition?: Disposition;
  qty: string;
  unitCost?: string;
  values?: Record<string, AttributeValue>;
}

export interface MergeRequest {
  batches: BatchInput[];
  mergeOverride?: string[];
}

export interface MergeResult {
  qty: string;
  unitCost: string;
  disposition: Disposition;
  values: Record<string, AttributeValue>;
  parents: string[];
}

/** 1 item default, 2 customer group, 3 customer: the most specific level wins (BR-24.13). */
export const REQUIREMENT_LEVELS = [3, 2, 1] as const;
export type RequirementLevel = (typeof REQUIREMENT_LEVELS)[number];

export interface RequirementInput {
  level: RequirementLevel;
  sellableDays?: number;
  attribute?: string;
  min?: string;
  max?: string;
  sameBatch?: boolean;
}

export interface CheckRequest {
  requirements: RequirementInput[];
  shipDate: string;
  qty: string;
  batches: BatchInput[];
}

export interface CheckResult {
  requirement: { sellableDays: number; sameBatch: boolean; ranges: Record<string, { min?: string; max?: string }> };
  qualifying: { batchId: string; qty: string; expiry?: string }[];
  excluded: { batchId: string; reason: string; warningCode: string }[];
  qtyAvailable: string;
  sameBatchId: string | null;
}

export interface ConsolidateRequest {
  relation: { packQtyPerUnit: string; multiple: string; max: string };
  packedOrders: { orderId: string; itemId: string; units: string; due: string }[];
}

export interface ConsolidateResult {
  bulkOrders: { qty: string; pegs: { orderId: string; itemId: string; bulkQty: string }[] }[];
}

export const EXCEPTION_DISPOSITIONS = ["", "accepted", "deviation-raised", "rejected"] as const;
export type ExceptionDisposition = (typeof EXCEPTION_DISPOSITIONS)[number];

export interface ApproveCheckRequest {
  exceptions: { id: string; disposition: ExceptionDisposition; signatureId: string }[];
}

export interface ApproveCheckResult {
  approvable: boolean;
  exceptions: number;
}

export function previewDerivation(req: DeriveRequest): Promise<DeriveResult> {
  return post<DeriveResult>("/batch-derivation:preview", req);
}

export function previewMerge(req: MergeRequest): Promise<MergeResult> {
  return post<MergeResult>("/batch-merge:preview", req);
}

export function checkRequirements(req: CheckRequest): Promise<CheckResult> {
  return post<CheckResult>("/batch-requirements:check", req);
}

export function previewConsolidation(req: ConsolidateRequest): Promise<ConsolidateResult> {
  return post<ConsolidateResult>("/bulk-consolidation:preview", req);
}

export function checkApproval(req: ApproveCheckRequest): Promise<ApproveCheckResult> {
  return post<ApproveCheckResult>("/batch-records:approve-check", req);
}

// Slice 3: batch attribute check, picking proposal and rework plan (FR-24.3,
// FR-24.5, FR-24.6, FR-24.18, BR-24.10), previews over batches the caller passes in.

export const ATTRIBUTE_KINDS = ["numeric", "text", "enum", "date"] as const;
export type AttributeKind = (typeof ATTRIBUTE_KINDS)[number];

export interface AttributeDefinition {
  name: string;
  kind: AttributeKind;
  target?: string;
  min?: string;
  max?: string;
  options?: string[];
}

export interface AttributesRequest {
  definitions: AttributeDefinition[];
  values: Record<string, AttributeValue>;
}

export interface AttributesResult {
  results: { attribute: string; pass: boolean; detail?: string }[];
  exceptions: number;
}

export interface PickingRequest {
  postingDate: string;
  qty: string;
  potency?: { attribute: string; min: string; max: string };
  overrideReason?: string;
  batches: BatchInput[];
}

export interface PickingResult {
  picked: { batchId: string; qty: string; expiry?: string; expiredOverride: boolean }[];
  excluded: { batchId: string; code: string; detail: string }[];
  shortfall: string;
  overrideHeld: boolean;
}

export interface StoredPickingRequest {
  itemId: string;
  postingDate: string;
  qty: string;
  /** The attribute defaults to the item's potency attribute. */
  potency?: { attribute?: string; min: string; max: string };
  overrideReason?: string;
  onHand: { batchId: string; qty: string }[];
}

export interface StoredPickingResult extends PickingResult {
  batchNumbers: Record<string, string>;
}

export const REWORK_OUTPUT_DISPOSITIONS = ["quarantine", "on-hold", "available"] as const;
export type ReworkOutputDisposition = (typeof REWORK_OUTPUT_DISPOSITIONS)[number];

export interface ReworkRequest {
  batch: BatchInput;
  reworkQty: string;
  outputQty: string;
  params: { keepBatchNumber?: boolean; requireQualityOrder?: boolean; outputDisposition?: ReworkOutputDisposition; capitalize?: boolean };
}

export interface ReworkResult {
  sourceBatchId: string;
  reworkQty: string;
  outputBatchId: string | null;
  outputQty: string;
  outputDisposition: Disposition;
  qualityOrderRequired: boolean;
  varianceType: "rework" | "capitalized";
  parents: string[];
}

export function checkAttributes(req: AttributesRequest): Promise<AttributesResult> {
  return post<AttributesResult>("/batch-attributes:check", req);
}

export function proposePicking(req: PickingRequest): Promise<PickingResult> {
  return post<PickingResult>("/batch-picking:proposal", req);
}

/** Picking from the stored batch register: disposition, expiry and potency are read, not sent. */
export function proposeStoredPicking(req: StoredPickingRequest): Promise<StoredPickingResult> {
  return post<StoredPickingResult>("/batch-picking:propose-stored", req);
}

export function previewRework(req: ReworkRequest): Promise<ReworkResult> {
  return post<ReworkResult>("/rework-orders:preview", req);
}

// Batch quality record (slice 11): attribute definitions, batches, recorded
// values with their limit check, and the disposition with its history.

export const ATTRIBUTE_ROLES = ["generic", "potency"] as const;
export type AttributeRole = (typeof ATTRIBUTE_ROLES)[number];

export interface StoredAttributeDefinition extends AttributeDefinition {
  id: string;
  itemId: string;
  role: AttributeRole;
  options: string[];
  createdBy: string;
  createdAt: string;
}

export interface NewAttributeDefinition extends AttributeDefinition {
  itemId: string;
  /** Defaults to generic; potency only on a numeric attribute, one per item. */
  role?: AttributeRole;
}

export interface RecordedValue extends AttributeValue {
  name: string;
  kind: AttributeKind;
  pass: boolean;
  detail?: string;
  recordedBy: string;
  recordedAt: string;
}

export interface DispositionChange {
  from: Disposition;
  to: Disposition;
  reason: string;
  /** The user who changed the disposition also recorded attribute values on the batch (PRD 4.24.17). */
  segregationConflict: boolean;
  changedBy: string;
  changedAt: string;
}

export interface Batch {
  id: string;
  legalEntityId: string;
  itemId: string;
  batchNumber: string;
  batchOrderId?: string;
  disposition: Disposition;
  expiryDate?: string;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  attributes?: RecordedValue[];
  attributeHistory?: RecordedValue[];
  dispositions?: DispositionChange[];
}

export interface NewBatch {
  itemId: string;
  batchNumber: string;
  batchOrderId?: string;
  disposition?: Disposition;
}

export interface RecordResult {
  batch: Batch;
  checks: { name: string; pass: boolean; detail?: string }[];
}

export function listAttributeDefinitions(itemId: string, signal?: AbortSignal): Promise<StoredAttributeDefinition[]> {
  return items<StoredAttributeDefinition>(`/batch-attribute-definitions${query({ itemId, limit: 200 })}`, signal);
}

export function defineAttribute(input: NewAttributeDefinition, key: string = newKey()): Promise<StoredAttributeDefinition> {
  return post<StoredAttributeDefinition>("/batch-attribute-definitions", input, key);
}

export function listBatches(filter: { disposition?: Disposition | ""; itemId?: string } = {}, signal?: AbortSignal): Promise<Batch[]> {
  return items<Batch>(`/batches${query({ ...filter, limit: 200 })}`, signal);
}

export function getBatch(id: string, signal?: AbortSignal): Promise<Batch> {
  return send<Batch>(`/batches/${encodeURIComponent(id)}`, {}, signal);
}

export function registerBatch(input: NewBatch, key: string = newKey()): Promise<Batch> {
  return post<Batch>("/batches", input, key);
}

export function recordBatchAttributes(id: string, input: { version: number; values: Record<string, AttributeValue> }, key: string = newKey()): Promise<RecordResult> {
  return send<RecordResult>(`/batches/${encodeURIComponent(id)}/attributes`, {
    method: "PUT",
    headers: { "Idempotency-Key": key },
    body: JSON.stringify(input),
  });
}

export function changeBatchDisposition(
  id: string,
  input: { version: number; disposition: Disposition; reason: string },
  key: string = newKey(),
): Promise<Batch> {
  return post<Batch>(`/batches/${encodeURIComponent(id)}:disposition`, input, key);
}
