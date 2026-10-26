import { base, toError } from "./client";

export type ReferenceStatus = "active" | "inactive";
export type ReferenceKind = "countries" | "currencies" | "regions" | "languages";

export const unitClasses = [
  "quantity",
  "each",
  "mass",
  "length",
  "volume",
  "area",
  "time",
  "temperature",
  "energy",
  "pressure",
  "power",
  "speed",
] as const;

export type UnitClass = (typeof unitClasses)[number];

interface ReferenceRow {
  code: string;
  name: string;
  status: ReferenceStatus;
}

export interface Country extends ReferenceRow {
  regionRequired?: boolean;
  postalRegex?: string;
}

export interface Currency extends ReferenceRow {
  numericCode: string;
  minorUnits: number;
}

export interface Region extends ReferenceRow {
  countryCode: string;
}

export type Language = ReferenceRow;

export interface Unit extends ReferenceRow {
  class: UnitClass;
  decimalPrecision: number;
  system: boolean;
}

export interface UomConversion {
  id: string;
  fromCode: string;
  toCode: string;
  class: UnitClass;
  factor: string;
  offset: string;
  inverseFactor: string;
  inverseOffset: string;
  roundTrip: boolean;
  system: boolean;
  productId?: string | null;
  status: ReferenceStatus;
}

export interface Conversion {
  from: string;
  to: string;
  quantity: string;
  displayQuantity: string;
}

export interface CreateUnitInput {
  code: string;
  name: string;
  class: UnitClass;
  decimalPrecision: number;
}

export interface CreateConversionInput {
  fromCode: string;
  toCode: string;
  factor: string;
  offset?: string | null;
}

interface ListOptions {
  signal?: AbortSignal;
}

export async function referenceFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  if ((init.method ?? "GET").toUpperCase() === "POST") {
    headers.set("Idempotency-Key", crypto.randomUUID());
  }
  const res = await fetch(`${base}${path}`, { ...init, headers });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

export function withQuery(path: string, filters: Record<string, string | undefined>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(filters)) {
    if (value) {
      params.set(key, value);
    }
  }
  const query = params.toString();
  return query ? `${path}?${query}` : path;
}

async function listItems<T>(path: string, filters: Record<string, string | undefined>, options?: ListOptions): Promise<T[]> {
  const body = await referenceFetch<{ items?: unknown }>(withQuery(path, filters), { signal: options?.signal });
  return Array.isArray(body?.items) ? (body.items as T[]) : [];
}

const patchStatus = <T>(path: string, status: ReferenceStatus) =>
  referenceFetch<T>(path, { method: "PATCH", body: JSON.stringify({ status }) });

export const listCountries = (status?: ReferenceStatus, options?: ListOptions) =>
  listItems<Country>("/countries", { status }, options);

export const listCurrencies = (status?: ReferenceStatus, options?: ListOptions) =>
  listItems<Currency>("/currencies", { status }, options);

export const listRegions = (status?: ReferenceStatus, options?: ListOptions & { country?: string }) =>
  listItems<Region>("/regions", { status, country: options?.country }, options);

export const listLanguages = (status?: ReferenceStatus, options?: ListOptions) =>
  listItems<Language>("/languages", { status }, options);

export const setReferenceStatus = <T = ReferenceRow>(kind: ReferenceKind, code: string, status: ReferenceStatus) =>
  patchStatus<T>(`/${kind}/${encodeURIComponent(code)}`, status);

export const listUnits = (filters: { class?: string; status?: ReferenceStatus } = {}, options?: ListOptions) =>
  listItems<Unit>("/uom/units", filters, options);

export const createUnit = (input: CreateUnitInput) =>
  referenceFetch<Unit>("/uom/units", { method: "POST", body: JSON.stringify(input) });

export const setUnitStatus = (code: string, status: ReferenceStatus) =>
  patchStatus<Unit>(`/uom/units/${encodeURIComponent(code)}`, status);

export const listConversions = (
  filters: { class?: string; unit?: string; status?: ReferenceStatus } = {},
  options?: ListOptions,
) => listItems<UomConversion>("/uom/conversions", filters, options);

export const createConversion = (input: CreateConversionInput) =>
  referenceFetch<UomConversion>("/uom/conversions", { method: "POST", body: JSON.stringify(input) });

export const convertUnit = (from: string, to: string, qty: string) =>
  referenceFetch<Conversion>(withQuery("/uom/convert", { from, to, qty }));
