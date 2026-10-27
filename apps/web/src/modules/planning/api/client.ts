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
  asOf: z.string(),
  itemsPlanned: z.number().int(),
  plannedOrders: z.number().int(),
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
  items: unknown[];
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
  limit?: number;
}

export async function listPlannedOrders(q: OrderQuery, init?: RequestInit): Promise<PlannedOrder[]> {
  const params = new URLSearchParams({ planCode: q.planCode });
  if (q.status) params.set("status", q.status);
  if (q.itemId) params.set("itemId", q.itemId);
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
