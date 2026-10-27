// Client for the EAM service (services/eam/api/openapi.yaml). Browser calls use
// /api/v1/eam/..., which the web BFF rewrites to the service.

export const base = "/api/v1/eam";

export type LifecycleState = "CREATED" | "ACTIVE" | "INACTIVE" | "DISPOSED";
export type WorkOrderStatus = "CREATED" | "SCHEDULED" | "RELEASED" | "IN_PROGRESS" | "ON_HOLD" | "COMPLETED" | "CLOSED" | "CANCELED";
export type WorkOrderType = "PREVENTIVE" | "CORRECTIVE" | "INSPECTION" | "EMERGENCY" | "CAPITAL";
export type RequestStatus = "NEW" | "TRIAGED" | "CONVERTED" | "REJECTED" | "DEFERRED";
export type RequestSource = "USER" | "MOBILE" | "QUALITY";
export type LineKind = "TIME" | "COUNTER" | "TIME_OR_COUNTER" | "TIME_AND_COUNTER";
export type IntervalUnit = "DAYS" | "WEEKS" | "MONTHS" | "YEARS";

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export interface Asset {
  id: string;
  assetNumber: string;
  name: string;
  assetType: string;
  siteId: string;
  criticality: number;
  lifecycleState: LifecycleState;
  productionResourceId?: string;
  version: number;
  createdAt: string;
}

export interface NewAsset {
  name: string;
  assetType: string;
  siteId: string;
  criticality: number;
  productionResourceId?: string;
}

export interface Counter {
  id: string;
  assetId: string;
  name: string;
  unit: string;
  cumulative: boolean;
  source: "MANUAL" | "IOT";
  lastReading: number;
  lastReadingAt?: string;
  avgDailyUsage: number;
}

export interface Rule {
  id: string;
  counterId: string;
  name: string;
  operator: ">" | "<";
  threshold: number;
  severity: number;
  cooldownMinutes: number;
}

export interface AssetDetail extends Asset {
  counters: Counter[];
  rules: Rule[];
  openWorkOrders: number;
  partsCost: string;
  laborCost: string;
}

export interface NewCounter {
  name: string;
  unit: string;
  cumulative: boolean;
  source: "MANUAL" | "IOT";
}

export interface NewReading {
  value: number;
  readingAt?: string;
  reset?: boolean;
}

export interface Reading {
  id: string;
  counterId: string;
  readingAt: string;
  value: number;
  reset: boolean;
  source: string;
}

export interface NewRule {
  name: string;
  operator: ">" | "<";
  threshold: number;
  severity: number;
  cooldownMinutes?: number;
}

export interface Kpi {
  failures: number;
  mtbfHours: number;
  mttrHours: number;
  availability: number;
}

export interface Workspace {
  requestsToTriage: number;
  dueIn7Days: number;
  overdue: number;
  openWorkOrders: number;
}

export interface PlanLine {
  id: string;
  assetId: string;
  kind: LineKind;
  intervalValue?: number;
  intervalUnit?: IntervalUnit;
  counterId?: string;
  counterInterval?: number;
  jobType: string;
  earlyTolerancePct: number;
  horizonDays: number;
  startAt: string;
  lastCompletedAt?: string;
  lastCompletedReading: number;
  nextDueAt?: string;
  nextDueReading?: number;
  nextDueForecastAt?: string;
}

export interface Plan {
  id: string;
  code: string;
  name: string;
  currency: string;
  active: boolean;
  lines: PlanLine[];
}

export interface NewPlanLine {
  assetId: string;
  kind: LineKind;
  intervalValue?: number;
  intervalUnit?: IntervalUnit;
  counterId?: string;
  counterInterval?: number;
  jobType: string;
  horizonDays?: number;
  startAt?: string;
}

export interface NewPlan {
  code: string;
  name: string;
  currency: string;
  lines: NewPlanLine[];
}

export interface CreatedPlan {
  id: string;
  code: string;
  lineIds: string[];
}

export interface MaintenanceRequest {
  id: string;
  assetId: string;
  source: string;
  severity: number;
  description: string;
  readingValue?: number;
  status: RequestStatus;
  workOrderId?: string;
  createdAt: string;
}

export interface NewRequest {
  assetId: string;
  source: RequestSource;
  severity: number;
  description: string;
}

export interface WorkOrder {
  id: string;
  number: string;
  assetId: string;
  workOrderType: string;
  status: WorkOrderStatus;
  priority: number;
  currency: string;
  planLineId?: string;
  requestId?: string;
  jobType?: string;
  dueAt?: string;
  actualStart?: string;
  actualEnd?: string;
  downtimeMinutes: number;
  partsCost: string;
  laborCost: string;
  version: number;
  createdAt: string;
}

export interface LaborLine {
  id: string;
  hours: string;
  rate: string;
  cost: string;
  workerId?: string;
  createdAt: string;
}

export interface StatusChange {
  from: string;
  to: string;
  at: string;
  actor?: string;
}

export interface WorkOrderDetail extends WorkOrder {
  labor: LaborLine[];
  history: StatusChange[];
}

export interface NewWorkOrder {
  assetId: string;
  workOrderType: WorkOrderType;
  currency: string;
  priority?: number;
  jobType?: string;
  dueAt?: string;
}

