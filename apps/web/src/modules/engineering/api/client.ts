const base = "/api/v1/engineering";

export interface UsedBy {
  productId: string;
  versionId: string;
  siteId?: string;
  level: number;
  parents?: UsedBy[];
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

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = init ? await fetch(`${base}${path}`, init) : await fetch(`${base}${path}`);
  if (!res.ok) {
    const problem: { code?: unknown; title?: unknown } = await res.json().catch(() => ({}));
    throw new ApiError(
      typeof problem.title === "string" ? problem.title : "Request failed",
      res.status,
      typeof problem.code === "string" ? problem.code : undefined,
    );
  }
  return res.json();
}

export async function whereUsed(component: string, asOf: string): Promise<UsedBy[]> {
  const query = new URLSearchParams({ component, ...(asOf ? { asOf } : {}) });
  return (await request<{ items: UsedBy[] }>(`/boms:where-used?${query}`)).items;
}

export function listRouteVersions(filters: RouteVersionFilters, cursor?: string): Promise<Page<RouteVersionHeader>> {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries({ ...filters, cursor })) {
    if (value) query.set(key, value);
  }
  const suffix = query.toString() ? `?${query}` : "";
  return request(`/route-versions${suffix}`);
}

export function getRouteVersion(id: string): Promise<RouteVersion> {
  return request(`/route-versions/${encodeURIComponent(id)}`);
}

export function computeLeadTime(body: LeadTimeRequest): Promise<LeadTime> {
  return request("/routes:lead-time", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}
