// Typed client for the financial reporting service (services/finreport/api/openapi.yaml).
// Amounts stay decimal strings end to end; nothing here turns money into a float.

export const base = "/api/v1/reporting";

export const DEFINITION_KINDS = ["trial_balance", "balance_sheet", "income_statement", "cash_flow", "custom"] as const;
export type DefinitionKind = (typeof DEFINITION_KINDS)[number];

export const ROW_KINDS = ["header", "account_range", "dimension_filter", "total", "calculation"] as const;
export type RowKind = (typeof ROW_KINDS)[number];

export const CURRENCY_TYPES = ["acct", "rpt", "txn"] as const;
export type CurrencyType = (typeof CURRENCY_TYPES)[number];

export const PERIOD_EXPRESSIONS = ["P", "P-1", "YTD", "PY", "PYTD", "Q", "FY", "CALC"] as const;

export interface RowFormat {
  bold?: boolean;
  indent?: number;
  printZero?: boolean;
  pageBreak?: boolean;
}

export interface DefinitionRow {
  code: string;
  kind: RowKind;
  label: string;
  accountFrom?: string;
  accountTo?: string;
  dimensionFilter?: Record<string, string[]>;
  references?: string[];
  expression?: string;
  normalBalance: "debit" | "credit";
  cashFlowTag?: string;
  format: RowFormat;
}

export interface DefinitionColumn {
  code: string;
  label: string;
  periodExpr: string;
  /** Omitted means the run's currency type. */
  currencyType?: CurrencyType;
  calcExpression?: string;
}

export interface Definition {
  schemaVersion: string;
  code: string;
  name: string;
  version: number;
  status: "draft" | "published" | "retired";
  kind: DefinitionKind;
  includeAdjustmentPeriods: boolean;
  rows: DefinitionRow[];
  columns: DefinitionColumn[];
}

export interface DefinitionSummary {
  code: string;
  name: string;
  kind: DefinitionKind;
  latestVersion: number;
  hasDraft: boolean;
  updatedAt: string;
}

export interface Issue {
  field: string;
  code: string;
  detail: string;
}

export interface RunSummary {
  id: string;
  definitionCode: string;
  definitionVersion: number;
  ledgerId: string;
  currencyType: string;
  periodYear: number;
  periodNo: number;
  status: string;
  hwmByLedger: Record<string, number>;
  resultHash: string;
  cellCount: number;
  requestedBy: string;
  startedAt: string;
  durationMs?: number;
}

export interface CellSummary {
  row: string;
  column: string;
  value: string;
  blank: boolean;
  computed: boolean;
  note?: string;
  keyCount: number;
}

export interface BalanceKey {
  ledgerId: string;
  period: string;
  account: string;
  combination: string;
  currencyType: string;
  amount: string;
}

export interface DrillResponse {
  runId: string;
  row: string;
  column: string;
  value: string;
  blank: boolean;
  computed: boolean;
  sumOfKeys: string;
  keys: BalanceKey[];
}

export interface LagRow {
  ledgerId: string;
  postingSequence: number;
  updatedAt: string;
}

export interface CalendarPeriod {
  year: number;
  no: number;
  quarter: number;
  adjustment?: boolean;
}

export interface RunRequest {
  definition: string;
  definitionVersion?: number;
  ledgerId: string;
  currencyType: CurrencyType;
  period: CalendarPeriod;
  calendar: CalendarPeriod[];
  includeAdjustmentPeriods?: boolean;
}

/** A failed call: the problem code and the text the service wrote. */
export class ReportingApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "ReportingApiError";
  }
}

export interface Problem {
  status: number;
  code: string;
  message: string;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ReportingApiError) {
    return { status: error.status, code: error.code, message: error.message };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed" };
}

/** A fresh Idempotency-Key for one user action; retries of the same action reuse it. */
export function newKey(): string {
  return crypto.randomUUID();
}

async function toError(res: Response): Promise<ReportingApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  return new ReportingApiError(res.status, code, message);
}

interface Sent<T> {
  body: T;
  etag: string | null;
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Sent<T>> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return { body: (await res.json()) as T, etag: res.headers.get("ETag") };
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  return (await send<T>(path, {}, signal)).body;
}

const seg = encodeURIComponent;

export async function listDefinitions(signal?: AbortSignal): Promise<DefinitionSummary[]> {
  return (await get<{ items: DefinitionSummary[] }>("/definitions", signal)).items;
}

/** A definition version: "draft", "latest" (newest published) or a version number. */
export async function getDefinition(
  code: string,
  version: "draft" | "latest" | number,
  signal?: AbortSignal,
): Promise<{ definition: Definition; etag: string | null }> {
  const sent = await send<Definition>(`/definitions/${seg(code)}?version=${seg(String(version))}`, {}, signal);
  return { definition: sent.body, etag: sent.etag };
}

/** Saves the draft. etag is the draft revision last read; omit it when no draft exists yet. */
export async function saveDraft(def: Definition, etag: string | null): Promise<{ definition: Definition; etag: string | null }> {
  const headers: Record<string, string> = {};
  if (etag) headers["If-Match"] = etag;
  const sent = await send<Definition>(`/definitions/${seg(def.code)}`, { method: "PUT", headers, body: JSON.stringify(def) });
  return { definition: sent.body, etag: sent.etag };
}

export async function validateDefinition(def: Definition): Promise<{ valid: boolean; issues: Issue[] }> {
  return (await send<{ valid: boolean; issues: Issue[] }>(`/definitions/${seg(def.code)}:validate`, { method: "POST", body: JSON.stringify(def) })).body;
}

export async function publishDefinition(code: string): Promise<Definition> {
  return (await send<Definition>(`/definitions/${seg(code)}:publish`, { method: "POST" })).body;
}

export function exportDefinition(code: string, signal?: AbortSignal): Promise<Definition> {
  return get<Definition>(`/definitions/${seg(code)}/export`, signal);
}

export async function importDefinition(def: unknown, overwrite: boolean, key: string): Promise<Definition> {
  const query = overwrite ? "?overwrite=true" : "";
  return (await send<Definition>(`/definitions/import${query}`, { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(def) })).body;
}

export async function listRuns(definition: string, signal?: AbortSignal): Promise<RunSummary[]> {
  const query = definition ? `?definition=${seg(definition)}` : "";
  return (await get<{ items: RunSummary[] }>(`/runs${query}`, signal)).items;
}

export function getRun(id: string, signal?: AbortSignal): Promise<RunSummary> {
  return get<RunSummary>(`/runs/${seg(id)}`, signal);
}

export async function listCells(runId: string, signal?: AbortSignal): Promise<CellSummary[]> {
  return (await get<{ items: CellSummary[] }>(`/runs/${seg(runId)}/cells`, signal)).items;
}

/** Drill level 1: the balance keys behind one cell. */
export function drillBalances(runId: string, row: string, column: string, signal?: AbortSignal): Promise<DrillResponse> {
  return get<DrillResponse>(`/runs/${seg(runId)}/cells/${seg(row)}/${seg(column)}/balances`, signal);
}

export async function getLag(signal?: AbortSignal): Promise<LagRow[]> {
  return (await get<{ items: LagRow[] }>("/lag", signal)).items;
}

/** Starts a run. The key makes a retried click return the same run. */
export async function createRun(req: RunRequest, key: string): Promise<RunSummary> {
  return (await send<RunSummary>("/runs", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(req) })).body;
}
