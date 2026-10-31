import { z } from "zod";

/** Base path of the planning service through the web BFF (see services/planning/api/openapi.yaml). */
export const PLANNING_API = "/api/v1/planning";

export const OrderType = z.enum(["purchase", "production", "transfer"]);
export const OrderStatus = z.enum(["unprocessed", "approved", "firmed", "deleted"]);
export const MessageAction = z.enum(["delay", "postpone", "advance", "shortage", "cancel", "decrease", "increase"]);
export const TargetType = z.enum(["purchase-order", "production-order", "transfer-order"]);

export const PlannedOrder = z.object({
  id: z.string(),
  versionId: z.string(),
  number: z.string(),
  itemId: z.string(),
  orderType: OrderType,
  qty: z.string(),
  orderDate: z.string(),
  deliveryDate: z.string(),
  futuresDate: z.string().nullish(),
  delayDays: z.number().int(),
  lotRule: z.string(),
  status: OrderStatus,
  /** The order's ETag: send it back as If-Match on PATCH and DELETE. */
  version: z.number().int().min(1),
  vendorId: z.string().nullish(),
  /** Set once the order is firmed: the purchase, production or transfer order it became (FR-22.10). */
  firmedTargetType: TargetType.nullish(),
  firmedTargetId: z.string().nullish(),
});
export type PlannedOrder = z.infer<typeof PlannedOrder>;

/** Only unprocessed and approved orders of the current version can be changed (FR-22.11). */
export const isOpen = (o: Pick<PlannedOrder, "status">) => o.status === "unprocessed" || o.status === "approved";

export const Message = z.object({
  action: MessageAction,
  itemId: z.string(),
  supplyRef: z.string().nullish(),
  demandRef: z.string().nullish(),
  suggestedDate: z.string().nullish(),
  delayDays: z.number().int(),
  qty: z.string(),
});
export type Message = z.infer<typeof Message>;

/** Response of GET /planned-orders/{id}/pegging: the explain tree of one order. */
export interface PegNode {
  ref: string;
  refType: string;
  qty: string;
  children: PegNode[];
}
export const PegNode: z.ZodType<PegNode> = z.lazy(() =>
  z.object({
    ref: z.string(),
    refType: z.string(),
    qty: z.string(),
    children: z.array(PegNode),
  }),
);

export const Pegging = z.object({
  orderId: z.string(),
  number: z.string(),
  qty: z.string(),
  tree: PegNode,
});
export type Pegging = z.infer<typeof Pegging>;

export const PlanVersion = z.object({
  id: z.string(),
  planCode: z.string(),
  number: z.number().int(),
  status: z.enum(["running", "complete", "failed", "current"]),
  /** A simulation version comes from a scenario run, is never current and cannot be firmed (FR-22.15). */
  kind: z.enum(["static", "simulation"]).optional(),
  scenarioCode: z.string().nullish(),
  asOf: z.string(),
  itemsPlanned: z.number().int(),
  plannedOrders: z.number().int(),
  /** Orders approved in the previous version that this run firmed (FR-22.11). */
  approvedFirmed: z.number().int().optional(),
  messages: z.number().int(),
  durationMs: z.number(),
  planHash: z.string(),
});
export type PlanVersion = z.infer<typeof PlanVersion>;

/**
 * Site work calendar of a run (FR-22.5, BR-22.8): lead times and margins then
 * count working days. Weekdays are 0 (Sunday) to 6 (Saturday); one must stay open.
 */
export interface WorkCalendar {
  closedWeekdays?: number[];
  closedDates?: string[];
}

/** The engine snapshot a plan run takes (RunRequest in the OpenAPI). */
export interface RunPlanRequest {
  asOf: string;
  siteId: string;
  /** Forecast model whose reduced cells at the site are netted as forecast demand. */
  forecastModel?: string;
  /** Without a calendar the run counts calendar days. */
  calendar?: WorkCalendar;
  /** Adds the plan's undelivered firmed orders as scheduled receipts (FR-22.1). */
  carryForward?: boolean;
  /** Demand dated beyond this many days from asOf is ignored (30 to 1095); omitted takes the service default. */
  horizonDays?: number;
  /** Restricts the items the version reports (FR-22.13). */
  scope?: PlanScope;
  items: unknown[];
}

/** Plan scope filter (FR-22.13): every given criterion must match; the whole snapshot is still planned. */
export interface PlanScope {
  itemIds?: string[];
  itemGroups?: string[];
  planners?: string[];
  /** Low-level code range, inclusive; 0 is an end item. */
  llcMin?: number;
  llcMax?: number;
}

