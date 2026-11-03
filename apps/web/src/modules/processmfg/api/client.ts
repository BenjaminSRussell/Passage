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

export interface MergeSourceInput {
  batchId: string;
  version: number;
  warehouseId: string;
  cwUnit?: string;
  qty: string;
  unitCost: string;
}

export interface StoredMergeRequest {
  targetBatchNumber: string;
  sources: MergeSourceInput[];
  mergeOverride?: string[];
}

export interface GenealogyLink {
  batchId: string;
  batchNumber: string;
  itemId: string;
  relation: "merge";
  qty: string;
  unitCost: string;
  createdBy: string;
  createdAt: string;
}

export interface StoredMergeResult {
  batch: Batch;
  qty: string;
  unitCost: string;
  parents: GenealogyLink[];
}

export interface Genealogy {
  batchId: string;
  parents: GenealogyLink[];
  children: GenealogyLink[];
}

/** FR-24.16: merges stored batches into a new batch; reuse one key per form submit so a retry cannot merge twice. */
export function mergeStoredBatches(input: StoredMergeRequest, key: string = newKey()): Promise<StoredMergeResult> {
  return post<StoredMergeResult>("/batches:merge", input, key);
}

export function getBatchGenealogy(id: string, signal?: AbortSignal): Promise<Genealogy> {
  return send<Genealogy>(`/batches/${encodeURIComponent(id)}/genealogy`, {}, signal);
}

