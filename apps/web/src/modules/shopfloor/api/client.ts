// Client for the shopfloor service (services/shopfloor/api/openapi.yaml).
// Browser calls use /api/v1/shopfloor/..., which the web BFF rewrites to the service.

export const base = "/api/v1/shopfloor";

/** The server accepts at most this many registrations per batch. */
export const MAX_BATCH = 500;

/** Longest range pay:calculate and pay:approve accept, in days. */
export const MAX_PAY_RANGE_DAYS = 28;

export type RegistrationKind = "clock-in" | "clock-out" | "job-start" | "job-stop" | "absence" | "indirect";

export interface Worker {
  id: string;
  partyId: string;
  name: string;
  badgeId: string | null;
  userId: string | null;
  /** Decimal string, money per hour. */
  costRate: string;
  currency: string;
  active: boolean;
  version: number;
  createdAt: string;
}

export interface NewWorker {
  partyId: string;
  name: string;
  badgeId?: string | null;
  userId?: string | null;
  costRate: string;
  currency: string;
}

export interface RegistrationInput {
  /** Client-generated UUID; the server treats a replayed id as a duplicate. */
  id: string;
  workerId: string;
  kind: RegistrationKind;
  jobId?: string | null;
  bundleId?: string | null;
  qtyGood?: string | null;
  qtyScrap?: string | null;
  scrapReasonId?: string | null;
  startedAt: string;
  stoppedAt?: string | null;
  deviceId?: string | null;
  offline?: boolean;
  note?: string | null;
}

export interface RegistrationResult {
  id: string;
  status: "accepted" | "duplicate";
}

export interface Registration {
  id: string;
  workerId: string;
  kind: RegistrationKind;
  jobId: string | null;
  bundleId: string | null;
  qtyGood: string;
  qtyScrap: string;
  scrapReasonId: string | null;
  startedAt: string;
  stoppedAt: string | null;
  hours: string | null;
  serverReceivedAt: string;
  deviceId: string | null;
  offline: boolean;
  note: string | null;
}

export interface PayRange {
  workerId: string;
  /** YYYY-MM-DD, inclusive. */
  from: string;
  /** YYYY-MM-DD, inclusive, at most 28 days after from. */
  to: string;
  timeZone?: string;
}

export interface PayLine {
  type: string;
  hours: string;
  multiplier: string;
  amount: string;
  currency: string;
}

export type DayFlag = "missing-clock-out" | "orphan-clock-out";

export interface PayDay {
  workDate: string;
  hours: string;
  flags: DayFlag[];
  payLines: PayLine[];
}

export interface PayCalculateResponse {
  workerId: string;
  from: string;
  to: string;
  timeZone: string;
  days: PayDay[];
}

export interface ApprovedDay {
  workDate: string;
  hours: string;
  status: "calculated" | "approved";
  newlyApproved: boolean;
  payLines: PayLine[];
}

export interface PayApproveResponse {
  workerId: string;
  from: string;
  to: string;
  days: ApprovedDay[];
}

export type DayStatus = "calculated" | "approved";

export interface WorkerDay {
  workerId: string;
  workerName: string;
  workDate: string;
  hours: string;
  flags: DayFlag[];
  status: DayStatus;
  timeZone: string;
  calculatedAt: string;
  approvedBy: string | null;
  approvedAt: string | null;
  payLines: PayLine[];
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class ShopfloorApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ShopfloorApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ShopfloorApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<ShopfloorApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new ShopfloorApiError(res.status, code, message, fields);
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

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new ShopfloorApiError(0, "COM-0001", "The response has no items");
  }
  return body.items;
}

function query(params: Record<string, string | number | boolean | undefined>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(key, String(value));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

export function newKey(): string {
  return crypto.randomUUID();
}

function post<T>(path: string, body: unknown, key?: string): Promise<T> {
  const headers: Record<string, string> = {};
  if (key) headers["Idempotency-Key"] = key;
  return send<T>(path, { method: "POST", headers, body: JSON.stringify(body) });
}

export function listWorkers(filter: { active?: boolean } = {}, signal?: AbortSignal): Promise<Worker[]> {
  return items<Worker>(`/workers${query({ active: filter.active })}`, signal);
}

export function getWorker(id: string, signal?: AbortSignal): Promise<Worker> {
  return send<Worker>(`/workers/${encodeURIComponent(id)}`, {}, signal);
}

/** Creates a worker; reuse one key per form so a retried submit cannot create two. */
export function createWorker(input: NewWorker, key: string = newKey()): Promise<Worker> {
  return post<Worker>("/workers", input, key);
}

export function listRegistrations(
  filter: { workerId: string; from: string; to: string; timeZone?: string },
  signal?: AbortSignal,
): Promise<Registration[]> {
  return items<Registration>(`/registrations${query(filter)}`, signal);
}

export function listWorkerDays(
  filter: { from: string; to: string; workerId?: string; status?: DayStatus | "" },
  signal?: AbortSignal,
): Promise<WorkerDay[]> {
  return items<WorkerDay>(`/worker-days${query(filter)}`, signal);
}

/** Sends one batch of at most MAX_BATCH registrations (each item is idempotent by its id). */
export async function createRegistrations(batch: RegistrationInput[]): Promise<RegistrationResult[]> {
  if (batch.length < 1 || batch.length > MAX_BATCH) {
    throw new RangeError(`a batch holds 1 to ${MAX_BATCH} registrations`);
  }
  const res = await post<{ items: RegistrationResult[] }>("/registrations", { items: batch });
  return res.items;
}

export function calculatePay(range: PayRange, key: string = newKey()): Promise<PayCalculateResponse> {
  return post<PayCalculateResponse>("/pay:calculate", range, key);
}

export function approvePay(range: PayRange, key: string = newKey()): Promise<PayApproveResponse> {
  return post<PayApproveResponse>("/pay:approve", range, key);
}

/** Splits an offline queue into batches the server accepts (at most MAX_BATCH each). */
export function chunk<T>(list: readonly T[], size: number = MAX_BATCH): T[][] {
  if (size < 1) {
    throw new RangeError("chunk size must be at least 1");
  }
  const out: T[][] = [];
  for (let i = 0; i < list.length; i += size) {
    out.push(list.slice(i, i + size));
  }
  return out;
}