/** One day of the MD04-style net requirements view of an item (FR-22.12). Quantities are decimal strings. */
export const NetRequirement = z.object({
  itemId: z.string(),
  date: z.string(),
  grossRequirements: z.string(),
  scheduledReceipts: z.string(),
  plannedReceipts: z.string(),
  /** PAB(t) = PAB(t-1) + scheduled + planned receipts - gross requirements. */
  projectedAvailableBalance: z.string(),
  netRequirement: z.string(),
});
export type NetRequirement = z.infer<typeof NetRequirement>;

export const ReductionPrinciple = z.enum(["none", "percent", "transactions", "transactions-dynamic"]);
export type ReductionPrinciple = z.infer<typeof ReductionPrinciple>;

/** Forecast consensus status (FR-22.9): only a draft accepts cell writes. */
export const ConsensusStatus = z.enum(["draft", "submitted", "published"]);
export type ConsensusStatus = z.infer<typeof ConsensusStatus>;

export const ForecastModel = z.object({
  code: z.string(),
  /** The model's ETag; a model that does not exist yet is version 0. */
  version: z.number().int().min(0),
  reductionPrinciple: ReductionPrinciple,
  reductionPercent: z.string(),
  status: ConsensusStatus,
  /** Who submitted the consensus; null while the model is a draft. */
  submittedBy: z.string().nullish(),
  /** Who last published or rejected it. */
  decidedBy: z.string().nullish(),
  publishedAt: z.string().nullish(),
});
export type ForecastModel = z.infer<typeof ForecastModel>;

export const ForecastLine = z.object({
  itemId: z.string(),
  siteId: z.string(),
  periodStart: z.string(),
  periodEnd: z.string(),
  source: z.enum(["statistical", "manual", "consensus"]),
  qty: z.string(),
  salesQty: z.string(),
  reducedQty: z.string(),
});
export type ForecastLine = z.infer<typeof ForecastLine>;

const ForecastLines = z.object({ model: ForecastModel, items: z.array(ForecastLine) });
export type ForecastLines = z.infer<typeof ForecastLines>;

/** Error raised for any non-2xx response; `code` is the stable problem code (e.g. PLAN-0001). */
export class PlanningApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "PlanningApiError";
  }
}

async function send(path: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(`${PLANNING_API}${path}`, {
    credentials: "same-origin",
    ...init,
    headers: { Accept: "application/json", ...(init?.headers ?? {}) },
  });
  if (!res.ok) {
    let code = "PLAN-UNKNOWN";
    let detail = res.statusText;
    try {
      const problem = (await res.json()) as { code?: string; detail?: string };
      code = problem.code ?? code;
      detail = problem.detail ?? detail;
    } catch {
      // non-JSON error body: keep the defaults
    }
    throw new PlanningApiError(res.status, code, detail);
  }
  return res;
}

export async function request<T>(path: string, schema: z.ZodType<T>, init?: RequestInit): Promise<T> {
  const res = await send(path, init);
  return schema.parse(await res.json());
}

const ifMatch = (version: number) => `"${version}"`;

export interface OrderQuery {
  planCode: string;
  status?: z.infer<typeof OrderStatus>;
  itemId?: string;
  /** A version number, e.g. a simulation; without it the current version is read. */
  version?: number;
  limit?: number;
}

export async function listPlannedOrders(q: OrderQuery, init?: RequestInit): Promise<PlannedOrder[]> {
  const params = new URLSearchParams({ planCode: q.planCode });
  if (q.status) params.set("status", q.status);
  if (q.itemId) params.set("itemId", q.itemId);
  if (q.version) params.set("version", String(q.version));
  if (q.limit) params.set("limit", String(q.limit));
  const body = await request(
    `/planned-orders?${params.toString()}`,
    z.object({ items: z.array(PlannedOrder) }),
    init,
  );
  return body.items;
}

export async function listMessages(
  planCode: string,
  action?: z.infer<typeof MessageAction>,
  init?: RequestInit,
): Promise<Message[]> {
  const params = new URLSearchParams({ planCode });
  if (action) params.set("action", action);
  const body = await request(`/messages?${params.toString()}`, z.object({ items: z.array(Message) }), init);
  return body.items;
}

/** Net requirements of one item in the current version of a plan, in date order (empty when the plan did not plan it). */
export async function listNetRequirements(planCode: string, itemId: string, init?: RequestInit): Promise<NetRequirement[]> {
  const params = new URLSearchParams({ planCode, itemId });
  const body = await request(`/net-requirements?${params.toString()}`, z.object({ items: z.array(NetRequirement) }), init);
  return body.items;
}

