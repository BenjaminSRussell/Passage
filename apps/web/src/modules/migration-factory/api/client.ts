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
  /** Addresses the record within its run; the release path uses it. */
  seq: number;
  legacyId: string;
  rule: "REFERENTIAL" | "DOMAIN" | "UNIQUENESS";
  reason: string;
  status: "OPEN" | "RELEASED";
  party?: string;
  account?: string;
  /** Dimension combination of a quarantined GL balance. */
  dimensions?: string;
  currency?: string;
  /** Decimal string as extracted (the cost amount for a project line, the balance for a GL balance). */
  amount: string;
  released?: OpenItemRecord;
  releasedBy?: string;
  releasedAt?: string;
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
  /** The open_items run a gl_balances run reconciled its control accounts with (BR-49.2). */
  openItemsRunId?: string;
  /** Bumped by every release; a release sends the version the user saw. */
  version: number;
  createdAt: string;
}

export interface PackRunDetail extends PackRun {
  controlTotals: ControlTotal[];
  quarantined: QuarantinedRecord[];
  /** MIG-1506 text for every control account whose open items differ from the GL balance. */
  mismatchMessages?: string[];
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

/** One legacy GL balance at (account, dimension combination, currency). Balance is signed: debit positive, credit negative. */
export interface GLBalanceRecord {
  legacyId: string;
  account: string;
  dimensions?: string;
  currency: string;
  /** Decimal string; never a float. */
  balance: string;
}

export interface GLRunRequest {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoffTs: string;
  freezeTs: string;
  quarantineMaxPct?: string;
  /** The chart of accounts the balances are checked against. */
  accounts: string[];
  /** An open_items run of the same company to reconcile the control accounts with (BR-49.2). */
  openItemsRunId?: string;
  records: GLBalanceRecord[];
}

/** A correction to a quarantined open item (FR-49.11). Blank fields keep the extracted value. */
export interface ReleaseRequest {
  party?: string;
  account?: string;
  /** Party ids added to master data since the run. */
  parties?: string[];
  version?: number;
}

export type Category = "HOUR" | "EXPENSE" | "ITEM" | "FEE" | "ON_ACCOUNT";
export type BillingStatus = "CHARGEABLE_UNBILLED" | "BILLED" | "NON_CHARGEABLE";

/** One legacy project transaction carried in as a beginning balance (FR-49.12). Amounts are decimal strings. */
export interface OpeningLine {
  legacyId: string;
  category: Category;
  transactionDate: string;
  qty?: string;
  costAmount: string;
  salesAmount: string;
  currency: string;
  workerId?: string;
  activityId?: string;
  dimensions?: Record<string, string>;
  billingStatus: BillingStatus;
  wipCost?: string;
  wipSales?: string;
}

export interface LegacyCategoryTotal {
  category: Category;
  cost?: string;
  sales?: string;
  wipCost?: string;
  wipSales?: string;
}

export interface ProjectExtract {
  projectId: string;
  legacyTotals: LegacyCategoryTotal[];
  lines: OpeningLine[];
}

export interface ProjectRunRequest {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoffTs: string;
  freezeTs: string;
  quarantineMaxPct?: string;
  projects: ProjectExtract[];
}

export interface ProjectOpening {
  runId: string;
  projectId: string;
  status: "RECONCILED" | "OPENING_MISMATCH";
  totals: ControlTotal[];
  lineCount: number;
  lines: (OpeningLine & { beginningBalance: true })[];
}

export interface Xref {
  xrefId: string;
  system: SourceSystem;
  company: string;
  table: string;
  legacyId: string;
  passageEntity: string;
  passageId: string;
  runId: string;
  createdAt: string;
}

export type ParallelState = "REPLAYING" | "DIFFING" | "REVIEW" | "SIGNED_OFF";
export type Area = "TB" | "INV" | "AP" | "AR" | "FA";
export type VarianceClass = "EXPECTED" | "EXPLAINED" | "UNEXPLAINED";

export interface Tuple {
  area: Area;
  key: string;
  /** Decimal string. */
  amount: string;
}

export interface ParallelRunRequest {
  period: string;
  sourceSystem: SourceSystem;
  documentsReplayed: number;
  revenue: string;
  explainedMaxPct?: string;
  legacy: Tuple[];
  passage: Tuple[];
}

export interface Variance {
  varianceId: string;
  area: Area;
  key: string;
  legacy: string;
  passage: string;
  delta: string;
  class: VarianceClass;
  ruleId?: string;
  reason?: string;
  explainedBy?: string;
  explainedAt?: string;
}

export interface ParallelRun {
  parallelRunId: string;
  period: string;
  sourceSystem: SourceSystem;
  documentsReplayed: number;
  revenue: string;
  explainedMaxPct: string;
  inputHash: string;
  state: ParallelState;
  unexplainedCount: number;
  unexplainedAmount: string;
  explainedAmount: string;
  expectedByRule: Record<string, number>;
  canSignOff: boolean;
  signOffBlocker?: string;
  startedBy?: string;
  signedOffBy?: string;
  signedOffAt?: string;
  version: number;
  createdAt: string;
  /** Present on get and commands; the list leaves it out. */
  variances?: Variance[];
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
  /** Set once the step is started; the runbook timer runs from it (FR-49.6). */
  startedAt?: string;
  startedBy?: string;
  /** Elapsed minutes when the plan was read: from the start while running, the recorded duration when done. */
  elapsedMin?: number;
  tone?: "ok" | "amber" | "red";
  /** CUT-1602 text for a running step at or above 110 % of its planned duration. */
  overrunWarning?: string;
}

/** One step of the generated rollback plan (FR-49.9); the first is the decision at the abort deadline. */
export interface RollbackStep {
  seq: number;
  offsetMin: number;
  name: string;
  owner: string;
  plannedDurationMin: number;
  decisionPoint: boolean;
}

/** One pack of the workspace catalog (FR-49.1). */
export interface CatalogEntry {
  pack: Pack;
  sourceSystems: SourceSystem[];
  /** The FR-49.2 control totals the pack compares. */
  controlTotals: string;
  /** A run can be started for this pack in this release. */
  available: boolean;
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
  /** Present once the plan is rolled back: the lines exported for re-keying (FR-49.9). */
  rekeying?: Rekeying;
  /** Present on get and commands; the list leaves it out. */
  steps?: RunbookStep[];
  /** The generated rollback plan, sent with the steps. */
  rollbackSteps?: RollbackStep[];
}

export interface Rekeying {
  journalLines: number;
  documentLines: number;
  /** Postings sent with the rollback that were dated before T-0 and left out. */
  excluded: number;
}

/** One Passage posting line since T-0 handed back to the legacy system for re-keying (FR-49.9). */
export interface RekeyPosting {
  kind: "JOURNAL" | "DOCUMENT";
  entity: string;
  documentRef: string;
  /** RFC 3339 UTC. */
  postedAt: string;
  account: string;
  dimensions?: string;
  currency: string;
  /** Signed decimal string: debit positive, credit negative. Never a float. */
  amount: string;
  text?: string;
}

export type RekeyFile = "journals.csv" | "documents.csv";

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

/** GET /migration/packs: the nine packs, the systems they cover and whether each runs today. */
export function listPackCatalog(signal?: AbortSignal): Promise<CatalogEntry[]> {
  return items("/migration/packs", signal);
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

/** POST /migration/packs/gl_balances/runs: GL opening balances (FR-49.1, BR-49.2). */
export function startGLRun(body: GLRunRequest, key: string = newKey()): Promise<PackRunDetail> {
  return post("/migration/packs/gl_balances/runs", body, key);
}

/** POST /migration/packs/project_wip/runs: project beginning balances (FR-49.12). */
export function startProjectRun(body: ProjectRunRequest, key: string = newKey()): Promise<PackRunDetail> {
  return post("/migration/packs/project_wip/runs", body, key);
}

/** POST /migration/runs/{id}/quarantine/{seq}/release. */
export function releaseQuarantined(runId: string, seq: number, body: ReleaseRequest, key: string = newKey()): Promise<PackRunDetail> {
  return post(`/migration/runs/${encodeURIComponent(runId)}/quarantine/${seq}/release`, body, key);
}

/** GET /migration/runs/{id}/projects/{project}/opening. */
export function getProjectOpening(runId: string, projectId: string, page: { limit?: number; after?: string } = {}, signal?: AbortSignal): Promise<ProjectOpening> {
  return send(`/migration/runs/${encodeURIComponent(runId)}/projects/${encodeURIComponent(projectId)}/opening${query(page)}`, {}, signal);
}

/** GET /migration/xref by legacy id (system, table, id and optionally company). */
export function lookupXref(params: { system: SourceSystem; table: string; id: string; company?: string }, signal?: AbortSignal): Promise<Xref[]> {
  return items(`/migration/xref${query(params)}`, signal);
}

/** GET /migration/parallel-runs, newest first, without variances. */
export function listParallelRuns(filter: { state?: ParallelState | ""; limit?: number } = {}, signal?: AbortSignal): Promise<ParallelRun[]> {
  return items(`/migration/parallel-runs${query(filter)}`, signal);
}

/** GET /migration/parallel-runs/{id} with variances. */
export function getParallelRun(id: string, signal?: AbortSignal): Promise<ParallelRun> {
  return send(`/migration/parallel-runs/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /migration/parallel-runs: diff a replayed period against legacy (FR-49.5). */
export function startParallelRun(body: ParallelRunRequest, key: string = newKey()): Promise<ParallelRun> {
  return post("/migration/parallel-runs", body, key);
}

/** POST /migration/parallel-runs/{id}/variances/{v}/explain. */
export function explainVariance(id: string, varianceId: string, reason: string, version: number, key: string = newKey()): Promise<ParallelRun> {
  return post(`/migration/parallel-runs/${encodeURIComponent(id)}/variances/${encodeURIComponent(varianceId)}/explain`, { reason, version }, key);
}

/** POST /migration/parallel-runs/{id}/sign-off. */
export function signOffParallelRun(id: string, version: number, key: string = newKey()): Promise<ParallelRun> {
  return post(`/migration/parallel-runs/${encodeURIComponent(id)}/sign-off`, { version }, key);
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

/** POST /cutover/plans/{id}/steps/{seq}/start: starts the runbook timer for the step. */
export function startStep(planId: string, seq: number, version: number, key: string = newKey()): Promise<CutoverPlan> {
  return post(`/cutover/plans/${encodeURIComponent(planId)}/steps/${seq}/start`, { version }, key);
}

/**
 * POST /cutover/plans/{id}/steps/{seq}/complete. version is the plan version the user saw. Without
 * actualDurationMin the service measures a started step from its start.
 */
export function completeStep(planId: string, seq: number, actualDurationMin: number | undefined, version: number, key: string = newKey()): Promise<CutoverPlan> {
  return post(`/cutover/plans/${encodeURIComponent(planId)}/steps/${seq}/complete`, actualDurationMin === undefined ? { version } : { actualDurationMin, version }, key);
}

/**
 * POST /cutover/plans/{id}/rollback. Needs two distinct approvers (CUT-1603). postings are the
 * Passage postings since T-0 to export for re-keying; one the service rejects leaves the plan as it was.
 */
export function rollbackPlan(planId: string, approvers: string[], postings: RekeyPosting[], version: number, key: string = newKey()): Promise<CutoverPlan> {
  return post(`/cutover/plans/${encodeURIComponent(planId)}/rollback`, { approvers, postings, version }, key);
}

/** GET /cutover/plans/{id}/rekeying/{file}: the CSV download of a rolled-back plan's re-keying file. */
export function rekeyFileUrl(planId: string, file: RekeyFile): string {
  return `/api/v1/cutover/plans/${encodeURIComponent(planId)}/rekeying/${file}`;
}
