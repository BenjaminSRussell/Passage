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

export type JournalType = "picking-list" | "route-card" | "job-card" | "report-as-finished";

/** A journal as listed: also the system journal a split of a Started order posts on both orders (BR-21.11). */
export type ListedJournalType = JournalType | "split-transfer";

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
  /** When that request was withdrawn: order cancelled or merged, operation removed, subcontracting off or vendor changed. */
  poCancelledAt?: string;
  queueBeforeH?: string;
  queueAfterH?: string;
  transitH?: string;
  /** The window the schedule gave the operation, queue and transit included; absent until the order is scheduled. */
  plannedStart?: string;
  plannedEnd?: string;
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
  /** UTC dates of the first start and last finish, set by scheduling; absent while unscheduled. */
  startDate?: string;
  endDate?: string;
  schedulingMethod?: SchedulingMethod;
  /** How the dates were found: infinite (:schedule, a re-date) or finite (scheduling's windows applied, FR-23.10). */
  scheduleBasis?: ScheduleBasis;
  finiteRunId?: string;
  finiteScheduledAt?: string;
  /** Operations whose finite window is shorter than their jobs' planned hours (applied, flagged). */
  finiteCompressedOperations?: number[];
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
  /** Co-products and by-products (FR-21.14); absent on an order without them. */
  coProducts?: CoProduct[];
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
  /** Hours the order waits before the operation, after it, and to reach the next one; they become jobs when scheduled. */
  queueBeforeH?: string;
  queueAfterH?: string;
  transitH?: string;
}

/** A co-product shares the order's cost by quantity; a by-product carries none (BR-9.12). */
export type CoProductType = "co-product" | "by-product";

/** A co-product or by-product declared on a new order (FR-21.14). */
export interface CoProductInput {
  lineNo: string;
  /** Defaults to co-product. */
  type?: CoProductType;
  itemId: string;
  /** Planned quantity per unit of the order's product. */
  qtyPer: string;
}

/** A declared co-product with its planned and received quantities. */
export interface CoProduct extends CoProductInput {
  type: CoProductType;
  qtyPlanned: string;
  qtyReported: string;
}

export interface CoProductReport {
  lineNo: string;
  quantity: string;
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
  /** Not allowed on a rework order. */
  coProducts?: CoProductInput[];
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
  kind: "issue" | "hours" | "receipt" | "coproduct";
  lineNo?: string;
  /** On a coproduct receipt: the order's co-product line. */
  coProductLineNo?: string;
  operNumber?: number;
  /** On a job-card hours line: the job of the operation it reported on. */
  jobType?: "setup" | "process";
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
  type: ListedJournalType;
  journalNumber: string;
  postingDate: string;
  posted: boolean;
  postedAt?: string;
  postedBy?: string;
  qtyGood: string;
  qtyError: string;
  reversalOf?: string;
  reversedBy?: string;
  /** On a split-transfer journal: the other order of the pair (BR-21.11). */
  counterpartOrderId?: string;
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
  /** A route card line names the operation. */
  operNumber?: number;
  /** A job card line names the setup or process job. */
  jobId?: string;
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
  /** Co-product receipts of a report as finished (FR-21.14). */
  coProducts?: CoProductReport[];
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
  /** Schedule only: how finely to cut the operations, the direction and the instant (RFC 3339). */
  method?: SchedulingMethod;
  direction?: ScheduleDirection;
  at?: string;
}

export type SchedulingMethod = "operations" | "jobs";
export type ScheduleBasis = "infinite" | "finite";
export type ScheduleDirection = "forward" | "backward";
export const SCHEDULING_METHODS: SchedulingMethod[] = ["operations", "jobs"];

export type JobType = "queue-before" | "setup" | "process" | "queue-after" | "transport";
export const JOB_TYPES: JobType[] = ["queue-before", "setup", "process", "queue-after", "transport"];