export interface TransitionInput {
  to: WorkOrderStatus;
  actualStart?: string;
  actualEnd?: string;
  downtimeMinutes?: number;
}

/** A failed call, carrying the RFC 9457 code. */
export class EamApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "EamApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof EamApiError) return { code: error.code, message: error.message };
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed" };
}

async function toError(res: Response): Promise<EamApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  return new EamApiError(res.status, code, message);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

async function page<T>(path: string, signal?: AbortSignal): Promise<Page<T>> {
  const body = await send<Page<T>>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new EamApiError(0, "COM-0001", "The response has no items");
  return body;
}

function post<T>(path: string, body: unknown, key: string, extra: Record<string, string> = {}): Promise<T> {
  return send<T>(path, {
    method: "POST",
    body: body === undefined ? undefined : JSON.stringify(body),
    headers: { "Idempotency-Key": key, ...extra },
  });
}

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(key, value);
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

const id = (value: string) => encodeURIComponent(value);

/** A fresh Idempotency-Key; reuse one per form so a retried submit cannot create two. */
export function newKey(): string {
  return globalThis.crypto.randomUUID();
}

export interface ListFilter {
  status?: string;
  asset?: string;
  cursor?: string;
  limit?: number;
}

const listQuery = (f: ListFilter = {}) =>
  query({ status: f.status, asset: f.asset, cursor: f.cursor, limit: f.limit === undefined ? undefined : String(f.limit) });

export function getWorkspace(signal?: AbortSignal): Promise<Workspace> {
  return send<Workspace>("/workspace", {}, signal);
}

export function listAssets(filter: ListFilter = {}, signal?: AbortSignal): Promise<Page<Asset>> {
  return page<Asset>(`/assets${listQuery(filter)}`, signal);
}

export function getAsset(assetId: string, signal?: AbortSignal): Promise<AssetDetail> {
  return send<AssetDetail>(`/assets/${id(assetId)}`, {}, signal);
}

export function createAsset(input: NewAsset, key: string): Promise<Asset> {
  return post<Asset>("/assets", input, key);
}

export function activateAsset(assetId: string, version: number, key = newKey()): Promise<Asset> {
  return post<Asset>(`/assets/${id(assetId)}/activate`, undefined, key, { "If-Match": `"${version}"` });
}

export function createCounter(assetId: string, input: NewCounter, key: string): Promise<Counter> {
  return post<Counter>(`/assets/${id(assetId)}/counters`, input, key);
}

export function createRule(counterId: string, input: NewRule, key: string): Promise<Rule> {
  return post<Rule>(`/counters/${id(counterId)}/rules`, input, key);
}

export function recordReading(counterId: string, input: NewReading, key: string): Promise<Reading> {
  return post<Reading>(`/counters/${id(counterId)}/readings`, input, key);
}

export function listReadings(counterId: string, limit = 10, signal?: AbortSignal): Promise<Page<Reading>> {
  return page<Reading>(`/counters/${id(counterId)}/readings${query({ limit: String(limit) })}`, signal);
}

export function listPlans(filter: ListFilter = {}, signal?: AbortSignal): Promise<Page<Plan>> {
  return page<Plan>(`/plans${listQuery(filter)}`, signal);
}

export function createPlan(input: NewPlan, key: string): Promise<CreatedPlan> {
  return post<CreatedPlan>("/plans", input, key);
}

export function listRequests(filter: ListFilter = {}, signal?: AbortSignal): Promise<Page<MaintenanceRequest>> {
  return page<MaintenanceRequest>(`/requests${listQuery(filter)}`, signal);
}

export function createRequest(input: NewRequest, key: string): Promise<MaintenanceRequest> {
  return post<MaintenanceRequest>("/requests", input, key);
}

export function convertRequest(requestId: string, currency: string, key = newKey()): Promise<WorkOrder> {
  return post<WorkOrder>(`/requests/${id(requestId)}/convert`, { currency }, key);
}

export function listWorkOrders(filter: ListFilter = {}, signal?: AbortSignal): Promise<Page<WorkOrder>> {
  return page<WorkOrder>(`/work-orders${listQuery(filter)}`, signal);
}

export function getWorkOrder(workOrderId: string, signal?: AbortSignal): Promise<WorkOrderDetail> {
  return send<WorkOrderDetail>(`/work-orders/${id(workOrderId)}`, {}, signal);
}

export function createWorkOrder(input: NewWorkOrder, key: string): Promise<WorkOrder> {
  return post<WorkOrder>("/work-orders", input, key);
}

export function transitionWorkOrder(workOrderId: string, input: TransitionInput, version: number, key = newKey()): Promise<WorkOrder> {
  return post<WorkOrder>(`/work-orders/${id(workOrderId)}/status`, input, key, { "If-Match": `"${version}"` });
}

export function recordLabor(workOrderId: string, hours: string, rate: string, key = newKey()): Promise<WorkOrder> {
  return post<WorkOrder>(`/work-orders/${id(workOrderId)}/labor`, { hours, rate }, key);
}

export function assetKpis(assetId: string, from: string, to: string, signal?: AbortSignal): Promise<Kpi> {
  return send<Kpi>(`/kpis${query({ asset: assetId, from, to })}`, {}, signal);
}