export async function getPlannedOrder(id: string, init?: RequestInit): Promise<PlannedOrder> {
  return request(`/planned-orders/${encodeURIComponent(id)}`, PlannedOrder, init);
}

export async function getPegging(orderId: string, depth = 20, init?: RequestInit): Promise<Pegging> {
  return request(`/planned-orders/${encodeURIComponent(orderId)}/pegging?depth=${depth}`, Pegging, init);
}

export async function getVersion(planCode: string, number: number): Promise<PlanVersion> {
  return request(`/plans/${encodeURIComponent(planCode)}/versions/${number}`, PlanVersion);
}

/** Plan run history, newest version first. */
export async function listVersions(planCode: string, limit = 50, init?: RequestInit): Promise<PlanVersion[]> {
  const body = await request(
    `/plans/${encodeURIComponent(planCode)}/versions?limit=${limit}`,
    z.object({ items: z.array(PlanVersion) }),
    init,
  );
  return body.items;
}

export const DiffOrder = z.object({
  number: z.string(),
  itemId: z.string(),
  orderType: OrderType,
  qty: z.string(),
  orderDate: z.string(),
  deliveryDate: z.string(),
  vendorId: z.string().nullish(),
});
export type DiffOrder = z.infer<typeof DiffOrder>;

/**
 * Response of GET /plans/{code}/diff: planned-order differences between two versions (FR-22.14).
 * The counts cover every order; the lists stop at the request's limit (`truncated`).
 */
export const VersionDiff = z.object({
  from: PlanVersion,
  to: PlanVersion,
  identical: z.boolean(),
  messagesDelta: z.number().int(),
  addedCount: z.number().int(),
  removedCount: z.number().int(),
  changedCount: z.number().int(),
  truncated: z.boolean(),
  added: z.array(DiffOrder),
  removed: z.array(DiffOrder),
  changed: z.array(z.object({ from: DiffOrder, to: DiffOrder, qtyDelta: z.string() })),
  unchanged: z.number().int(),
  qtyDeltaByOrderType: z.record(z.string(), z.string()),
});
export type VersionDiff = z.infer<typeof VersionDiff>;

/** Compares version `from` with the later version `to` of a plan, listing at most `limit` orders per list. */
export async function diffVersions(planCode: string, from: number, to: number, limit = 500, init?: RequestInit): Promise<VersionDiff> {
  return request(`/plans/${encodeURIComponent(planCode)}/diff?from=${from}&to=${to}&limit=${limit}`, VersionDiff, init);
}

