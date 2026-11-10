import { apiFetch } from "../../../shell/api/http";
// Client for the EAM service (services/eam/api/openapi.yaml). Browser calls use
// /api/v1/eam/..., which the web BFF rewrites to the service.

export const base = "/api/v1/eam";

export type LifecycleState = "CREATED" | "ACTIVE" | "INACTIVE" | "DISPOSED";
export type WorkOrderStatus = "CREATED" | "SCHEDULED" | "RELEASED" | "IN_PROGRESS" | "ON_HOLD" | "COMPLETED" | "CLOSED" | "CANCELED";
export type WorkOrderType = "PREVENTIVE" | "CORRECTIVE" | "INSPECTION" | "EMERGENCY" | "CAPITAL";
export type RequestStatus = "NEW" | "TRIAGED" | "CONVERTED" | "REJECTED" | "DEFERRED";
export type RequestSource = "USER" | "MOBILE" | "QUALITY";
export type LineKind = "TIME" | "COUNTER" | "TIME_OR_COUNTER" | "TIME_AND_COUNTER" | "ROUND";
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
  functionalLocationId?: string;
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
  location?: FunctionalLocation;
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
  /** ROUND lines only: the round they schedule; assetId is its first stop. */
  roundId?: string;
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

/** A plan line; ROUND lines name roundId instead of assetId and jobType (FR-44.14). */
export interface NewPlanLine {
  assetId?: string;
  roundId?: string;
  kind: LineKind;
  intervalValue?: number;
  intervalUnit?: IntervalUnit;
  counterId?: string;
  counterInterval?: number;
  jobType?: string;
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
  triageNote?: string;
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
  warrantyId?: string;
  warrantyCovered: boolean;
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

export interface Fault {
  symptom: string;
  area: string;
  type: string;
  cause: string;
  remedy: string;
}

export interface WorkOrderFault extends Fault {
  id: string;
  notes?: string;
  registeredBy?: string;
  registeredAt: string;
}

export interface DowntimeWindow {
  id: string;
  resourceId: string;
  from: string;
  to: string;
  minutes: number;
  recordedBy?: string;
}

export interface WorkOrderDetail extends WorkOrder {
  assetType: string;
  labor: LaborLine[];
  history: StatusChange[];
  fault?: WorkOrderFault;
  downtime: DowntimeWindow[];
  /** ROUND orders only, in stop order (BR-44.9). */
  stops?: WorkOrderStop[];
}

export interface NewDowntime {
  from: string;
  to: string;
  resourceId?: string;
}

export type TriageAction = "TRIAGE" | "REJECT" | "DEFER";

export interface Consequences {
  safety: number;
  environment: number;
  production: number;
  quality: number;
  cost: number;
}

export interface CriticalityAssessment {
  id: string;
  assetId: string;
  version: number;
  consequence: Consequences;
  likelihood: number;
  riskScore: number;
  criticality: number;
  assessedBy?: string;
  assessedAt: string;
}

export interface NewWarranty {
  warrantyType: string;
  vendorPartyId: string;
  startBasis: "PURCHASE" | "INSTALL";
  startsOn: string;
  months: number;
  partsCovered: boolean;
  laborCovered: boolean;
  exclusions?: string[];
}

export interface Warranty extends NewWarranty {
  id: string;
  assetId: string;
  endsOn: string;
  exclusions: string[];
  status: "ACTIVE" | "VOIDED";
  createdAt: string;
}

export interface FaultCode {
  code: string;
  text: string;
}

export interface FaultCombination {
  symptom: string;
  area: string;
  type: string;
}

export interface FaultCatalogInput {
  symptoms: FaultCode[];
  areas: FaultCode[];
  types: FaultCode[];
  causes: FaultCode[];
  remedies: FaultCode[];
  allowedCombinations: FaultCombination[];
}

export interface FaultCatalogSummary {
  assetType: string;
  version: number;
  codes: number;
  combinations: number;
  updatedAt: string;
}

export interface FaultCatalog extends FaultCatalogInput {
  assetType: string;
  version: number;
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
  const res = await apiFetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
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

export function changeLifecycle(assetId: string, to: "INACTIVE" | "DISPOSED", version: number, key = newKey()): Promise<Asset> {
  return post<Asset>(`/assets/${id(assetId)}/lifecycle`, { to }, key, { "If-Match": `"${version}"` });
}

export async function listAssessments(assetId: string, signal?: AbortSignal): Promise<CriticalityAssessment[]> {
  return (await page<CriticalityAssessment>(`/assets/${id(assetId)}/criticality`, signal)).items;
}

export function assessCriticality(assetId: string, consequence: Consequences, likelihood: number, key: string): Promise<CriticalityAssessment> {
  return post<CriticalityAssessment>(`/assets/${id(assetId)}/criticality`, { consequence, likelihood }, key);
}

export async function listWarranties(assetId: string, signal?: AbortSignal): Promise<Warranty[]> {
  return (await page<Warranty>(`/assets/${id(assetId)}/warranties`, signal)).items;
}

export function createWarranty(assetId: string, input: NewWarranty, key: string): Promise<Warranty> {
  return post<Warranty>(`/assets/${id(assetId)}/warranties`, input, key);
}

export async function listFaultCatalogs(signal?: AbortSignal): Promise<FaultCatalogSummary[]> {
  return (await page<FaultCatalogSummary>("/fault-catalogs", signal)).items;
}

/** The catalog of an asset type, or null when the type has none yet (404). */
export async function getFaultCatalog(assetType: string, signal?: AbortSignal): Promise<FaultCatalog | null> {
  try {
    return await send<FaultCatalog>(`/fault-catalogs/${id(assetType)}`, {}, signal);
  } catch (error) {
    if (error instanceof EamApiError && error.status === 404) return null;
    throw error;
  }
}

/** Replaces a catalog; version is the one read (0 for a new catalog). */
export function putFaultCatalog(assetType: string, input: FaultCatalogInput, version: number): Promise<FaultCatalog> {
  return send<FaultCatalog>(`/fault-catalogs/${id(assetType)}`, {
    method: "PUT",
    body: JSON.stringify(input),
    headers: { "If-Match": `"${version}"` },
  });
}

export function registerFault(workOrderId: string, input: Fault & { notes?: string }, key: string): Promise<WorkOrderDetail> {
  return post<WorkOrderDetail>(`/work-orders/${id(workOrderId)}/faults`, input, key);
}

export function recordDowntime(workOrderId: string, input: NewDowntime, key: string): Promise<WorkOrderDetail> {
  return post<WorkOrderDetail>(`/work-orders/${id(workOrderId)}/downtime`, input, key);
}

export function triageRequest(requestId: string, action: TriageAction, note: string, key = newKey()): Promise<MaintenanceRequest> {
  return post<MaintenanceRequest>(`/requests/${id(requestId)}/triage`, note ? { action, note } : { action }, key);
}

// Slice 3: permits to work and lockout/tagout (FR-44.19, BR-44.11).

export type PermitStatus = "REQUESTED" | "APPROVED" | "ISSUED" | "SUSPENDED" | "CLOSED" | "CANCELED";
export type EnergyType = "ELECTRICAL" | "PNEUMATIC" | "HYDRAULIC" | "MECHANICAL" | "THERMAL" | "CHEMICAL";
export type LockAction = "APPLY" | "VERIFY" | "REMOVE";
export type LockState = "APPLIED" | "VERIFIED" | "REMOVED";

export interface PermitType {
  code: string;
  name: string;
  approverRole: string;
  maxValidityHours: number;
  requiresIsolation: boolean;
  requiresGasTest: boolean;
}

export interface JobPermits {
  jobType: string;
  permitTypeCodes: string[];
}

export interface NewIsolationPoint {
  energyType: EnergyType;
  location: string;
  method: string;
  verificationStep: string;
  tagCode: string;
}

export interface IsolationPoint extends NewIsolationPoint {
  id: string;
  assetId: string;
  createdAt: string;
}

export interface Permit {
  id: string;
  number: string;
  permitTypeCode: string;
  workOrderId: string;
  assetId: string;
  status: PermitStatus;
  validFrom: string;
  validTo: string;
  requestedBy: string;
  approvedBy?: string;
  issuedBy?: string;
  closedBy?: string;
  locksOpen: number;
  version: number;
  createdAt: string;
}

export interface NewPermit {
  workOrderId: string;
  permitTypeCode: string;
  validFrom: string;
  validTo: string;
}

export interface LotoLock {
  id: string;
  permitId: string;
  isolationPointId: string;
  technicianId: string;
  lockNumber: string;
  state: LockState;
  appliedAt: string;
  verifiedAt?: string;
  removedAt?: string;
}

export interface PermitDetail {
  permit: Permit;
  isolationPoints: IsolationPoint[];
  locks: LotoLock[];
  unlockedPoints: number;
  history: StatusChange[];
}

export interface ExpiredPermit {
  permitId: string;
  number: string;
  workOrderId: string;
  validTo: string;
  workOrderInProgress: boolean;
  code: string;
}

export interface PermitFilter extends ListFilter {
  workOrder?: string;
}

export async function listPermitTypes(signal?: AbortSignal): Promise<PermitType[]> {
  return (await page<PermitType>("/permit-types", signal)).items;
}

/** Creates or replaces a permit type; saving the same definition again changes nothing. */
export function upsertPermitType(input: PermitType, key = newKey()): Promise<PermitType> {
  return post<PermitType>("/permit-types", input, key);
}

export function getJobPermits(jobType: string, signal?: AbortSignal): Promise<JobPermits> {
  return send<JobPermits>(`/job-types/${id(jobType)}/permits`, {}, signal);
}

/** Replaces the permit types a job type requires; an empty list clears the requirement. */
export function setJobPermits(jobType: string, permitTypeCodes: string[]): Promise<JobPermits> {
  return send<JobPermits>(`/job-types/${id(jobType)}/permits`, { method: "PUT", body: JSON.stringify({ permitTypeCodes }) });
}

export async function listIsolationPoints(assetId: string, signal?: AbortSignal): Promise<IsolationPoint[]> {
  return (await page<IsolationPoint>(`/assets/${id(assetId)}/isolation-points`, signal)).items;
}

export function createIsolationPoint(assetId: string, input: NewIsolationPoint, key: string): Promise<IsolationPoint> {
  return post<IsolationPoint>(`/assets/${id(assetId)}/isolation-points`, input, key);
}

export function listPermits(filter: PermitFilter = {}, signal?: AbortSignal): Promise<Page<Permit>> {
  const q = query({
    status: filter.status,
    asset: filter.asset,
    workOrder: filter.workOrder,
    cursor: filter.cursor,
    limit: filter.limit === undefined ? undefined : String(filter.limit),
  });
  return page<Permit>(`/permits${q}`, signal);
}

export function getPermit(permitId: string, signal?: AbortSignal): Promise<PermitDetail> {
  return send<PermitDetail>(`/permits/${id(permitId)}`, {}, signal);
}

export function requestPermit(input: NewPermit, key: string): Promise<Permit> {
  return post<Permit>("/permits", input, key);
}

/** Approve, issue, suspend, close or cancel; version is the permit version read (If-Match). */
export function transitionPermit(permitId: string, to: Exclude<PermitStatus, "REQUESTED">, version: number, key = newKey()): Promise<Permit> {
  return post<Permit>(`/permits/${id(permitId)}/transition`, { to }, key, { "If-Match": `"${version}"` });
}

/** Applies, verifies or removes the caller's own personal lock on an isolation point. */
export function recordLock(permitId: string, action: LockAction, isolationPointId: string, key = newKey()): Promise<LotoLock> {
  return post<LotoLock>(`/permits/${id(permitId)}/locks`, { action, isolationPointId }, key);
}

/** Runs the permit expiry check: ISSUED permits past their validity are suspended (EAM-PMT-004). */
export async function expirePermits(key = newKey()): Promise<ExpiredPermit[]> {
  return (await post<{ items: ExpiredPermit[] }>("/permits/expire", undefined, key)).items;
}

// Slice 4: functional locations (FR-44.1, FR-44.2) and maintenance sequences (FR-44.15).

/** A node of a site's plant structure; path joins the codes from the root with "/". */
export interface FunctionalLocation {
  id: string;
  code: string;
  name: string;
  siteId: string;
  parentId?: string;
  locationType?: string;
  depth: number;
  path: string;
  createdAt: string;
}

export interface NewLocation {
  code: string;
  name: string;
  siteId: string;
  parentId?: string;
  locationType?: string;
}

export interface LocationDetail extends FunctionalLocation {
  children: FunctionalLocation[];
  assets: Asset[];
}

export type LocationMove = "INSTALL" | "MOVE" | "REMOVE";

export interface LocationHistory {
  id: string;
  assetId: string;
  action: LocationMove;
  fromLocationId?: string;
  fromPath?: string;
  toLocationId?: string;
  toPath?: string;
  actor: string;
  at: string;
}

export interface LocationFilter {
  site?: string;
  under?: string;
  cursor?: string;
  limit?: number;
}

export interface SequenceStep {
  code: string;
  jobType: string;
  multiple: number;
  hierarchy: number;
  includes?: string[];
}

export interface MaintenanceSequence {
  id: string;
  code: string;
  name: string;
  assetId?: string;
  assetType?: string;
  baseIntervalValue: number;
  baseIntervalUnit?: IntervalUnit;
  counterId?: string;
  steps: SequenceStep[];
  lastOccurrenceNumber: number;
  version: number;
  createdAt: string;
}

export interface NewSequence {
  code: string;
  name: string;
  assetId?: string;
  assetType?: string;
  baseIntervalValue: number;
  baseIntervalUnit?: IntervalUnit;
  counterId?: string;
  steps: SequenceStep[];
}

export interface SimulatedOccurrence {
  occurrenceNumber: number;
  stepCodes: string[];
  jobTypes: string[];
}

export interface SequenceOccurrence extends SimulatedOccurrence {
  id: string;
  sequenceId: string;
  workOrderId?: string;
  actor: string;
  createdAt: string;
}

export interface NewOccurrence {
  currency: string;
  dueAt?: string;
}

export function listLocations(filter: LocationFilter = {}, signal?: AbortSignal): Promise<Page<FunctionalLocation>> {
  const q = query({ site: filter.site, under: filter.under, cursor: filter.cursor, limit: filter.limit === undefined ? undefined : String(filter.limit) });
  return page<FunctionalLocation>(`/functional-locations${q}`, signal);
}

export function getLocation(locationId: string, signal?: AbortSignal): Promise<LocationDetail> {
  return send<LocationDetail>(`/functional-locations/${id(locationId)}`, {}, signal);
}

export function createLocation(input: NewLocation, key: string): Promise<FunctionalLocation> {
  return post<FunctionalLocation>("/functional-locations", input, key);
}

/** Installs or moves the asset to a location, or removes it (null); version is the asset version read (If-Match). */
export function placeAsset(assetId: string, functionalLocationId: string | null, version: number, key = newKey()): Promise<Asset> {
  return post<Asset>(`/assets/${id(assetId)}/location`, { functionalLocationId }, key, { "If-Match": `"${version}"` });
}

export async function listLocationHistory(assetId: string, signal?: AbortSignal): Promise<LocationHistory[]> {
  return (await page<LocationHistory>(`/assets/${id(assetId)}/location-history`, signal)).items;
}

export function listSequences(filter: ListFilter = {}, signal?: AbortSignal): Promise<Page<MaintenanceSequence>> {
  return page<MaintenanceSequence>(`/sequences${listQuery(filter)}`, signal);
}

export function getSequence(sequenceId: string, signal?: AbortSignal): Promise<MaintenanceSequence> {
  return send<MaintenanceSequence>(`/sequences/${id(sequenceId)}`, {}, signal);
}

export function createSequence(input: NewSequence, key: string): Promise<MaintenanceSequence> {
  return post<MaintenanceSequence>("/sequences", input, key);
}

/** The next occurrences and the steps BR-44.10 selects, without generating them. */
export async function simulateSequence(sequenceId: string, occurrences = 12, signal?: AbortSignal): Promise<SimulatedOccurrence[]> {
  return (await page<SimulatedOccurrence>(`/sequences/${id(sequenceId)}/simulation?occurrences=${occurrences}`, signal)).items;
}

/** Generates the next occurrence of an asset sequence; it opens a preventive work order for the selected step. */
export function generateOccurrence(sequenceId: string, input: NewOccurrence, key: string): Promise<SequenceOccurrence> {
  return post<SequenceOccurrence>(`/sequences/${id(sequenceId)}/occurrences`, input, key);
}

export async function listOccurrences(sequenceId: string, signal?: AbortSignal): Promise<SequenceOccurrence[]> {
  return (await page<SequenceOccurrence>(`/sequences/${id(sequenceId)}/occurrences`, signal)).items;
}

// Slice 5: maintenance rounds (FR-44.14, BR-44.9), reminders (BR-44.7, BR-44.15), cost and backlog KPIs (FR-44.13).

export type StopOutcome = "DONE" | "SKIPPED";

export interface RoundStop {
  sequence: number;
  assetId: string;
  jobType: string;
  mandatory: boolean;
  travelMinutes: number;
}

export interface MaintenanceRound {
  id: string;
  code: string;
  name: string;
  siteId: string;
  active: boolean;
  stops: RoundStop[];
  travelMinutes: number;
  createdAt: string;
}

export interface NewRound {
  code: string;
  name: string;
  siteId: string;
  stops: RoundStop[];
}

export interface WorkOrderStop extends RoundStop {
  outcome?: StopOutcome;
  reason?: string;
  requestId?: string;
  recordedBy?: string;
  recordedAt?: string;
}

export interface RegularizationAlert extends WorkOrder {
  code: string;
  detail: string;
}

export interface PlanReview {
  assetId: string;
  assetNumber: string;
  name: string;
  criticality: number;
  lastReviewedAt?: string;
  dueAt?: string;
}

export interface AssetCost {
  assetId: string;
  assetNumber: string;
  name: string;
  assetType: string;
  currency: string;
  workOrders: number;
  partsCost: string;
  laborCost: string;
  total: string;
}

export interface BacklogRow {
  priority: number;
  openCount: number;
  overdueCount: number;
  oldestDueAt?: string;
}

export function listRounds(filter: ListFilter = {}, signal?: AbortSignal): Promise<Page<MaintenanceRound>> {
  return page<MaintenanceRound>(`/rounds${listQuery(filter)}`, signal);
}

export function getRound(roundId: string, signal?: AbortSignal): Promise<MaintenanceRound> {
  return send<MaintenanceRound>(`/rounds/${id(roundId)}`, {}, signal);
}

export function createRound(input: NewRound, key: string): Promise<MaintenanceRound> {
  return post<MaintenanceRound>("/rounds", input, key);
}

/** Opens a ROUND work order for one extra occurrence of the round; ROUND plan lines create the scheduled ones. */
export function createRoundWorkOrder(roundId: string, input: NewOccurrence, key: string): Promise<WorkOrder> {
  return post<WorkOrder>(`/rounds/${id(roundId)}/work-orders`, input, key);
}

/** Records one stop of an in-progress round as DONE or SKIPPED (a skip needs a reason). */
export function recordStopOutcome(workOrderId: string, sequence: number, outcome: StopOutcome, reason: string, key = newKey()): Promise<WorkOrderStop> {
  return post<WorkOrderStop>(`/work-orders/${id(workOrderId)}/stops/${sequence}`, reason ? { outcome, reason } : { outcome }, key);
}

export async function listEmergencyOverdue(hours = 24, signal?: AbortSignal): Promise<RegularizationAlert[]> {
  return (await page<RegularizationAlert>(`/work-orders/emergency-overdue?hours=${hours}`, signal)).items;
}

export async function listPlanReviews(signal?: AbortSignal): Promise<PlanReview[]> {
  return (await page<PlanReview>("/assets/plan-reviews", signal)).items;
}

export async function costByAsset(from: string, to: string, assetType = "", signal?: AbortSignal): Promise<AssetCost[]> {
  return (await page<AssetCost>(`/kpis/cost${query({ from, to, assetType })}`, signal)).items;
}

export async function backlog(signal?: AbortSignal): Promise<BacklogRow[]> {
  return (await page<BacklogRow>("/kpis/backlog", signal)).items;
}
