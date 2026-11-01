// Client for the scheduling service (services/scheduling/api/openapi.yaml).
// Browser calls use /api/v1/scheduling/..., which the web BFF rewrites to the service.

export const base = "/api/v1/scheduling";

export interface Shift {
  /** 0 = Sunday ... 6 = Saturday. */
  weekday: number;
  startMinute: number;
  endMinute: number;
}

export interface Resource {
  id: string;
  code: string;
  name: string;
  finite: boolean;
  timeZone: string;
  shifts: Shift[];
  version: number;
  createdAt: string;
}

export interface NewResource {
  code: string;
  name: string;
  finite: boolean;
  timeZone: string;
  shifts: Shift[];
}

export const OPERATION_STATUSES = ["open", "scheduled", "unplaced"] as const;
export type OperationStatus = (typeof OPERATION_STATUSES)[number];

export interface Operation {
  id: string;
  orderId: string;
  operNumber: number;
  minutes: number;
  dueAt?: string;
  readyAt?: string;
  priority: number;
  resourceIds: string[];
  /** Resources booked for the same segments as the primary (BR-23.4). */
  secondaryResourceIds: string[];
  /** Setup group that drives changeovers against the resource's setup matrix (FR-23.15). */
  setupGroup: string | null;
  /** Route setup time, used when the matrix has no entry for a pair (SCHED-0012). */
  setupMinutes: number;
  /** Product or order attributes that capacity block conditions match on (FR-23.17). */
  attributes: Record<string, string>;
  /** Network governing the step from the order's previous operation into this one (FR-23.13). */
  networkId: string | null;
  status: OperationStatus;
  createdAt: string;
}

export interface NewOperation {
  orderId: string;
  operNumber: number;
  minutes: number;
  dueAt?: string;
  readyAt?: string;
  priority?: number;
  resourceIds: string[];
  /** At most 5, none of them a candidate (BR-23.4). */
  secondaryResourceIds?: string[];
  setupGroup?: string;
  setupMinutes?: number;
  attributes?: Record<string, string>;
  networkId?: string;
}

/** Codes a run reports on placed operations (PRD 4.23.18). */
export const RUN_WARNING_LABELS: Record<string, string> = {
  "SCHED-0003": "Order late after backward fallback",
  "SCHED-0010": "Network wait over the maximum",
  "SCHED-0012": "No setup matrix entry",
  "SCHED-0014": "No matching capacity block",
};

export interface RunWarning {
  code: string;
  operationId: string;
  detail: string;
}

export interface Run {
  id: string;
  scope: "site" | "selection" | "move";
  mode: string;
  direction: string;
  finite: boolean;
  algorithm: string;
  status: string;
  operationsCount: number;
  makespanMinutes: number;
  totalLatenessMinutes: number;
  solverStatus: string | null;
  /** One entry per unplaced operation, "operationId: reason". */
  messages: string[];
  /** Findings on placed operations (SCHED-0010, SCHED-0012, SCHED-0014). */
  warnings: RunWarning[];
  createdAt: string;
}

export interface NewRun {
  scope: "site" | "selection";
  mode: "jobs" | "operations";
  finite: boolean;
  direction?: "forward" | "backward";
  operationIds?: string[];
  start?: string;
  horizonDays?: number;
}

/** One scheduled segment as returned by GET /gantt. */
export interface GanttBar {
  jobId: string;
  orderId: string;
  operNumber: number;
  resourceId: string;
  segment: number;
  start: string;
  end: string;
  lateMinutes: number;
  firmed: boolean;
  setupGroup: string | null;
  /** Changeover at the start of the job, on its first segment only (FR-23.15). */
  changeoverMinutes: number;
  /**
   * Cost of the changeover (exact decimal string) in changeoverCurrency, from the
   * setup matrix entry that priced it. Null when no entry priced the job (same
   * setup group, or the route setup was used, SCHED-0012) and on later segments.
   */
  changeoverCost: string | null;
  changeoverCurrency: string | null;
  /** Cleaning standard operation set the matrix entry requires; null when none. */
  cleaningSetId: string | null;
  /** Resources booked with the job's primary resource for the same segments (BR-23.4). */
  secondaryResourceIds: string[];
  /**
   * Part of the segment lies outside the resource's open capacity as it is
   * now, e.g. inside a downtime added after it was booked (SCHED-0002, AC-23.7).
   */
  outsideCapacity: boolean;
}

