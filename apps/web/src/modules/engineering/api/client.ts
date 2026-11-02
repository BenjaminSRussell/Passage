const base = "/api/v1/engineering";

export interface UsedBy {
  productId: string;
  versionId: string;
  siteId?: string;
  level: number;
  parents?: UsedBy[];
}

export interface BomLine {
  lineNo?: number;
  componentProductId: string;
  quantity: string;
  series?: string;
  lineType?: string;
  scrapConst: string;
  scrapVarPct: string;
  roundUp?: string;
  effectiveFrom?: string;
  effectiveTo?: string;
  /** FR-9.20 alternative item group; a group needs two or more lines and one strategy (BR-9.16). */
  altGroup?: string;
  altStrategy?: string;
  altPriority?: number;
  usageProbabilityPct?: string;
  discontinuationGroup?: string;
  discontinueType?: string;
  followUpGroup?: string;
  followUpComponentId?: string;
  discontinueOn?: string;
}

/** FR-9.19 BOM usages (SAP STLAN 1 to 6); a version without one is production. */
export const bomUsages = ["production", "engineering", "costing", "maintenance", "sales", "universal"];
/** FR-9.19 categories this wave maintains; equipment, functional location, sales order and project BOMs come later. */
export const bomCategories = ["material", "document"];

export interface BomVersion {
  id: string;
  productId: string;
  siteId?: string;
  effectiveFrom: string;
  effectiveTo?: string;
  fromQty?: string;
  toQty?: string;
  status: string;
  lines?: BomLine[];
  /** FR-9.19 usage and category. */
  usage?: string;
  category?: string;
  /** The version this one was copied from (FR-9.12 lineage). */
  copiedFromVersionId?: string;
  /** Optimistic-locking counter of a draft (BR-9.3); send the loaded value back when saving an edit. */
  version?: number;
}

export interface BomVersionInput {
  fromQty?: string;
  toQty?: string;
  productId: string;
  siteId?: string;
  effectiveFrom: string;
  effectiveTo?: string;
  status?: string;
  usage?: string;
  category?: string;
  lines: BomLine[];
}

export interface BomVersionFilters {
  productId?: string;
  siteId?: string;
  status?: string;
  usage?: string;
  category?: string;
  asOf?: string;
}

export interface RouteVersionHeader {
  id: string;
  productId: string;
  siteId: string;
  routeCode: string;
  versionNo: number;
  effectiveFrom: string;
  effectiveTo?: string;
  status: string;
  operationCount: number;
  /** The version this one was copied from (FR-9.12 lineage). */
  copiedFromVersionId?: string;
  /** The optimistic-locking counter (BR-9.3): starts at 1, each edit, activation and cancellation adds 1. */
  version?: number;
}

export interface OperationRequirement {
  kind: string;
  refId: string;
  level?: number;
  quantity?: number;
}

export interface RouteVersionOperation {
  no: number;
  operationId: string;
  nextNo?: number;
  linkType?: string;
  priority?: string;
  setupH?: string;
  runH?: string;
  processQty: string;
  queueBeforeH?: string;
  queueAfterH?: string;
  transitH?: string;
  efficiencyPct?: string;
  overlapQty?: string;
  scrapPercent?: string;
  requirements?: OperationRequirement[];
}

export interface RouteVersion extends RouteVersionHeader {
  operations: RouteVersionOperation[];
}

export interface RouteVersionInput {
  productId: string;
  siteId: string;
  routeCode: string;
  versionNo?: number;
  effectiveFrom: string;
  effectiveTo?: string;
  operations: RouteVersionOperation[];
}

export interface LeadTime {
  hours: string;
  calendarDays: number;
}

export interface LeadTimeRequest {
  operations: Array<{
    no: number;
    next?: number;
    priority?: string;
    setup?: string;
    run?: string;
    processQty: string;
    queueBefore?: string;
    queueAfter?: string;
    transit?: string;
    efficiencyPct?: string;
  }>;
  quantity: string;
  hoursPerDay: string;
}

export interface RouteVersionFilters {
  productId?: string;
  siteId?: string;
  status?: string;
  asOf?: string;
}

export interface ResourceCapability {
  capabilityId: string;
  level: number;
  effectiveFrom: string;
  effectiveTo?: string;
}

