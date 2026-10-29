// Client for the fieldservice service (services/fieldservice/api/openapi.yaml).
// Browser calls use /api/v1/service/..., which the web BFF rewrites to /v1/service/....
// No npm dependencies: fetch only. Decimals travel as strings, never floats.

export const base = "/api/v1/service";

export const CHANNELS = ["PHONE", "EMAIL", "PORTAL", "IOT", "INTERNAL"] as const;
export type CaseChannel = (typeof CHANNELS)[number];

export const PRIORITIES = [1, 2, 3, 4] as const;
export type Priority = (typeof PRIORITIES)[number];

export const CASE_STATUSES = ["NEW", "IN_PROGRESS", "WAITING_CUSTOMER", "WAITING_PARTS", "RESOLVED", "CLOSED", "CANCELED"] as const;
export type CaseStatus = (typeof CASE_STATUSES)[number];

export const SLA_STATES = ["RUNNING", "PAUSED", "MET", "BREACHED"] as const;
export type SlaState = (typeof SLA_STATES)[number];

export const BILLING_MODELS = ["PREPAID", "PER_VISIT", "SUBSCRIPTION"] as const;
export type BillingModel = (typeof BILLING_MODELS)[number];

export const AGREEMENT_STATUSES = ["DRAFT", "ACTIVE", "SUSPENDED", "EXPIRED", "CANCELED"] as const;
export type AgreementStatus = (typeof AGREEMENT_STATUSES)[number];

export const WO_STATUSES = ["UNSCHEDULED", "SCHEDULED", "DISPATCHED", "TRAVELING", "IN_PROGRESS", "ON_HOLD", "COMPLETED", "POSTED", "CANCELED"] as const;
export type WorkOrderStatus = (typeof WO_STATUSES)[number];

export const LINE_KINDS = ["LABOR", "PART", "TRAVEL", "FEE"] as const;
export type LineKind = (typeof LINE_KINDS)[number];

export type Coverage = "BILLABLE" | "WARRANTY" | "AGREEMENT";

/** Decimal values travel as strings. */
export type Decimal = string;

/**
 * The work-order status machine (BR-45.5), mirrored from internal/domain so a
 * page offers only the moves the service accepts. POSTED is reached through
 * posting, never by hand.
 */
export const NEXT_STATUSES: Record<WorkOrderStatus, WorkOrderStatus[]> = {
  UNSCHEDULED: ["SCHEDULED", "CANCELED"],
  SCHEDULED: ["DISPATCHED", "CANCELED"],
  DISPATCHED: ["TRAVELING", "CANCELED"],
  TRAVELING: ["IN_PROGRESS", "CANCELED"],
  IN_PROGRESS: ["ON_HOLD", "COMPLETED", "CANCELED"],
  ON_HOLD: ["IN_PROGRESS", "CANCELED"],
  COMPLETED: [],
  POSTED: [],
  CANCELED: [],
};

/**
 * The case status machine (FR-45.5), mirrored from internal/domain/case.go.
 * NEW is only the start; CLOSED and CANCELED are final.
 */
export const CASE_NEXT: Record<CaseStatus, CaseStatus[]> = {
  NEW: ["IN_PROGRESS", "WAITING_CUSTOMER", "WAITING_PARTS", "RESOLVED", "CANCELED"],
  IN_PROGRESS: ["WAITING_CUSTOMER", "WAITING_PARTS", "RESOLVED", "CANCELED"],
  WAITING_CUSTOMER: ["IN_PROGRESS", "WAITING_PARTS", "RESOLVED", "CANCELED"],
  WAITING_PARTS: ["IN_PROGRESS", "WAITING_CUSTOMER", "RESOLVED", "CANCELED"],
  RESOLVED: ["CLOSED"],
  CLOSED: [],
  CANCELED: [],
};

/** Statuses that keep a case open, so its SLA clocks run or are paused. */
export const OPEN_CASE_STATUSES: CaseStatus[] = ["NEW", "IN_PROGRESS", "WAITING_CUSTOMER", "WAITING_PARTS"];

/** Work orders that can take a follow-up visit (BR-45.5). */
export const FOLLOW_UP_STATUSES: WorkOrderStatus[] = ["COMPLETED", "POSTED"];

export type SlaAlert = "RESPONSE_WARNING" | "RESPONSE_BREACH" | "RESOLUTION_WARNING" | "RESOLUTION_BREACH";

export interface SlaTarget {
  priority: number;
  responseMinutes: number;
  resolutionMinutes: number;
}

export interface SlaProfileInput {
  code: string;
  name: string;
  timeZone: string;
  allDay: boolean;
  openMinute: number;
  closeMinute: number;
  pauseOnParts: boolean;
  holidays: string[];
  targets: SlaTarget[];
}

export interface SlaProfile extends SlaProfileInput {
  id: string;
}

export interface AgreementLine {
  serviceObjectId?: string;
  categoryId?: string;
  includedVisits: number;
  includedHours: Decimal;
  partsCovered: boolean;
  laborCovered: boolean;
  travelCovered: boolean;
}

