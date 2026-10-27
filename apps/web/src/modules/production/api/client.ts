/** Client for the production order API (services/production, wave 21). Paths follow api/openapi.yaml. */

export const root = "/api/v1/production";
export const base = `${root}/orders`;

export type ProductionStatus =
  | "Created"
  | "Estimated"
  | "Scheduled"
  | "Released"
  | "Started"
  | "ReportedAsFinished"
  | "Ended"
  | "Cancelled";

export const STATUSES: ProductionStatus[] = [
  "Created",
  "Estimated",
  "Scheduled",
  "Released",
  "Started",
  "ReportedAsFinished",
  "Ended",
  "Cancelled",
];

export type ProductionCommand =
  | "estimate"
  | "schedule"
  | "release"
  | "start"
  | "report-finished"
  | "end"
  | "cancel"
  | "reset-status";

/** The status-log command of a row: a status command, or a split or merge (FR-21.15). */
export type LoggedCommand = ProductionCommand | "split" | "merge";

/** The commands a bulk status update accepts (FR-21.12); the others need per-order input. */
export type BulkCommand = "estimate" | "schedule" | "release" | "start" | "end" | "cancel";

export const BULK_COMMANDS: BulkCommand[] = ["estimate", "schedule", "release", "start", "end", "cancel"];

/** The most orders one bulk status update or merge carries. */
export const MAX_BULK = 500;

export type CostGroup = "material" | "labor" | "overhead" | "subcontract";

/** When a BOM line's material is consumed (FR-21.5). */
export type Flushing = "manual" | "start" | "finish" | "available-at-location";

export type JournalType = "picking-list" | "route-card" | "report-as-finished";

export type Proposal = "remaining" | "proportional" | "none";

/** The four variance types of BR-21.6, and rework for a rework order (FR-21.20). */
export type VarianceType = "lot-size" | "production-quantity" | "production-price" | "production-substitution" | "rework";

/** What a rework order reports against (FR-21.20): an order by id, a batch or a serial by number. */
export type ReworkRefType = "order" | "batch" | "serial";

export interface ReworkReference {
  type: ReworkRefType;
  id: string;
}

/** The origin type of a rework order. */
export const REWORK = "Rework";

/** The origin type of an order a split created (FR-21.15). */
export const SPLIT = "Split";

/** How an as-built component is identified (FR-21.19): a serial in quantity 1, or a batch. */
export type Tracking = "serial" | "batch";

export interface OrderLine {
  lineNo: string;
  itemId: string;
  qtyPer: string;
  seriesSize: string;
  scrapPct: string;
  roundUp: boolean;
  qtyRequired: string;
  unitCost: string;
  flushing?: Flushing;
  qtyConsumed?: string;
}

/** The feedback status of an operation (FR-21.6). */
export type OperStatus = "open" | "started" | "finished";

export const OPER_STATUSES: OperStatus[] = ["open", "started", "finished"];

export interface Operation {
  operNumber: number;
  resourceId: string;
  setupTimeH: string;
  runTimeHPer: string;
  scrapPct: string;
  laborRate: string;
  overheadRate: string;
  subcontract: boolean;
  subcontractPrice: string;
  processQty: string;
  hoursReported?: string;
  qtyGood?: string;
  qtyError?: string;
  status?: OperStatus;
  /** When release asked purchasing for this subcontract operation's vendor PO (FR-21.10). */
  poRequestedAt?: string;
}

export interface CostRow {
  group: CostGroup;
  key: string;
  qty: string;
  amount: string;
  fixed: string;
}

export interface ProductionOrder {
  id: string;
  legalEntityId: string;
  orderNumber: string;
  productId: string;
  siteId: string;
  warehouseId: string;
  currency: string;
  itemGroup?: string;
  quantity: string;
  qtyRaf: string;
  qtyError: string;
  status: ProductionStatus;
  deliveryDate?: string;
  originType: string;
  originId?: string;
  reworkReference?: ReworkReference;
  /** The order a split took this order's quantity from (FR-21.15). */
  splitFromOrderId?: string;
  /** Set on a cancelled order that a merge moved into another one. */
  mergedIntoOrderId?: string;
  /** The BOM or route was edited after explosion (FR-21.18). */
  orderBomModified?: boolean;
  version: number;
  lines: OrderLine[];
  operations: Operation[];
  costs: CostRow[];
  createdAt: string;
  updatedAt: string;
}