export interface MoveRequest {
  resourceId?: string;
  start: string;
  dryRun: boolean;
  /** Also push the orders pegged to the moved order forward behind their parents (FR-23.8). */
  propagate?: boolean;
}

export interface MoveViolation {
  code: string;
  detail: string;
  jobId?: string;
}

export interface MoveChange {
  operationId: string;
  orderId: string;
  resourceId: string;
  oldStart: string;
  oldEnd: string;
  newStart: string;
  newEnd: string;
  lateMinutes: number;
}

/** A pegged order whose dates move with a propagated move. */
export interface DependentChange {
  orderId: string;
  oldStart: string;
  oldEnd: string;
  newStart: string;
  newEnd: string;
}

export interface MoveResponse {
  dryRun: boolean;
  feasible: boolean;
  violations: MoveViolation[];
  changes: MoveChange[];
  orderId?: string;
  orderStart?: string;
  orderEnd?: string;
  /** Pegged orders that move too, parents first (propagate only). */
  dependents?: DependentChange[];
  /** The dependent walk stopped at 500 orders or 5 levels (SCHED-0007, BR-23.10). */
  truncated?: boolean;
  run?: Run;
}

export interface Firm {
  jobId: string;
  orderId: string;
  resourceId: string;
  start: string;
  end: string;
  firmed: boolean;
}

export type LoadPeriod = "week" | "day";

export interface LoadItem {
  resourceId: string;
  period?: LoadPeriod;
  /** First day of the bucket, YYYY-MM-DD: a Monday for weeks. */
  periodStart?: string;
  /** Monday of the ISO week, YYYY-MM-DD; week items only. */
  weekStart?: string;
  /** UTC instants bounding the bucket, [start, end): the drill-down range. */
  start?: string;
  end?: string;
  openMinutes: number;
  bookedMinutes: number;
  loadPct: number;
  bottleneck: boolean;
}