export interface AgreementInput {
  customerPartyId: string;
  legalEntityId: string;
  validFrom: string;
  validTo?: string | null;
  slaProfileId: string;
  billingModel: BillingModel;
  priorityRank: number;
  status: "DRAFT" | "ACTIVE";
  lines: AgreementLine[];
}

export interface Agreement extends Omit<AgreementInput, "status"> {
  id: string;
  number: string;
  status: AgreementStatus;
  hoursConsumed: Decimal;
}

export interface OpenCaseInput {
  customerPartyId: string;
  legalEntityId: string;
  serviceObjectId?: string | null;
  channel: CaseChannel;
  priority: Priority;
  description?: string;
  fallbackSlaProfileId?: string | null;
}

export interface ServiceCase {
  id: string;
  number: string;
  customerPartyId: string;
  legalEntityId: string;
  serviceObjectId?: string | null;
  channel: CaseChannel;
  priority: number;
  description: string;
  agreementId?: string | null;
  slaProfileId: string;
  entitlementTrace: string[];
  openedAt: string;
  respondedAt?: string | null;
  resolvedAt?: string | null;
  slaResponseDue: string;
  slaResponseWarnAt: string;
  slaResolutionDue: string;
  slaResolutionWarnAt: string;
  /** Business minutes of closed pauses of the resolution clock. */
  slaPausedMinutes: number;
  /** Start of the open pause, while the case waits. */
  slaPausedAt?: string | null;
  slaState: SlaState;
  /** SLA events already emitted for the case; each fires once. */
  slaAlerts: SlaAlert[];
  status: CaseStatus;
  /** Optimistic-concurrency version, echoed as If-Match on transitions. */
  version: number;
}

export interface WorkLine {
  id?: string;
  kind: LineKind;
  qty: Decimal;
  unitPrice: Decimal;
  unitCost: Decimal;
}

export interface WorkOrderInput {
  customerPartyId: string;
  legalEntityId: string;
  territoryId: string;
  caseId?: string | null;
  agreementId?: string | null;
  incidentTypeId?: string | null;
  estDurationMinutes: number;
  currency: string;
  lines: WorkLine[];
}

export interface PostedLine {
  lineId: string;
  kind: LineKind;
  coverage: Coverage;
  qty: Decimal;
  amount: Decimal;
  warrantyCost: Decimal;
  agreementHours: Decimal;
}

export interface Posting {
  currency: string;
  lines: PostedLine[];
  billableTotal: Decimal;
  warrantyCost: Decimal;
  entitlementConsumed: Decimal;
}

export interface WorkOrder {
  id: string;
  number: string;
  caseId?: string | null;
  agreementId?: string | null;
  customerPartyId: string;
  legalEntityId: string;
  territoryId: string;
  incidentTypeId?: string | null;
  estDurationMinutes: number;
  currency: string;
  status: WorkOrderStatus;
  lines: Required<WorkLine>[];
  posting?: Posting;
  /** The finished order this one follows up (BR-45.5). */
  followUpOf?: string | null;
  completedAt?: string | null;
  /** Set at completion; false once a follow-up was opened. */
  firstTimeFix?: boolean | null;
  version: number;
}

export interface KpiQuery {
  /** RFC 3339, inclusive. */
  from: string;
  /** RFC 3339, exclusive; at most 366 days after from. */
  to: string;
  territory?: string;
}

/** Service measures over a window (FR-45.11). Rates are decimal strings, null when nothing was measured. */
export interface Kpis {
  from: string;
  to: string;
  territory?: string;
  casesOpened: number;
  responseMet: number;
  responseMissed: number;
  responseAttainmentPct: Decimal | null;
  meanResponseMinutes: Decimal | null;
  resolutionMet: number;
  resolutionBreached: number;
  resolutionAttainmentPct: Decimal | null;
  meanResolutionMinutes: Decimal | null;
  workOrdersCompleted: number;
  firstTimeFix: number;
  firstTimeFixPct: Decimal | null;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

/** A value read together with the ETag that If-Match must echo back. */
export interface Versioned<T> {
  value: T;
  etag: string;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class ServiceApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ServiceApiError";
  }
}

export interface Problem {
  status: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ServiceApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<ServiceApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new ServiceApiError(res.status, code, message, fields);
}

async function send(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return res;
}

async function json<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  return (await (await send(path, init, signal)).json()) as T;
}

