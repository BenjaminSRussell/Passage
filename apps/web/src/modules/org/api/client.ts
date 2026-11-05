export const base = "/api/v1/org";

export const ALL = "ALL";

export type Scope = string;

export interface Problem {
  code: string;
  message: string;
}

export class OrgApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "OrgApiError";
  }
}

export interface LegalEntity {
  id: string;
  code: string;
  name: string;
  country: string;
  currency: string;
  timeZone: string;
  defaultLanguage: string;
  status: "active" | "inactive";
  version: number;
  createdAt: string;
  updatedAt: string;
  fiscalCalendarId?: string;
  primaryAddressId?: string;
  registrationNumber?: string;
}

export interface OperatingUnit {
  id: string;
  type: "department" | "cost_center" | "business_unit" | "value_stream";
  code: string;
  name: string;
  defaultLegalEntityId?: string;
  managerPartyId?: string;
  status: string;
  version: number;
}

export interface Site {
  id: string;
  addressId?: string;
  legalEntityId: string;
  code: string;
  name: string;
  timeZone: string;
  status: string;
  defaultWarehouseId?: string;
  fiscalCalendarId?: string;
  version: number;
}

export interface Warehouse {
  id: string;
  addressId?: string;
  legalEntityId: string;
  siteId: string;
  code: string;
  name: string;
  type: "standard" | "transit" | "quarantine" | "subcontractor";
  timeZone: string;
  status: string;
  version: number;
}

export interface Location {
  id: string;
  legalEntityId: string;
  warehouseId: string;
  code: string;
  name: string;
  type: "storage" | "receiving" | "shipping" | "quarantine";
  status: string;
  addressId?: string;
  version: number;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export interface ListOptions {
  pageSize?: number;
  signal?: AbortSignal;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof OrgApiError) {
    return { code: error.code, message: error.message };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed" };
}

export async function toError(res: Response): Promise<OrgApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : "COM-0001";
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (${res.status})`;
  return new OrgApiError(res.status, code, message);
}

export async function orgFetch<T>(path: string, init: RequestInit, scope: Scope): Promise<T> {
  if (typeof scope !== "string" || scope === "") {
    throw new OrgApiError(0, "COM-0001", "A legal-entity scope is required");
  }
  const headers = new Headers(init.headers);
  if (scope === ALL) {
    if ((init.method ?? "GET").toUpperCase() !== "GET") {
      throw new OrgApiError(0, "COM-0001", "The ALL scope is read-only");
    }
    headers.set("X-Passage-LegalEntity-Scope", ALL);
  } else {
    headers.set("X-Passage-LegalEntity", scope);
  }
  const res = await fetch(`${base}${path}`, { ...init, headers });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

function clampPageSize(value: unknown): number {
  const size = Number(value);
  return Number.isNaN(size) ? 100 : Math.min(100, Math.max(1, Math.floor(size)));
}

async function listAll<T>(
  path: string,
  scope: Scope,
  filters: Record<string, string | undefined>,
  { pageSize, signal }: ListOptions = {},
): Promise<Page<T>> {
  const items: T[] = [];
  let cursor: string | undefined;
  do {
    const params = new URLSearchParams({ limit: String(clampPageSize(pageSize ?? 100)) });
    for (const [key, value] of Object.entries(filters)) {
      if (value) {
        params.set(key, value);
      }
    }
    if (cursor) {
      params.set("cursor", cursor);
    }
    const page = await orgFetch<Partial<Page<T>>>(`${path}?${params}`, { signal }, scope);
    if (Array.isArray(page?.items)) {
      items.push(...page.items);
    }
    const next = page?.nextCursor;
    cursor = typeof next === "string" && next !== "" && next !== cursor ? next : undefined;
  } while (cursor);
  return { items };
}

const entityFilter = (scope: Scope) => ({ legalEntityId: scope === ALL ? undefined : scope });

export const listLegalEntities = (scope: Scope, options?: ListOptions) =>
  listAll<LegalEntity>("/legal-entities", scope, {}, options);

export const listOperatingUnits = (scope: Scope, options?: ListOptions) =>
  listAll<OperatingUnit>("/operating-units", scope, {}, options);

export const listSites = (scope: Scope, options?: ListOptions) =>
  listAll<Site>("/sites", scope, entityFilter(scope), options);

export const listWarehouses = (scope: Scope, options?: ListOptions) =>
  listAll<Warehouse>("/warehouses", scope, entityFilter(scope), options);

export const listLocations = (scope: Scope, options?: ListOptions) =>
  listAll<Location>("/locations", scope, entityFilter(scope), options);