/** Requests a static plan run. The Idempotency-Key makes retries safe (409 when a run is already in progress). */
export async function runPlan(planCode: string, idempotencyKey: string, body: RunPlanRequest): Promise<PlanVersion> {
  return request(`/plans/${encodeURIComponent(planCode)}:run`, PlanVersion, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

/** A planner's change to a planned order; absent fields stay as they are. */
export interface PlannedOrderPatch {
  qty?: string;
  orderDate?: string;
  deliveryDate?: string;
  vendorId?: string;
  /** Approves the order; firming is a separate command. */
  status?: "approved";
}

/**
 * Changes or approves an open planned order under If-Match (FR-22.11). A stale
 * version fails with 412 `conflict_version`; a firmed, deleted or superseded
 * order with 409 `PLAN-ORDER-NOT-EDITABLE`.
 */
export async function updatePlannedOrder(id: string, version: number, patch: PlannedOrderPatch): Promise<PlannedOrder> {
  return request(`/planned-orders/${encodeURIComponent(id)}`, PlannedOrder, {
    method: "PATCH",
    headers: { "Content-Type": "application/json", "If-Match": ifMatch(version) },
    body: JSON.stringify(patch),
  });
}

/** Deletes an open planned order under If-Match; the service answers 204. */
export async function deletePlannedOrder(id: string, version: number): Promise<void> {
  await send(`/planned-orders/${encodeURIComponent(id)}`, { method: "DELETE", headers: { "If-Match": ifMatch(version) } });
}

/** How planned purchase orders of one site and vendor merge into purchase orders when firmed (4.22.13). */
export const FirmGrouping = z.enum(["vendor-date", "vendor-week", "vendor", "none"]);
export type FirmGrouping = z.infer<typeof FirmGrouping>;

/** The service accepts at most this many planned orders in one firming (4.22.5). */
export const MAX_FIRM_BATCH = 1000;

export const FirmedGroup = z.object({
  targetType: TargetType,
  targetId: z.string(),
  orderType: OrderType,
  siteId: z.string(),
  vendorId: z.string().nullish(),
  deliveryDate: z.string(),
  qty: z.string(),
  plannedOrderIds: z.array(z.string()),
});
export type FirmedGroup = z.infer<typeof FirmedGroup>;

export const FirmBatch = z.object({
  id: z.string(),
  grouping: FirmGrouping,
  firmed: z.number().int(),
  groups: z.array(FirmedGroup),
});
export type FirmBatch = z.infer<typeof FirmBatch>;

/**
 * Firms planned orders of the current version into purchase, production and
 * transfer orders (FR-22.10). All or none are firmed. Send the same
 * Idempotency-Key when retrying the same firming; 409 `PLAN-0002` means an
 * order is already firmed, 409 `PLAN-ORDER-NOT-EDITABLE` that one is deleted or
 * superseded.
 */
export async function firmPlannedOrders(plannedOrderIds: string[], grouping: FirmGrouping, idempotencyKey: string): Promise<FirmBatch> {
  return request(`/planned-orders:firm`, FirmBatch, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify({ plannedOrderIds, grouping }),
  });
}

export interface ForecastQuery {
  itemId?: string;
  siteId?: string;
  limit?: number;
}

/** A model's forecast cells with their reduced quantities (404 PLAN-FORECAST-NOT-FOUND before its first write). */
export async function getForecastLines(modelCode: string, q: ForecastQuery = {}, init?: RequestInit): Promise<ForecastLines> {
  const params = new URLSearchParams();
  if (q.itemId) params.set("itemId", q.itemId);
  if (q.siteId) params.set("siteId", q.siteId);
  if (q.limit) params.set("limit", String(q.limit));
  const query = params.toString();
  return request(`/forecasts/${encodeURIComponent(modelCode)}/lines${query ? `?${query}` : ""}`, ForecastLines, init);
}

export interface ForecastCellWrite {
  itemId: string;
  siteId: string;
  periodStart: string;
  periodEnd: string;
  qty: string;
  salesQty?: string;
}

export interface ForecastWrite {
  reductionPrinciple?: ReductionPrinciple;
  reductionPercent?: string;
  lines: ForecastCellWrite[];
}

/**
 * Upserts forecast cells under If-Match on the model (version 0 creates it) and
 * returns the model with its new version. Cells written here are manual overrides.
 */
export async function putForecastLines(modelCode: string, version: number, body: ForecastWrite): Promise<ForecastModel> {
  const out = await request(
    `/forecasts/${encodeURIComponent(modelCode)}/lines`,
    z.object({ model: ForecastModel, updated: z.number().int() }),
    {
      method: "PUT",
      headers: { "Content-Type": "application/json", "If-Match": ifMatch(version) },
      body: JSON.stringify(body),
    },
  );
  return out.model;
}

export const ForecastPublication = z.object({
  items: z.number().int(),
  horizonPeriods: z.number().int(),
  periodFrom: z.string(),
  periodTo: z.string(),
});
export type ForecastPublication = z.infer<typeof ForecastPublication>;

const ConsensusResult = z.object({ model: ForecastModel, publication: ForecastPublication.nullish() });
export type ConsensusResult = z.infer<typeof ConsensusResult>;

/** The consensus steps of FR-22.9 and the route of each. */
export type ConsensusStep = "submit" | "publish" | "reject";

/**
 * Moves a forecast model through the consensus workflow under If-Match on its
 * version. `submit` (Maintain forecasts) locks a draft for approval;
 * `publish` and `reject` (Approve forecast consensus) decide it. 403
 * `PLAN-CONSENSUS-SOD` means the user submitted or edited the model and must
 * leave the decision to someone else; 409 `PLAN-CONSENSUS-STATE` that the
 * model is not in the status the step needs.
 */
export async function forecastConsensus(modelCode: string, step: ConsensusStep, version: number): Promise<ConsensusResult> {
  return request(`/forecasts/${encodeURIComponent(modelCode)}:${step}`, ConsensusResult, {
    method: "POST",
    headers: { "If-Match": ifMatch(version) },
  });
}

/** Coverage codes of FR-22.2: lot-for-lot, period grouping, min/max reorder, or messages only. */
export const CoverageCode = z.enum(["requirement", "period", "minmax", "manual"]);
export type CoverageCode = z.infer<typeof CoverageCode>;

/** Safety-stock policies of BR-22.5: demand at today, or at the first issue date. */
export const SafetyStockPolicy = z.enum(["today", "first-issue"]);
export type SafetyStockPolicy = z.infer<typeof SafetyStockPolicy>;

/** The settings of a coverage group, as a create or update sends them (CoverageSettings in the OpenAPI). */
export const CoverageSettings = z.object({
  coverageCode: CoverageCode,
  coverageTimeFenceDays: z.number().int(),
  negativeDays: z.number().int(),
  positiveDays: z.number().int(),
  periodDays: z.number().int(),
  freezeFenceDays: z.number().int(),
  actionFenceDays: z.number().int(),
  futuresFenceDays: z.number().int(),
  firmingFenceDays: z.number().int(),
  /** Firm planned orders inside the firming fence during the run (BR-22.4). */
  autoFirm: z.boolean(),
  generateActions: z.boolean(),
  generateFutures: z.boolean(),
  safetyStockPolicy: SafetyStockPolicy,
});
export type CoverageSettings = z.infer<typeof CoverageSettings>;

export const CoverageGroup = CoverageSettings.extend({
  id: z.string(),
  code: z.string(),
  /** The group's ETag: send it back as If-Match on PUT. */
  version: z.number().int().min(1),
});
export type CoverageGroup = z.infer<typeof CoverageGroup>;

/** The FR-22.2 defaults the service applies to omitted fields. */
export const COVERAGE_DEFAULTS: CoverageSettings = {
  coverageCode: "requirement",
  coverageTimeFenceDays: 100,
  negativeDays: 1,
  positiveDays: 0,
  periodDays: 0,
  freezeFenceDays: 0,
  actionFenceDays: 0,
  futuresFenceDays: 0,
  firmingFenceDays: 0,
  autoFirm: false,
  generateActions: true,
  generateFutures: true,
  safetyStockPolicy: "today",
};

export async function listCoverageGroups(init?: RequestInit): Promise<CoverageGroup[]> {
  const out = await request(`/coverage-groups?limit=500`, z.object({ items: z.array(CoverageGroup) }), init);
  return out.items;
}

export async function getCoverageGroup(code: string, init?: RequestInit): Promise<CoverageGroup> {
  return request(`/coverage-groups/${encodeURIComponent(code)}`, CoverageGroup, init);
}

/**
 * Creates a coverage group (Maintain planning setup). The Idempotency-Key makes
 * a retry return the first result; 409 `PLAN-COVERAGE-EXISTS` means the code is taken.
 */
export async function createCoverageGroup(code: string, settings: CoverageSettings, idempotencyKey: string): Promise<CoverageGroup> {
  return request(`/coverage-groups`, CoverageGroup, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify({ code, ...settings }),
  });
}