async function versioned<T extends { version: number }>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Versioned<T>> {
  const res = await send(path, init, signal);
  const value = (await res.json()) as T;
  return { value, etag: res.headers.get("ETag") ?? `"${value.version}"` };
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

/** A fresh Idempotency-Key for one user intent. */
export function newKey(): string {
  return crypto.randomUUID();
}

const post = (body: unknown, key: string): RequestInit => ({ method: "POST", body: JSON.stringify(body), headers: { "Idempotency-Key": key } });
const id = (value: string) => encodeURIComponent(value);

// --- SLA profiles ---

export function listSlaProfiles(signal?: AbortSignal): Promise<Page<SlaProfile>> {
  return json<Page<SlaProfile>>("/sla-profiles", {}, signal);
}

export function createSlaProfile(input: SlaProfileInput, key: string = newKey()): Promise<SlaProfile> {
  return json<SlaProfile>("/sla-profiles", post(input, key));
}

// --- Agreements ---

export interface AgreementFilter {
  status?: AgreementStatus | "";
  customerPartyId?: string;
  cursor?: string;
}

export function listAgreements(f: AgreementFilter = {}, signal?: AbortSignal): Promise<Page<Agreement>> {
  return json<Page<Agreement>>(`/agreements${query({ status: f.status, customerPartyId: f.customerPartyId, cursor: f.cursor })}`, {}, signal);
}

export function getAgreement(agreementId: string, signal?: AbortSignal): Promise<Agreement> {
  return json<Agreement>(`/agreements/${id(agreementId)}`, {}, signal);
}

export function createAgreement(input: AgreementInput, key: string = newKey()): Promise<Agreement> {
  return json<Agreement>("/agreements", post(input, key));
}

// --- Cases ---

export interface CaseFilter {
  status?: CaseStatus | "";
  slaState?: SlaState | "";
  cursor?: string;
}

export function listCases(f: CaseFilter = {}, signal?: AbortSignal): Promise<Page<ServiceCase>> {
  return json<Page<ServiceCase>>(`/cases${query({ status: f.status, slaState: f.slaState, cursor: f.cursor })}`, {}, signal);
}

export function getCase(caseId: string, signal?: AbortSignal): Promise<ServiceCase> {
  return json<ServiceCase>(`/cases/${id(caseId)}`, {}, signal);
}

/** Opens a case: the service resolves the entitlement and starts the SLA clocks (FR-45.5). */
export function openCase(input: OpenCaseInput, key: string = newKey()): Promise<ServiceCase> {
  return json<ServiceCase>("/cases", post(input, key));
}

/**
 * Moves a case along its status machine at the version the caller read,
 * pausing, resuming or stopping its SLA clocks (FR-45.5, BR-45.2). The key
 * makes a retry of the same intent return the first answer.
 */
export function transitionCase(caseId: string, version: number, to: CaseStatus, key: string = newKey()): Promise<ServiceCase> {
  return json<ServiceCase>(`/cases/${id(caseId)}:transition`, {
    method: "POST",
    body: JSON.stringify({ to }),
    headers: { "If-Match": `"${version}"`, "Idempotency-Key": key },
  });
}

/** Service KPIs over a window and optional territory (FR-45.11). */
export function getKpis(q: KpiQuery, signal?: AbortSignal): Promise<Kpis> {
  return json<Kpis>(`/kpis${query({ from: q.from, to: q.to, territory: q.territory })}`, {}, signal);
}

// --- Work orders ---

export interface WorkOrderFilter {
  status?: WorkOrderStatus | "";
  caseId?: string;
  cursor?: string;
}

export function listWorkOrders(f: WorkOrderFilter = {}, signal?: AbortSignal): Promise<Page<WorkOrder>> {
  return json<Page<WorkOrder>>(`/work-orders${query({ status: f.status, caseId: f.caseId, cursor: f.cursor })}`, {}, signal);
}

export function getWorkOrder(workOrderId: string, signal?: AbortSignal): Promise<Versioned<WorkOrder>> {
  return versioned<WorkOrder>(`/work-orders/${id(workOrderId)}`, {}, signal);
}

export function createWorkOrder(input: WorkOrderInput, key: string = newKey()): Promise<Versioned<WorkOrder>> {
  return versioned<WorkOrder>("/work-orders", post(input, key));
}

/** Moves the work order along BR-45.5 at the given ETag. */
export function transitionWorkOrder(workOrderId: string, etag: string, to: WorkOrderStatus): Promise<Versioned<WorkOrder>> {
  return versioned<WorkOrder>(`/work-orders/${id(workOrderId)}:transition`, {
    method: "POST",
    body: JSON.stringify({ to }),
    headers: { "If-Match": etag },
  });
}

/** Posts a COMPLETED work order to billing and warranty cost (FR-45.10, BR-45.4). */
export function postWorkOrder(workOrderId: string, etag: string, key: string = newKey()): Promise<Versioned<WorkOrder>> {
  return versioned<WorkOrder>(`/work-orders/${id(workOrderId)}:post`, { method: "POST", headers: { "If-Match": etag, "Idempotency-Key": key } });
}

/** Opens the follow-up of a COMPLETED or POSTED work order; clears its first-time fix (BR-45.5). */
export function followUpWorkOrder(workOrderId: string, etag: string, key: string = newKey()): Promise<Versioned<WorkOrder>> {
  return versioned<WorkOrder>(`/work-orders/${id(workOrderId)}:follow-up`, { method: "POST", headers: { "If-Match": etag, "Idempotency-Key": key } });
}