export interface Interval {
  start: string;
  end: string;
  minutes: number;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class SchedulingApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "SchedulingApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof SchedulingApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<SchedulingApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new SchedulingApiError(res.status, code, message, fields);
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
    throw new SchedulingApiError(0, "COM-0001", "The response has no items");
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

export function listResources(signal?: AbortSignal): Promise<Resource[]> {
  return items<Resource>("/resources", signal);
}

export function getResource(id: string, signal?: AbortSignal): Promise<Resource> {
  return send<Resource>(`/resources/${encodeURIComponent(id)}`, {}, signal);
}

export function getResourceCapacity(id: string, from: string, to: string, signal?: AbortSignal): Promise<Interval[]> {
  return items<Interval>(`/resources/${encodeURIComponent(id)}/capacity${query({ from, to })}`, signal);
}

/** Creates a resource; reuse one key per form so a retried submit cannot create two. */
export function createResource(input: NewResource, key: string = newKey()): Promise<Resource> {
  return post<Resource>("/resources", input, key);
}

export interface ResourcePatch {
  name?: string;
  timeZone?: string;
  shifts?: Shift[];
}

export interface ResourceUpdate extends Resource {
  affectedJobs: AffectedJob[];
}

/** Changes a resource under the version the planner saw (If-Match); the answer lists the jobs the new calendar no longer covers. */
export function updateResource(id: string, version: number, patch: ResourcePatch, key: string = newKey()): Promise<ResourceUpdate> {
  return send<ResourceUpdate>(`/resources/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Idempotency-Key": key, "If-Match": `"${version}"` },
    body: JSON.stringify(patch),
  });
}

export function listOperations(filter: { status?: OperationStatus | ""; orderId?: string; limit?: number } = {}, signal?: AbortSignal): Promise<Operation[]> {
  return items<Operation>(`/operations${query({ status: filter.status, orderId: filter.orderId, limit: filter.limit })}`, signal);
}

export function createOperation(input: NewOperation, key: string = newKey()): Promise<Operation> {
  return post<Operation>("/operations", input, key);
}

export function listRuns(signal?: AbortSignal): Promise<Run[]> {
  return items<Run>("/runs", signal);
}

export function getRun(id: string, signal?: AbortSignal): Promise<Run> {
  return send<Run>(`/runs/${encodeURIComponent(id)}`, {}, signal);
}

export function startRun(input: NewRun, key: string = newKey()): Promise<Run> {
  return post<Run>("/runs", input, key);
}

export function fetchGantt(from: string, to: string, resourceIds: string[] = [], signal?: AbortSignal): Promise<GanttBar[]> {
  return items<GanttBar>(`/gantt${query({ from, to, resourceIds: resourceIds.join(",") })}`, signal);
}

/** Preview (dryRun) or commit a move. A commit needs the key; send the same key to retry safely. */
export function moveJob(jobId: string, req: MoveRequest, key?: string): Promise<MoveResponse> {
  return post<MoveResponse>(`/jobs/${encodeURIComponent(jobId)}:move`, req, req.dryRun ? undefined : (key ?? newKey()));
}

export function firmJob(jobId: string, firmed: boolean, key: string = newKey()): Promise<Firm> {
  return post<Firm>(`/jobs/${encodeURIComponent(jobId)}:firm`, { firmed }, key);
}

export function fetchCapacityLoad(from: string, weeks: number, resourceIds: string[] = [], signal?: AbortSignal): Promise<LoadItem[]> {
  return items<LoadItem>(`/capacity-load${query({ from, weeks, resourceIds: resourceIds.join(",") })}`, signal);
}

/** Load per local day of each resource's calendar time zone (FR-23.9, BR-23.7); days are never flagged as bottlenecks. */
export function fetchDailyLoad(from: string, days: number, resourceIds: string[] = [], signal?: AbortSignal): Promise<LoadItem[]> {
  return items<LoadItem>(`/capacity-load${query({ period: "day", from, days, resourceIds: resourceIds.join(",") })}`, signal);
}

/** One booked segment behind a load cell (PRD 4.23.14 Capacity load detail). */
export interface LoadJob {
  jobId: string;
  orderId: string;
  operNumber: number;
  segment: number;
  role: "primary" | "secondary";
  start: string;
  end: string;
  minutesInRange: number;
  finite: boolean;
  firmed: boolean;
  lateMinutes: number;
}

export interface LoadJobs {
  items: LoadJob[];
  truncated: boolean;
}

export async function fetchLoadJobs(resourceId: string, from: string, to: string, signal?: AbortSignal): Promise<LoadJobs> {
  const body = await send<Partial<LoadJobs>>(`/capacity-load/jobs${query({ resourceId, from, to })}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new SchedulingApiError(0, "COM-0001", "The response has no items");
  }
  return { items: body.items, truncated: body.truncated === true };
}

/** Utilization of one resource by a run, over the span of the run's current jobs (PRD 4.23.14). */
export interface RunResourceKpi {
  resourceId: string;
  resourceCode: string;
  jobs: number;
  bookedMinutes: number;
  openMinutes: number;
  utilizationPct: number;
}

/** Measures of the jobs a run still holds; a later run or move replaces jobs. */
export interface RunKpis {
  runId: string;
  placedJobs: number;
  orders: number;
  lateJobs: number;
  lateOrders: number;
  maxLateMinutes: number;
  changeoverMinutes: number;
  bookedMinutes: number;
  spanStart: string | null;
  spanEnd: string | null;
  resources: RunResourceKpi[];
}

export async function getRunKpis(id: string, signal?: AbortSignal): Promise<RunKpis> {
  const body = await send<Partial<RunKpis>>(`/runs/${encodeURIComponent(id)}/kpis`, {}, signal);
  if (!Array.isArray(body.resources)) {
    throw new SchedulingApiError(0, "COM-0001", "The response has no resources");
  }
  return body as RunKpis;
}

/** An order planned past its due date or holding an unplaced operation (the Late orders inquiry). */
export interface LateOrder {
  orderId: string;
  dueAt: string | null;
  plannedStart: string | null;
  plannedEnd: string | null;
  lateMinutes: number;
  lateOperations: number;
  unplacedOperations: number;
  operations: number;
  firmed: boolean;
  /** Pegged demand: orders pegged to consume this order's output. */
  dependentOrders: number;
}

export interface LateOrders {
  items: LateOrder[];
  truncated: boolean;
}

export async function fetchLateOrders(minLateMinutes: number, signal?: AbortSignal): Promise<LateOrders> {
  const body = await send<Partial<LateOrders>>(`/late-orders${query({ minLateMinutes: String(minLateMinutes), limit: "200" })}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new SchedulingApiError(0, "COM-0001", "The response has no items");
  }
  return { items: body.items, truncated: body.truncated === true };
}

/** Why an operation of a production order was not registered (or was left as it was). */
export const ORDER_IMPORT_SKIP_LABELS: Record<string, string> = {
  "no-process-job": "No process job",
  "too-long": "Process time too long",
  "setup-too-long": "Setup time too long",
  "unknown-resource": "Resource not in scheduling",
  "bad-hours": "Hours not a number",
  firmed: "Held by a firmed job",
};

export interface OrderImportSkipped {
  operNumber: number;
  code: string;
  detail: string;
}

/** A production order received from wave 21 and what became of its operations (FR-23.10). */
export interface OrderImport {
  orderId: string;
  orderNumber: string;
  orderVersion: number;
  reason: "schedule" | "redate";
  eventId: string;
  status: "imported" | "partial" | "rejected";
  operationsTotal: number;
  created: number;
  updated: number;
  unchanged: number;
  removed: number;
  keptFirmed: number;
  skipped: OrderImportSkipped[];
  replanRequired: boolean;
  orderStart: string | null;
  orderEnd: string | null;
  receivedAt: string;
}

export async function listOrderImports(status: string, signal?: AbortSignal): Promise<OrderImport[]> {
  const body = await send<{ items?: OrderImport[] }>(`/order-imports${query({ status, limit: "200" })}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new SchedulingApiError(0, "COM-0001", "The response has no items");
  }
  return body.items;
}

export function getOrderImport(orderId: string, signal?: AbortSignal): Promise<OrderImport> {
  return send<OrderImport>(`/order-imports/${encodeURIComponent(orderId)}`, {}, signal);
}

// Slice 2: resource networks, setup matrices, cleanout rules, capacity blocks
// and campaigns (FR-23.13 to FR-23.17). Decimals travel as strings.

export interface NetworkEdge {
  fromResourceId: string;
  toResourceId: string;
  transferMinutes: number;
  maxWaitMinutes: number | null;
  secondaryResourceId?: string;
}

export interface Network {
  id: string;
  code: string;
  name: string;
  activeFrom: string;
  activeTo: string | null;
  edges: NetworkEdge[];
  version: number;
  createdAt: string;
}

export interface NewNetwork {
  code: string;
  name: string;
  activeFrom: string;
  activeTo?: string;
  edges: NetworkEdge[];
}

export interface SetupEntry {
  fromGroup: string;
  toGroup: string;
  minutes: number;
  cost: string;
  cleaningSetId?: string;
}

export interface SetupMatrix {
  id: string;
  resourceId: string;
  version: number;
  activeFrom: string;
  currency: string;
  entries: SetupEntry[];
  createdAt: string;
}

export interface NewSetupMatrix {
  resourceId: string;
  activeFrom: string;
  currency: string;
  entries: SetupEntry[];
}

export interface CleanoutRule {
  id: string;
  setupGroup: string;
  durationMinutes: number;
  operationSetId?: string;
  afterBatches: number | null;
  afterHours: number | null;
  atCampaignEnd: boolean;
  version: number;
  createdAt: string;
}

export interface NewCleanoutRule {
  setupGroup: string;
  durationMinutes: number;
  operationSetId?: string;
  afterBatches?: number;
  afterHours?: number;
  atCampaignEnd: boolean;
}

export const PREDICATE_OPS = ["=", "!=", ">=", "<=", ">", "<"] as const;
export type PredicateOp = (typeof PREDICATE_OPS)[number];

export interface Predicate {
  attribute: string;
  op: PredicateOp;
  value: string;
}

/** A recurring block window: an RRULE subset from a local start date (FR-23.17). */
export interface BlockRecurrence {
  /** FREQ=DAILY or WEEKLY, with INTERVAL, BYDAY (weekly) and COUNT or UNTIL=YYYYMMDD. */
  rule: string;
  /** First local date, YYYY-MM-DD. */
  startDate: string;
  startMinute: number;
  endMinute: number;
}

export interface CapacityBlock {
  id: string;
  resourceId: string;
  code: string;
  windows?: Shift[];
  startAt?: string;
  endAt?: string;
  recurrence?: BlockRecurrence;
  condition: Predicate[];
  priority: number;
  active: boolean;
  version: number;
  createdAt: string;
}

export interface NewCapacityBlock {
  resourceId: string;
  code: string;
  windows?: Shift[];
  startAt?: string;
  endAt?: string;
  recurrence?: BlockRecurrence;
  condition: Predicate[];
  priority: number;
  active: boolean;
}

/**
 * One window of an active capacity block inside an inquiry range (FR-23.17,
 * PRD 4.23.14 Block utilization). blockMinutes are the open minutes the window
 * holds after higher-priority blocks; matchedMinutes are booked by jobs that
 * match the block condition; unusedMinutes are the rest.
 */
export interface BlockWindow {
  blockId: string;
  code: string;
  priority: number;
  condition: Predicate[];
  start: string;
  end: string;
  blockMinutes: number;
  matchedMinutes: number;
  unusedMinutes: number;
}

export const CAMPAIGN_STATUSES = ["proposed", "planned", "started", "completed", "cancelled"] as const;
export type CampaignStatus = (typeof CAMPAIGN_STATUSES)[number];
export const SEQUENCE_RULES = ["nearest-neighbor", "manual", "cp-sat"] as const;
export type SequenceRule = (typeof SEQUENCE_RULES)[number];
export const SEQUENCE_RULE_LABELS: Record<SequenceRule, string> = {
  "nearest-neighbor": "Nearest neighbour",
  manual: "Manual",
  "cp-sat": "CP-SAT optimizer",
};

/** Batches and run minutes since the resource's last cleanout (BR-23.13). */
export interface CleanoutCounter {
  batches: number;
  minutes: number;
}

export interface CampaignOrder {
  orderId: string;
  setupGroup: string;
  quantity: string;
  runMinutes: number;
  routeSetupMinutes: number;
  dueAt?: string;
  position: number;
  sequence: number | null;
}

export interface NewCampaignOrder {
  orderId: string;
  setupGroup: string;
  quantity: string;
  runMinutes: number;
  routeSetupMinutes: number;
  dueAt?: string;
}

export interface Changeover {
  beforeOrderId?: string;
  afterOrderId: string;
  fromGroup: string;
  toGroup: string;
  minutes: number;
  cost: string;
  cleaningSetId?: string;
  missing: boolean;
}

export interface PlannedCleanout {
  afterOrderId: string;
  setupGroup: string;
  minutes: number;
  reason: "batches" | "run-time" | "campaign-end";
  cleaningSetId?: string;
}

export interface CampaignPlan {
  changeovers: Changeover[];
  cleanouts: PlannedCleanout[];
  warnings: { code: string; detail: string }[];
  matrixId?: string;
  /** Absent on plans stored before slice 6. */
  carryIn?: CleanoutCounter;
  carryOut?: CleanoutCounter;
  /** cp-sat campaigns only: scheduler-opt's status (SCHED-0006). */
  solverStatus?: string;
  optimized?: boolean;
}

export interface Campaign {
  id: string;
  resourceId: string;
  campaignKey: string;
  status: CampaignStatus;
  plannedStart: string;
  maxOrders: number | null;
  maxQty: string | null;
  maxMinutes: number | null;
  sequenceRule: SequenceRule;
  currentSetupGroup: string | null;
  orderCount: number;
  runMinutes: number;
  changeoverMinutes: number;
  cleanoutMinutes: number;
  plan: CampaignPlan | null;
  sequencedAt: string | null;
  startedAt: string | null;
  completedAt?: string | null;
  cancelledAt?: string | null;
  cancelReason?: string | null;
  version: number;
  createdAt: string;
  orders?: CampaignOrder[];
}

export interface NewCampaign {
  resourceId: string;
  campaignKey: string;
  plannedStart: string;
  sequenceRule: SequenceRule;
  currentSetupGroup?: string;
  maxOrders?: number;
  maxQty?: string;
  maxMinutes?: number;
  orders: NewCampaignOrder[];
}

/** One scheduled order a campaign proposal groups (FR-23.14). */
export interface ProposedCampaignOrder {
  orderId: string;
  operationId: string;
  operNumber: number;
  resourceId: string;
  setupGroup: string;
  runMinutes: number;
  routeSetupMinutes: number;
  start: string;
  dueAt?: string;
  firmed: boolean;
}

/** Orders sharing a campaign key on one resource within the window. Nothing is stored until a campaign is created from it. */
export interface ProposedCampaign {
  campaignKey: string;
  resourceId: string;
  plannedStart: string;
  runMinutes: number;
  orders: ProposedCampaignOrder[];
}

export interface CampaignProposals {
  from: string;
  to: string;
  windowDays: number;
  items: ProposedCampaign[];
}

export const PROPOSAL_WINDOWS = [7, 14, 30, 60, 90] as const;

export const ALLOCATION_BASES = ["quantity", "equal", "run-minutes"] as const;
export type AllocationBasis = (typeof ALLOCATION_BASES)[number];

export interface AllocationRequest {
  amount: string;
  currency: string;
  basis: AllocationBasis;
  actuals?: { orderId: string; quantity?: string; runMinutes?: number }[];
}

export interface Allocation {
  campaignId: string;
  basis: AllocationBasis;
  currency: string;
  amount: string;
  lines: { orderId: string; weight: string; amount: string }[];
}

export function listNetworks(signal?: AbortSignal): Promise<Network[]> {
  return items<Network>("/networks", signal);
}

export function getNetwork(id: string, signal?: AbortSignal): Promise<Network> {
  return send<Network>(`/networks/${encodeURIComponent(id)}`, {}, signal);
}

export function createNetwork(input: NewNetwork, key: string = newKey()): Promise<Network> {
  return post<Network>("/networks", input, key);
}

export function listSetupMatrices(resourceId = "", signal?: AbortSignal): Promise<SetupMatrix[]> {
  return items<SetupMatrix>(`/setup-matrices${query({ resourceId })}`, signal);
}

export function getSetupMatrix(id: string, signal?: AbortSignal): Promise<SetupMatrix> {
  return send<SetupMatrix>(`/setup-matrices/${encodeURIComponent(id)}`, {}, signal);
}

export function createSetupMatrix(input: NewSetupMatrix, key: string = newKey()): Promise<SetupMatrix> {
  return post<SetupMatrix>("/setup-matrices", input, key);
}

export function listCleanoutRules(signal?: AbortSignal): Promise<CleanoutRule[]> {
  return items<CleanoutRule>("/cleanout-rules", signal);
}

export function createCleanoutRule(input: NewCleanoutRule, key: string = newKey()): Promise<CleanoutRule> {
  return post<CleanoutRule>("/cleanout-rules", input, key);
}

export function listCapacityBlocks(resourceId = "", signal?: AbortSignal): Promise<CapacityBlock[]> {
  return items<CapacityBlock>(`/capacity-blocks${query({ resourceId })}`, signal);
}

export function getCapacityBlock(id: string, signal?: AbortSignal): Promise<CapacityBlock> {
  return send<CapacityBlock>(`/capacity-blocks/${encodeURIComponent(id)}`, {}, signal);
}

export interface CapacityBlockPatch {
  priority?: number;
  active?: boolean;
  condition?: Predicate[];
}

/** The updated block with the booked jobs the resource's active blocks now keep out (BR-23.15). */
export interface CapacityBlockUpdate extends CapacityBlock {
  affectedJobs: AffectedJob[];
}

/** Changes a block under the version the planner saw (If-Match); runs and moves after it use the new values. */
export function updateCapacityBlock(id: string, version: number, patch: CapacityBlockPatch, key: string = newKey()): Promise<CapacityBlockUpdate> {
  return send<CapacityBlockUpdate>(`/capacity-blocks/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Idempotency-Key": key, "If-Match": `"${version}"` },
    body: JSON.stringify(patch),
  });
}

export interface NetworkPatch {
  name?: string;
  activeTo?: string;
}

/** Renames a network or ends it on a date, under the version the planner saw (If-Match). */
export function updateNetwork(id: string, version: number, patch: NetworkPatch, key: string = newKey()): Promise<Network> {
  return send<Network>(`/networks/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Idempotency-Key": key, "If-Match": `"${version}"` },
    body: JSON.stringify(patch),
  });
}