/** Replaces the settings of a coverage group under If-Match; 412 means someone else changed it. */
export async function updateCoverageGroup(code: string, version: number, settings: CoverageSettings): Promise<CoverageGroup> {
  return request(`/coverage-groups/${encodeURIComponent(code)}`, CoverageGroup, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "If-Match": ifMatch(version) },
    body: JSON.stringify(settings),
  });
}

/** The order a planned order becomes when firmed (FR-22.3). */
export type OrderType = z.infer<typeof OrderType>;

/** Planning strategies of FR-22.19 (SAP strategy groups 10, 11, 20, 40, 50/52, 70). */
export const PlanningStrategy = z.enum(["mts-net", "mts-gross", "mto", "assemble-to-order-final", "plan-without-final-assembly", "subassembly"]);
export type PlanningStrategy = z.infer<typeof PlanningStrategy>;

/** Where an item is planned (FR-22.18): the site, a warehouse of its own (MRP area) or a subcontractor. */
export const PlanningScope = z.enum(["site", "warehouse", "subcontractor"]);
export type PlanningScope = z.infer<typeof PlanningScope>;

/** The fields of an item coverage a create or update sends (ItemCoverageInput in the OpenAPI); null clears an optional field. */
export interface ItemCoverageInput {
  itemId: string;
  siteId: string;
  coverageGroupId: string;
  warehouseId: string | null;
  vendorId: string | null;
  bufferProfileId: string | null;
  minQty: string;
  maxQty: string;
  safetyMarginReceiptDays: number;
  safetyMarginIssueDays: number;
  safetyMarginReorderDays: number;
  leadTimeDays: number | null;
  plannedOrderType: z.infer<typeof OrderType>;
  planningStrategy: PlanningStrategy;
  planningScope: PlanningScope;
  decouplingPoint: boolean;
  requiredShelfLifeDays: number;
  priorityBased: boolean;
}