/** FR-24.19: a stored customer batch requirement. Level 3 is a customer, 2 a customer group, 1 the item default. */
export interface CustomerRequirement {
  id: string;
  legalEntityId: string;
  itemId: string;
  customerId?: string;
  customerGroupId?: string;
  level: 1 | 2 | 3;
  sellableDays?: number;
  attribute?: string;
  min?: string;
  max?: string;
  sameBatch?: boolean;
  validFrom?: string;
  validTo?: string;
  retiredAt?: string;
  retiredBy?: string;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewCustomerRequirement {
  itemId: string;
  customerId?: string;
  customerGroupId?: string;
  sellableDays?: number;
  attribute?: string;
  min?: string;
  max?: string;
  sameBatch?: boolean;
  validFrom?: string;
  validTo?: string;
}

export interface RequirementFilter {
  itemId?: string;
  customerId?: string;
  customerGroupId?: string;
  activeOn?: string;
}

export function listCustomerRequirements(filter: RequirementFilter = {}, signal?: AbortSignal): Promise<CustomerRequirement[]> {
  return items<CustomerRequirement>(`/customer-batch-requirements${query({ ...filter, limit: 200 })}`, signal);
}

export function getCustomerRequirement(id: string, signal?: AbortSignal): Promise<CustomerRequirement> {
  return send<CustomerRequirement>(`/customer-batch-requirements/${encodeURIComponent(id)}`, {}, signal);
}

/** Reuse one key per form so a retried submit cannot define the rule twice. */
export function createCustomerRequirement(input: NewCustomerRequirement, key: string = newKey()): Promise<CustomerRequirement> {
  return post<CustomerRequirement>("/customer-batch-requirements", input, key);
}

export function retireCustomerRequirement(id: string, version: number, key: string = newKey()): Promise<CustomerRequirement> {
  return post<CustomerRequirement>(`/customer-batch-requirements/${encodeURIComponent(id)}:retire`, { version }, key);
}

export interface StoredRequirementCheckRequest {
  itemId: string;
  customerId?: string;
  customerGroupId?: string;
  shipDate: string;
  qty: string;
  onHand: { batchId: string; qty: string }[];
}

export interface StoredRequirementCheck {
  requirement: { sellableDays: number; sameBatch: boolean; ranges: Record<string, { min?: string; max?: string }> };
  qualifying: { batchId: string; qty: string; expiry?: string }[];
  excluded: { batchId: string; reason: string; warningCode: string }[];
  qtyAvailable: string;
  sameBatchId?: string | null;
  applied: string[];
  batchNumbers: Record<string, string>;
}

/** FR-24.19, AC-24.14: checks stored batches against the stored requirements of a customer. */
export function checkStoredRequirements(input: StoredRequirementCheckRequest): Promise<StoredRequirementCheck> {
  return post<StoredRequirementCheck>("/batch-requirements:check-stored", input);
}

/** FR-24.21: says that an item's batches get an electronic batch record and whether releasing a batch needs it approved. */
export interface RecordProfile {
  id: string;
  legalEntityId: string;
  code: string;
  itemId: string;
  approvalRequiredForRelease: boolean;
  retiredAt?: string;
  retiredBy?: string;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewRecordProfile {
  code: string;
  itemId: string;
  approvalRequiredForRelease: boolean;
}

export type RecordStatus = "in-review" | "approved" | "sealed";
export type RecordDisposition = "accepted" | "deviation-raised" | "rejected";
export type ExceptionSeverity = "minor" | "major" | "critical";

/** One exception of a batch record: a value outside its limits (attribute) or one a reviewer raised (manual). */
export interface RecordException {
  id: string;
  sourceType: "attribute" | "manual";
  sourceId: string;
  description: string;
  severity: ExceptionSeverity;
  disposition?: RecordDisposition;
  dispositionComment?: string;
  signatureId?: string;
  dispositionedBy?: string;
  dispositionedAt?: string;
  createdBy: string;
  createdAt: string;
}

/** The electronic batch record; the list leaves out content and exceptions. */
export interface BatchRecord {
  id: string;
  legalEntityId: string;
  batchOrderId: string;
  batchId: string;
  batchNumber: string;
  itemId: string;
  profileId: string;
  status: RecordStatus;
  compiledAt: string;
  approvedBy?: string;
  approvedAt?: string;
  sealedBy?: string;
  sealedAt?: string;
  contentHash?: string;
  version: number;
  exceptionCount: number;
  openExceptions: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  content?: unknown;
  sealedDocument?: string;
  exceptions?: RecordException[];
}

export function listRecordProfiles(filter: { itemId?: string; active?: boolean } = {}, signal?: AbortSignal): Promise<RecordProfile[]> {
  return items<RecordProfile>(`/batch-record-profiles${query({ itemId: filter.itemId, active: filter.active ? "true" : undefined, limit: 200 })}`, signal);
}

export function defineRecordProfile(input: NewRecordProfile, key: string = newKey()): Promise<RecordProfile> {
  return post<RecordProfile>("/batch-record-profiles", input, key);
}

export function retireRecordProfile(id: string, version: number, key: string = newKey()): Promise<RecordProfile> {
  return post<RecordProfile>(`/batch-record-profiles/${encodeURIComponent(id)}:retire`, { version }, key);
}

export function listBatchRecords(filter: { status?: string; itemId?: string } = {}, signal?: AbortSignal): Promise<BatchRecord[]> {
  return items<BatchRecord>(`/batch-records${query({ ...filter, limit: 200 })}`, signal);
}

export function getBatchRecord(id: string, signal?: AbortSignal): Promise<BatchRecord> {
  return send<BatchRecord>(`/batch-records/${encodeURIComponent(id)}`, {}, signal);
}

/** Compiles the record of a batch, or refreshes it while it is in review. */
export function compileBatchRecord(batchId: string, key: string = newKey()): Promise<BatchRecord> {
  return post<BatchRecord>("/batch-records", { batchId }, key);
}

export function raiseRecordException(
  id: string,
  input: { version: number; description: string; severity: ExceptionSeverity },
  key: string = newKey(),
): Promise<BatchRecord> {
  return post<BatchRecord>(`/batch-records/${encodeURIComponent(id)}/exceptions`, input, key);
}

export function dispositionRecordException(
  id: string,
  exceptionId: string,
  input: { version: number; disposition: RecordDisposition; comment?: string; signatureId: string },
  key: string = newKey(),
): Promise<BatchRecord> {
  return post<BatchRecord>(`/batch-records/${encodeURIComponent(id)}/exceptions/${encodeURIComponent(exceptionId)}:disposition`, input, key);
}

export function approveBatchRecord(id: string, version: number, key: string = newKey()): Promise<BatchRecord> {
  return post<BatchRecord>(`/batch-records/${encodeURIComponent(id)}:approve`, { version }, key);
}

export function sealBatchRecord(id: string, version: number, key: string = newKey()): Promise<BatchRecord> {
  return post<BatchRecord>(`/batch-records/${encodeURIComponent(id)}:seal`, { version }, key);
}

/** FR-24.15: how one attribute of a receiver batch is derived from its sender batches. */
export type RuleAggregation = "copy" | "min" | "max" | "weighted-average" | "earliest-date" | "latest-date" | "fixed";

export interface InheritanceRule {
  id: string;
  legalEntityId: string;
  itemId: string;
  attribute: string;
  aggregation: RuleAggregation;
  decimals?: number;
  fixedNum?: string;
  fixedText?: string;
  fixedDate?: string;
  retiredAt?: string;
  retiredBy?: string;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewInheritanceRule {
  itemId: string;
  attribute: string;
  aggregation: RuleAggregation;
  decimals?: number;
  fixed?: { num?: string; text?: string; date?: string };
}

export function listInheritanceRules(filter: { itemId?: string; active?: boolean } = {}, signal?: AbortSignal): Promise<InheritanceRule[]> {
  return items<InheritanceRule>(`/inheritance-rules${query({ itemId: filter.itemId, active: filter.active ? "true" : undefined, limit: 200 })}`, signal);
}

export function getInheritanceRule(id: string, signal?: AbortSignal): Promise<InheritanceRule> {
  return send<InheritanceRule>(`/inheritance-rules/${encodeURIComponent(id)}`, {}, signal);
}

/** Reuse one key per form so a retried submit cannot define the rule twice. */
export function createInheritanceRule(input: NewInheritanceRule, key: string = newKey()): Promise<InheritanceRule> {
  return post<InheritanceRule>("/inheritance-rules", input, key);
}

export function retireInheritanceRule(id: string, version: number, key: string = newKey()): Promise<InheritanceRule> {
  return post<InheritanceRule>(`/inheritance-rules/${encodeURIComponent(id)}:retire`, { version }, key);
}

export interface DeriveSenderInput {
  batchId: string;
  qty: string;
}

export interface DerivedValue {
  ruleId: string;
  attribute: string;
  aggregation: RuleAggregation;
  recorded: boolean;
  value?: string;
  missing: string[];
}

export interface DeriveStoredResult {
  batch: Batch;
  derived: DerivedValue[];
  warnings: string[];
}

/** Derives the attributes of a stored batch from its senders with the item's active rules (FR-24.15). */
export function deriveStoredBatch(id: string, version: number, senders: DeriveSenderInput[], key: string = newKey()): Promise<DeriveStoredResult> {
  return post<DeriveStoredResult>(`/batches/${encodeURIComponent(id)}:derive`, { version, senders }, key);
}

export interface Derivation {
  id: string;
  legalEntityId: string;
  receiverBatchId: string;
  batchNumber: string;
  receiverVersion: number;
  ruleId: string;
  attribute: string;
  aggregation: RuleAggregation;
  recorded: boolean;
  value?: string;
  /** The receiver's value before the derivation; absent when it had none. */
  valueBefore?: string;
  senders: { batchId: string; batchNumber: string; qty: string }[];
  missing: string[];
  runBy: string;
  runAt: string;
}

export function listDerivations(filter: { batchId?: string; itemId?: string; withMissing?: boolean } = {}, signal?: AbortSignal): Promise<Derivation[]> {
  return items<Derivation>(
    `/batch-derivations${query({ batchId: filter.batchId, itemId: filter.itemId, withMissing: filter.withMissing ? "true" : undefined, limit: 200 })}`,
    signal,
  );
}
