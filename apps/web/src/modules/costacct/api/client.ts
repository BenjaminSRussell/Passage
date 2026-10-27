// API client for the costacct service (services/costacct/api/openapi.yaml).
// Browser calls use /api/v1/controlling/... which the web BFF rewrites to the service's /v1/controlling/... paths.

const BASE = "/api/v1/controlling";

export type CostObjectKind = "CostCenter" | "InternalOrder" | "Product" | "Project";
export type CostElementKind = "Primary" | "Secondary";

export interface CostObject {
  id: string;
  legalEntityId: string;
  kind: CostObjectKind;
  code: string;
  name: string;
}

export type NewCostObject = Omit<CostObject, "id">;

export interface CostElement {
  id: string;
  legalEntityId: string;
  kind: CostElementKind;
  code: string;
  name: string;
  mainAccountFrom?: string;
  mainAccountTo?: string;
}

export type NewCostElement = Omit<CostElement, "id">;

export interface CostEntry {
  id: string;
  costElementId: string;
  sourceId: string;
  allocationRunId?: string;
  period: string;
  currency: string;
  amount: string;
  behavior: "Fixed" | "Variable" | "Unclassified";
  origin: "Journal" | "Allocation" | "Activity" | "Statistical" | "Manual";
}

export interface Flow {
  from: string;
  to: string;
  amount: string;
}

export interface Posting {
  centerId: string;
  amount: string;
}

export interface AllocationResult {
  method: "exact-lu" | "gauss-seidel";
  iterations: number;
  residual: string;
  converged: boolean;
  swept: string;
  totalIn: string;
  totalOut: string;
  flows: Flow[];
  postings: Posting[];
  unallocated: string[];
}

export interface RunInput {
  legalEntityId: string;
  period: string;
  currency: string;
  secondaryElementId: string;
  costObjectIds: string[];
  basis: string[][];
}

export interface RunResponse {
  runId: string;
  period: string;
  result: AllocationResult;
}

export interface Run extends AllocationResult {
  id: string;
  legalEntityId: string;
  period: string;
  currency: string;
  secondaryElementId: string;
  status: "Completed";
  costObjectIds: string[];
  createdAt: string;
}

export interface JournalBalance {
  mainAccount: string;
  amount: string;
}

export interface ReconcileInput {
  legalEntityId: string;
  period: string;
  currency: string;
  journalBalances: JournalBalance[];
}

export interface Reconciliation {
  reconciled: boolean;
  lines: { costElementId: string; journal: string; costLedger: string; difference: string }[];
  unmappedAccounts: JournalBalance[];
}

export interface Problem {
  title: string;
  status: number;
  code: string;
  detail?: string;
}

export class CostAcctApiError extends Error {
  constructor(public readonly problem: Problem) {
    super(problem.detail ?? problem.title);
    this.name = "CostAcctApiError";
  }
}

/** One readable sentence for a failed call: the stable code and the detail. */
export function describeError(err: unknown, fallback: string): string {
  return err instanceof CostAcctApiError ? `${err.problem.code}: ${err.message}` : fallback;
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers: { accept: "application/json", ...(init.headers as Record<string, string> | undefined) },
  });
  if (!res.ok) {
    const problem = (await res.json().catch(() => ({}))) as Partial<Problem>;
    throw new CostAcctApiError({ title: problem.title ?? `HTTP ${res.status}`, status: res.status, code: problem.code ?? "COM-0000", detail: problem.detail });
  }
  return (await res.json()) as T;
}

function post<T>(path: string, body: unknown, idempotencyKey?: string): Promise<T> {
  const headers: Record<string, string> = { "content-type": "application/json" };
  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
  return request<T>(path, { method: "POST", headers, body: JSON.stringify(body) });
}

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : "";
}

interface Items<T> {
  items: T[];
}

export function listCostObjects(filter: { legalEntityId?: string; kind?: CostObjectKind | "" } = {}, signal?: AbortSignal): Promise<Items<CostObject>> {
  return request(`${BASE}/cost-objects${query({ legalEntityId: filter.legalEntityId, kind: filter.kind || undefined })}`, { signal });
}

export function getCostObject(id: string, signal?: AbortSignal): Promise<CostObject> {
  return request(`${BASE}/cost-objects/${encodeURIComponent(id)}`, { signal });
}

export function createCostObject(input: NewCostObject, idempotencyKey: string): Promise<CostObject> {
  return post(`${BASE}/cost-objects`, input, idempotencyKey);
}

export function listEntries(costObjectId: string, period: string, signal?: AbortSignal): Promise<Items<CostEntry>> {
  return request(`${BASE}/cost-objects/${encodeURIComponent(costObjectId)}/entries${query({ period })}`, { signal });
}

export function listCostElements(legalEntityId?: string, signal?: AbortSignal): Promise<Items<CostElement>> {
  return request(`${BASE}/cost-elements${query({ legalEntityId })}`, { signal });
}

export function createCostElement(input: NewCostElement, idempotencyKey: string): Promise<CostElement> {
  return post(`${BASE}/cost-elements`, input, idempotencyKey);
}

export function listRuns(legalEntityId?: string, signal?: AbortSignal): Promise<Items<Run>> {
  return request(`${BASE}/allocation-runs${query({ legalEntityId })}`, { signal });
}

export function getRun(id: string, signal?: AbortSignal): Promise<Run> {
  return request(`${BASE}/allocation-runs/${encodeURIComponent(id)}`, { signal });
}

/** Runs one reciprocal allocation step for a period (FR-38.7). */
export function runAllocation(input: RunInput, idempotencyKey: string): Promise<RunResponse> {
  return post(`${BASE}/allocation-runs`, input, idempotencyKey);
}

/** Compares primary cost entries with journal balances (FR-38.11). Read-only, so no Idempotency-Key. */
export function reconcile(input: ReconcileInput): Promise<Reconciliation> {
  return post(`${BASE}/reconciliations`, input);
}