/** A dated slice of an operation written by :schedule. */
export interface ProductionJob {
  id: string;
  orderId: string;
  orderNumber: string;
  orderStatus: ProductionStatus;
  legalEntityId: string;
  siteId: string;
  productId: string;
  operNumber: number;
  seq: number;
  jobType: JobType;
  resourceId: string;
  plannedStart: string;
  plannedEnd: string;
  plannedHours: string;
  /** What the job cards reported on the job (FR-21.6). */
  hoursReported: string;
  qtyGood: string;
  qtyError: string;
  status: "planned" | "started" | "finished";
}

export interface JobPage {
  items: ProductionJob[];
  total: number;
  plannedHours: string;
  nextCursor?: string;
}

export interface JobsQuery {
  resourceId?: string;
  siteId?: string;
  orderId?: string;
  jobType?: JobType | "";
  orderStatus?: ProductionStatus | "";
  /** RFC 3339 instants; a job is in the window when it ends after from and starts before to. */
  from?: string;
  to?: string;
  cursor?: string;
  limit?: number;
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
  /** Started orders only: proportional moves the share of posted consumption, hours and WIP; keep-on-original moves nothing (BR-21.11). */
  allocation?: SplitAllocation;
}

export type SplitAllocation = "proportional" | "keep-on-original";

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
  /** Set while the vendor PO request is withdrawn; null otherwise. */
  poCancelledAt?: string | null;
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

/** One operation of the operations overview (4.21.14). */
export interface OperationOverviewRow {
  orderId: string;
  orderNumber: string;
  orderStatus: ProductionStatus;
  legalEntityId: string;
  siteId: string;
  productId: string;
  /** The order's delivery date until scheduling dates the operation. */
  dueDate: string | null;
  operNumber: number;
  resourceId: string;
  subcontract: boolean;
  processQty: string;
  /** Setup + run × process quantity, two places. */
  plannedHours: string;
  /** Hours posted by route cards, two places. */
  hoursReported: string;
  qtyGood: string;
  qtyError: string;
  operStatus: OperStatus;
}

export interface OperationOverviewTotals {
  operations: number;
  open: number;
  started: number;
  finished: number;
  plannedHours: string;
  hoursReported: string;
  qtyGood: string;
  qtyError: string;
}

export interface OperationsOverview {
  items: OperationOverviewRow[];
  totals: OperationOverviewTotals;
  nextCursor?: string;
}

export interface OperationsQuery {
  status?: OperStatus | "";
  orderStatus?: ProductionStatus | "";
  resourceId?: string;
  siteId?: string;
  orderId?: string;
  dueFrom?: string;
  dueTo?: string;
  cursor?: string;
  limit?: number;
}

/** One journal of the journal inquiry across orders (4.21.14). */
export interface JournalInquiryRow {
  id: string;
  orderId: string;
  orderNumber: string;
  legalEntityId: string;
  type: ListedJournalType;
  journalNumber: string;
  postingDate: string;
  posted: boolean;
  postedAt: string | null;
  qtyGood: string;
  qtyError: string;
  reversalOf: string | null;
  journalEntryId: string | null;
  currency: string;
  lines: number;
  /** Issue and hours lines, rounded to the currency's minor unit, negative on a reversal; set when the journal posts. */
  consumed: string;
  /** Receipt and co-product lines, rounded to the currency's minor unit, negative on a reversal; set when the journal posts. */
  received: string;
}

export interface JournalInquiry {
  items: JournalInquiryRow[];
  total: number;
  nextCursor?: string;
}

