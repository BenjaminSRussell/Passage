// Client for the sales service (services/sales/api/openapi.yaml).
// Browser calls use /api/v1/sales/..., which the web BFF rewrites to /v1/sales/....

export const base = "/api/v1/sales";

export const ORDER_TYPES = ["ORDER", "JOURNAL", "SUBSCRIPTION", "RETURNED"] as const;
export type OrderType = (typeof ORDER_TYPES)[number];

export const ORDER_STATUSES = ["DRAFT", "OPEN", "CONFIRMED", "PARTIALLY_SHIPPED", "SHIPPED", "INVOICED", "CLOSED", "CANCELLED"] as const;
export type OrderStatus = (typeof ORDER_STATUSES)[number];

export const HOLD_TYPES = ["CREDIT", "FRAUD", "MANUAL", "PRICE_REVIEW"] as const;
export type HoldType = (typeof HOLD_TYPES)[number];

export const LIMIT_TYPES = ["NONE", "BALANCE", "BALANCE_PACKING_SLIP", "BALANCE_ALL"] as const;
export type LimitType = (typeof LIMIT_TYPES)[number];

/** Decimal values travel as strings (never floats). */
export type Decimal = string;

export interface OrderSummary {
  id: string;
  legalEntityId: string;
  orderNumber: string;
  customerId: string;
  orderType: OrderType;
  status: OrderStatus;
  currency: string;
  requestedShipDate: string;
  channel: string;
  externalRef?: string;
  netTotal: Decimal;
  lineCount: number;
  activeHolds: HoldType[];
  version: number;
  createdAt: string;
}

export interface OrderPage {
  items: OrderSummary[];
  nextCursor?: string;
}

export interface OrderLine {
  id: string;
  lineNo: number;
  productId: string;
  quantity: Decimal;
  unit: string;
  siteId: string;
  warehouseId: string;
  unitPrice: Decimal;
  discPct: Decimal;
  lineNet: Decimal;
  requestedShipDate: string;
  confirmedShipDate?: string;
  lineStatus: string;
}

export interface Hold {
  id: string;
  holdType: HoldType;
  reasonCode: string;
  status: "ACTIVE" | "RELEASED";
  orderAmount: Decimal;
  appliedBy: string;
  releasedBy?: string;
  releasedUntil?: string;
  appliedAt: string;
}

export interface CreditRule {
  id: string;
  result: "PASS" | "WARN" | "FAIL";
  value: string;
  threshold: string;
  note?: string;
}

export interface CreditEvaluation {
  outcome: "PASS" | "WARN" | "FAIL";
  rules: CreditRule[];
}

export interface Order {
  id: string;
  legalEntityId: string;
  orderNumber: string;
  customerId: string;
  invoiceAccountId: string;
  orderType: OrderType;
  status: OrderStatus;
  currency: string;
  requestedShipDate: string;
  channel: string;
  externalRef?: string;
  netTotal: Decimal;
  version: number;
  creditCheck?: CreditEvaluation;
  lines: OrderLine[];
  holds: Hold[];
  createdAt: string;
  updatedAt: string;
}

/** Confirm and release answer with the order and whether it is now confirmed. */
export interface ConfirmResult extends Order {
  confirmed: boolean;
}

export interface NewOrderLine {
  productId: string;
  quantity: Decimal;
  unit: string;
  siteId: string;
  warehouseId: string;
  unitPrice: Decimal;
  discPct?: Decimal;
  requestedShipDate: string;
}

export interface NewOrder {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  orderType: OrderType;
  currency: string;
  requestedShipDate: string;
  channel?: string;
  externalRef?: string;
  lines: NewOrderLine[];
}

export interface Exposure {
  openAr?: Decimal;
  unapplied?: Decimal;
  deliveredNotInvoiced?: Decimal;
  openOrders?: Decimal;
  overdueAr?: Decimal;
  oldestItemDays?: number;
  dunningLevel?: number;
}

export interface CreditProfileInput {
  limit: Decimal;
  currency: string;
  limitType: LimitType;
  tolerancePct: Decimal;
  toleranceAmount: Decimal;
  oldestItemDaysMax: number;
  maxDunningLevel: number;
  reviewDate?: string;
  customerSince?: string;
  mandatoryCheck: boolean;
  exposure: Exposure;
}