/** How one item is planned at a site, warehouse or subcontractor (data model ItemCoverage, FR-22.16..FR-22.20). */
export const ItemCoverage = z.object({
  id: z.string(),
  /** The coverage's ETag: send it back as If-Match on PUT. */
  version: z.number().int().min(1),
  itemId: z.string(),
  siteId: z.string(),
  warehouseId: z.string().nullable(),
  coverageGroupId: z.string(),
  coverageGroupCode: z.string(),
  minQty: z.string(),
  maxQty: z.string(),
  safetyMarginReceiptDays: z.number().int(),
  safetyMarginIssueDays: z.number().int(),
  safetyMarginReorderDays: z.number().int(),
  leadTimeDays: z.number().int().nullable(),
  vendorId: z.string().nullable(),
  plannedOrderType: OrderType,
  planningStrategy: PlanningStrategy,
  planningScope: PlanningScope,
  decouplingPoint: z.boolean(),
  bufferProfileId: z.string().nullable(),
  requiredShelfLifeDays: z.number().int(),
  priorityBased: z.boolean(),
});
export type ItemCoverage = z.infer<typeof ItemCoverage>;

/** Item coverages of the tenant, optionally of one item, ordered by item, site and scope. */
export async function listItemCoverages(itemId = "", init?: RequestInit): Promise<ItemCoverage[]> {
  const params = new URLSearchParams({ limit: "500" });
  if (itemId) params.set("itemId", itemId);
  const out = await request(`/item-coverages?${params}`, z.object({ items: z.array(ItemCoverage) }), init);
  return out.items;
}

export async function getItemCoverage(id: string, init?: RequestInit): Promise<ItemCoverage> {
  return request(`/item-coverages/${encodeURIComponent(id)}`, ItemCoverage, init);
}

/**
 * Creates an item coverage (Maintain planning setup). The Idempotency-Key makes a retry
 * return the first result; 409 `PLAN-ITEM-COVERAGE-EXISTS` means the item already has a
 * coverage for that site and scope, 422 `PLAN-COVERAGE-GROUP-UNKNOWN` an unknown group.
 */
export async function createItemCoverage(input: ItemCoverageInput, idempotencyKey: string): Promise<ItemCoverage> {
  return request(`/item-coverages`, ItemCoverage, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** Replaces an item coverage under If-Match; 412 means someone else changed it. */
export async function updateItemCoverage(id: string, version: number, input: ItemCoverageInput): Promise<ItemCoverage> {
  return request(`/item-coverages/${encodeURIComponent(id)}`, ItemCoverage, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "If-Match": ifMatch(version) },
    body: JSON.stringify(input),
  });
}

/* ---- DDMRP buffer profiles and stored buffers (FR-22.16) ---- */

export const BufferItemType = z.enum(["manufactured", "purchased", "distributed"]);
export type BufferItemType = z.infer<typeof BufferItemType>;

/** The fields a buffer profile create or update sends (BufferProfileInput in the OpenAPI); factors and quantities are decimal strings. */
export interface BufferProfileInput {
  code: string;
  itemType: BufferItemType;
  leadTimeFactor: string;
  variabilityFactor: string;
  orderCycleDays: number;
  minOrderQty: string;
  spikeThresholdPct: string;
  /** null means the decoupled lead time of the buffered item. */
  spikeHorizonDays: number | null;
  aduWindowPastDays: number;
  aduWindowFutureDays: number;
}

/** A stored buffer profile; `version` is its ETag. */
export const BufferProfile = z.object({
  id: z.string(),
  version: z.number().int().min(1),
  code: z.string(),
  itemType: BufferItemType,
  leadTimeFactor: z.string(),
  variabilityFactor: z.string(),
  orderCycleDays: z.number().int(),
  minOrderQty: z.string(),
  spikeThresholdPct: z.string(),
  spikeHorizonDays: z.number().int().nullable(),
  aduWindowPastDays: z.number().int(),
  aduWindowFutureDays: z.number().int(),
});
export type BufferProfile = z.infer<typeof BufferProfile>;

export async function listBufferProfiles(init?: RequestInit): Promise<BufferProfile[]> {
  const out = await request(`/buffer-profiles?limit=500`, z.object({ items: z.array(BufferProfile) }), init);
  return out.items;
}

export async function getBufferProfile(id: string, init?: RequestInit): Promise<BufferProfile> {
  return request(`/buffer-profiles/${encodeURIComponent(id)}`, BufferProfile, init);
}

/** Creates a buffer profile (Maintain planning setup); 409 `PLAN-PROFILE-EXISTS` means the code is taken. */
export async function createBufferProfile(input: BufferProfileInput, idempotencyKey: string): Promise<BufferProfile> {
  return request(`/buffer-profiles`, BufferProfile, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** Replaces a buffer profile under If-Match; 412 means someone else changed it. */
export async function updateBufferProfile(id: string, version: number, input: BufferProfileInput): Promise<BufferProfile> {
  return request(`/buffer-profiles/${encodeURIComponent(id)}`, BufferProfile, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "If-Match": ifMatch(version) },
    body: JSON.stringify(input),
  });
}

