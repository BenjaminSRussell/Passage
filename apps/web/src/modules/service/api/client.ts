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

// --- Installed base (FR-45.1, FR-45.2) ---

export const WARRANTY_TRIGGERS = ["SHIP", "INSTALL", "REGISTRATION"] as const;
export type WarrantyTrigger = (typeof WARRANTY_TRIGGERS)[number];

export const OBJECT_STATUSES = ["ACTIVE", "INACTIVE", "RETURNED"] as const;
export type ObjectStatus = (typeof OBJECT_STATUSES)[number];

export interface WarrantyTerm {
  startTrigger: WarrantyTrigger;
  /** 0 to 240; 0 means no warranty. */
  months: number;
  partsCovered: boolean;
  laborCovered: boolean;
  travelCovered: boolean;
}

export interface ServiceObjectInput {
  itemId: string;
  serial: string;
  customerPartyId: string;
  legalEntityId: string;
  siteAddressId?: string | null;
  parentId?: string | null;
  shippedOn?: string | null;
  installedOn?: string | null;
  warranty: WarrantyTerm;
}

export interface ServiceObject extends Omit<ServiceObjectInput, "siteAddressId" | "parentId" | "shippedOn" | "installedOn"> {
  id: string;
  siteAddressId?: string;
  parentId?: string;
  shipmentId?: string;
  shippedOn?: string;
  installedOn?: string;
  status: ObjectStatus;
  /** Absent until the trigger date is known (an INSTALL warranty before installation). */
  warrantyStartsOn?: string;
  /** The first day the warranty no longer covers. */
  warrantyEndsOn?: string;
  createdAt: string;
  version: number;
}

export interface ShipmentInput {
  shipmentId: string;
  shipDate: string;
  customerPartyId: string;
  legalEntityId: string;
  siteAddressId?: string | null;
  lines: { itemId: string; serials: string[]; warranty: WarrantyTerm }[];
}

export interface ShipmentReceipt {
  created: number;
  existing: number;
}

export interface ObjectFilter {
  customerPartyId?: string;
  serial?: string;
  parentId?: string;
  status?: ObjectStatus | "";
  cursor?: string;
}

export function listServiceObjects(f: ObjectFilter = {}, signal?: AbortSignal): Promise<Page<ServiceObject>> {
  return json<Page<ServiceObject>>(
    `/service-objects${query({ customerPartyId: f.customerPartyId, serial: f.serial, parentId: f.parentId, status: f.status, cursor: f.cursor })}`,
    {},
    signal,
  );
}

export function getServiceObject(objectId: string, signal?: AbortSignal): Promise<Versioned<ServiceObject>> {
  return versioned<ServiceObject>(`/service-objects/${id(objectId)}`, {}, signal);
}

export function createServiceObject(input: ServiceObjectInput, key: string = newKey()): Promise<Versioned<ServiceObject>> {
  return versioned<ServiceObject>("/service-objects", post(input, key));
}

/** Creates one service object per shipped serial; a re-delivered shipment creates nothing twice (AC-45.8). */
export function receiveShipment(input: ShipmentInput): Promise<ShipmentReceipt> {
  return json<ShipmentReceipt>("/service-objects:receive-shipment", { method: "POST", body: JSON.stringify(input) });
}

/** Records the installation day; an INSTALL warranty starts that day. */
export function installServiceObject(objectId: string, etag: string, installedOn: string): Promise<Versioned<ServiceObject>> {
  return versioned<ServiceObject>(`/service-objects/${id(objectId)}:install`, {
    method: "POST",
    body: JSON.stringify({ installedOn }),
    headers: { "If-Match": etag },
  });
}

// --- Activities (FR-45.14) ---

export const ACTIVITY_KINDS = ["TASK", "APPOINTMENT", "CALL_NOTE", "EMAIL_NOTE"] as const;
export type ActivityKind = (typeof ACTIVITY_KINDS)[number];

export const ACTIVITY_STATUSES = ["OPEN", "DONE", "CANCELED"] as const;
export type ActivityStatus = (typeof ACTIVITY_STATUSES)[number];

/** Record types an activity can be attached to (internal/domain/activity.go). */
export const ASSOCIABLE_TYPES = [
  "case",
  "work_order",
  "agreement",
  "service_object",
  "knowledge_article",
  "project",
  "customer",
  "vendor",
  "worker",
  "invoice",
  "item",
  "asset",
  "audit_finding",
] as const;
export type AssociableType = (typeof ASSOCIABLE_TYPES)[number];

export interface Association {
  entityType: AssociableType;
  entityId: string;
}

export interface ActivitySchedule {
  dueAt?: string | null;
  startAt?: string | null;
  endAt?: string | null;
}

export interface ActivityInput extends ActivitySchedule {
  kind: ActivityKind;
  subject: string;
  ownerWorkerId: string;
  reminderMinutes?: number | null;
  associations: Association[];
}

export interface Activity {
  id: string;
  kind: ActivityKind;
  subject: string;
  ownerWorkerId: string;
  dueAt?: string;
  startAt?: string;
  endAt?: string;
  status: ActivityStatus;
  reminderMinutes?: number;
  associations: Association[];
  completedAt?: string;
  createdAt: string;
  version: number;
}

/** "My activities" (owner) or the activities of one record (entityType with entityId). */
export interface ActivityFilter {
  owner?: string;
  entityType?: AssociableType;
  entityId?: string;
  status?: ActivityStatus | "";
  cursor?: string;
}

const activityQuery = (f: ActivityFilter) => query({ owner: f.owner, entityType: f.entityType, entityId: f.entityId, status: f.status, cursor: f.cursor });

