import { orgFetch, type OperatingUnit, type Site, type Warehouse } from "./client";
import { referenceFetch } from "./reference";

// Writes for operating units, sites and warehouses. Operating units are tenant-level (no legal-entity policy), so
// they carry no scope header. Sites and warehouses sit behind the legal-entity row-level security policy, and the
// service drops the all-scope duty on every write, so each site or warehouse call names the row's own legal entity
// in X-Passage-LegalEntity; without it the service sees no site rows and refuses the insert.

export type OperatingUnitType = OperatingUnit["type"];
export const operatingUnitTypes: readonly OperatingUnitType[] = ["department", "cost_center", "business_unit", "value_stream"];
export type WarehouseType = Warehouse["type"];
export const warehouseTypes: readonly WarehouseType[] = ["standard", "transit", "quarantine", "subcontractor"];
export type RowStatus = "active" | "inactive";

export interface CreateOperatingUnitInput {
  type: OperatingUnitType;
  code: string;
  name: string;
  defaultLegalEntityId?: string;
}

export interface CreateSiteInput {
  legalEntityId: string;
  code: string;
  name: string;
  timeZone?: string;
}

export interface CreateWarehouseInput {
  siteId: string;
  code: string;
  name: string;
  type: WarehouseType;
  timeZone?: string;
}

const post = <T>(path: string, input: object) => referenceFetch<T>(path, { method: "POST", body: JSON.stringify(input) });
const patch = <T>(path: string, id: string, input: object) =>
  referenceFetch<T>(`${path}/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(input) });
const get = <T>(path: string, id: string) => referenceFetch<T>(`${path}/${encodeURIComponent(id)}`);

const jsonHeaders = (method: string): Record<string, string> =>
  method === "POST" ? { "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() } : { "Content-Type": "application/json" };
const scopedPost = <T>(legalEntityId: string, path: string, input: object) =>
  orgFetch<T>(path, { method: "POST", headers: jsonHeaders("POST"), body: JSON.stringify(input) }, legalEntityId);
const scopedPatch = <T>(legalEntityId: string, path: string, id: string, input: object) =>
  orgFetch<T>(`${path}/${encodeURIComponent(id)}`, { method: "PATCH", headers: jsonHeaders("PATCH"), body: JSON.stringify(input) }, legalEntityId);
const scopedGet = <T>(legalEntityId: string, path: string, id: string) => orgFetch<T>(`${path}/${encodeURIComponent(id)}`, {}, legalEntityId);

export const createOperatingUnit = (input: CreateOperatingUnitInput) => post<OperatingUnit>("/operating-units", input);
export const getOperatingUnit = (id: string) => get<OperatingUnit>("/operating-units", id);
export const setOperatingUnitStatus = (id: string, version: number, status: RowStatus) =>
  patch<OperatingUnit>("/operating-units", id, { version, status });

export const createSite = (input: CreateSiteInput) => scopedPost<Site>(input.legalEntityId, "/sites", input);
export const getSite = (legalEntityId: string, id: string) => scopedGet<Site>(legalEntityId, "/sites", id);
export const setSiteStatus = (legalEntityId: string, id: string, version: number, status: RowStatus) =>
  scopedPatch<Site>(legalEntityId, "/sites", id, { version, status });

// legalEntityId is the chosen site's legal entity; the body names only the site, as the service derives the rest.
export const createWarehouse = (legalEntityId: string, input: CreateWarehouseInput) => scopedPost<Warehouse>(legalEntityId, "/warehouses", input);
export const getWarehouse = (legalEntityId: string, id: string) => scopedGet<Warehouse>(legalEntityId, "/warehouses", id);
export const setWarehouseStatus = (legalEntityId: string, id: string, version: number, status: RowStatus) =>
  scopedPatch<Warehouse>(legalEntityId, "/warehouses", id, { version, status });
