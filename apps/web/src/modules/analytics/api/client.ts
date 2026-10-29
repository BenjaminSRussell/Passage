import { z } from "zod";

// Client for the analytics service (services/analytics/api/openapi.yaml).
// Responses are validated with zod before the pages use them.

export const base = "/api/v1/analytics";

export const GRAINS = ["journal_line", "sales_line", "resource_shift"] as const;
export const RECON_STATUSES = ["OK", "FAILED", "BLOCKED"] as const;

/** Query limits from the service (PRD §9.46.5). */
export const MAX_MEASURES = 10;
export const MAX_DIMENSIONS = 8;
export const MAX_SPAN_YEARS = 5;

const metricSchema = z.object({
  metricId: z.string(),
  version: z.number().int(),
  name: z.string(),
  formula: z.string(),
  grain: z.enum(GRAINS),
  allowedDimensions: z.array(z.string()),
  reconciliationSource: z.string().optional(),
  nonFinancial: z.boolean(),
  status: z.enum(["DRAFT", "PUBLISHED", "DEPRECATED"]),
});

const metricsResponseSchema = z.object({ items: z.array(metricSchema) });

const filterSchema = z.object({ member: z.string(), operator: z.string(), values: z.array(z.string()) });

const planSchema = z.object({
  model: z.string(),
  grain: z.string(),
  measures: z.array(z.string()),
  dimensions: z.array(z.string()),
  from: z.string(),
  to: z.string(),
  filters: z.array(filterSchema),
  reason: z.literal("NO_SCOPE").optional(),
});

const queryResponseSchema = z.object({ plan: planSchema });

/** Amounts are decimal strings, never floats. */
const decimalString = z.string().regex(/^-?\d+(\.\d+)?$/);

const reconciliationSchema = z.object({
  metricId: z.string(),
  legalEntityId: z.string(),
  date: z.string(),
  currency: z.string(),
  semanticValue: decimalString,
  sourceValue: decimalString,
  delta: decimalString,
  tolerance: decimalString,
  status: z.enum(RECON_STATUSES),
  consecutiveOkDays: z.number().int(),
  trusted: z.boolean(),
});

const reconciliationResponseSchema = z.object({
  date: z.string(),
  failedCount: z.number().int(),
  items: z.array(reconciliationSchema),
});

export type Metric = z.infer<typeof metricSchema>;
export type Filter = z.infer<typeof filterSchema>;
export type Plan = z.infer<typeof planSchema>;
export type Reconciliation = z.infer<typeof reconciliationSchema>;
export type ReconciliationDay = z.infer<typeof reconciliationResponseSchema>;

export interface QueryInput {
  measures: string[];
  dimensions: string[];
  from: string;
  to: string;
}

/** A failed call. `code` is the stable Passage problem code (e.g. AUTH-0003). */
export class AnalyticsApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "AnalyticsApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export interface Problem {
  status: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof AnalyticsApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  if (error instanceof z.ZodError) {
    return { status: 0, code: "CONTRACT", message: "The analytics service sent a response this page does not understand.", fields: {} };
  }
  return { status: 0, code: "NETWORK", message: error instanceof Error ? error.message : "Request failed.", fields: {} };
}

async function toError(res: Response): Promise<AnalyticsApiError> {
  let code = `HTTP-${res.status}`;
  let detail = `Request failed with status ${res.status}.`;
  let fields: Record<string, string> = {};
  try {
    const body = (await res.json()) as { code?: unknown; detail?: unknown; errors?: unknown };
    if (typeof body.code === "string") code = body.code;
    if (typeof body.detail === "string") detail = body.detail;
    if (body.errors && typeof body.errors === "object" && !Array.isArray(body.errors)) {
      fields = Object.fromEntries(
        Object.entries(body.errors as Record<string, unknown>).filter((e): e is [string, string] => typeof e[1] === "string"),
      );
    }
  } catch {
    // Non-JSON error bodies keep the generic message.
  }
  return new AnalyticsApiError(res.status, code, detail, fields);
}

type Fetch = typeof fetch;
const defaultFetch: Fetch = (input, init) => fetch(input, init);