export interface Resource {
  id: string;
  code: string;
  name: string;
  type: string;
  siteId: string;
  groupId?: string;
  calendarId?: string;
  efficiencyPct?: string;
  capacityPerDayH?: string;
  exclusive: boolean;
  vendorPartyId?: string;
  capabilities?: ResourceCapability[];
  effectiveCalendarId?: string;
  /** Counts each saved edit; an edit must send the value it loaded. */
  version?: number;
}

export interface ResourceGroup {
  id: string;
  code: string;
  name: string;
  siteId: string;
  calendarId?: string;
}

export interface Capability {
  id: string;
  code: string;
  name: string;
}

export interface CalendarDay {
  date: string;
  open: boolean;
  shifts: Array<{ start: string; end: string }>;
  totalH: string;
}

export interface Shift {
  start: string;
  end: string;
  efficiencyPct?: string;
}

export interface WorkCalendar {
  id: string;
  code: string;
  name: string;
  template: Shift[][];
  closedDates: string[];
  version: number;
  generatedFrom?: string;
  generatedTo?: string;
  warnings?: string[];
}

export interface ResourceFilters {
  siteId?: string;
  type?: string;
  groupId?: string;
  capabilityId?: string;
}

export interface ResourceGroupFilters {
  siteId?: string;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
    /** Per-field validation messages from a 400 COM-0001 problem. */
    readonly errors: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ApiError";
  }
}

function fieldErrors(raw: unknown): Record<string, string> {
  if (!raw || typeof raw !== "object") return {};
  return Object.fromEntries(Object.entries(raw).filter((entry): entry is [string, string] => typeof entry[1] === "string"));
}

const NO_CONTENT = 204;

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = init ? await fetch(`${base}${path}`, init) : await fetch(`${base}${path}`);
  if (!res.ok) {
    const problem: { code?: unknown; title?: unknown; detail?: unknown; errors?: unknown } = await res.json().catch(() => ({}));
    // The service puts the readable reason in detail; title is only the HTTP status text.
    const message = typeof problem.detail === "string" && problem.detail ? problem.detail : typeof problem.title === "string" ? problem.title : "Request failed";
    throw new ApiError(message, res.status, typeof problem.code === "string" ? problem.code : undefined, fieldErrors(problem.errors));
  }
  if (res.status === NO_CONTENT) return undefined as T;
  return res.json();
}

export async function whereUsed(component: string, asOf: string): Promise<UsedBy[]> {
  const query = new URLSearchParams({ component, ...(asOf ? { asOf } : {}) });
  return (await request<{ items: UsedBy[] }>(`/boms:where-used?${query}`)).items;
}

function query(params: object): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value) search.set(key, value);
  }
  return search.toString() ? `?${search}` : "";
}

export function listRouteVersions(filters: RouteVersionFilters, cursor?: string): Promise<Page<RouteVersionHeader>> {
  return request(`/route-versions${query({ ...filters, cursor })}`);
}

export function getRouteVersion(id: string): Promise<RouteVersion> {
  return request(`/route-versions/${encodeURIComponent(id)}`);
}