export interface OrderSummary {
  id: string;
  legalEntityId: string;
  orderNumber: string;
  productId: string;
  siteId: string;
  warehouseId: string;
  currency: string;
  quantity: string;
  qtyRaf: string;
  qtyError: string;
  remaining: string;
  status: ProductionStatus;
  deliveryDate?: string;
  originType?: string;
  reworkReference?: ReworkReference;
  plannedCost: string;
  actualCost?: string;
  version: number;
  createdAt: string;
}

export interface OrderPage {
  items: OrderSummary[];
  nextCursor?: string;
}

export interface StatusLogEntry {
  command: LoggedCommand;
  from: ProductionStatus;
  to: ProductionStatus;
  actor: string;
  traceId: string;
  occurredAt: string;
}

export interface BOMComponentInput {
  lineNo: string;
  itemId: string;
  qtyPer: string;
  scrapPct?: string;
  seriesSize?: string;
  unitCost?: string;
  roundUp?: boolean;
  flushing?: Flushing;
}

export interface OperationInput {
  operNumber: number;
  resourceId: string;
  setupTimeH?: string;
  runTimeHPer?: string;
  laborRate?: string;
  overheadRate?: string;
  scrapPct?: string;
}

export interface CreateOrderInput {
  itemGroup?: string;
  /** Rework makes a rework order; it needs the Create rework orders duty. */
  originType?: string;
  reworkReference?: ReworkReference;
  productId: string;
  siteId: string;
  warehouseId: string;
  currency: string;
  quantity: string;
  deliveryDate?: string;
  /** The item's order rules (FR-21.1); absent or 0 means no rule. */
  minimumQty?: string;
  maximumQty?: string;
  multiple?: string;
  bom: BOMComponentInput[];
  operations: OperationInput[];
}

export interface Variance {
  type: VarianceType;
  costGroup: CostGroup;
  amount: string;
}

/**
 * Cost read (FR-21.11): planned and actual totals per group and overall in the currency's minor
 * unit, the 4-decimal rows, the WIP balance and the variances posted at End.
 */
export interface CostView {
  orderId: string;
  currency: string;
  totals: Partial<Record<CostGroup, string>>;
  total: string;
  rows: CostRow[];
  actualTotals: Partial<Record<CostGroup, string>>;
  actualTotal: string;
  actualRows: CostRow[];
  wipBalance: string;
  variances: Variance[];
  varianceTotal: string;
}

export interface JournalLine {
  kind: "issue" | "hours" | "receipt";
  lineNo?: string;
  operNumber?: number;
  itemId?: string;
  qty: string;
  qtyGood: string;
  qtyError: string;
  finished: boolean;
  unitCost: string;
  amount: string;
  overheadAmount: string;
}

export interface Posting {
  postingType: string;
  account: string;
  dr: string;
  cr: string;
  wip: boolean;
}

export interface Journal {
  id: string;
  orderId: string;
  type: JournalType;
  journalNumber: string;
  postingDate: string;
  posted: boolean;
  postedAt?: string;
  postedBy?: string;
  qtyGood: string;
  qtyError: string;
  reversalOf?: string;
  reversedBy?: string;
  journalEntryId?: string;
  createdBy: string;
  createdAt: string;
  lines: JournalLine[];
  postings: Posting[];
  /** On a posted route card: the trigger points its feedback made due (FR-21.17). */
  triggerFirings?: TriggerFiring[];
}

/**
 * What a trigger point does when its condition holds (FR-21.17). release-next and
 * start-workflow need scheduling (wave 23) and the workflow engine (wave 4); the service refuses them.
 */
export type TriggerFunction = "insert-operations" | "create-rework-order" | "notify";

