// Client for the HR service (services/hr/api/openapi.yaml). Browser calls use
// /api/v1/hr/..., which the web BFF rewrites to the service.

export const base = "/api/v1/hr";

/** Minimum reveal reason length (HCM-PII-001, PRD 8.42.6). */
export const REVEAL_REASON_MIN = 10;

export type WorkerType = "EMPLOYEE" | "CONTRACTOR";
export type EmploymentType = "FULL_TIME" | "PART_TIME" | "TEMP" | "CONTRACTOR";
export type AssignmentKind = "PRIMARY" | "CONCURRENT" | "GLOBAL_HOST";
export type EmploymentStatus = "PENDING" | "ACTIVE" | "TERMINATED";

export interface Worker {
  id: string;
  personnelNumber: string;
  workerType: WorkerType;
  partyPersonId: string;
  legalName: string;
  preferredName?: string;
  userId?: string;
  createdAt: string;
  updatedAt: string;
}

export interface PIIView {
  nationalId: string;
  bankAccount?: string;
  masked: boolean;
}

export interface WorkerDetail extends Worker {
  pii: PIIView;
}

export interface NewWorker {
  personnelNumber: string;
  workerType: WorkerType;
  partyPersonId: string;
  legalName: string;
  preferredName?: string;
  userId?: string;
  nationalId: string;
  bankAccount?: string;
}

export interface Employment {
  id: string;
  workerId: string;
  legalEntityId: string;
  employmentType: EmploymentType;
  assignmentKind: AssignmentKind;
  homeLegalEntityId?: string;
  plannedEndDate?: string;
  hireDate: string;
  terminationDate?: string;
  validFrom: string;
  validTo?: string;
  status: EmploymentStatus;
}

export interface NewEmployment {
  legalEntityId: string;
  employmentType: EmploymentType;
  assignmentKind?: AssignmentKind;
  homeLegalEntityId?: string;
  plannedEndDate?: string;
  hireDate: string;
  terminationDate?: string;
}

export interface AsOfView {
  workerId: string;
  date: string;
  employments: Employment[];
  positionId: string | null;
  positionNumber: string | null;
  fteShare: string | null;
  managerWorkerId: string | null;
}

export interface Position {
  id: string;
  positionNumber: string;
  jobCode: string;
  departmentId: string;
  legalEntityId: string;
  /** Decimal string, 0.05 to 2.00. */
  fte: string;
  allowOverfill: boolean;
  validFrom: string;
  validTo?: string;
  reportsToPositionId?: string;
}

export interface PositionAssignment {
  id: string;
  positionId: string;
  workerId: string;
  employmentId: string;
  fteShare: string;
  isPrimary: boolean;
  validFrom: string;
  validTo?: string;
}

export interface PositionDetail extends Position {
  assignments: PositionAssignment[];
}

export interface NewPosition {
  positionNumber: string;
  jobCode: string;
  departmentId: string;
  legalEntityId: string;
  fte: string;
  allowOverfill: boolean;
  validFrom: string;
  validTo?: string;
  reportsToPositionId?: string;
}

export interface NewAssignment {
  workerId: string;
  employmentId: string;
  fteShare: string;
  isPrimary: boolean;
  validFrom: string;
  validTo?: string;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class HrApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "HrApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof HrApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<HrApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new HrApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

async function page<T>(path: string, signal?: AbortSignal): Promise<Page<T>> {
  const body = await send<Page<T>>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new HrApiError(0, "COM-0001", "The response has no items");
  return body;
}

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(key, value);
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

const id = (value: string) => encodeURIComponent(value);

/** A fresh Idempotency-Key; reuse one per form so a retried submit cannot create two. */
export function newKey(): string {
  return crypto.randomUUID();
}

function post<T>(path: string, body: unknown, key: string): Promise<T> {
  return send<T>(path, { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) });
}

export function listWorkers(filter: { q?: string; cursor?: string } = {}, signal?: AbortSignal): Promise<Page<Worker>> {
  return page<Worker>(`/workers${query({ q: filter.q?.trim(), cursor: filter.cursor })}`, signal);
}

/** The worker with PII masked; the service audits every call. */
export function getWorker(workerId: string, signal?: AbortSignal): Promise<WorkerDetail> {
  return send<WorkerDetail>(`/workers/${id(workerId)}`, {}, signal);
}

/** Unmasked PII (HcmPIIRead); the reason is recorded in the audit log. */
export function revealPII(workerId: string, reason: string): Promise<PIIView> {
  return send<PIIView>(`/workers/${id(workerId)}/pii${query({ reason: reason.trim() })}`);
}

export function createWorker(input: NewWorker, key: string = newKey()): Promise<Worker> {
  return post<Worker>("/workers", input, key);
}

export async function listEmployments(workerId: string, signal?: AbortSignal): Promise<Employment[]> {
  return (await page<Employment>(`/workers/${id(workerId)}/employments`, signal)).items;
}

export function createEmployment(workerId: string, input: NewEmployment, key: string = newKey()): Promise<Employment> {
  return post<Employment>(`/workers/${id(workerId)}/employments`, input, key);
}

export function workerAsOf(workerId: string, date: string, signal?: AbortSignal): Promise<AsOfView> {
  return send<AsOfView>(`/workers/${id(workerId)}/as-of${query({ date })}`, {}, signal);
}

export function listPositions(filter: { cursor?: string } = {}, signal?: AbortSignal): Promise<Page<Position>> {
  return page<Position>(`/positions${query({ cursor: filter.cursor })}`, signal);
}

export function getPosition(positionId: string, signal?: AbortSignal): Promise<PositionDetail> {
  return send<PositionDetail>(`/positions/${id(positionId)}`, {}, signal);
}

export function createPosition(input: NewPosition, key: string = newKey()): Promise<Position> {
  return post<Position>("/positions", input, key);
}

export function createAssignment(positionId: string, input: NewAssignment, key: string = newKey()): Promise<PositionAssignment> {
  return post<PositionAssignment>(`/positions/${id(positionId)}/assignments`, input, key);
}