export interface CreditProfile extends CreditProfileInput {
  legalEntityId: string;
  customerId: string;
  version: number;
  updatedAt: string;
}

export interface AtpPeriod {
  date: string;
  supply: Decimal;
  demand: Decimal;
}

export interface AtpCheckInput {
  requested: string;
  quantity: Decimal;
  completeOnly: boolean;
  periods: AtpPeriod[];
}

export interface AtpScheduleLine {
  date: string;
  quantity: Decimal;
  period: number;
}

export interface AtpCheckResult {
  promise: {
    promisedDate?: string;
    complete: boolean;
    backorder: Decimal;
    source: string;
    schedule: AtpScheduleLine[];
  };
  periods: AtpPeriod[];
  cumulative: Decimal[];
}

/** A value read together with the ETag that If-Match must echo back. */
export interface Versioned<T> {
  value: T;
  etag: string;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class SalesApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "SalesApiError";
  }
}

export interface Problem {
  status: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof SalesApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<SalesApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new SalesApiError(res.status, code, message, fields);
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

async function versioned<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Versioned<T>> {
  const res = await send(path, init, signal);
  return { value: (await res.json()) as T, etag: res.headers.get("ETag") ?? "" };
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

/** The ETag of a version, as the service writes it ("3"). */
export function etagOf(version: number): string {
  return `"${version}"`;
}

export interface OrderFilter {
  status?: OrderStatus | "";
  hold?: HoldType | "";
  customerId?: string;
  cursor?: string;
  limit?: number;
}

export function listOrders(filter: OrderFilter = {}, signal?: AbortSignal): Promise<OrderPage> {
  return json<OrderPage>(
    `/orders${query({ status: filter.status, hold: filter.hold, customerId: filter.customerId, cursor: filter.cursor, limit: filter.limit })}`,
    {},
    signal,
  );
}

export function getOrder(id: string, signal?: AbortSignal): Promise<Versioned<Order>> {
  return versioned<Order>(`/orders/${encodeURIComponent(id)}`, {}, signal);
}

export function createOrder(input: NewOrder, key: string = newKey()): Promise<Versioned<Order>> {
  return versioned<Order>("/orders", { method: "POST", body: JSON.stringify(input), headers: { "Idempotency-Key": key } });
}

/** Confirms at the given ETag. A failing credit check answers confirmed=false with a CREDIT hold. */
export function confirmOrder(id: string, etag: string): Promise<Versioned<ConfirmResult>> {
  return versioned<ConfirmResult>(`/orders/${encodeURIComponent(id)}:confirm`, { method: "POST", headers: { "If-Match": etag } });
}

/** Releases one ACTIVE hold; releasing the CREDIT hold of an unconfirmed order confirms it. */
export function releaseHold(orderId: string, holdId: string, etag: string): Promise<Versioned<ConfirmResult>> {
  return versioned<ConfirmResult>(`/orders/${encodeURIComponent(orderId)}/holds/${encodeURIComponent(holdId)}:release`, {
    method: "POST",
    headers: { "If-Match": etag },
  });
}

export async function listCreditProfiles(legalEntityId = "", signal?: AbortSignal): Promise<CreditProfile[]> {
  const body = await json<{ items?: CreditProfile[] }>(`/credit-profiles${query({ legalEntityId, limit: 200 })}`, {}, signal);
  return body.items ?? [];
}

export function getCreditProfile(legalEntityId: string, customerId: string, signal?: AbortSignal): Promise<Versioned<CreditProfile>> {
  return versioned<CreditProfile>(`/credit-profiles/${encodeURIComponent(customerId)}${query({ legalEntityId })}`, {}, signal);
}

/** Creates (etag "0") or replaces the profile at the given ETag. */
export function putCreditProfile(legalEntityId: string, customerId: string, input: CreditProfileInput, etag: string): Promise<Versioned<CreditProfile>> {
  return versioned<CreditProfile>(`/credit-profiles/${encodeURIComponent(customerId)}${query({ legalEntityId })}`, {
    method: "PUT",
    body: JSON.stringify(input),
    headers: { "If-Match": etag },
  });
}

/** Stateless ATP promise check over entered buckets (BR-27.1). */
export function atpCheck(input: AtpCheckInput): Promise<AtpCheckResult> {
  return json<AtpCheckResult>("/atp:check", { method: "POST", body: JSON.stringify(input) });
}