export const TRIGGER_FUNCTIONS: TriggerFunction[] = ["insert-operations", "create-rework-order", "notify"];

/** The feedback fields a condition compares: the route-card line of the operation. */
export type TriggerField = "qty_error" | "qty_good" | "hours";

export type TriggerOperator = ">" | ">=" | "<" | "<=" | "==" | "!=";

/** An operation a trigger inserts, or routes on the rework order it creates; numbered when it fires. */
export interface TriggerOperation {
  resourceId: string;
  setupTimeH?: string;
  runTimeHPer?: string;
  scrapPct?: string;
  laborRate?: string;
  overheadRate?: string;
}

export interface CreateTriggerPointInput {
  operNumber: number;
  sequence: number;
  /** `<field> <op> <number>`, for example `qty_error > 0`. */
  condition: string;
  function: TriggerFunction;
  repeatable?: boolean;
  operations?: TriggerOperation[];
}

export interface TriggerFeedback {
  hours: string;
  qtyGood: string;
  qtyError: string;
}

/** One due trigger point on a route-card post: fired, or failed with the reason (PROD-0019). */
export interface TriggerFiring {
  id: string;
  triggerPointId: string;
  journalId: string;
  operNumber: number;
  function: TriggerFunction;
  status: "fired" | "failed";
  reason?: string;
  feedback: TriggerFeedback;
  insertedOperations: number[];
  reworkOrderId?: string;
  actor: string;
  firedAt: string;
}

export interface TriggerPoint {
  id: string;
  orderId: string;
  operNumber: number;
  sequence: number;
  condition: string;
  function: TriggerFunction;
  operations: TriggerOperation[];
  repeatable: boolean;
  firedCount: number;
  lastFiredAt?: string;
  createdBy: string;
  createdAt: string;
  firings: TriggerFiring[];
}

export interface IssueInput {
  lineNo: string;
  quantity: string;
  unitCost?: string;
}

export interface HoursInput {
  operNumber: number;
  hours: string;
  qtyGood?: string;
  qtyError?: string;
  finished?: boolean;
}

/** CreateJournal: without issues or hours the service proposes the lines. */
export interface CreateJournalInput {
  type: JournalType;
  postingDate?: string;
  proposal?: Proposal;
  quantity?: string;
  errorQuantity?: string;
  issues?: IssueInput[];
  hours?: HoursInput[];
}

export interface PostingProfileAccounts {
  wipAccount: string;
  inventoryAccount: string;
  finishedGoodsAccount: string;
  laborAbsorptionAccount: string;
  overheadAbsorptionAccount: string;
  lotSizeVarianceAccount: string;
  quantityVarianceAccount: string;
  priceVarianceAccount: string;
  substitutionVarianceAccount: string;
  /** Optional until the legal entity runs rework orders; ending one with rework cost needs it. */
  reworkVarianceAccount?: string;
}

export interface PostingProfileInput extends PostingProfileAccounts {
  legalEntityId?: string;
  itemGroup: string;
  siteId: string;
}

export interface PostingProfile extends PostingProfileInput {
  legalEntityId: string;
  version: number;
  updatedBy: string;
  updatedAt: string;
}

/** One component serial or batch consumed into a finished serial or batch (FR-21.19). */
export interface AsBuiltRecord {
  id: string;
  orderId: string;
  orderNumber: string;
  finishedItemId: string;
  finishedSerialOrBatch: string;
  componentItemId: string;
  tracking: Tracking;
  componentSerialOrBatch: string;
  qty: string;
  journalId?: string;
  rafJournalId?: string;
  assignedBy: string;
  assignedAt: string;
  detachedBy?: string;
  detachedAt?: string;
}

export interface AssignAsBuiltInput {
  finishedSerialOrBatch: string;
  componentItemId: string;
  tracking: Tracking;
  componentSerialOrBatch: string;
  /** Defaults to 1; a serial is always 1. */
  qty?: string;
}

/** The as-built inquiry: as-built of a finished serial, or where-used of a component serial. */
export interface AsBuiltQuery {
  finishedSerialOrBatch?: string;
  componentSerialOrBatch?: string;
  orderId?: string;
}