export function getBlockUtilization(resourceId: string, from: string, to: string, signal?: AbortSignal): Promise<BlockWindow[]> {
  return items<BlockWindow>(`/resources/${encodeURIComponent(resourceId)}/block-utilization${query({ from, to })}`, signal);
}

export function createCapacityBlock(input: NewCapacityBlock, key: string = newKey()): Promise<CapacityBlock> {
  return post<CapacityBlock>("/capacity-blocks", input, key);
}

export function listCampaigns(filter: { status?: CampaignStatus | ""; resourceId?: string } = {}, signal?: AbortSignal): Promise<Campaign[]> {
  return items<Campaign>(`/campaigns${query({ status: filter.status, resourceId: filter.resourceId })}`, signal);
}

export function getCampaign(id: string, signal?: AbortSignal): Promise<Campaign> {
  return send<Campaign>(`/campaigns/${encodeURIComponent(id)}`, {}, signal);
}

export function createCampaign(input: NewCampaign, key: string = newKey()): Promise<Campaign> {
  return post<Campaign>("/campaigns", input, key);
}

export async function proposeCampaigns(
  filter: { from?: string; windowDays?: number; resourceId?: string } = {},
  signal?: AbortSignal,
): Promise<CampaignProposals> {
  const body = await send<CampaignProposals>(
    `/campaign-proposals${query({ from: filter.from, windowDays: filter.windowDays, resourceId: filter.resourceId })}`,
    {},
    signal,
  );
  if (!Array.isArray(body.items)) {
    throw new SchedulingApiError(0, "COM-0001", "The response has no items");
  }
  return body;
}

