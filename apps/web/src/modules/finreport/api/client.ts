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
  /** An explicit account list instead of a range; hierarchy runs write it. */
  accounts?: string[];
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
  /** published: a version can run; retired: every version was retired (BR-15.8); draft: never published. */
  status: "draft" | "published" | "retired";
  latestVersion: number;
  hasDraft: boolean;
  updatedAt: string;
}

export interface Issue {
  field: string;
  code: string;
  detail: string;
}

/** A warning recorded with a completed run, such as REPORT-0015 (FR-15.14). Blocking checks fail the run instead. */
export interface RunCheck {
  code: string;
  severity: "warning";
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
  /** Absent on runs stored before checks were recorded. */
  checks?: RunCheck[];
  /** Set when the rows of the run are the nodes of a hierarchy version. */
  hierarchy?: { code: string; version: number };
  /** Set when the run is limited to a unit of a reporting tree; unit is empty for the whole tree. */
  tree?: { code: string; unit: string; revision: number };
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

/** One journal line under a cell (drill level 2, BR-15.4). amount is the line as the cell shows it. */
export interface DrillLine {
  /** Position of the line's balance key in the drill level 1 list. */
  key: number;
  postingDate: string;
  postingSequence: number;
  lineNumber: number;
  voucher: string;
  period: string;
  account: string;
  combination: string;
  currencyType: string;
  debit: string;
  credit: string;
  amount: string;
  closingEntry: boolean;
  sourceUri?: string;
  traceId?: string;
  traceUrl?: string;
}

/** One page of drill level 2 with its sums and the reconciliation of every line against the balance keys. */
export interface LinePage {
  runId: string;
  row: string;
  column: string;
  key: number | null;
  blank: boolean;
  computed: boolean;
  value: string;
  hwm: number;
  limit: number;
  items: DrillLine[];
  sumOfLines: string;
  cumulativeSum: string;
  nextCursor: string | null;
  linesTotal: string;
  difference: string;
  unreconciledKeys: number;
  reconciled: boolean;
}

export interface LagRow {
  ledgerId: string;
  postingSequence: number;
  updatedAt: string;
  /** Journal entries the ledger consumer has applied; absent from older services. */
  entriesApplied?: number;
  /** Entries that arrived ahead of the mark and wait for an earlier one; absent from older services. */
  entriesPending?: number;
  /** Seconds the oldest waiting entry has waited; 0 when nothing waits. Absent from older services. */
  lagSeconds?: number;
  /** Highest posting sequence seen for the ledger, applied or waiting. */
  hwmLedger?: number;
}

/** The lag rows and the gate (reporting.max_lag_seconds) a run is refused above. */
export interface LagStatus {
  items: LagRow[];
  /** Absent from older services. */
  maxLagSeconds?: number;
}

export const VERIFICATION_STATUSES = ["matched", "differs", "inconclusive"] as const;
export type VerificationStatus = (typeof VERIFICATION_STATUSES)[number];

/** One account whose net differs between the read model and the ledger (readModel minus ledger). */
export interface VerificationDiff {
  account: string;
  readModel: string;
  ledger: string;
  difference: string;
}

/** A stored comparison of a ledger's read model with its trial balance (FR-15.1), as the list serves it. */
export interface VerificationSummary {
  id: string;
  ledgerId: string;
  legalEntityId?: string;
  status: VerificationStatus;
  hwmBefore: number;
  hwmAfter: number;
  /** Entries seen waiting ahead of the read model's mark (they make the check inconclusive). */
  entriesPending: number;
  accountsCompared: number;
  /** Accounts that differ; diffs holds the first 500. */
  diffCount: number;
  requestedBy: string;
  verifiedAt: string;
}

/** One stored check with its differing accounts; they are withheld from a user without ledger.balance.read. */
export interface Verification extends VerificationSummary {
  diffs: VerificationDiff[];
  diffsWithheld?: boolean;
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
  /** Accepts a read model behind the lag gate: the run completes with a REPORT-0004 warning instead of being refused. */
  allowStale?: boolean;
  /** Takes the rows from a published hierarchy; the definition supplies the columns. */
  hierarchy?: HierarchyRunRequest;
  /** Limits the run to the balances of one unit of a reporting tree, rolled up; no unit means the whole tree. */
  tree?: TreeRunRequest;
}

export interface TreeRunRequest {
  code: string;
  unit?: string;
}

export interface HierarchyRunRequest {
  code: string;
  /** Omitted means the latest published version. */
  version?: number;
  normalBalance?: "debit" | "credit";
  /** Dimension hierarchies only: limit the rows to this account range. */
  accountFrom?: string;
  accountTo?: string;
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

/** The definition a run was evaluated with: for a hierarchy run, one row per hierarchy node. */
export function getRunDefinition(runId: string, signal?: AbortSignal): Promise<Definition> {
  return get<Definition>(`/runs/${seg(runId)}/definition`, signal);
}

export async function listCells(runId: string, signal?: AbortSignal): Promise<CellSummary[]> {
  return (await get<{ items: CellSummary[] }>(`/runs/${seg(runId)}/cells`, signal)).items;
}

/** Drill level 1: the balance keys behind one cell. */
export function drillBalances(runId: string, row: string, column: string, signal?: AbortSignal): Promise<DrillResponse> {
  return get<DrillResponse>(`/runs/${seg(runId)}/cells/${seg(row)}/${seg(column)}/balances`, signal);
}

/** Drill level 2: one page of the journal lines behind a cell, or behind one of its balance keys. */
export function drillLines(
  runId: string,
  row: string,
  column: string,
  opts: { key?: number; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<LinePage> {
  const query = new URLSearchParams();
  if (opts.key !== undefined) query.set("key", String(opts.key));
  if (opts.cursor) query.set("cursor", opts.cursor);
  if (opts.limit !== undefined) query.set("limit", String(opts.limit));
  const qs = query.toString();
  return get<LinePage>(`/runs/${seg(runId)}/cells/${seg(row)}/${seg(column)}/lines${qs ? `?${qs}` : ""}`, signal);
}

export async function getLag(signal?: AbortSignal): Promise<LagStatus> {
  return get<LagStatus>("/lag", signal);
}

export async function listVerifications(ledgerId: string, signal?: AbortSignal): Promise<VerificationSummary[]> {
  const query = ledgerId ? `?ledgerId=${seg(ledgerId)}` : "";
  return (await get<{ items: VerificationSummary[] }>(`/balance-verifications${query}`, signal)).items;
}

export function getVerification(id: string, signal?: AbortSignal): Promise<Verification> {
  return get<Verification>(`/balance-verifications/${seg(id)}`, signal);
}

/** Checks one ledger. The key makes a retried click return the same check. */
export async function createVerification(ledgerId: string, key: string): Promise<Verification> {
  return (await send<Verification>("/balance-verifications", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify({ ledgerId }) })).body;
}

/** Starts a run. The key makes a retried click return the same run. */
export async function createRun(req: RunRequest, key: string): Promise<RunSummary> {
  return (await send<RunSummary>("/runs", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(req) })).body;
}

/** A statutory definition the service ships (FR-15.5, FR-15.14). */
export interface StandardDefinition {
  code: string;
  name: string;
  kind: DefinitionKind;
  rows: number;
  columns: number;
}

export interface InstallResult {
  code: string;
  action: "installed" | "skipped";
  version?: number;
  reason?: string;
}

export async function listStandardDefinitions(signal?: AbortSignal): Promise<StandardDefinition[]> {
  return (await get<{ items: StandardDefinition[] }>("/standard-definitions", signal)).items;
}

/** Publishes every shipped definition whose code is not in use yet; safe to repeat. */
export async function installStandardDefinitions(): Promise<InstallResult[]> {
  return (await send<{ items: InstallResult[] }>("/standard-definitions:install", { method: "POST" })).body.items;
}

/** Retires every published version of a definition: runs can no longer use it (BR-15.8). */
export async function retireDefinition(code: string): Promise<{ code: string; status: "retired"; retiredVersions: number }> {
  return (await send<{ code: string; status: "retired"; retiredVersions: number }>(`/definitions/${seg(code)}:retire`, { method: "POST" })).body;
}

// --- derived hierarchies (FR-15.13) -------------------------------------------

export const HIERARCHY_SUBJECTS = ["main_account", "main_account_category", "dimension_value"] as const;
export type HierarchySubject = (typeof HIERARCHY_SUBJECTS)[number];

/** One membership rule of a leaf: exact values (a trailing * is a prefix wildcard) or a from..to range. */
export interface HierarchyRule {
  attribute?: string;
  from?: string;
  to?: string;
  values?: string[];
}

export interface HierarchyNode {
  code: string;
  label: string;
  parent?: string;
  rules?: HierarchyRule[];
}

export interface Hierarchy {
  schemaVersion: string;
  code: string;
  name: string;
  version: number;
  status: "draft" | "published" | "retired";
  subject: HierarchySubject;
  dimensionAttribute?: string;
  strict: boolean;
  effectiveFrom?: string;
  ownerUserId?: string;
  nodes: HierarchyNode[];
}

export interface HierarchySummary {
  code: string;
  name: string;
  subject: HierarchySubject;
  strict: boolean;
  latestVersion: number;
  hasDraft: boolean;
  assignedCount: number;
  unassignedCount: number;
  updatedAt: string;
}

/** An account, category or dimension value the hierarchy classifies. */
export interface HierarchyMember {
  id: string;
  key: string;
  attributes?: Record<string, string>;
}

export interface Membership {
  /** member id to leaf node code */
  assigned: Record<string, string>;
  unassigned: string[];
}

export async function listHierarchies(signal?: AbortSignal): Promise<HierarchySummary[]> {
  return (await get<{ items: HierarchySummary[] }>("/hierarchies", signal)).items;
}

export async function getHierarchy(
  code: string,
  version: "draft" | "latest" | number,
  signal?: AbortSignal,
): Promise<{ hierarchy: Hierarchy; etag: string | null }> {
  const sent = await send<Hierarchy>(`/hierarchies/${seg(code)}?version=${seg(String(version))}`, {}, signal);
  return { hierarchy: sent.body, etag: sent.etag };
}

/** Saves the draft. etag is the draft revision last read; omit it when no draft exists yet. */
export async function saveHierarchy(h: Hierarchy, etag: string | null): Promise<{ hierarchy: Hierarchy; etag: string | null }> {
  const headers: Record<string, string> = {};
  if (etag) headers["If-Match"] = etag;
  const sent = await send<Hierarchy>(`/hierarchies/${seg(h.code)}`, { method: "PUT", headers, body: JSON.stringify(h) });
  return { hierarchy: sent.body, etag: sent.etag };
}

export async function publishHierarchy(code: string, members: HierarchyMember[]): Promise<{ hierarchy: Hierarchy; membership: Membership }> {
  return (await send<{ hierarchy: Hierarchy; membership: Membership }>(`/hierarchies/${seg(code)}:publish`, { method: "POST", body: JSON.stringify({ members }) })).body;
}

export function getHierarchyMembers(code: string, signal?: AbortSignal): Promise<Membership & { version: number }> {
  return get<Membership & { version: number }>(`/hierarchies/${seg(code)}/members`, signal);
}

/** Output formats a schedule can send (FR-15.8). */
export const OUTPUT_FORMATS = ["pdf", "xlsx", "csv", "html"] as const;
export type OutputFormat = (typeof OUTPUT_FORMATS)[number];

/** Formats the service renders for download today; pdf needs the renderer sidecar. */
export const DOWNLOAD_FORMATS = ["csv", "xlsx", "html"] as const satisfies readonly OutputFormat[];

/** Where a run's file output is served. The browser downloads it with the session cookie. */
export function runOutputUrl(runId: string, format: OutputFormat): string {
  return `${base}/runs/${seg(runId)}/outputs/${format}`;
}

/** A distribution schedule as it is saved (FR-15.8). */
export interface ScheduleInput {
  code: string;
  name: string;
  definition: string;
  cron: string;
  timezone: string;
  ledgerIds: string[];
  currencyType: CurrencyType;
  formats: OutputFormat[];
  recipients: string[];
  attachmentLimitMb: number;
  enabled: boolean;
}

/** A distribution schedule as the service returns it. */
export interface Schedule extends ScheduleInput {
  /** Read-only: the user who saved the schedule last. */
  updatedBy?: string;
  /** Read-only: the next firing; absent when the schedule is paused. */
  nextRunAt?: string;
}

export async function listSchedules(signal?: AbortSignal): Promise<Schedule[]> {
  return (await get<{ items: Schedule[] }>("/schedules", signal)).items;
}

/** One schedule and its revision, which a replace echoes in If-Match. */
export async function getSchedule(code: string, signal?: AbortSignal): Promise<{ schedule: Schedule; etag: string | null }> {
  const sent = await send<Schedule>(`/schedules/${seg(code)}`, {}, signal);
  return { schedule: sent.body, etag: sent.etag };
}

/** Creates (etag null) or replaces a schedule. A stale etag answers 412 conflict_version. */
export async function saveSchedule(schedule: ScheduleInput, etag: string | null): Promise<{ schedule: Schedule; etag: string | null }> {
  const headers: Record<string, string> = {};
  if (etag) headers["If-Match"] = etag;
  const sent = await send<Schedule>(`/schedules/${seg(schedule.code)}`, { method: "PUT", headers, body: JSON.stringify(schedule) });
  return { schedule: sent.body, etag: sent.etag };
}

// --- designer parts (FR-15.2, FR-15.3) -------------------------------------

/** A reusable list of report rows. */
export interface RowSet {
  code: string;
  name: string;
  rows: DefinitionRow[];
  /** Read-only: the user who saved the set last. */
  updatedBy?: string;
}

/** A reusable list of report columns; templates are column sets the service ships. */
export interface ColumnSet {
  code: string;
  name: string;
  description?: string;
  columns: DefinitionColumn[];
  updatedBy?: string;
}

export async function listRowSets(signal?: AbortSignal): Promise<RowSet[]> {
  return (await get<{ items: RowSet[] }>("/row-sets", signal)).items;
}

export async function getRowSet(code: string, signal?: AbortSignal): Promise<{ part: RowSet; etag: string | null }> {
  const sent = await send<RowSet>(`/row-sets/${seg(code)}`, {}, signal);
  return { part: sent.body, etag: sent.etag };
}

/** Creates (etag null) or replaces a row set. A stale etag answers 412 conflict_version. */
export async function saveRowSet(set: RowSet, etag: string | null): Promise<{ part: RowSet; etag: string | null }> {
  const headers: Record<string, string> = {};
  if (etag) headers["If-Match"] = etag;
  const sent = await send<RowSet>(`/row-sets/${seg(set.code)}`, { method: "PUT", headers, body: JSON.stringify(set) });
  return { part: sent.body, etag: sent.etag };
}

export async function listColumnSets(signal?: AbortSignal): Promise<ColumnSet[]> {
  return (await get<{ items: ColumnSet[] }>("/column-sets", signal)).items;
}

export async function getColumnSet(code: string, signal?: AbortSignal): Promise<{ part: ColumnSet; etag: string | null }> {
  const sent = await send<ColumnSet>(`/column-sets/${seg(code)}`, {}, signal);
  return { part: sent.body, etag: sent.etag };
}

export async function saveColumnSet(set: ColumnSet, etag: string | null): Promise<{ part: ColumnSet; etag: string | null }> {
  const headers: Record<string, string> = {};
  if (etag) headers["If-Match"] = etag;
  const sent = await send<ColumnSet>(`/column-sets/${seg(set.code)}`, { method: "PUT", headers, body: JSON.stringify(set) });
  return { part: sent.body, etag: sent.etag };
}

/** The comparative column sets the service ships (actual against prior year, and so on). */
export async function listColumnTemplates(signal?: AbortSignal): Promise<ColumnSet[]> {
  return (await get<{ items: ColumnSet[] }>("/column-set-templates", signal)).items;
}

// --- reporting trees (FR-15.4) ---------------------------------------------

/** A reporting unit: values of the tree's dimension attribute, grouped under a parent for roll-up. */
export interface TreeUnit {
  code: string;
  name: string;
  parent?: string;
  /** Values that belong to the unit itself; the unit also covers the values of the units below it. */
  values?: string[];
}

export interface Tree {
  code: string;
  name: string;
  /** The dimension attribute the units group, such as profit_center. */
  attribute: string;
  units: TreeUnit[];
  updatedBy?: string;
}

export async function listTrees(signal?: AbortSignal): Promise<Tree[]> {
  return (await get<{ items: Tree[] }>("/trees", signal)).items;
}

export async function getTree(code: string, signal?: AbortSignal): Promise<{ part: Tree; etag: string | null }> {
  const sent = await send<Tree>(`/trees/${seg(code)}`, {}, signal);
  return { part: sent.body, etag: sent.etag };
}

/** Creates (etag null) or replaces a tree. A stale etag answers 412 conflict_version. */
export async function saveTree(tree: Tree, etag: string | null): Promise<{ part: Tree; etag: string | null }> {
  const headers: Record<string, string> = {};
  if (etag) headers["If-Match"] = etag;
  const sent = await send<Tree>(`/trees/${seg(tree.code)}`, { method: "PUT", headers, body: JSON.stringify(tree) });
  return { part: sent.body, etag: sent.etag };
}

// --- audit trail (FR-15.10) ------------------------------------------------

/** Ledger kinds (posted, approvals, reversals, period changes, ...) then the reporting service's own kinds. */
export const AUDIT_KINDS = [
  "posted",
  "replayed",
  "reversed",
  "batch_submitted",
  "batch_approved",
  "batch_rejected",
  "period_changed",
  "reopen_approved",
  "rate_override",
  "tax_fallback",
  "allocation_run",
  "year_end_run",
  "rebuild",
  "statement_run",
  "definition_published",
  "definition_retired",
  "hierarchy_published",
  "schedule_saved",
  "audit_exported",
] as const;
export type AuditKind = (typeof AUDIT_KINDS)[number];

export interface AuditEntry {
  id: string;
  occurredAt: string;
  ledgerId?: string;
  period?: string;
  kind: AuditKind;
  actorId: string;
  objectRef: string;
  details: Record<string, unknown>;
  traceId?: string;
  traceUrl?: string;
}

/** The service needs a period, or from and to (RFC 3339, to exclusive) at most 366 days apart. */
export interface AuditQuery {
  ledger?: string;
  period?: string;
  kind?: AuditKind;
  actor?: string;
  from?: string;
  to?: string;
}

export interface AuditPage {
  items: AuditEntry[];
  limit: number;
  nextCursor: string | null;
}

export interface AuditExport {
  id: string;
  sha256: string;
  entries: number;
  fileName: string;
  csv: string;
}

function auditParams(q: AuditQuery): URLSearchParams {
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(q)) {
    if (typeof value === "string" && value !== "") params.set(name, value);
  }
  return params;
}

/** One page of the audit trail, oldest first. */
export function listAuditTrail(q: AuditQuery, cursor?: string, signal?: AbortSignal): Promise<AuditPage> {
  const params = auditParams(q);
  if (cursor) params.set("cursor", cursor);
  return get<AuditPage>(`/audit-trail?${params.toString()}`, signal);
}

/** Exports every matching entry as CSV. The service records the export and sends the file's SHA-256. */
export async function exportAuditTrail(q: AuditQuery): Promise<AuditExport> {
  const res = await fetch(`${base}/audit-trail:export?${auditParams(q).toString()}`, {
    headers: { Accept: "text/csv" },
    credentials: "same-origin",
  });
  if (!res.ok) {
    throw await toError(res);
  }
  const id = res.headers.get("X-Passage-Export-Id") ?? "";
  return {
    id,
    sha256: res.headers.get("X-Passage-Export-Sha256") ?? "",
    entries: Number(res.headers.get("X-Passage-Export-Entries") ?? "0"),
    fileName: `audit-trail-${id || "export"}.csv`,
    csv: await res.text(),
  };
}