export interface CommandBody {
  quantity?: string;
  errorQuantity?: string;
  status?: ProductionStatus;
}

/** The outcome for one order of a bulk status update. */
export interface BulkItem {
  orderId: string;
  ok: boolean;
  status?: ProductionStatus;
  version?: number;
  /** An earlier run of the same batch already moved the order. */
  replayed?: boolean;
  code?: string;
  detail?: string;
}

/** Per-order results of a bulk status update; code is PRODUCTION_BULK_PARTIAL when some failed. */
export interface BulkResult {
  command: BulkCommand;
  succeeded: number;
  failed: number;
  code?: string;
  detail?: string;
  results: BulkItem[];
}

/** A split: the new order receives splitQty; minimumQty and multiple are the item's order rules. */
export interface SplitInput {
  splitQty: string;
  minimumQty?: string;
  multiple?: string;
}

export interface SplitResult {
  original: ProductionOrder;
  new: ProductionOrder;
}

/** An order of a merge with the version (ETag) the user read. */
export interface MergeRef {
  orderId: string;
  version: number;
}

export interface MergeResult {
  survivor: ProductionOrder;
  merged: string[];
}

/** Order-specific BOM and route edits (FR-21.18). */
export type EditOp = "add" | "remove" | "change";

/** One BOM line edit: add needs itemId and qtyPer; change sends only what changes; remove sends lineNo only. */
export interface LineEdit {
  op: EditOp;
  lineNo: string;
  itemId?: string;
  qtyPer?: string;
  seriesSize?: string;
  scrapPct?: string;
  roundUp?: boolean;
  unitCost?: string;
  flushing?: Flushing;
}

/** One route operation edit: add needs resourceId; change sends only what changes; remove sends operNumber only. */
export interface OperationEdit {
  op: EditOp;
  operNumber: number;
  resourceId?: string;
  setupTimeH?: string;
  runTimeHPer?: string;
  scrapPct?: string;
  laborRate?: string;
  overheadRate?: string;
  subcontract?: boolean;
  subcontractPrice?: string;
}

export interface OrderEdits {
  lines?: LineEdit[];
  operations?: OperationEdit[];
}

/** One audit row of an order edit: the line or operation before and after it. */
export interface OrderEditEntry {
  orderVersion: number;
  seq: number;
  target: "line" | "operation";
  op: EditOp;
  key: string;
  before: OrderLine | Operation | null;
  after: OrderLine | Operation | null;
  actor: string;
  traceId: string;
  occurredAt: string;
}

/** One order of the WIP balance report (4.21.14); amounts in the order currency's minor unit. */
export interface WIPRow {
  orderId: string;
  orderNumber: string;
  legalEntityId: string;
  siteId: string;
  productId: string;
  status: ProductionStatus;
  currency: string;
  consumption: string;
  rafValue: string;
  settled: string;
  wipBalance: string;
}

export interface WIPTotal {
  currency: string;
  orders: number;
  consumption: string;
  rafValue: string;
  settled: string;
  wipBalance: string;
}

export interface WIPReport {
  asOf: string;
  items: WIPRow[];
  totals: WIPTotal[];
  nextCursor?: string;
}

export interface WIPQuery {
  status?: ProductionStatus | "";
  siteId?: string;
  asOf?: string;
  cursor?: string;
  limit?: number;
}

/** One posted variance of the variance report (4.21.14); positive is unfavourable. */
export interface VarianceRow {
  id: string;
  orderId: string;
  orderNumber: string;
  siteId: string;
  itemGroup?: string;
  currency: string;
  type: VarianceType;
  costGroup: CostGroup;
  amount: string;
  planned: string;
  /** Null when nothing was planned for the cost group. */
  pctOfPlanned: string | null;
  postedOn: string;
}

export interface VarianceReport {
  items: VarianceRow[];
  totals: { currency: string; type: VarianceType; amount: string }[];
  net: { currency: string; amount: string }[];
  nextCursor?: string;
}

