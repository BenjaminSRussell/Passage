// Client for the scheduling service (services/scheduling/api/openapi.yaml).
// Browser calls use /api/v1/scheduling/..., which the web BFF rewrites to the service.

export const base = "/api/v1/scheduling";

export interface Shift {
  /** 0 = Sunday ... 6 = Saturday. */
  weekday: number;
  startMinute: number;
  endMinute: number;
}

export interface Resource {
  id: string;
  code: string;
  name: string;
  finite: boolean;
  timeZone: string;
  shifts: Shift[];
  version: number;
  createdAt: string;
}

export interface NewResource {
  code: string;
  name: string;
  finite: boolean;
  timeZone: string;
  shifts: Shift[];
}

export const OPERATION_STATUSES = ["open", "scheduled", "unplaced"] as const;
export type OperationStatus = (typeof OPERATION_STATUSES)[number];

export interface Operation {
  id: string;
  orderId: string;
  operNumber: number;
  minutes: number;
  dueAt?: string;
  readyAt?: string;
  priority: number;
  resourceIds: string[];
  status: OperationStatus;
  createdAt: string;
}

export interface NewOperation {
  orderId: string;
  operNumber: number;
  minutes: number;
  dueAt?: string;
  readyAt?: string;
  priority?: number;
  resourceIds: string[];
}

export interface Run {
  id: string;
  scope: "site" | "selection" | "move";
  mode: string;
  direction: string;
  finite: boolean;
  algorithm: string;
  status: string;
  operationsCount: number;
  makespanMinutes: number;
  totalLatenessMinutes: number;
  solverStatus: string | null;
  messages: string[];
  createdAt: string;
}

export interface NewRun {
  scope: "site" | "selection";
  mode: "jobs" | "operations";
  finite: boolean;
  operationIds?: string[];
  start?: string;
  horizonDays?: number;
}

/** One scheduled segment as returned by GET /gantt. */
export interface GanttBar {
  jobId: string;
  orderId: string;
  operNumber: number;
  resourceId: string;
  segment: number;
  start: string;
  end: string;
  lateMinutes: number;
  firmed: boolean;
}

export interface MoveRequest {
  resourceId?: string;
  start: string;
  dryRun: boolean;
}

export interface MoveViolation {
  code: string;
  detail: string;
  jobId?: string;
}

export interface MoveChange {
  operationId: string;
  orderId: string;
  resourceId: string;
  oldStart: string;
  oldEnd: string;
  newStart: string;
  newEnd: string;
  lateMinutes: number;
}

export interface MoveResponse {
  dryRun: boolean;
  feasible: boolean;
  violations: MoveViolation[];
  changes: MoveChange[];
  orderId?: string;
  orderStart?: string;
  orderEnd?: string;
  run?: Run;
}

export interface Firm {
  jobId: string;
  orderId: string;
  resourceId: string;
  start: string;
  end: string;
  firmed: boolean;
}

export interface LoadItem {
  resourceId: string;
  /** Monday of the ISO week, YYYY-MM-DD. */
  weekStart: string;
  openMinutes: number;
  bookedMinutes: number;
  loadPct: number;
  bottleneck: boolean;
}

export interface Interval {
  start: string;
  end: string;
  minutes: number;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class SchedulingApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "SchedulingApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof SchedulingApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<SchedulingApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new SchedulingApiError(res.status, code, message, fields);
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
    throw new SchedulingApiError(0, "COM-0001", "The response has no items");
  }
  return body.items;
}

function query(params: Record<string, string | number | undefined>): string {
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

export function listResources(signal?: AbortSignal): Promise<Resource[]> {
  return items<Resource>("/resources", signal);
}

export function getResource(id: string, signal?: AbortSignal): Promise<Resource> {
  return send<Resource>(`/resources/${encodeURIComponent(id)}`, {}, signal);
}

export function getResourceCapacity(id: string, from: string, to: string, signal?: AbortSignal): Promise<Interval[]> {
  return items<Interval>(`/resources/${encodeURIComponent(id)}/capacity${query({ from, to })}`, signal);
}

/** Creates a resource; reuse one key per form so a retried submit cannot create two. */
export function createResource(input: NewResource, key: string = newKey()): Promise<Resource> {
  return post<Resource>("/resources", input, key);
}

export function listOperations(filter: { status?: OperationStatus | ""; orderId?: string; limit?: number } = {}, signal?: AbortSignal): Promise<Operation[]> {
  return items<Operation>(`/operations${query({ status: filter.status, orderId: filter.orderId, limit: filter.limit })}`, signal);
}

export function createOperation(input: NewOperation, key: string = newKey()): Promise<Operation> {
  return post<Operation>("/operations", input, key);
}

export function listRuns(signal?: AbortSignal): Promise<Run[]> {
  return items<Run>("/runs", signal);
}

export function getRun(id: string, signal?: AbortSignal): Promise<Run> {
  return send<Run>(`/runs/${encodeURIComponent(id)}`, {}, signal);
}

export function startRun(input: NewRun, key: string = newKey()): Promise<Run> {
  return post<Run>("/runs", input, key);
}

export function fetchGantt(from: string, to: string, resourceIds: string[] = [], signal?: AbortSignal): Promise<GanttBar[]> {
  return items<GanttBar>(`/gantt${query({ from, to, resourceIds: resourceIds.join(",") })}`, signal);
}

/** Preview (dryRun) or commit a move. A commit needs the key; send the same key to retry safely. */
export function moveJob(jobId: string, req: MoveRequest, key?: string): Promise<MoveResponse> {
  return post<MoveResponse>(`/jobs/${encodeURIComponent(jobId)}:move`, req, req.dryRun ? undefined : (key ?? newKey()));
}

export function firmJob(jobId: string, firmed: boolean, key: string = newKey()): Promise<Firm> {
  return post<Firm>(`/jobs/${encodeURIComponent(jobId)}:firm`, { firmed }, key);
}

export function fetchCapacityLoad(from: string, weeks: number, resourceIds: string[] = [], signal?: AbortSignal): Promise<LoadItem[]> {
  return items<LoadItem>(`/capacity-load${query({ from, weeks, resourceIds: resourceIds.join(",") })}`, signal);
}