export const BufferZone = z.enum(["red", "yellow", "green"]);
export type BufferZone = z.infer<typeof BufferZone>;

/** The stored result of one buffer recalculation (data model DdmrpBuffer, BR-22.11). */
export const DdmrpBuffer = z.object({
  id: z.string(),
  itemId: z.string(),
  siteId: z.string(),
  warehouseId: z.string().nullable(),
  profileId: z.string(),
  profileCode: z.string(),
  adu: z.string(),
  dltDays: z.string(),
  daf: z.string(),
  adjustedAdu: z.string(),
  zones: z.object({
    redBase: z.string(),
    redSafety: z.string(),
    yellow: z.string(),
    green: z.string(),
    topOfRed: z.string(),
    topOfYellow: z.string(),
    topOfGreen: z.string(),
  }),
  qualifiedDemand: z.string(),
  netFlow: z.string(),
  plannedQty: z.string(),
  /** NFP / TOG; lower is more urgent. */
  priority: z.string(),
  zone: BufferZone,
  calculatedAt: z.string(),
});
export type DdmrpBuffer = z.infer<typeof DdmrpBuffer>;

export interface BufferFilter {
  siteId?: string;
  zone?: BufferZone | "";
  /** Priority ceiling as a decimal (0.5 for 50%). */
  maxPriority?: string;
}

/** The DDMRP buffer status inquiry (4.22.14): stored buffers, most urgent first. */
export async function listDdmrpBuffers(f: BufferFilter = {}, init?: RequestInit): Promise<DdmrpBuffer[]> {
  const params = new URLSearchParams({ limit: "500" });
  if (f.siteId) params.set("siteId", f.siteId);
  if (f.zone) params.set("zone", f.zone);
  if (f.maxPriority) params.set("maxPriority", f.maxPriority);
  const out = await request(`/ddmrp/buffers?${params}`, z.object({ items: z.array(DdmrpBuffer) }), init);
  return out.items;
}

/** Today's state of one buffered item at a site; the stored profile supplies the factors. */
export interface BufferRecalculation {
  itemId: string;
  siteId: string;
  warehouseId?: string;
  profileId: string;
  adu: string;
  dltDays: string;
  daf: string;
  onHand: string;
  openSupply: string;
  /** Past-due and today's demand (day 0) plus any spikes, as dated quantities. */
  demand: { day: number; qty: string }[];
}