export interface VarianceQuery {
  varianceType?: VarianceType | "";
  siteId?: string;
  itemGroup?: string;
  orderId?: string;
  from?: string;
  to?: string;
  cursor?: string;
  limit?: number;
}

/** One subcontract operation of the subcontract operations inquiry (4.21.14, FR-21.10). */
export interface SubcontractRow {
  orderId: string;
  orderNumber: string;
  orderStatus: ProductionStatus;
  legalEntityId: string;
  siteId: string;
  currency: string;
  /** The order's delivery date until scheduling dates the operation. */
  dueDate: string | null;
  operNumber: number;
  /** The vendor resource. */
  resourceId: string;
  qty: string;
  qtyGood: string;
  unitPrice: string;
  /** Unit price × quantity in the currency's minor unit. */
  amount: string;
  operStatus: OperStatus;
  /** Null until release requests the vendor PO. */
  poRequestedAt: string | null;
}

export interface SubcontractReport {
  items: SubcontractRow[];
  totals: { currency: string; operations: number; amount: string }[];
  nextCursor?: string;
}

export interface SubcontractQuery {
  status?: OperStatus | "";
  orderStatus?: ProductionStatus | "";
  resourceId?: string;
  siteId?: string;
  dueFrom?: string;
  dueTo?: string;
  cursor?: string;
  limit?: number;
}

export interface Problem {
  status: number;
  code: string;
  message: string;
  errors?: Record<string, string>;
}

export class ProductionApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly errors?: Record<string, string>,
  ) {
    super(message);
    this.name = "ProductionApiError";
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ProductionApiError) {
    return { status: error.status, code: error.code, message: error.message, errors: error.errors };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed" };
}

async function toError(res: Response): Promise<ProductionApiError> {
  const body = (await res.json().catch(() => null)) as {
    code?: unknown;
    detail?: unknown;
    title?: unknown;
    errors?: unknown;
  } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : "COM-0001";
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (${res.status})`;
  const errors =
    body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : undefined;
  return new ProductionApiError(res.status, code, message, errors);
}

async function requestAt<T>(url: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(url, init);
  if (!res.ok) {
    throw await toError(res);
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return (await res.json()) as T;
}

function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  return requestAt<T>(`${base}${path}`, init);
}

/** A fresh Idempotency-Key. Keep one per user intent so a retry replays instead of duplicating. */
export function newIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `key-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export interface ListOptions {
  status?: ProductionStatus | "";
  /** Only orders of this origin, for example Rework. */
  originType?: string;
  /** Only the rework orders that reference this order id (FR-21.20). */
  reworkOf?: string;
  cursor?: string;
  limit?: number;
  signal?: AbortSignal;
}

export async function listOrders({ status, originType, reworkOf, cursor, limit = 50, signal }: ListOptions = {}): Promise<OrderPage> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (status) {
    params.set("status", status);
  }
  if (originType) {
    params.set("originType", originType);
  }
  if (reworkOf) {
    params.set("reworkOf", reworkOf);
  }
  if (cursor) {
    params.set("cursor", cursor);
  }
  const page = await request<Partial<OrderPage>>(`?${params}`, { signal });
  return { items: Array.isArray(page.items) ? page.items : [], nextCursor: page.nextCursor };
}

export function getOrder(id: string, signal?: AbortSignal): Promise<ProductionOrder> {
  return request<ProductionOrder>(`/${encodeURIComponent(id)}`, { signal });
}