/** A campaign command carries the version the planner saw (If-Match) and an Idempotency-Key. */
function campaignCommand(id: string, suffix: string, method: "POST" | "DELETE", version: number, body: unknown, key: string): Promise<Campaign> {
  return send<Campaign>(`/campaigns/${encodeURIComponent(id)}${suffix}`, {
    method,
    headers: { "Idempotency-Key": key, "If-Match": `"${version}"` },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

export function addCampaignOrder(id: string, version: number, order: NewCampaignOrder, key: string = newKey()): Promise<Campaign> {
  return campaignCommand(id, "/orders", "POST", version, order, key);
}

export function removeCampaignOrder(id: string, version: number, orderId: string, key: string = newKey()): Promise<Campaign> {
  return campaignCommand(id, `/orders/${encodeURIComponent(orderId)}`, "DELETE", version, undefined, key);
}

export function sequenceCampaign(id: string, version: number, key: string = newKey()): Promise<Campaign> {
  return campaignCommand(id, ":sequence", "POST", version, {}, key);
}

export function startCampaign(id: string, version: number, key: string = newKey()): Promise<Campaign> {
  return campaignCommand(id, ":start", "POST", version, {}, key);
}

/** Finishes a started campaign; its orders stay on it. */
export function completeCampaign(id: string, version: number, key: string = newKey()): Promise<Campaign> {
  return campaignCommand(id, ":complete", "POST", version, {}, key);
}

/** Cancels a campaign that is not finished; its orders are released for another campaign. */
export function cancelCampaign(id: string, version: number, reason?: string, key: string = newKey()): Promise<Campaign> {
  return campaignCommand(id, ":cancel", "POST", version, reason ? { reason } : {}, key);
}

/** Computes the split of a campaign's setup and cleanout cost; nothing is posted. */
export function allocateCampaignCost(id: string, input: AllocationRequest): Promise<Allocation> {
  return post<Allocation>(`/campaigns/${encodeURIComponent(id)}:allocate-cost`, input);
}

// Slice 4: order pegs that moves propagate along (FR-23.8).

/** The dependent order consumes the parent order's output, so it cannot start before the parent ends. */
export interface OrderPeg {
  id: string;
  parentOrderId: string;
  dependentOrderId: string;
  createdAt: string;
}

export interface NewOrderPeg {
  parentOrderId: string;
  dependentOrderId: string;
}

/** Pegs, or those where orderId is the parent or the dependent. */
export function listPegs(orderId = "", signal?: AbortSignal): Promise<OrderPeg[]> {
  return items<OrderPeg>(`/order-pegs${query({ orderId })}`, signal);
}

export function createPeg(input: NewOrderPeg, key: string = newKey()): Promise<OrderPeg> {
  return post<OrderPeg>("/order-pegs", input, key);
}

/** Removes a peg; the service answers 204 with no body. */
export async function deletePeg(id: string): Promise<void> {
  const res = await fetch(`${base}/order-pegs/${encodeURIComponent(id)}`, {
    method: "DELETE",
    headers: { Accept: "application/json" },
    credentials: "same-origin",
  });
  if (!res.ok) {
    throw await toError(res);
  }
}

// Slice 9: capacity overrides, downtimes and extra shifts (FR-23.1, AC-23.7)

export type OverrideKind = "downtime" | "extra";

export interface CapacityOverride {
  id: string;
  resourceId: string;
  kind: OverrideKind;
  start: string;
  end: string;
  reason: string;
  createdAt: string;
}

export interface NewCapacityOverride {
  kind: OverrideKind;
  start: string;
  end: string;
  reason?: string;
}

/** A booked job a downtime overlaps. */
export interface AffectedJob {
  jobId: string;
  orderId: string;
  operNumber: number;
  start: string;
  end: string;
  firmed: boolean;
}

export interface CapacityOverrideResult extends CapacityOverride {
  affectedJobs: AffectedJob[];
}

export function listCapacityOverrides(resourceId: string, signal?: AbortSignal): Promise<CapacityOverride[]> {
  return items<CapacityOverride>(`/resources/${encodeURIComponent(resourceId)}/capacity-overrides`, signal);
}

export function createCapacityOverride(resourceId: string, input: NewCapacityOverride, key: string = newKey()): Promise<CapacityOverrideResult> {
  return post<CapacityOverrideResult>(`/resources/${encodeURIComponent(resourceId)}/capacity-overrides`, input, key);
}

/** Removes an override; the service answers 204 with no body. */
export async function deleteCapacityOverride(resourceId: string, id: string): Promise<void> {
  const res = await fetch(`${base}/resources/${encodeURIComponent(resourceId)}/capacity-overrides/${encodeURIComponent(id)}`, {
    method: "DELETE",
    headers: { Accept: "application/json" },
    credentials: "same-origin",
  });
  if (!res.ok) {
    throw await toError(res);
  }
}
