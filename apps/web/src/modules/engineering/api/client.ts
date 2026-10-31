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
}

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
}

export interface BomVersionInput {
  productId: string;
  siteId?: string;
  effectiveFrom: string;
  effectiveTo?: string;
  status?: string;
  lines: BomLine[];
}

export interface BomVersionFilters {
  productId?: string;
  siteId?: string;
  status?: string;
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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = init ? await fetch(`${base}${path}`, init) : await fetch(`${base}${path}`);
  if (!res.ok) {
    const problem: { code?: unknown; title?: unknown; detail?: unknown; errors?: unknown } = await res.json().catch(() => ({}));
    // The service puts the readable reason in detail; title is only the HTTP status text.
    const message = typeof problem.detail === "string" && problem.detail ? problem.detail : typeof problem.title === "string" ? problem.title : "Request failed";
    throw new ApiError(message, res.status, typeof problem.code === "string" ? problem.code : undefined, fieldErrors(problem.errors));
  }
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
