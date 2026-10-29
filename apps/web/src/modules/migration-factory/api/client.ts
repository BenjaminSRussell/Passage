// Client for the migration factory (tools/migration-factory/api/openapi.yaml). Every path is
// under /api/v1; commands send an Idempotency-Key so a retried submit replays the first outcome.
import type { ControlTotal, Pack, RunStatus, SourceSystem } from "../domain";

/** Problem details as returned by the service (RFC 9457), reduced to what screens show. */
export interface Problem {
  code: string;
  message: string;
}

export class MigrationApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof MigrationApiError) return { code: error.code, message: error.message };
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed" };
}

export interface QuarantinedRecord {
  legacyId: string;
  rule: "REFERENTIAL" | "DOMAIN" | "UNIQUENESS";
  reason: string;
}

export interface PackRun {
  runId: string;
  pack: Pack;
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoffTs: string;
  inputHash: string;
  status: RunStatus;
  recordsIn: number;
  recordsLoaded: number;
  recordsQuarantined: number;
  alreadyPresent: number;
  message?: string;
  createdAt: string;
}

export interface PackRunDetail extends PackRun {
  controlTotals: ControlTotal[];
  quarantined: QuarantinedRecord[];
}

export interface OpenItemRecord {
  legacyId: string;
  party: string;
  account: string;
  currency: string;
  /** Decimal string; never a float. */
  amount: string;
}

export interface PackRunRequest {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoffTs: string;
  freezeTs: string;
  quarantineMaxPct?: string;
  parties: string[];
  records: OpenItemRecord[];
}

export interface RunbookStep {
  seq: number;
  offsetMin: number;
  name: string;
  owner: string;
  plannedDurationMin: number;
  actualDurationMin: number;
  status: "PENDING" | "DONE";
  goNoGo: boolean;
  pack?: string;
  completedBy?: string;
  completedAt?: string;
}

export interface CutoverPlan {
  planId: string;
  t0: string;
  packs: Pack[];
  entities: string[];
  abortOffsetMin: number;
  rolledBack: boolean;
  approvers: string[];
  version: number;
  createdAt: string;
  stepsDone: number;
  stepsTotal: number;
  openGoNoGo: string[];
  abortDue: boolean;
  abortWarning?: string;
  /** Present on get and commands; the list leaves it out. */
  steps?: RunbookStep[];
}

export interface GenerateCutoverRequest {
  t0: string;
  packs: Pack[];
  entities: string[];
  abortOffsetHours?: number;
  rehearsalDurationsMin?: Record<string, number>;
}

export function newKey(): string {
  return crypto.randomUUID();
}

async function toError(res: Response): Promise<MigrationApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  return new MigrationApiError(res.status, code, message);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`/api/v1${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new MigrationApiError(0, "COM-0001", "The response has no items");
  return body.items;
}

function post<T>(path: string, body: unknown, key: string): Promise<T> {
  return send<T>(path, { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) });
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

/** GET /migration/runs, newest first. */
export function listRuns(filter: { pack?: Pack | ""; status?: RunStatus | ""; limit?: number } = {}, signal?: AbortSignal): Promise<PackRun[]> {
  return items(`/migration/runs${query(filter)}`, signal);
}

/** GET /migration/runs/{id} with control totals and quarantine. */
export function getRun(runId: string, signal?: AbortSignal): Promise<PackRunDetail> {
  return send(`/migration/runs/${encodeURIComponent(runId)}`, {}, signal);
}

/** POST /migration/packs/{pack}/runs. */
export function startPackRun(pack: Pack, body: PackRunRequest, key: string = newKey()): Promise<PackRunDetail> {
  return post(`/migration/packs/${encodeURIComponent(pack)}/runs`, body, key);
}

/** GET /cutover/plans, newest first, without steps. */
export function listPlans(signal?: AbortSignal): Promise<CutoverPlan[]> {
  return items("/cutover/plans", signal);
}

/** GET /cutover/plans/{id} with steps. */
export function getPlan(planId: string, signal?: AbortSignal): Promise<CutoverPlan> {
  return send(`/cutover/plans/${encodeURIComponent(planId)}`, {}, signal);
}

/** POST /cutover/plans/generate. */
export function generateCutoverPlan(body: GenerateCutoverRequest, key: string = newKey()): Promise<CutoverPlan> {
  return post("/cutover/plans/generate", body, key);
}

/** POST /cutover/plans/{id}/steps/{seq}/complete. version is the plan version the user saw. */
export function completeStep(planId: string, seq: number, actualDurationMin: number, version: number, key: string = newKey()): Promise<CutoverPlan> {
  return post(`/cutover/plans/${encodeURIComponent(planId)}/steps/${seq}/complete`, { actualDurationMin, version }, key);
}

/** POST /cutover/plans/{id}/rollback. Needs two distinct approvers (CUT-1603). */
export function rollbackPlan(planId: string, approvers: string[], version: number, key: string = newKey()): Promise<CutoverPlan> {
  return post(`/cutover/plans/${encodeURIComponent(planId)}/rollback`, { approvers, version }, key);
}