export function listActivities(f: ActivityFilter, signal?: AbortSignal): Promise<Page<Activity>> {
  return json<Page<Activity>>(`/activities${activityQuery(f)}`, {}, signal);
}

/** The browser path of the iCalendar export of a filter (RFC 5545). */
export function activityCalendarUrl(f: ActivityFilter): string {
  return `${base}/activities:calendar${activityQuery({ ...f, cursor: undefined })}`;
}

export function getActivity(activityId: string, signal?: AbortSignal): Promise<Versioned<Activity>> {
  return versioned<Activity>(`/activities/${id(activityId)}`, {}, signal);
}

export function createActivity(input: ActivityInput, key: string = newKey()): Promise<Versioned<Activity>> {
  return versioned<Activity>("/activities", post(input, key));
}

export function completeActivity(activityId: string, etag: string): Promise<Versioned<Activity>> {
  return versioned<Activity>(`/activities/${id(activityId)}:complete`, { method: "POST", headers: { "If-Match": etag } });
}

export function cancelActivity(activityId: string, etag: string): Promise<Versioned<Activity>> {
  return versioned<Activity>(`/activities/${id(activityId)}:cancel`, { method: "POST", headers: { "If-Match": etag } });
}

export function rescheduleActivity(activityId: string, etag: string, when: ActivitySchedule): Promise<Versioned<Activity>> {
  return versioned<Activity>(`/activities/${id(activityId)}:reschedule`, { method: "POST", body: JSON.stringify(when), headers: { "If-Match": etag } });
}

// --- Knowledge base (FR-45.13, BR-45.11) ---

export const ARTICLE_STATUSES = ["DRAFT", "IN_REVIEW", "PUBLISHED", "RETIRED"] as const;
export type ArticleStatus = (typeof ARTICLE_STATUSES)[number];

export interface Applicability {
  itemIds: string[];
  itemGroupIds: string[];
  incidentTypeIds: string[];
  assetTypeIds: string[];
  categoryIds: string[];
}

export interface ArticleInput {
  title: string;
  language: string;
  symptom: string;
  cause: string;
  solution: string;
  applicability: Applicability;
}

/** One version of an article; number groups the versions. revision is the ETag. */
export interface Article extends ArticleInput {
  id: string;
  number: string;
  version: number;
  status: ArticleStatus;
  authorId: string;
  reviewerId?: string;
  publishedAt?: string;
  helpfulYes: number;
  helpfulNo: number;
  /** Decimal percent, present from ten votes. */
  helpfulPercent?: Decimal;
  createdAt: string;
  revision: number;
}

export interface PublishResult {
  article: Article;
  retiredVersion?: number;
  /** KB-PUB-002 when publishing retired an earlier version. */
  warnings: string[];
}

export interface Suggestion {
  article: Article;
  score: number;
}

export interface ArticleFilter {
  status?: ArticleStatus | "";
  number?: string;
  cursor?: string;
}

export interface SearchQuery {
  q?: string;
  itemId?: string;
  incidentTypeId?: string;
  lang?: string;
  limit?: number;
}

/** An article read with its ETag, which carries the revision (not the article version number). */
async function revisioned(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Versioned<Article>> {
  const res = await send(path, init, signal);
  const value = (await res.json()) as Article;
  return { value, etag: res.headers.get("ETag") ?? `"${value.revision}"` };
}

export function listArticles(f: ArticleFilter = {}, signal?: AbortSignal): Promise<Page<Article>> {
  return json<Page<Article>>(`/knowledge/articles${query({ status: f.status, number: f.number, cursor: f.cursor })}`, {}, signal);
}

/** Ranks PUBLISHED articles for a description and item (BR-45.11). */
export function searchArticles(q: SearchQuery, signal?: AbortSignal): Promise<{ items: Suggestion[] }> {
  return json<{ items: Suggestion[] }>(
    `/knowledge/articles/search${query({ q: q.q, itemId: q.itemId, incidentTypeId: q.incidentTypeId, lang: q.lang, limit: q.limit })}`,
    {},
    signal,
  );
}

export function getArticle(articleId: string, signal?: AbortSignal): Promise<Versioned<Article>> {
  return revisioned(`/knowledge/articles/${id(articleId)}`, {}, signal);
}

export function createArticle(input: ArticleInput, key: string = newKey()): Promise<Versioned<Article>> {
  return revisioned("/knowledge/articles", post(input, key));
}

export function updateArticle(articleId: string, etag: string, input: ArticleInput): Promise<Versioned<Article>> {
  return revisioned(`/knowledge/articles/${id(articleId)}`, { method: "PUT", body: JSON.stringify(input), headers: { "If-Match": etag } });
}

export type ArticleMove = "submit" | "reject" | "retire" | "new-version";

export function moveArticle(articleId: string, etag: string, move: ArticleMove): Promise<Versioned<Article>> {
  return revisioned(`/knowledge/articles/${id(articleId)}:${move}`, { method: "POST", headers: { "If-Match": etag } });
}

/** Publishes with the caller as reviewer, who must not be the author (KB-PUB-001). */
export async function publishArticle(articleId: string, etag: string): Promise<{ value: PublishResult; etag: string }> {
  const res = await send(`/knowledge/articles/${id(articleId)}:publish`, { method: "POST", headers: { "If-Match": etag } });
  const value = (await res.json()) as PublishResult;
  return { value, etag: res.headers.get("ETag") ?? `"${value.article.revision}"` };
}

export function voteArticle(articleId: string, helpful: boolean): Promise<Versioned<Article>> {
  return revisioned(`/knowledge/articles/${id(articleId)}:vote`, { method: "POST", body: JSON.stringify({ helpful }) });
}