/** Recalculates and stores one buffer (Run master planning); repeats replace the same row. */
export async function recalculateDdmrpBuffer(body: BufferRecalculation): Promise<DdmrpBuffer> {
  return request(`/ddmrp/buffers:recalculate`, DdmrpBuffer, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

/* ---- Quota arrangements (FR-22.17) ---- */

export const QuotaSourceType = z.enum(["vendor", "production", "transfer"]);
export type QuotaSourceType = z.infer<typeof QuotaSourceType>;

/** One source of an arrangement as sent; only the reference of its type is set. */
export interface QuotaSourceInput {
  type: QuotaSourceType;
  vendorId?: string;
  productionVersionId?: string;
  fromWarehouseId?: string;
  quota: string;
  baseQty: string;
  allocatedQty: string;
  maxQtyPerPeriod: string | null;
  periodDays: number | null;
  priority: number;
  blocked: boolean;
}

export interface QuotaArrangementInput {
  itemId: string;
  siteId: string;
  validFrom: string;
  validTo: string;
  split: boolean;
  minSplitQty: string;
  sources: QuotaSourceInput[];
}

export const QuotaSource = z.object({
  id: z.string(),
  type: QuotaSourceType,
  vendorId: z.string().nullable(),
  productionVersionId: z.string().nullable(),
  fromWarehouseId: z.string().nullable(),
  quota: z.string(),
  baseQty: z.string(),
  allocatedQty: z.string(),
  maxQtyPerPeriod: z.string().nullable(),
  periodDays: z.number().int().nullable(),
  priority: z.number().int(),
  blocked: z.boolean(),
  /** (allocated + base) / quota (BR-22.12); null for a quota-0 fallback source. */
  rating: z.string().nullable(),
});
export type QuotaSource = z.infer<typeof QuotaSource>;

/** A stored quota arrangement; `version` is its ETag. */
export const QuotaArrangement = z.object({
  id: z.string(),
  version: z.number().int().min(1),
  itemId: z.string(),
  siteId: z.string(),
  validFrom: z.string(),
  validTo: z.string(),
  split: z.boolean(),
  minSplitQty: z.string(),
  sources: z.array(QuotaSource),
});
export type QuotaArrangement = z.infer<typeof QuotaArrangement>;

export interface QuotaFilter {
  itemId?: string;
  siteId?: string;
  validOn?: string;
}

export async function listQuotaArrangements(f: QuotaFilter = {}, init?: RequestInit): Promise<QuotaArrangement[]> {
  const params = new URLSearchParams({ limit: "500" });
  if (f.itemId) params.set("itemId", f.itemId);
  if (f.siteId) params.set("siteId", f.siteId);
  if (f.validOn) params.set("validOn", f.validOn);
  const out = await request(`/quota-arrangements?${params}`, z.object({ items: z.array(QuotaArrangement) }), init);
  return out.items;
}

export async function getQuotaArrangement(id: string, init?: RequestInit): Promise<QuotaArrangement> {
  return request(`/quota-arrangements/${encodeURIComponent(id)}`, QuotaArrangement, init);
}

/** Creates an arrangement; 409 `PLAN-QUOTA-OVERLAP` means another one of the item and site is valid in that period. */
export async function createQuotaArrangement(input: QuotaArrangementInput, idempotencyKey: string): Promise<QuotaArrangement> {
  return request(`/quota-arrangements`, QuotaArrangement, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** Replaces an arrangement and its sources under If-Match; 412 means someone else changed it. */
export async function updateQuotaArrangement(id: string, version: number, input: QuotaArrangementInput): Promise<QuotaArrangement> {
  return request(`/quota-arrangements/${encodeURIComponent(id)}`, QuotaArrangement, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "If-Match": ifMatch(version) },
    body: JSON.stringify(input),
  });
}

/** Settings of a planning scenario (FR-22.15). Factors and quantities are decimal strings. */
export const ScenarioSettings = z.object({
  planCode: z.string(),
  name: z.string(),
  /** Multiplies the independent demand of every item without its own factor; 1 leaves demand as it is. */
  demandFactor: z.string(),
  itemFactors: z.array(z.object({ itemId: z.string(), factor: z.string() })),
  addedDemand: z.array(z.object({ itemId: z.string(), date: z.string(), qty: z.string() })),
  leadTimeOverrides: z.array(z.object({ itemId: z.string(), leadTimeDays: z.number().int() })),
  active: z.boolean(),
});
export type ScenarioSettings = z.infer<typeof ScenarioSettings>;

export const Scenario = ScenarioSettings.extend({
  id: z.string(),
  code: z.string(),
  /** The scenario's ETag: send it back as If-Match on PUT. */
  version: z.number().int().min(1),
});
export type Scenario = z.infer<typeof Scenario>;

/** At most this many scenarios of one plan are active (FR-22.15). */
export const MAX_ACTIVE_SCENARIOS = 10;

export async function listScenarios(planCode = "", init?: RequestInit): Promise<Scenario[]> {
  const q = planCode ? `&planCode=${encodeURIComponent(planCode)}` : "";
  const out = await request(`/scenarios?limit=500${q}`, z.object({ items: z.array(Scenario) }), init);
  return out.items;
}

export async function getScenario(code: string, init?: RequestInit): Promise<Scenario> {
  return request(`/scenarios/${encodeURIComponent(code)}`, Scenario, init);
}

/** Creates a scenario (Run simulations). 409 `PLAN-SCENARIO-EXISTS` means the code is taken, `PLAN-SCENARIO-LIMIT` that the plan has 10 active ones. */
export async function createScenario(code: string, settings: ScenarioSettings, idempotencyKey: string): Promise<Scenario> {
  return request(`/scenarios`, Scenario, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify({ code, ...settings }),
  });
}

/** Replaces the settings of a scenario under If-Match; 412 means someone else changed it. */
export async function updateScenario(code: string, version: number, settings: ScenarioSettings): Promise<Scenario> {
  return request(`/scenarios/${encodeURIComponent(code)}`, Scenario, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "If-Match": ifMatch(version) },
    body: JSON.stringify(settings),
  });
}

/**
 * Runs a scenario on a snapshot and stores a simulation version of its plan.
 * The operational plan, its messages and events stay as they are; the
 * Idempotency-Key makes a retry replay the same simulation.
 */
export async function runScenario(code: string, idempotencyKey: string, body: RunPlanRequest): Promise<PlanVersion> {
  return request(`/scenarios/${encodeURIComponent(code)}:run`, PlanVersion, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}