export async function getStatusLog(id: string, signal?: AbortSignal): Promise<StatusLogEntry[]> {
  const body = await request<{ items?: StatusLogEntry[] }>(`/${encodeURIComponent(id)}/status-log`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

export async function getCosts(id: string, signal?: AbortSignal): Promise<CostView> {
  const body = await request<Partial<CostView>>(`/${encodeURIComponent(id)}/costs`, { signal });
  return {
    orderId: body.orderId ?? id,
    currency: body.currency ?? "",
    totals: body.totals ?? {},
    total: body.total ?? "0",
    rows: Array.isArray(body.rows) ? body.rows : [],
    actualTotals: body.actualTotals ?? {},
    actualTotal: body.actualTotal ?? "0",
    actualRows: Array.isArray(body.actualRows) ? body.actualRows : [],
    wipBalance: body.wipBalance ?? "0",
    variances: Array.isArray(body.variances) ? body.variances : [],
    varianceTotal: body.varianceTotal ?? "0",
  };
}

export async function listJournals(orderId: string, signal?: AbortSignal): Promise<Journal[]> {
  const body = await request<{ items?: Journal[] }>(`/${encodeURIComponent(orderId)}/journals`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** Creates a draft journal on an order (FR-21.5 to FR-21.7). */
export function createJournal(orderId: string, input: CreateJournalInput, idempotencyKey: string): Promise<Journal> {
  return request<Journal>(`/${encodeURIComponent(orderId)}/journals`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** Posts a draft journal; a retry with the same key replays. */
export function postJournal(journalId: string, idempotencyKey: string): Promise<Journal> {
  return requestAt<Journal>(`${root}/journals/${encodeURIComponent(journalId)}:post`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

export function deleteJournal(journalId: string): Promise<void> {
  return requestAt<void>(`${root}/journals/${encodeURIComponent(journalId)}`, { method: "DELETE" });
}

export async function listPostingProfiles(signal?: AbortSignal): Promise<PostingProfile[]> {
  const body = await requestAt<{ items?: PostingProfile[] }>(`${root}/posting-profiles`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** Creates a profile (no version) or replaces it under If-Match (FR-21.13). */
export function savePostingProfile(input: PostingProfileInput, version?: number): Promise<PostingProfile> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (version !== undefined) {
    headers["If-Match"] = `"${version}"`;
  }
  return requestAt<PostingProfile>(`${root}/posting-profiles`, { method: "PUT", headers, body: JSON.stringify(input) });
}

/** The attached as-built records of an order, oldest first (FR-21.19). */
export async function listOrderAsBuilt(orderId: string, signal?: AbortSignal): Promise<AsBuiltRecord[]> {
  const body = await request<{ items?: AsBuiltRecord[] }>(`/${encodeURIComponent(orderId)}/as-built`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** Assigns a component serial or batch to a finished serial or batch; a retry with the same key replays. */
export function assignAsBuilt(orderId: string, input: AssignAsBuiltInput, idempotencyKey: string): Promise<AsBuiltRecord> {
  return request<AsBuiltRecord>(`/${encodeURIComponent(orderId)}/as-built`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** Detaches an as-built record (a wrong assignment); detaching twice returns it unchanged. */
export function detachAsBuilt(recordId: string): Promise<AsBuiltRecord> {
  return requestAt<AsBuiltRecord>(`${root}/as-built/${encodeURIComponent(recordId)}:detach`, { method: "POST" });
}

/** The as-built of a finished serial or the where-used of a component serial (4.21.14). */
export async function queryAsBuilt(query: AsBuiltQuery, signal?: AbortSignal): Promise<AsBuiltRecord[]> {
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(query)) {
    if (value) {
      params.set(name, value);
    }
  }
  const body = await requestAt<{ items?: AsBuiltRecord[] }>(`${root}/as-built?${params}`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** The trigger points of an order with their firings, by operation and sequence (FR-21.17). */
export async function listTriggerPoints(orderId: string, signal?: AbortSignal): Promise<TriggerPoint[]> {
  const body = await request<{ items?: TriggerPoint[] }>(`/${encodeURIComponent(orderId)}/trigger-points`, { signal });
  return Array.isArray(body.items) ? body.items.map((tp) => ({ ...tp, firings: Array.isArray(tp.firings) ? tp.firings : [] })) : [];
}

/** Defines a trigger point on an operation of the order; a retry with the same key replays. */
export function createTriggerPoint(orderId: string, input: CreateTriggerPointInput, idempotencyKey: string): Promise<TriggerPoint> {
  return request<TriggerPoint>(`/${encodeURIComponent(orderId)}/trigger-points`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** Removes a trigger point that never fired. */
export function deleteTriggerPoint(id: string): Promise<void> {
  return requestAt<void>(`${root}/trigger-points/${encodeURIComponent(id)}`, { method: "DELETE" });
}

/** Trigger points can be defined until the order is ended or cancelled. */
export function canDefineTriggers(status: ProductionStatus): boolean {
  return status !== "Ended" && status !== "Cancelled";
}

/** Journal types an order in this status can take (FR-21.5 to FR-21.7; a route card starts a Released order). */
export function journalTypesFor(status: ProductionStatus): JournalType[] {
  switch (status) {
    case "Released":
      return ["route-card"];
    case "Started":
    case "ReportedAsFinished":
      return ["picking-list", "route-card", "report-as-finished"];
    default:
      return [];
  }
}

export function createOrder(input: CreateOrderInput, idempotencyKey: string): Promise<ProductionOrder> {
  return request<ProductionOrder>("", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

export function updateOrder(
  id: string,
  version: number,
  patch: { quantity?: string; deliveryDate?: string | null },
): Promise<ProductionOrder> {
  return request<ProductionOrder>(`/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json", "If-Match": `"${version}"` },
    body: JSON.stringify(patch),
  });
}

/** Applies a status command. version is the ETag of the last read (If-Match). */
export function runCommand(
  id: string,
  command: ProductionCommand,
  version: number,
  idempotencyKey: string,
  body: CommandBody = {},
): Promise<ProductionOrder> {
  return request<ProductionOrder>(`/${encodeURIComponent(id)}:${command}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
    body: JSON.stringify(body),
  });
}

/** A command on one operation of an order (FR-21.6). */
export type OperationCommand = "finish" | "reopen";

/**
 * Finishes or reopens an operation. version is the order's ETag (If-Match); the
 * answer is the order with the operation's new status and the next version.
 */
export function runOperationCommand(
  id: string,
  operNumber: number,
  command: OperationCommand,
  version: number,
  idempotencyKey: string,
): Promise<ProductionOrder> {
  return request<ProductionOrder>(`/${encodeURIComponent(id)}/operations/${operNumber}:${command}`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
  });
}

/** Operations are finished and reopened while the order takes feedback (FR-21.6). */
export function canCommandOperations(status: ProductionStatus): boolean {
  return status === "Started" || status === "ReportedAsFinished";
}

/** Applies one command to up to 500 orders; every order gets its own result (FR-21.12). */
export async function bulkStatus(orderIds: string[], command: BulkCommand, idempotencyKey: string): Promise<BulkResult> {
  const body = await requestAt<Partial<BulkResult>>(`${base}:bulk-status`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify({ command, orderIds }),
  });
  return {
    command: body.command ?? command,
    succeeded: body.succeeded ?? 0,
    failed: body.failed ?? 0,
    code: body.code,
    detail: body.detail,
    results: Array.isArray(body.results) ? body.results : [],
  };
}

/** Splits splitQty off a Created to Released order into a new order (FR-21.15). */
export function splitOrder(id: string, version: number, input: SplitInput, idempotencyKey: string): Promise<SplitResult> {
  return request<SplitResult>(`/${encodeURIComponent(id)}:split`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
    body: JSON.stringify(input),
  });
}

/** Merges the orders into the first one; the others are cancelled into it (FR-21.15). */
export function mergeOrders(orders: MergeRef[], idempotencyKey: string): Promise<MergeResult> {
  return requestAt<MergeResult>(`${base}:merge`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify({ orders }),
  });
}

/** Applies BOM and route edits under If-Match (FR-21.18); a Released order also needs the after-release duty. */
export function editOrderBOM(id: string, version: number, edits: OrderEdits): Promise<ProductionOrder> {
  return request<ProductionOrder>(`/${encodeURIComponent(id)}/bom`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json", "If-Match": `"${version}"` },
    body: JSON.stringify(edits),
  });
}

/** The audit trail of an order's BOM and route edits, oldest first. */
export async function listOrderEdits(id: string, signal?: AbortSignal): Promise<OrderEditEntry[]> {
  const body = await request<{ items?: OrderEditEntry[] }>(`/${encodeURIComponent(id)}/bom-edits`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** The BOM and route can be edited after explosion and before Started (FR-21.18). */
export function canEditBOM(status: ProductionStatus): boolean {
  return canSplit(status);
}

function query(values: Record<string, string | number | undefined>): string {
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(values)) {
    if (value !== undefined && value !== "") {
      params.set(name, String(value));
    }
  }
  return params.toString();
}

/** The WIP balance by order as of a date (4.21.14). */
export async function getWIPBalances({ limit = 50, ...filter }: WIPQuery = {}, signal?: AbortSignal): Promise<WIPReport> {
  const body = await requestAt<Partial<WIPReport>>(`${root}/wip-balances?${query({ ...filter, limit })}`, { signal });
  return {
    asOf: body.asOf ?? filter.asOf ?? "",
    items: Array.isArray(body.items) ? body.items : [],
    totals: Array.isArray(body.totals) ? body.totals : [],
    nextCursor: body.nextCursor,
  };
}

/** The posted variances with totals by type and the net per currency (4.21.14). */
export async function getVarianceReport({ limit = 50, ...filter }: VarianceQuery = {}, signal?: AbortSignal): Promise<VarianceReport> {
  const body = await requestAt<Partial<VarianceReport>>(`${root}/variances?${query({ ...filter, limit })}`, { signal });
  return {
    items: Array.isArray(body.items) ? body.items : [],
    totals: Array.isArray(body.totals) ? body.totals : [],
    net: Array.isArray(body.net) ? body.net : [],
    nextCursor: body.nextCursor,
  };
}

/** The subcontract operations with their planned amount and PO request, totals per currency (4.21.14). */
export async function getSubcontractOperations(
  { limit = 50, ...filter }: SubcontractQuery = {},
  signal?: AbortSignal,
): Promise<SubcontractReport> {
  const body = await requestAt<Partial<SubcontractReport>>(`${root}/subcontract-operations?${query({ ...filter, limit })}`, { signal });
  return {
    items: Array.isArray(body.items) ? body.items : [],
    totals: Array.isArray(body.totals) ? body.totals : [],
    nextCursor: body.nextCursor,
  };
}

/** Split and merge take orders before Started (FR-21.15; a Started split needs the WIP transfer of a later slice). */
export function canSplit(status: ProductionStatus): boolean {
  return status === "Created" || status === "Estimated" || status === "Scheduled" || status === "Released";
}

/** The forward command for a status, or null at the end of the line (BR-21.1). */
export function nextCommand(status: ProductionStatus): ProductionCommand | null {
  switch (status) {
    case "Created":
      return "estimate";
    case "Estimated":
      return "schedule";
    case "Scheduled":
      return "release";
    case "Released":
      return "start";
    case "Started":
      return "report-finished";
    case "ReportedAsFinished":
      return "end";
    default:
      return null;
  }
}

/** Every command the state machine allows from a status, forward command first. */
export function allowedCommands(status: ProductionStatus): ProductionCommand[] {
  switch (status) {
    case "Created":
      return ["estimate", "cancel"];
    case "Estimated":
      return ["schedule", "estimate", "cancel", "reset-status"];
    case "Scheduled":
      return ["release", "estimate", "cancel", "reset-status"];
    case "Released":
      return ["start", "estimate", "cancel", "reset-status"];
    case "Started":
      return ["report-finished", "end", "reset-status"];
    case "ReportedAsFinished":
      return ["end", "report-finished", "reset-status"];
    default:
      return [];
  }
}

/** Statuses a reset may return to from status (FR-21.9), latest first. */
export function resetTargets(status: ProductionStatus): ProductionStatus[] {
  const order: ProductionStatus[] = ["Created", "Estimated", "Scheduled", "Released", "Started", "ReportedAsFinished"];
  const at = order.indexOf(status);
  return at <= 0 ? [] : order.slice(0, at).reverse();
}