export interface JournalInquiryQuery {
  type?: ListedJournalType | "";
  posted?: "true" | "false" | "";
  orderId?: string;
  /** The exact order number (unique per legal entity). */
  orderNumber?: string;
  from?: string;
  to?: string;
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

/** Journal types an order in this status can take (FR-21.5 to FR-21.7; a route card or job card starts a Released order). */
export function journalTypesFor(status: ProductionStatus): JournalType[] {
  switch (status) {
    case "Released":
      return ["route-card", "job-card"];
    case "Started":
    case "ReportedAsFinished":
      return ["picking-list", "route-card", "job-card", "report-as-finished"];
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

/** One operation's window from finite-capacity scheduling, as RFC 3339 instants. */
export interface FiniteWindow {
  operNumber: number;
  start: string;
  end: string;
}

/** Statuses whose schedule finite dates can replace. */
export function canApplyFiniteDates(status: ProductionStatus): boolean {
  return status === "Scheduled" || status === "Released" || status === "Started";
}

/**
 * Replaces the dates of the listed operations (and the jobs inside them) with finite-capacity windows
 * (FR-23.10). version is the ETag of the last read (If-Match, required for a person): a form filled
 * against an older order is refused (412) instead of overwriting newer dates. Applying the dates the
 * order already has changes nothing, so a retry needs no Idempotency-Key.
 */
export function applyFiniteDates(id: string, version: number, windows: FiniteWindow[], runId?: string): Promise<ProductionOrder> {
  const body: { operations: FiniteWindow[]; runId?: string } = { operations: windows };
  if (runId) {
    body.runId = runId;
  }
  return request<ProductionOrder>(`/${encodeURIComponent(id)}:apply-finite-dates`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "If-Match": `"${version}"` },
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
  return canMerge(status);
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

/** The operations of every order with planned and reported hours and quantities, totals over the filter (4.21.14). */
export async function getOperationsOverview({ limit = 50, ...filter }: OperationsQuery = {}, signal?: AbortSignal): Promise<OperationsOverview> {
  const body = await requestAt<Partial<OperationsOverview>>(`${root}/operations?${query({ ...filter, limit })}`, { signal });
  const zero = "0.00";
  return {
    items: Array.isArray(body.items) ? body.items : [],
    totals: body.totals ?? { operations: 0, open: 0, started: 0, finished: 0, plannedHours: zero, hoursReported: zero, qtyGood: "0", qtyError: "0" },
    nextCursor: body.nextCursor,
  };
}

/** The dated jobs across orders, by planned start (the load of a resource over a window). */
export async function getJobs({ limit = 50, ...filter }: JobsQuery = {}, signal?: AbortSignal): Promise<JobPage> {
  const body = await requestAt<Partial<JobPage>>(`${root}/jobs?${query({ ...filter, limit })}`, { signal });
  return {
    items: Array.isArray(body.items) ? body.items : [],
    total: body.total ?? 0,
    plannedHours: body.plannedHours ?? "0",
    nextCursor: body.nextCursor,
  };
}

/** The jobs of one order in route order; empty while it is unscheduled. */
export async function getOrderJobs(orderId: string, signal?: AbortSignal): Promise<ProductionJob[]> {
  const body = await request<{ items?: ProductionJob[] }>(`/${encodeURIComponent(orderId)}/jobs`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** The production journals across orders, newest ids last (4.21.14). */
export async function getJournalInquiry({ limit = 50, ...filter }: JournalInquiryQuery = {}, signal?: AbortSignal): Promise<JournalInquiry> {
  const body = await requestAt<Partial<JournalInquiry>>(`${root}/journals?${query({ ...filter, limit })}`, { signal });
  return {
    items: Array.isArray(body.items) ? body.items : [],
    total: typeof body.total === "number" ? body.total : 0,
    nextCursor: body.nextCursor,
  };
}

/** One group of the order KPIs (4.21.14); rates are null when there is nothing to measure. */
export interface OrderKpiGroup {
  /** The site or product id of the group; absent on the totals and without grouping. */
  key?: string;
  ended: number;
  dated: number;
  onTime: number;
  onTimePct: string | null;
  avgCycleDays: string | null;
  qtyFinished: string;
  qtyError: string;
  scrapPct: string | null;
  open: number;
  overdue: number;
}

export type KpiGroupBy = "site" | "product";

export interface OrderKpis {
  asOf: string;
  groupBy?: KpiGroupBy;
  groups: OrderKpiGroup[];
  groupsTotal: number;
  totals: OrderKpiGroup;
}

export interface OrderKpiQuery {
  siteId?: string;
  productId?: string;
  from?: string;
  to?: string;
  asOf?: string;
  groupBy?: KpiGroupBy | "";
  limit?: number;
}

const noKpi: OrderKpiGroup = {
  ended: 0,
  dated: 0,
  onTime: 0,
  onTimePct: null,
  avgCycleDays: null,
  qtyFinished: "0",
  qtyError: "0",
  scrapPct: null,
  open: 0,
  overdue: 0,
};

/** Orders ended in a period with on-time %, cycle days, scrap % and the open and overdue orders (4.21.14). */
export async function getOrderKpis({ limit = 50, ...filter }: OrderKpiQuery = {}, signal?: AbortSignal): Promise<OrderKpis> {
  const body = await requestAt<Partial<OrderKpis>>(`${root}/kpi?${query({ ...filter, limit })}`, { signal });
  return {
    asOf: body.asOf ?? "",
    groupBy: body.groupBy,
    groups: Array.isArray(body.groups) ? body.groups : [],
    groupsTotal: typeof body.groupsTotal === "number" ? body.groupsTotal : 0,
    totals: body.totals ?? noKpi,
  };
}

// ---- Order change management (FR-21.16, BR-21.12) ----

export type OCMMode = "automatic" | "manual" | "none";

export const OCM_MODES: OCMMode[] = ["automatic", "manual", "none"];

export type ChangeStatus = "pending" | "applied" | "dismissed";

export const CHANGE_STATUSES: ChangeStatus[] = ["pending", "applied", "dismissed"];

export interface OCMProfileInput {
  /** An item group, or All. */
  itemGroup: string;
  /** A site id, or All. */
  siteId: string;
  mode: OCMMode;
  surchargeFixed: string;
  surchargePct: string;
}

export interface OCMProfile extends OCMProfileInput {
  version: number;
  updatedBy?: string;
  updatedAt?: string;
}

export interface ChangeDelta {
  linesAdded: number;
  linesRemoved: number;
  linesChanged: number;
  operationsAdded: number;
  operationsRemoved: number;
  operationsChanged: number;
}

/** An OrderChangeRecord: an open order an ECO's BOM or route supersedes. */
export interface OrderChange {
  id: string;
  orderId: string;
  orderNumber: string;
  productId: string;
  siteId: string;
  ecoId: string;
  status: ChangeStatus;
  profileMode: "automatic" | "manual";
  /** The order was edited by hand (FR-21.18): flagged for manual review. */
  needsReview: boolean;
  changesBom: boolean;
  changesRoute: boolean;
  delta: ChangeDelta;
  deltaCount: number;
  currency: string;
  changeCost: string;
  beforeHash: string | null;
  afterHash: string | null;
  note: string;
  version: number;
  detectedBy: string;
  detectedAt: string;
  resolvedBy: string | null;
  resolvedAt: string | null;
}

export interface ChangeQuery {
  status?: ChangeStatus | "";
  needsReview?: boolean;
  ecoId?: string;
  orderId?: string;
  cursor?: string;
  limit?: number;
}

export interface ChangePage {
  items: OrderChange[];
  nextCursor?: string;
}

export interface ChangeCost {
  oldPlanned: string;
  newPlanned: string;
  delta: string;
  surcharge: string;
  total: string;
}

export interface ChangePreview {
  change: OrderChange;
  delta: ChangeDelta;
  cost: ChangeCost;
  /** Why applying would be refused now, or null. */
  blocked: { code: string; detail: string } | null;
}

export async function listOCMProfiles(signal?: AbortSignal): Promise<OCMProfile[]> {
  const body = await requestAt<{ items?: OCMProfile[] }>(`${root}/ocm-profiles`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** Creates an OCM profile (no version) or replaces it under If-Match. */
export function saveOCMProfile(input: OCMProfileInput, version?: number): Promise<OCMProfile> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (version !== undefined) {
    headers["If-Match"] = `"${version}"`;
  }
  return requestAt<OCMProfile>(`${root}/ocm-profiles`, { method: "PUT", headers, body: JSON.stringify(input) });
}

/** The order change worklist, newest first. */
export async function listOrderChanges({ limit = 50, ...filter }: ChangeQuery = {}, signal?: AbortSignal): Promise<ChangePage> {
  const params = query({ ...filter, needsReview: filter.needsReview === undefined ? undefined : String(filter.needsReview), limit });
  const body = await requestAt<Partial<ChangePage>>(`${root}/order-changes?${params}`, { signal });
  return { items: Array.isArray(body.items) ? body.items : [], nextCursor: body.nextCursor };
}

export function getOrderChange(id: string, signal?: AbortSignal): Promise<OrderChange> {
  return requestAt<OrderChange>(`${root}/order-changes/${encodeURIComponent(id)}`, { signal });
}

/** What applying the change does to the order as it is now, with the change cost. */
export function previewOrderChange(id: string, signal?: AbortSignal): Promise<ChangePreview> {
  return requestAt<ChangePreview>(`${root}/order-changes/${encodeURIComponent(id)}:preview`, { method: "POST", signal });
}

/** Applies a pending change; a retry with the same key replays the first result. */
export function applyOrderChange(id: string, version: number, idempotencyKey: string): Promise<{ change: OrderChange; order: ProductionOrder }> {
  return requestAt(`${root}/order-changes/${encodeURIComponent(id)}:apply`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
    body: "{}",
  });
}

export function dismissOrderChange(id: string, version: number, note: string): Promise<OrderChange> {
  return requestAt<OrderChange>(`${root}/order-changes/${encodeURIComponent(id)}:dismiss`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "If-Match": `"${version}"` },
    body: JSON.stringify({ note }),
  });
}

/** Merge takes orders before Started, where nothing is posted yet (FR-21.15). */
export function canMerge(status: ProductionStatus): boolean {
  return status === "Created" || status === "Estimated" || status === "Scheduled" || status === "Released";
}

/** A split takes Created to Started orders; a Started one moves its posted share (FR-21.15, BR-21.11). */
export function canSplit(status: ProductionStatus): boolean {
  return canMerge(status) || status === "Started";
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

// ---- Production parameters (PRD 4.21.13) ----

export const SPLIT_ALLOCATIONS: SplitAllocation[] = ["proportional", "keep-on-original"];

export interface ProductionParametersInput {
  /** A site id, or All. */
  siteId: string;
  rafTolerancePct: string;
  overConsumptionPct: string;
  autoStartOnFeedback: boolean;
  estimateOnCreate: boolean;
  splitAllocation: SplitAllocation;
  triggerPointsEnabled: boolean;
  asBuiltRequired: boolean;
  /** The proposal of a picking-list journal that names none. */
  pickingListProposal: Proposal;
  /** The hours basis of a route-card journal that names none. */
  routeCardProposal: RouteCardProposal;
  /** Days after which a Created order that was never estimated is cancelled by the stale-order job; 0 never. */
  staleOrderDays: number;
  /** Opts the site in to the auto-end job. */
  autoEndEnabled: boolean;
  /** The report as finished that completes the order quantity finishes every job of the order. */
  endJobs: boolean;
  /** Ending a rework order capitalizes its cost into finished goods instead of posting a rework variance. */
  reworkCapitalize: boolean;
}

export type RouteCardProposal = "remaining" | "proportional";

export const PICKING_PROPOSALS: Proposal[] = ["remaining", "proportional", "none"];
export const ROUTE_CARD_PROPOSALS: RouteCardProposal[] = ["remaining", "proportional"];
export const MAX_STALE_ORDER_DAYS = 3650;

export interface ProductionParameters extends ProductionParametersInput {
  version: number;
  updatedBy?: string;
  updatedAt?: string;
}

/** The stored parameters of the caller's legal entity and the service defaults that apply to a site with none. */
export interface ProductionParametersList {
  items: ProductionParameters[];
  defaults: ProductionParametersInput | null;
}

export async function listProductionParameters(signal?: AbortSignal): Promise<ProductionParametersList> {
  const body = await requestAt<{ items?: ProductionParameters[]; defaults?: ProductionParametersInput }>(`${root}/parameters`, { signal });
  return { items: Array.isArray(body.items) ? body.items : [], defaults: body.defaults ?? null };
}

/** Creates the parameters of a site (no version) or replaces them under If-Match. */
export function saveProductionParameters(input: ProductionParametersInput, version?: number): Promise<ProductionParameters> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (version !== undefined) {
    headers["If-Match"] = `"${version}"`;
  }
  return requestAt<ProductionParameters>(`${root}/parameters`, { method: "PUT", headers, body: JSON.stringify(input) });
}

// ---- Order housekeeping jobs (PRD 4.21.16) ----

/** cancel-stale-created-orders and auto-end-fully-reported. */
export type HousekeepingJob = "cancel-stale" | "auto-end";

/** An order a housekeeping job selects. */
export interface HousekeepingOrder {
  orderId: string;
  orderNumber: string;
  siteId: string;
  productId: string;
  status: ProductionStatus;
  quantity: string;
  qtyReportedFinished: string;
  version: number;
  createdAt: string;
  ageDays: number;
}

/** What a job selected and, unless it was a dry run, the result per order. */
export interface HousekeepingResult {
  job: string;
  dryRun: boolean;
  /** The Idempotency-Key was used before: the earlier run's orders are replayed, nothing is scanned again. */
  replayed: boolean;
  total: number;
  /** More orders qualify than one call takes (500): run it again. */
  truncated: boolean;
  candidates: HousekeepingOrder[];
  succeeded: number;
  failed: number;
  code?: string;
  detail?: string;
  results: BulkItem[];
}

/**
 * Lists what a job would do (dryRun), or does it; a run needs an Idempotency-Key. orderIds limits a run to the orders a
 * dry run showed, so it acts on no order the user did not review.
 */
export async function runHousekeeping(job: HousekeepingJob, dryRun: boolean, idempotencyKey: string, orderIds?: string[]): Promise<HousekeepingResult> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (!dryRun) {
    headers["Idempotency-Key"] = idempotencyKey;
  }
  const payload = orderIds && orderIds.length > 0 ? { dryRun, orderIds } : { dryRun };
  const body = await requestAt<Partial<HousekeepingResult>>(`${base}:${job}`, { method: "POST", headers, body: JSON.stringify(payload) });
  return {
    job: body.job ?? job,
    dryRun: body.dryRun ?? dryRun,
    replayed: body.replayed ?? false,
    total: body.total ?? 0,
    truncated: body.truncated ?? false,
    candidates: Array.isArray(body.candidates) ? body.candidates : [],
    succeeded: body.succeeded ?? 0,
    failed: body.failed ?? 0,
    code: body.code,
    detail: body.detail,
    results: Array.isArray(body.results) ? body.results : [],
  };
}

// ---- Order cost snapshots (PRD 4.21.16 order-cost-snapshot) ----

/** The planned and actual cost of one open order on one day. */
export interface CostSnapshot {
  orderId: string;
  orderNumber: string;
  siteId?: string;
  snapshotDate: string;
  status: ProductionStatus;
  currency: string;
  qtyOrdered: string;
  qtyReported: string;
  plannedCost: string;
  actualCost: string;
  wipBalance: string;
  /** Actual minus planned cost to date. */
  costVariance: string;
}

export interface CostSnapshotQuery {
  orderId?: string;
  from?: string;
  to?: string;
  limit?: number;
  cursor?: string;
}

export interface CostSnapshotPage {
  items: CostSnapshot[];
  nextCursor?: string;
}

/** The stored snapshots, newest day first. */
export async function listCostSnapshots({ limit = 50, ...filter }: CostSnapshotQuery = {}, signal?: AbortSignal): Promise<CostSnapshotPage> {
  const body = await requestAt<Partial<CostSnapshotPage>>(`${root}/cost-snapshots?${query({ ...filter, limit })}`, { signal });
  return { items: Array.isArray(body.items) ? body.items : [], nextCursor: body.nextCursor || undefined };
}

export interface CostSnapshotRunResult {
  snapshotDate: string;
  orders: number;
}

/** Takes the snapshot of the open orders for a day (default today); a second run of the day replaces its rows. */
export async function runCostSnapshot(date?: string): Promise<CostSnapshotRunResult> {
  const body = await requestAt<Partial<CostSnapshotRunResult>>(`${root}/cost-snapshots:run`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(date ? { date } : {}),
  });
  return { snapshotDate: body.snapshotDate ?? date ?? "", orders: body.orders ?? 0 };
}
