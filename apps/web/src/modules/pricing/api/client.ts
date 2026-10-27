// Typed client for the pricing service contract (services/pricing/api/openapi.yaml).
// The browser calls /api/v1/pricing/...; the BFF rewrites to /v1/pricing/... on the service.

export const base = "/api/v1/pricing";

export const CONDITION_TYPES = ["PRICE", "LINE_DISC", "TOTAL_DISC"] as const;
export type ConditionType = (typeof CONDITION_TYPES)[number];

export const RECORD_STATUSES = ["DRAFT", "ACTIVE", "EXPIRED", "WITHDRAWN"] as const;
export type RecordStatus = (typeof RECORD_STATUSES)[number];

export const CUSTOMER_KINDS = ["customer", "customer_group", "all"] as const;
export type CustomerKind = (typeof CUSTOMER_KINDS)[number];

export const PRODUCT_KINDS = ["product", "product_group", "variant", "all"] as const;
export type ProductKind = (typeof PRODUCT_KINDS)[number];

/** Fields a caller sends to create a condition record (OpenAPI ConditionRecordInput). */
export interface ConditionRecordInput {
  legalEntityId: string;
  type: ConditionType;
  customerKind: CustomerKind;
  customerKey: string;
  productKind: ProductKind;
  productKey: string;
  variant: string;
  currency: string;
  unit: string;
  fromQuantity: string;
  amount: string;
  discPct1: string;
  discPct2: string;
  validFrom: string;
  validTo: string;
  searchNext: boolean;
  exclusive: boolean;
}

export interface ConditionRecord extends ConditionRecordInput {
  id: string;
  status: RecordStatus;
  version: number;
}

export interface RecordFilter {
  legalEntityId?: string;
  status?: RecordStatus | "";
  type?: ConditionType | "";
  customer?: string;
  product?: string;
  asOf?: string;
  limit?: number;
}

export interface ResolveLineInput {
  productId: string;
  productGroupId?: string;
  variant?: string;
  unit: string;
  quantity: string;
}

export interface ResolveRequest {
  legalEntityId: string;
  customerId: string;
  customerGroupId?: string;
  currency: string;
  pricingDate: string;
  lines: ResolveLineInput[];
}

export interface AppliedCondition {
  type: ConditionType;
  recordId: string;
  access: number;
}

export interface TraceAccess {
  seq: number;
  access: string;
  evaluated: boolean;
  candidates: string[];
  winner?: string;
  reason: string;
}

export interface TraceStep {
  condition: string;
  selected?: string;
  accesses: TraceAccess[];
}

export interface ResolvedLine {
  lineNo: number;
  unit: string;
  quantity: string;
  unitPrice: string;
  netUnitPrice: string;
  lineNet: string;
  exclusive?: boolean;
  applied: AppliedCondition[];
  errorCode?: string;
  errorDetail?: string;
  trace?: TraceStep[];
}

export interface ResolveResponse {
  currency: string;
  lines: ResolvedLine[];
  subtotal: string;
  totalDiscount: string;
  orderNet: string;
  applied: AppliedCondition[];
  explainRef: string;
  explainTruncated?: boolean;
  trace?: TraceStep[];
}

/** Problem details (RFC 9457) surfaced to the UI with the stable service code and field errors. */
export class PricingApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "PricingApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof PricingApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

/** A fresh Idempotency-Key for one user action; retries of the same action reuse it. */
export function newKey(): string {
  return crypto.randomUUID();
}

async function toError(res: Response): Promise<PricingApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new PricingApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

function post<T>(path: string, body: unknown, key?: string): Promise<T> {
  const headers: Record<string, string> = {};
  if (key) headers["Idempotency-Key"] = key;
  return send<T>(path, { method: "POST", headers, body: JSON.stringify(body) });
}

/** GET /condition-records with the optional filters; at most 100 rows, ordered by id. */
export async function listConditionRecords(filter: RecordFilter = {}, signal?: AbortSignal): Promise<ConditionRecord[]> {
  const q = new URLSearchParams();
  for (const [name, value] of Object.entries(filter)) {
    if (value !== undefined && value !== "") q.set(name, String(value));
  }
  const query = q.toString();
  const body = await send<{ items?: ConditionRecord[] }>(`/condition-records${query ? `?${query}` : ""}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new PricingApiError(0, "COM-0001", "The response has no items");
  }
  return body.items;
}

export function getConditionRecord(id: string, signal?: AbortSignal): Promise<ConditionRecord> {
  return send<ConditionRecord>(`/condition-records/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /condition-records - creates a DRAFT; the overlap check runs at activation. */
export function createConditionRecord(input: ConditionRecordInput, key: string = newKey()): Promise<ConditionRecord> {
  return post<ConditionRecord>("/condition-records", input, key);
}

/** POST /condition-records/{id}:activate - 409 PRC-0002 names an overlapping active record. */
export function activateConditionRecord(id: string, version: number, key: string = newKey()): Promise<ConditionRecord> {
  return post<ConditionRecord>(`/condition-records/${encodeURIComponent(id)}:activate`, { version }, key);
}

export function withdrawConditionRecord(id: string, version: number, reason: string, key: string = newKey()): Promise<ConditionRecord> {
  return post<ConditionRecord>(`/condition-records/${encodeURIComponent(id)}:withdraw`, { version, reason }, key);
}

/** POST /resolve - price a document against the stored ACTIVE records, with the explain trace. */
export function resolvePrice(body: ResolveRequest, explain = true): Promise<ResolveResponse> {
  return post<ResolveResponse>(`/resolve?explain=${explain ? "true" : "false"}`, body);
}