interface Reply {
  body: unknown;
  etag: string | null;
  replayed: boolean;
}

/** Calls an absolute /api/v1 path; the analytics service also serves /catalog and /archive. */
async function send(fetchImpl: Fetch, url: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Reply> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetchImpl(url, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return { body: await res.json(), etag: res.headers.get("ETag"), replayed: res.headers.get("Idempotency-Replayed") === "true" };
}

async function call(fetchImpl: Fetch, path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<unknown> {
  return (await send(fetchImpl, `${base}${path}`, init, signal)).body;
}

/** Lists the published governed metrics. */
export async function listMetrics(fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<Metric[]> {
  return metricsResponseSchema.parse(await call(fetchImpl, "/metrics", {}, signal)).items;
}

/** Validates a query and returns the Cube plan with the caller's scope filters. */
export async function planQuery(input: QueryInput, fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<Plan> {
  const body = JSON.stringify(input);
  return queryResponseSchema.parse(await call(fetchImpl, "/query", { method: "POST", body }, signal)).plan;
}

/** Reconciliation results for one day, optionally for one metric. */
export async function getReconciliation(
  date: string,
  metric = "",
  fetchImpl: Fetch = defaultFetch,
  signal?: AbortSignal,
): Promise<ReconciliationDay> {
  const q = new URLSearchParams({ date });
  if (metric !== "") q.set("metric", metric);
  return reconciliationResponseSchema.parse(await call(fetchImpl, `/reconciliation?${q.toString()}`, {}, signal));
}

// --- Slice 2: data-quality rules, archive partitions, report schedules -------

export const catalogBase = "/api/v1/catalog";
export const archiveBase = "/api/v1/archive";

export const RULE_TYPES = ["COMPLETENESS", "VALIDITY", "UNIQUENESS", "REFERENTIAL", "FRESHNESS"] as const;
export const SEVERITIES = ["LOW", "MEDIUM", "HIGH"] as const;
export const RULE_STATUSES = ["ACTIVE", "RETIRED"] as const;
export const ARCHIVE_STATUSES = ["PENDING", "VERIFIED", "DROPPED", "RESTORED"] as const;
export const TARGET_TYPES = ["DASHBOARD", "QUERY"] as const;
export const SCHEDULE_FORMATS = ["PDF", "XLSX"] as const;
export const DESTINATION_TYPES = ["EMAIL", "SFTP", "INBOX"] as const;

/** Patterns the service enforces (services/analytics/internal/domain). */
export const RULE_ID = /^[a-z][a-z0-9_]{1,62}$/;
export const ASSET_FQN = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$/;
export const ARCHIVE_NAME = /^[a-z][a-z0-9_]{1,62}$/;
export const ARCHIVE_PERIOD = /^\d{4}-(0[1-9]|1[0-2])$/;
export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const dqRuleSchema = z.object({
  ruleId: z.string(),
  assetFqn: z.string(),
  ruleType: z.enum(RULE_TYPES),
  expression: z.string(),
  thresholdPct: decimalString,
  severity: z.enum(SEVERITIES),
  ownerUserId: z.string(),
  status: z.enum(RULE_STATUSES),
  muteUntil: z.string().optional(),
  version: z.number().int(),
});

const dqScoreSchema = z.object({
  assetFqn: z.string(),
  ruleId: z.string(),
  date: z.string(),
  passPct: decimalString,
  failedRows: z.number().int(),
  totalRows: z.number().int(),
  below: z.boolean(),
  belowStreak: z.number().int(),
});

const partitionSchema = z.object({
  partitionId: z.string(),
  domain: z.string(),
  tableName: z.string(),
  period: z.string(),
  rowsOltp: z.number().int(),
  rowsArchive: z.number().int(),
  contentHashOltp: z.string(),
  contentHashArchive: z.string(),
  status: z.enum(ARCHIVE_STATUSES),
  legalHold: z.boolean(),
  openPeriod: z.boolean(),
  movedAt: z.string().optional(),
  restoreUntil: z.string().optional(),
});

const destinationSchema = z.object({ type: z.enum(DESTINATION_TYPES), targets: z.array(z.string()) });

const scheduleSchema = z.object({
  scheduleId: z.string(),
  name: z.string(),
  targetType: z.enum(TARGET_TYPES),
  targetId: z.string(),
  cron: z.string(),
  timezone: z.string(),
  format: z.enum(SCHEDULE_FORMATS),
  destination: destinationSchema,
  lastRunAt: z.string().optional(),
  nextRunAt: z.string().optional(),
  failureCount: z.number().int(),
  version: z.number().int(),
});

const items = <T extends z.ZodTypeAny>(item: T) => z.object({ items: z.array(item) });

export type DQRule = z.infer<typeof dqRuleSchema>;
export type DQScore = z.infer<typeof dqScoreSchema>;
export type ArchivePartition = z.infer<typeof partitionSchema>;
export type Destination = z.infer<typeof destinationSchema>;
export type Schedule = z.infer<typeof scheduleSchema>;

export interface DQRuleInput {
  assetFqn: string;
  ruleType: DQRule["ruleType"];
  expression: string;
  thresholdPct: string;
  severity: DQRule["severity"];
  ownerUserId: string;
  status: DQRule["status"];
  muted: boolean;
}

export interface ScheduleInput {
  name: string;
  targetType: Schedule["targetType"];
  targetId: string;
  cron: string;
  timezone: string;
  format: Schedule["format"];
  destination: Destination;
}

function query(params: Record<string, string>): string {
  const q = new URLSearchParams(Object.entries(params).filter(([, v]) => v !== ""));
  const text = q.toString();
  return text === "" ? "" : `?${text}`;
}

/** Scorecard filters (PRD §9.46.14); an empty field matches every rule. */
export interface DQRuleFilter {
  asset?: string;
  ruleType?: string;
  owner?: string;
}

/** Data-quality rules filtered by catalog asset, rule type and owner. */
export async function listDQRules(filter: DQRuleFilter = {}, fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<DQRule[]> {
  const q = query({ asset: filter.asset ?? "", ruleType: filter.ruleType ?? "", owner: filter.owner ?? "" });
  const reply = await send(fetchImpl, `${catalogBase}/dq-rules${q}`, {}, signal);
  return items(dqRuleSchema).parse(reply.body).items;
}

/** One rule with its version, which a later save sends back as If-Match. */
export async function getDQRule(ruleId: string, fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<DQRule> {
  const reply = await send(fetchImpl, `${catalogBase}/dq-rules/${encodeURIComponent(ruleId)}`, {}, signal);
  return dqRuleSchema.parse(reply.body);
}

/**
 * Creates a rule (version null: no If-Match) or replaces it (If-Match with
 * the version the form was loaded at). A stale version fails with ANA-0113.
 */
export async function putDQRule(ruleId: string, input: DQRuleInput, version: number | null, fetchImpl: Fetch = defaultFetch): Promise<DQRule> {
  const headers: Record<string, string> = version === null ? {} : { "If-Match": `"${version}"` };
  const reply = await send(fetchImpl, `${catalogBase}/dq-rules/${encodeURIComponent(ruleId)}`, {
    method: "PUT",
    headers,
    body: JSON.stringify(input),
  });
  return dqRuleSchema.parse(reply.body);
}

/** Score history, newest first. */
export async function listDQScores(
  filter: { asset?: string; rule?: string; limit?: number },
  fetchImpl: Fetch = defaultFetch,
  signal?: AbortSignal,
): Promise<DQScore[]> {
  const q = query({ asset: filter.asset ?? "", rule: filter.rule ?? "", limit: filter.limit ? String(filter.limit) : "" });
  const reply = await send(fetchImpl, `${catalogBase}/dq-rules/scores${q}`, {}, signal);
  return items(dqScoreSchema).parse(reply.body).items;
}

/** Archive partitions, newest period first. */
export async function listArchivePartitions(
  filter: { domain: string; period: string; status: string },
  fetchImpl: Fetch = defaultFetch,
  signal?: AbortSignal,
): Promise<ArchivePartition[]> {
  const reply = await send(fetchImpl, `${archiveBase}/partitions${query(filter)}`, {}, signal);
  return items(partitionSchema).parse(reply.body).items;
}

export async function getArchivePartition(id: string, fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<ArchivePartition> {
  const reply = await send(fetchImpl, `${archiveBase}/partitions/${encodeURIComponent(id)}`, {}, signal);
  return partitionSchema.parse(reply.body);
}

/** Restores a dropped partition for 30 days; repeating it while live is harmless. */
export async function restoreArchivePartition(id: string, fetchImpl: Fetch = defaultFetch): Promise<ArchivePartition> {
  const reply = await send(fetchImpl, `${archiveBase}/partitions/${encodeURIComponent(id)}/restore`, { method: "POST" });
  return partitionSchema.parse(reply.body);
}

export async function listSchedules(fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<Schedule[]> {
  return items(scheduleSchema).parse(await call(fetchImpl, "/schedules", {}, signal)).items;
}

export async function getSchedule(id: string, fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<Schedule> {
  return scheduleSchema.parse(await call(fetchImpl, `/schedules/${encodeURIComponent(id)}`, {}, signal));
}

/**
 * Creates a schedule. The caller keeps one idempotency key per form, so a
 * retry after a lost response returns the schedule created the first time.
 */
export async function createSchedule(input: ScheduleInput, idempotencyKey: string, fetchImpl: Fetch = defaultFetch): Promise<Schedule> {
  const reply = await send(fetchImpl, `${base}/schedules`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
  return scheduleSchema.parse(reply.body);
}

// --- Slice 4: dashboards, exports, schedule deliveries, lineage ---------------

export const TILE_PERIODS = ["MTD", "QTD", "YTD", "TRAILING_12M"] as const;
export const EXPORT_FORMATS = ["XLSX", "CSV"] as const;
export const EXPORT_MODES = ["INLINE", "TEMPORAL_JOB"] as const;
export const EXPORT_STATUSES = ["QUEUED", "READY", "DELETED"] as const;
export const DELIVERY_STATUSES = ["DELIVERED", "RETRY", "FAILED"] as const;

/** Limits the service enforces (services/analytics/internal/domain). */
export const DASHBOARD_ID = /^[a-z][a-z0-9_-]{1,62}$/;
export const MAX_TILES = 12;
export const MIN_REFRESH_SECONDS = 15;
export const MAX_REFRESH_SECONDS = 600;
export const MAX_EXPORT_ROWS = 1_000_000;
export const ASYNC_EXPORT_ROWS = 100_000;
export const MAX_LINEAGE_DEPTH = 20;

const tileSchema = z.object({ title: z.string(), metric: z.string(), period: z.enum(TILE_PERIODS) });

const dashboardSchema = z.object({
  dashboardId: z.string(),
  title: z.string(),
  tiles: z.array(tileSchema),
  refreshSeconds: z.number().int(),
  version: z.number().int(),
  updatedAt: z.string().optional(),
});

const deliverySchema = z.object({
  deliveryId: z.string(),
  scheduleId: z.string(),
  runId: z.string(),
  attempt: z.number().int(),
  status: z.enum(DELIVERY_STATUSES),
  format: z.enum(SCHEDULE_FORMATS),
  destinationType: z.enum(DESTINATION_TYPES),
  rowCount: z.number().int(),
  bytes: z.number().int(),
  error: z.string().optional(),
  runAt: z.string(),
  retryAt: z.string().optional(),
});

const exportSchema = z.object({
  exportId: z.string(),
  requestedBy: z.string(),
  queryHash: z.string(),
  plan: planSchema,
  format: z.enum(EXPORT_FORMATS),
  mode: z.enum(EXPORT_MODES),
  rowCount: z.number().int(),
  linkTtlHours: z.number().int(),
  status: z.enum(EXPORT_STATUSES),
  createdAt: z.string(),
  linkExpiresAt: z.string(),
  deleteAt: z.string(),
  reused: z.boolean(),
});

const lineageAssetSchema = z.object({ fqn: z.string(), distance: z.number().int() });

const lineageSchema = z.object({
  asset: z.string(),
  depth: z.number().int(),
  upstream: z.array(lineageAssetSchema),
  downstream: z.array(lineageAssetSchema),
});

export type Tile = z.infer<typeof tileSchema>;
export type Dashboard = z.infer<typeof dashboardSchema>;
export type Delivery = z.infer<typeof deliverySchema>;
export type ExportJob = z.infer<typeof exportSchema>;
export type LineageAsset = z.infer<typeof lineageAssetSchema>;
export type Lineage = z.infer<typeof lineageSchema>;

export interface DashboardInput {
  title: string;
  tiles: Tile[];
  refreshSeconds: number;
}

export interface ExportInput extends QueryInput {
  format: ExportJob["format"];
  rowCount: number;
  linkTtlHours: number;
}

export interface LineageEdge {
  source: string;
  target: string;
}

export async function listDashboards(fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<Dashboard[]> {
  return items(dashboardSchema).parse(await call(fetchImpl, "/dashboards", {}, signal)).items;
}

/** One dashboard with its version, which a later save sends back as If-Match. */
export async function getDashboard(id: string, fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<Dashboard> {
  return dashboardSchema.parse(await call(fetchImpl, `/dashboards/${encodeURIComponent(id)}`, {}, signal));
}

/**
 * Creates a dashboard (version null: no If-Match) or replaces it (If-Match
 * with the version the editor was loaded at). A stale version fails with ANA-0113.
 */
export async function putDashboard(id: string, input: DashboardInput, version: number | null, fetchImpl: Fetch = defaultFetch): Promise<Dashboard> {
  const headers: Record<string, string> = version === null ? {} : { "If-Match": `"${version}"` };
  const body = await call(fetchImpl, `/dashboards/${encodeURIComponent(id)}`, { method: "PUT", headers, body: JSON.stringify(input) });
  return dashboardSchema.parse(body);
}

/** Delivery attempts of one schedule, newest first. */
export async function listDeliveries(scheduleId: string, status = "", fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<Delivery[]> {
  const body = await call(fetchImpl, `/schedules/${encodeURIComponent(scheduleId)}/deliveries${query({ status })}`, {}, signal);
  return items(deliverySchema).parse(body).items;
}

/** Export log filters (PRD §9.46.14: user, date, format); an empty field matches every job. */
export interface ExportFilter {
  user?: string;
  format?: string;
  from?: string;
  to?: string;
}

export async function listExports(filter: ExportFilter = {}, fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<ExportJob[]> {
  const q = query({ user: filter.user ?? "", format: filter.format ?? "", from: filter.from ?? "", to: filter.to ?? "" });
  return items(exportSchema).parse(await call(fetchImpl, `/exports${q}`, {}, signal)).items;
}

/**
 * Starts an export job. The caller keeps one idempotency key per filled-in
 * form, so a retry after a lost response returns the job created the first
 * time. `reused` is true when an identical export from the last hour is returned.
 */
export async function createExport(input: ExportInput, idempotencyKey: string, fetchImpl: Fetch = defaultFetch): Promise<ExportJob> {
  const reply = await send(fetchImpl, `${base}/exports`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
  return exportSchema.parse(reply.body);
}

/** Upstream and downstream assets of a catalog asset, nearest first. */
export async function getLineage(asset: string, depth: number, fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<Lineage> {
  const reply = await send(fetchImpl, `${catalogBase}/lineage/${encodeURIComponent(asset)}${query({ depth: depth > 0 ? String(depth) : "" })}`, {}, signal);
  return lineageSchema.parse(reply.body);
}

/** Records lineage edges; re-sending an edge is harmless. Returns how many were new. */
export async function addLineageEdges(edges: LineageEdge[], fetchImpl: Fetch = defaultFetch): Promise<number> {
  const reply = await send(fetchImpl, `${catalogBase}/lineage/edges`, { method: "POST", body: JSON.stringify({ edges }) });
  return z.object({ added: z.number().int() }).parse(reply.body).added;
}