export function createRouteVersion(body: RouteVersionInput, idempotencyKey: string): Promise<RouteVersion> {
  return request("/route-versions", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

/** Saves an edited draft (BR-9.3): replaces the dates and every operation. A stale `version` is 409 ENG-ROUTE-VERSION-CONFLICT. */
export function updateRouteVersion(id: string, body: RouteVersionInput & { version: number }, idempotencyKey: string): Promise<RouteVersion> {
  return request(`/route-versions/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

export function activateRouteVersion(id: string): Promise<RouteVersion> {
  return request(`/route-versions/${encodeURIComponent(id)}:activate`, { method: "POST" });
}

export function computeLeadTime(body: LeadTimeRequest): Promise<LeadTime> {
  return request("/routes:lead-time", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

export function listResources(filters: ResourceFilters, cursor?: string): Promise<Page<Resource>> {
  return request(`/resources${query({ ...filters, cursor })}`);
}

export function getResource(id: string): Promise<Resource> {
  return request(`/resources/${encodeURIComponent(id)}`);
}

/** Saves an edited resource: replaces its fields and capabilities. A stale `version` is 409 ENG-RES-VERSION-CONFLICT. */
export function updateResource(id: string, body: Resource & { version: number }, idempotencyKey: string): Promise<Resource> {
  return request(`/resources/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

/** Deletes a resource (BR-9.11). 409 ENG-RES-IN-USE while an active route requires it directly. */
export function deleteResource(id: string): Promise<void> {
  return request(`/resources/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export interface ResourceInput {
  code: string;
  name: string;
  type: string;
  siteId: string;
  groupId?: string;
  calendarId?: string;
  efficiencyPct?: string;
  capacityPerDayH?: string;
  exclusive: boolean;
  vendorPartyId?: string;
  capabilities: ResourceCapability[];
}

export interface CalendarInput {
  code: string;
  name: string;
  template: Shift[][];
  closedDates: string[];
}

export interface CalendarGeneration {
  openDays: number;
  totalHours: string;
}

function post<T>(path: string, body: unknown, idempotencyKey?: string): Promise<T> {
  return request(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}) },
    body: JSON.stringify(body),
  });
}

/** Creates a resource with its capabilities. 422 ENG-RES-VENDOR (vendor without a party) and ENG-RES-SITE (group at another site). */
export function createResource(body: ResourceInput, idempotencyKey: string): Promise<Resource> {
  return post("/resources", body, idempotencyKey);
}

export function createResourceGroup(body: { code: string; name: string; siteId: string; calendarId?: string }, idempotencyKey: string): Promise<ResourceGroup> {
  return post("/resource-groups", body, idempotencyKey);
}

export function createCapability(body: { code: string; name: string }, idempotencyKey: string): Promise<Capability> {
  return post("/capabilities", body, idempotencyKey);
}

/** Creates a work calendar from its weekly template (Monday first) and closed dates. 422 ENG-CAL-SHIFT for overlapping or malformed shifts. */
export function createCalendar(body: CalendarInput, idempotencyKey: string): Promise<WorkCalendar> {
  return post("/calendars", body, idempotencyKey);
}

/** Generates the calendar's days for a range; repeating a range upserts the same days. */
export function generateCalendar(id: string, range: { from: string; to: string }): Promise<CalendarGeneration> {
  return post(`/calendars/${encodeURIComponent(id)}:generate`, range);
}

export function listResourceGroups(filters: ResourceGroupFilters, cursor?: string): Promise<Page<ResourceGroup>> {
  return request(`/resource-groups${query({ ...filters, cursor })}`);
}

export function listCapabilities(cursor?: string): Promise<Page<Capability>> {
  return request(`/capabilities${query({ cursor })}`);
}

export function getCalendarDays(id: string, from: string, to: string): Promise<Page<CalendarDay>> {
  return request(`/calendars/${encodeURIComponent(id)}/days${query({ from, to })}`);
}

export function getResourceGroup(id: string): Promise<ResourceGroup> {
  return request(`/resource-groups/${encodeURIComponent(id)}`);
}

export function getCapability(id: string): Promise<Capability> {
  return request(`/capabilities/${encodeURIComponent(id)}`);
}

export function listCalendars(cursor?: string): Promise<Page<WorkCalendar>> {
  return request(`/calendars${query({ cursor })}`);
}

export function getCalendar(id: string): Promise<WorkCalendar> {
  return request(`/calendars/${encodeURIComponent(id)}`);
}

export function listBomVersions(filters: BomVersionFilters, cursor?: string): Promise<Page<BomVersion>> {
  return request(`/bom-versions${query({ ...filters, cursor })}`);
}

export function getBomVersion(id: string): Promise<BomVersion> {
  return request(`/bom-versions/${encodeURIComponent(id)}`);
}

export function createBomVersion(body: BomVersionInput, idempotencyKey: string): Promise<BomVersion> {
  return request("/bom-versions", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

/** Saves an edited draft (BR-9.3): replaces the header and every line. A stale `version` is 409 ENG-BOM-VERSION-CONFLICT. */
export function updateBomVersion(id: string, body: BomVersionInput & { version: number }, idempotencyKey: string): Promise<BomVersion> {
  return request(`/bom-versions/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

export function activateBomVersion(id: string): Promise<BomVersion> {
  return request(`/bom-versions/${encodeURIComponent(id)}:activate`, { method: "POST" });
}

/** Cancels a draft BOM version (BR-9.1). The version is kept with status cancelled. */
export function cancelBomVersion(id: string): Promise<BomVersion> {
  return request(`/bom-versions/${encodeURIComponent(id)}:cancel`, { method: "POST" });
}

/** A copy's effectivity and, optionally, another product or site; omitted fields keep the source's. */
export interface CopyRequest {
  effectiveFrom: string;
  effectiveTo?: string;
  productId?: string;
  siteId?: string;
}

/** A BOM copy can also keep only some source lines; omitted lineNos copies every line. */
export interface BomCopyRequest extends CopyRequest {
  lineNos?: number[];
}

/** Copies a BOM version into a new draft, optionally for another product or site and with chosen lines (FR-9.12). */
export function copyBomVersion(id: string, body: BomCopyRequest, idempotencyKey: string): Promise<BomVersion> {
  return request(`/bom-versions/${encodeURIComponent(id)}:copy`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

/** Cancels a draft route version (BR-9.1). The version is kept with status cancelled. */
export function cancelRouteVersion(id: string): Promise<RouteVersion> {
  return request(`/route-versions/${encodeURIComponent(id)}:cancel`, { method: "POST" });
}

/** Copies a route version into a new draft, optionally for another product or site, with the next version number there. */
export function copyRouteVersion(id: string, body: CopyRequest, idempotencyKey: string): Promise<RouteVersion> {
  return request(`/route-versions/${encodeURIComponent(id)}:copy`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

export interface EcoReplacement {
  oldVersionId: string;
  newVersionId: string;
}

export interface Eco {
  id: string;
  number: string;
  state: string;
  releaseDate?: string;
  decidedBy?: string;
  /** The approved change request this ECO carries out, if any. */
  ecrId?: string;
  replaces: EcoReplacement[];
}

export interface EcoInput {
  number: string;
  releaseDate?: string;
  ecrId?: string;
  replaces: EcoReplacement[];
}

export function listEcos(state: string, cursor?: string): Promise<Page<Eco>> {
  return request(`/ecos${query({ state, cursor })}`);
}

export function getEco(id: string): Promise<Eco> {
  return request(`/ecos/${encodeURIComponent(id)}`);
}

export interface EcoLineChange {
  componentProductId: string;
  change: "added" | "removed" | "changed";
  oldQuantity?: string;
  newQuantity?: string;
}

export interface EcoImpactProduct {
  productId: string;
  reason: "replaced" | "parent";
  level: number;
  topLevel: boolean;
  siteIds: string[];
  versionIds: string[];
}

export interface EcoImpact {
  asOf: string;
  replacements: { oldVersionId: string; newVersionId: string; productId: string; changes: EcoLineChange[] }[];
  products: EcoImpactProduct[];
  siteIds: string[];
  openProductionOrders: number;
  openPlannedOrders: number;
}

export function getEcoImpact(id: string, asOf?: string): Promise<EcoImpact> {
  return request(`/ecos/${encodeURIComponent(id)}/impact${query({ asOf })}`);
}

export function createEco(body: EcoInput, idempotencyKey: string): Promise<Eco> {
  return request("/ecos", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

export function releaseEco(id: string, idempotencyKey: string): Promise<Eco> {
  return request(`/ecos/${encodeURIComponent(id)}:release`, { method: "POST", headers: { "Idempotency-Key": idempotencyKey } });
}

export interface Ecr {
  id: string;
  number: string;
  title: string;
  reason?: string;
  priority: string;
  state: string;
  products: string[];
  requestedBy?: string;
  decidedBy?: string;
  decisionReason?: string;
  /** The live ECO that carries this request out. */
  ecoId?: string;
}

export interface EcrInput {
  number: string;
  title: string;
  reason?: string;
  priority: string;
  products: string[];
}

export type EcrAction = "submit" | "review" | "approve" | "reject" | "cancel";

export function listEcrs(filters: { state?: string; productId?: string }, cursor?: string): Promise<Page<Ecr>> {
  return request(`/ecrs${query({ ...filters, cursor })}`);
}

export function getEcr(id: string): Promise<Ecr> {
  return request(`/ecrs/${encodeURIComponent(id)}`);
}

export function createEcr(body: EcrInput, idempotencyKey: string): Promise<Ecr> {
  return post("/ecrs", body, idempotencyKey);
}

export function actOnEcr(id: string, action: EcrAction, reason?: string): Promise<Ecr> {
  return post(`/ecrs/${encodeURIComponent(id)}:${action}`, reason ? { reason } : {});
}
