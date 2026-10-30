// Client for the migration service (services/migration/api/openapi.yaml). Commands send an
// Idempotency-Key so a retried submit replays the first outcome.
export interface Problem {
  code: string;
  message: string;
  /** Field or test name to reason, from the problem's errors map. */
  errors?: Record<string, string>;
}

export class MigrationApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly errors?: Record<string, string>,
  ) {
    super(message);
    this.name = "MigrationApiError";
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof MigrationApiError) return { code: error.code, message: error.message, errors: error.errors };
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed" };
}

export type SourceSystem = "ax2012" | "d365fo" | "sap_ecc" | "sap_s4";
export type RunMode = "rehearsal" | "cutover" | "delta";
export type RunStatus = "succeeded" | "partial" | "failed";

export interface RowIssue {
  field: string;
  code: string;
  detail: string;
}

export interface MigrationRun {
  id: string;
  sourceSystem: SourceSystem;
  mode: RunMode;
  entity: string;
  status: RunStatus;
  dryRun: boolean;
  mappingVersion: number;
  mappingSpecId?: string;
  parentRunId?: string;
  counts: { extracted: number; mapped: number; created: number; updated: number; unchanged: number; rejected: number; warned: number };
  rejects: { sourceKey: string; rejects: RowIssue[] }[];
  warnings: { sourceKey: string; warnings: RowIssue[] }[];
  absent: string[];
  reconciliation?: { source: number; target: number; checks: { name: string; ok: boolean; detail: string }[] };
  createdAt: string;
}

export interface MappingSpec {
  id: string;
  entity: string;
  version: number;
  checksum: string;
  fieldCount: number;
  testCount: number;
  createdAt: string;
  mapping?: unknown;
}

export interface CreateRunInput {
  sourceSystem: SourceSystem;
  mode: RunMode;
  dryRun: boolean;
  mappingSpecId: string;
}

export function newKey(): string {
  return crypto.randomUUID();
}

async function toError(res: Response): Promise<MigrationApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message = typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const errors = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : undefined;
  return new MigrationApiError(res.status, code, message, errors);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`/api/v1/integration${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new MigrationApiError(0, "COM-0001", "The response has no items");
  return body.items;
}

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : "";
}

export const listRuns = (filter: { entity?: string; status?: string }, signal?: AbortSignal) =>
  items<MigrationRun>(`/migration-runs${query(filter)}`, signal);

export const getRun = (id: string, signal?: AbortSignal) => send<MigrationRun>(`/migration-runs/${encodeURIComponent(id)}`, {}, signal);

export async function createRun(input: CreateRunInput, key: string = newKey()): Promise<MigrationRun> {
  const body = await send<{ run: MigrationRun }>("/migration-runs", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
  return body.run;
}

export async function rerunRun(id: string, key: string = newKey()): Promise<MigrationRun> {
  const body = await send<{ run: MigrationRun }>(`/migration-runs/${encodeURIComponent(id)}:rerun`, { method: "POST", headers: { "Idempotency-Key": key } });
  return body.run;
}

export const listSpecs = (entity: string | undefined, signal?: AbortSignal) => items<MappingSpec>(`/mapping-specs${query({ entity })}`, signal);

export const getSpec = (id: string, signal?: AbortSignal) => send<MappingSpec>(`/mapping-specs/${encodeURIComponent(id)}`, {}, signal);

export const publishSpec = (mapping: unknown, key: string = newKey()) =>
  send<MappingSpec>("/mapping-specs", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify({ mapping }) });
