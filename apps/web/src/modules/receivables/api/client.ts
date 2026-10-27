// Typed client for the receivables service (services/receivables/api/openapi.yaml).
// Money stays a decimal string end to end; nothing here converts to a float.

export const base = "/api/v1/receivables";

export const BUCKETS = ["CURRENT", "1-30", "31-60", "61-90", "91-180", "OVER-180"] as const;
export type Bucket = (typeof BUCKETS)[number];

export const PAYMENT_MODES = ["CHECK", "ACH", "WIRE", "CARD", "CASH"] as const;
export type PaymentMode = (typeof PAYMENT_MODES)[number];

export const PAYMENT_STATUSES = ["RECEIVED", "SUGGESTED", "APPLIED", "ON_ACCOUNT", "REVERSED"] as const;
export type PaymentStatus = (typeof PAYMENT_STATUSES)[number];

export interface AgingTotal {
  currency: string;
  bucket: Bucket;
  amount: string;
}

export interface AgingResponse {
  asOf: string;
  totals: AgingTotal[];
}

export interface OpenItem {
  id: string;
  legalEntityId: string;
  customerId: string;
  sourceType: string;
  documentNumber: string;
  documentDate: string;
  dueDate: string;
  currency: string;
  amount: string;
  openAmount: string;
  cashDiscountDate?: string | null;
  cashDiscountAmount: string;
  dunningLevel: number;
  dunningBlock: boolean;
  status: string;
  version: number;
}

export interface RemittanceLine {
  reference: string;
  amount: string;
}

export interface PaymentInput {
  legalEntityId: string;
  customerId: string | null;
  paymentMode: PaymentMode;
  source: "MANUAL";
  sourceRef: string | null;
  amount: string;
  currency: string;
  receivedDate: string;
  valueDate: string;
  payerName: string | null;
  remittance: RemittanceLine[];
}

export interface Payment {
  id: string;
  legalEntityId: string;
  customerId?: string | null;
  paymentMode: PaymentMode;
  source: string;
  sourceRef?: string | null;
  amount: string;
  unappliedAmount: string;
  currency: string;
  receivedDate: string;
  valueDate: string;
  payerName?: string | null;
  remittance: RemittanceLine[];
  status: PaymentStatus;
  version: number;
}

export interface MatchCandidate {
  openItemIds: string[];
  score: number;
  rules: string[];
}

export interface MatchResult {
  decision: "AUTO_APPLY" | "SUGGESTED" | "ON_ACCOUNT";
  candidates: MatchCandidate[];
}

export interface SettlementInput {
  openItemId: string;
  amount: string;
  cashDiscount?: string;
  writeOff?: string;
}

export interface ApplyInput {
  settlements: SettlementInput[];
  onAccount: string;
}

export interface SettlementRecord {
  openItemId: string;
  amount: string;
  cashDiscount: string;
  writeOff: string;
  openAfter: string;
  itemStatus: string;
}

export interface ApplyResult {
  payment: Payment;
  settlements: SettlementRecord[];
}

/** Problem details (RFC 9457) surfaced to the UI with the stable service code and field errors. */
export class ReceivablesApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ReceivablesApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ReceivablesApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

/** A fresh Idempotency-Key for one user action; retries of the same action reuse it. */
export function newKey(): string {
  return crypto.randomUUID();
}

const DECIMAL = /^-?\d+(\.\d{1,6})?$/;
const DAY = /^\d{4}-\d{2}-\d{2}$/;
const CURRENCY = /^[A-Z]{3}$/;

/** Validates an aging payload; throws on any shape the OpenAPI schema forbids. */
export function parseAging(raw: unknown): AgingResponse {
  if (typeof raw !== "object" || raw === null) throw new TypeError("aging: body is not an object");
  const body = raw as Record<string, unknown>;
  if (typeof body.asOf !== "string" || !DAY.test(body.asOf)) throw new TypeError("aging: asOf is not a date");
  if (!Array.isArray(body.totals)) throw new TypeError("aging: totals is not an array");
  const totals = body.totals.map((item: unknown, i: number): AgingTotal => {
    if (typeof item !== "object" || item === null) throw new TypeError(`aging: totals[${i}] is not an object`);
    const t = item as Record<string, unknown>;
    if (typeof t.currency !== "string" || !CURRENCY.test(t.currency)) throw new TypeError(`aging: totals[${i}].currency invalid`);
    if (typeof t.bucket !== "string" || !(BUCKETS as readonly string[]).includes(t.bucket)) {
      throw new TypeError(`aging: totals[${i}].bucket invalid`);
    }
    if (typeof t.amount !== "string" || !DECIMAL.test(t.amount)) throw new TypeError(`aging: totals[${i}].amount invalid`);
    return { currency: t.currency, bucket: t.bucket as Bucket, amount: t.amount };
  });
  return { asOf: body.asOf, totals };
}

async function toError(res: Response): Promise<ReceivablesApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new ReceivablesApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [name, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(name, String(value));
  }
  const text = q.toString();
  return text ? `?${text}` : "";
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new ReceivablesApiError(0, "COM-0001", "The response has no items");
  return body.items;
}

/** GET /aging - totals per currency and bucket; asOf defaults to the server's today. */
export async function fetchAging(asOf?: string, signal?: AbortSignal): Promise<AgingResponse> {
  return parseAging(await send<unknown>(`/aging${query({ asOf })}`, {}, signal));
}

export interface OpenItemFilter {
  customer?: string;
  status?: "open" | "all";
  limit?: number;
}

/** GET /open-items - ordered by due date. */
export function listOpenItems(filter: OpenItemFilter = {}, signal?: AbortSignal): Promise<OpenItem[]> {
  return items<OpenItem>(`/open-items${query({ customer: filter.customer, status: filter.status, limit: filter.limit })}`, signal);
}

export interface PaymentFilter {
  status?: PaymentStatus | "";
  customer?: string;
  limit?: number;
}

/** GET /payments - newest received first. */
export function listPayments(filter: PaymentFilter = {}, signal?: AbortSignal): Promise<Payment[]> {
  return items<Payment>(`/payments${query({ status: filter.status, customer: filter.customer, limit: filter.limit })}`, signal);
}

export function getPayment(id: string, signal?: AbortSignal): Promise<Payment> {
  return send<Payment>(`/payments/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /payments - records a manual payment; the key makes a retry return the same payment. */
export function createPayment(input: PaymentInput, key: string = newKey()): Promise<Payment> {
  return send<Payment>("/payments", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

/** POST /payments/{id}:match - scores candidate settlements; changes nothing. */
export function matchPayment(id: string): Promise<MatchResult> {
  return send<MatchResult>(`/payments/${encodeURIComponent(id)}:match`, { method: "POST" });
}

/** POST /payments/{id}:apply - If-Match carries the payment version; 412 when it changed. */
export function applyPayment(id: string, version: number, input: ApplyInput, key: string = newKey()): Promise<ApplyResult> {
  return send<ApplyResult>(`/payments/${encodeURIComponent(id)}:apply`, {
    method: "POST",
    headers: { "If-Match": `"${version}"`, "Idempotency-Key": key },
    body: JSON.stringify(input),
  });
}
