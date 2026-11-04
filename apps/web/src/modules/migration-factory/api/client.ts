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
  /** The subledger of a quarantined open item that is a payable ("AP"); absent for a receivable. */
  ledger?: "AP";
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
  /** The gl_balances run an onhand run reconciled its inventory account with (BR-49.3). */
  glRunId?: string;
  /** Bumped by every release; a release sends the version the user saw. */
  version: number;
  createdAt: string;
}

export interface PackRunDetail extends PackRun {
  controlTotals: ControlTotal[];
  quarantined: QuarantinedRecord[];
  /** MIG-1506 text for every control account whose open items differ from the GL balance, and MIG-1502 text for an inventory account that differs from the loaded on-hand value. */
  mismatchMessages?: string[];
  /** Set when the run was started with a load object: where its rows went and how they came out. */
  targetLoad?: TargetLoad;
}

/** Status of a run's load into receivables; see the TargetLoad schema in api/openapi.yaml. */
export type TargetLoadStatus = "STAGED" | "LOADING" | "PARTIAL" | "UNRECONCILED" | "MISMATCH" | "RECONCILED" | "FAILED";
export type TargetRowStatus = "PENDING" | "LOADED" | "UNCHANGED" | "SKIPPED" | "REJECTED" | "NOT_AVAILABLE";

export interface TargetLoad {
  runId: string;
  target: "receivables";
  legalEntityId: string;
  importSource: string;
  status: TargetLoadStatus;
  /** Reconciled with no row pending, rejected or unavailable. */
  clean: boolean;
  staged: number;
  created: number;
  updated: number;
  unchanged: number;
  skipped: number;
  rejected: number;
  notAvailable: number;
  pending: number;
  attempts: number;
  lastError?: string;
  startedAt?: string;
  finishedAt?: string;
  version: number;
}

export interface TargetRow {
  seq: number;
  legacyId: string;
  ledger: "AR" | "AP";
  status: TargetRowStatus;
  outcome?: string;
  openItemId?: string;
  /** The target's own code (AR-OPENING-*) or the factory's MIG-* code. */
  code?: string;
  detail?: string;
  side?: "DEBIT" | "CREDIT";
  amount?: string;
  currency?: string;
  customerId?: string;
}

export interface TargetLoadDetail extends TargetLoad {
  /** The run's target.receivables.* control totals: what the factory sent against what receivables reports. */
  reconciliation: ControlTotal[];
  rejects: TargetRow[];
}

export interface OpenItemRecord {
  legacyId: string;
  party: string;
  account: string;
  currency: string;
  /** Decimal string; never a float. */
  amount: string;
  /** Receivables fields, used when the run has a load object. */
  ledger?: "AR" | "AP";
  documentNumber?: string;
  customerId?: string;
  kind?: "INVOICE" | "CREDIT_NOTE" | "PAYMENT" | "ADJUSTMENT";
  documentDate?: string;
  dueDate?: string;
  originalAmount?: string;
}

/** Asks for the accepted records to be staged for loading into receivables. */
export interface LoadRequest {
  target: "receivables";
  legalEntityId: string;
  importSource?: string;
  /** Legacy party id to Passage customer id. */
  customerIds?: Record<string, string>;
}

export interface PackRunRequest {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoffTs: string;
  freezeTs: string;
  quarantineMaxPct?: string;
  parties: string[];
  records: OpenItemRecord[];
  load?: LoadRequest;
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

/** One legacy on-hand line at (item, inventory dimension). Quantity and value are decimal strings at legacy cost. */
export interface OnhandLineRecord {
  legacyId: string;
  item: string;
  site: string;
  warehouse: string;
  location?: string;
  batch?: string;
  serial?: string;
  unit: string;
  currency: string;
  quantity: string;
  value: string;
}

export interface OnhandRunRequest {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoffTs: string;
  freezeTs: string;
  quarantineMaxPct?: string;
  /** The item master the lines are checked against. */
  items: string[];
  /** A gl_balances run of the same company and the inventory account to compare with the loaded value (BR-49.3); they go together. */
  glRunId?: string;
  inventoryAccount?: string;
  records: OnhandLineRecord[];
}

/** One legacy bank account balance at the cutoff. The balance is a signed decimal string: an overdrawn account is negative. */
export interface BankBalanceRecord {
  legacyId: string;
  bankAccount: string;
  currency: string;
  balance: string;
}

export interface BankRunRequest {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoffTs: string;
  freezeTs: string;
  quarantineMaxPct?: string;
  /** The bank account master the lines are checked against. */
  bankAccounts: string[];
  records: BankBalanceRecord[];
}

/** One legacy open order line at the cutoff. Quantities and the price are decimal strings. */
export interface OpenOrderRecord {
  legacyId: string;
  orderType: "SALES" | "PURCHASE";
  orderNumber: string;
  lineNumber: string;
  item: string;
  party: string;
  unit: string;
  currency: string;
  orderedQty: string;
  deliveredQty: string;
  unitPrice: string;
  deliveryDate?: string;
}

export interface OpenOrdersRunRequest {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoffTs: string;
  freezeTs: string;
  quarantineMaxPct?: string;
  /** The item master, and the customer and vendor masters of the order types present, the lines are checked against. */
  items: string[];
  customers?: string[];
  vendors?: string[];
  records: OpenOrderRecord[];
}

/**
 * A correction to a quarantined open item, bank balance or on-hand line (FR-49.11). Blank fields keep
 * the extracted value; each pack takes its own fields (open_items: party, account; bank_balances:
 * bankAccount; onhand: item, site, warehouse, location, batch, unit).
 */
export interface ReleaseRequest {
  party?: string;
  account?: string;
  bankAccount?: string;
  item?: string;
  site?: string;
  warehouse?: string;
  location?: string;
  batch?: string;
  unit?: string;
  /** Corrected delivery date (ISO) of an open_orders line; party, item and unit also correct one. */
  deliveryDate?: string;
  /** Customer and vendor ids added since an open_orders run. */
  customers?: string[];
  vendors?: string[];
  /** Master-data ids added since the run: parties, bank accounts or items (also of open_orders), by pack. */
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
  /** True once passageId is the id the target service assigned (the factory's own before that). */
  targetConfirmed?: boolean;
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
  /** The latest go/no-go decision; absent until one is recorded. */
  decision?: DecisionSummary;
  /** The rehearsal plan the planned durations were measured on, and how many steps took one. */
  rehearsalPlanId?: string;
  rehearsalCarried?: number;
  /**
   * The first approval of a rollback in the current go/no-go round, waiting for a second, different CutoverManager
   * (CUT-1603). A go/no-go decision recorded after it voids it.
   */
  pendingRollback?: { approver: string; approvedAt: string };
  /** Present once the plan is rolled back: the lines exported for re-keying (FR-49.9). */
  rekeying?: Rekeying;
  /** Present on get and commands; the list leaves it out. */
  steps?: RunbookStep[];
  /** The generated rollback plan, sent with the steps. */
  rollbackSteps?: RollbackStep[];
}

export type GoNoGoValue = "GO" | "NO_GO";
export type ChecklistStatus = "MET" | "OPEN" | "UNCONFIRMED";

export interface DecisionSummary {
  seq: number;
  decision: GoNoGoValue;
  /** Authenticated users who approved: two for a GO, the recording user for a NO_GO. */
  approvers: string[];
  reason?: string;
  decidedBy?: string;
  decidedAt: string;
}

/** One go/no-go checklist item (PRD 9.49.1); system items are evaluated by the factory, attested ones by the approvers. */
export interface ChecklistItem {
  code: string;
  title: string;
  source: "system" | "attested";
  status: ChecklistStatus;
  detail?: string;
  /** Run ids (pack runs or parallel runs) the item rests on. */
  references: string[];
}

/** Acceptance of a pack that is staged in the factory and not posted to its ledger, attested by both approvers (CUT-1608). */
export interface Waiver {
  pack: string;
  reason: string;
}

export interface GoNoGoDecision extends DecisionSummary {
  waivers: Waiver[];
  planVersion: number;
  /** The checklist as it was when the decision was taken. */
  checklist: ChecklistItem[];
}

export interface Readiness {
  planId: string;
  version: number;
  rolledBack: boolean;
  /** Both items the factory evaluates are met and the plan is not rolled back. */
  systemReady: boolean;
  items: ChecklistItem[];
  openItems: string[];
  decisions: GoNoGoDecision[];
  /** Packs of the plan that are staged and not posted; each keeps the packs item open unless waived. */
  stagedPacks: { pack: string; reason: string }[];
  /** A CutoverManager's approval of a GO waiting for a second, different one; the checklist shows its attestations. */
  pendingGo?: GoApproval;
}

export interface Attestation {
  code: string;
  confirmed: boolean;
  reference?: string;
}

/** The first approval of a GO (CUT-1605). Round is the seq the decision takes; a NO_GO in between voids it. */
export interface GoApproval {
  round: number;
  approver: string;
  attestations: Attestation[];
  waivers: Waiver[];
  planVersion: number;
  approvedAt: string;
}

/** The approver is the signed-in user; approvers are never named in the request. */
export interface DecisionRequest {
  decision: GoNoGoValue;
  reason?: string;
  attestations?: Attestation[];
  waivers?: Waiver[];
  version?: number;
}

/** A recorded decision (201), or the caller's approval of a GO waiting for a second approver (202). */
export interface DecisionResponse {
  decision?: GoNoGoDecision;
  pendingGo?: GoApproval;
  plan: CutoverPlan;
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
  /** Carry the durations measured on that earlier plan into this one (FR-49.6). */
  rehearsalPlanId?: string;
}

/** A configured control-total tolerance (FR-49.2, migration.tolerance.<pack>). `total` is `*` for the pack default. */
export interface Tolerance {
  pack: Pack;
  total: string;
  amount: string;
  reason: string;
  version: number;
  updatedBy: string;
  updatedAt: string;
}

export interface ToleranceRequest {
  pack: Pack;
  total: string;
  amount: string;
  reason?: string;
  /** The stored version when changing an existing tolerance. */
  version?: number;
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

/** POST /migration/runs/{id}/load: send the run's pending rows to receivables and reconcile its totals. */
export function loadRunTarget(runId: string): Promise<TargetLoad> {
  return send(`/migration/runs/${encodeURIComponent(runId)}/load`, { method: "POST" });
}

/** GET /migration/runs/{id}/target-load: status, reconciliation lines and the first rejects. */
export function getTargetLoad(runId: string, signal?: AbortSignal): Promise<TargetLoadDetail> {
  return send(`/migration/runs/${encodeURIComponent(runId)}/target-load`, {}, signal);
}

/** GET /migration/runs/{id}/target-rows. */
export function listTargetRows(runId: string, filter: { status?: TargetRowStatus; after?: number; limit?: number } = {}, signal?: AbortSignal): Promise<TargetRow[]> {
  return items(`/migration/runs/${encodeURIComponent(runId)}/target-rows${query(filter)}`, signal);
}

/** POST /migration/packs/bank_balances/runs: bank account opening balances (FR-49.1). */
export function startBankRun(body: BankRunRequest, key: string = newKey()): Promise<PackRunDetail> {
  return post("/migration/packs/bank_balances/runs", body, key);
}

/** POST /migration/packs/onhand/runs: opening stock at legacy cost (FR-49.1, BR-49.3). */
export function startOnhandRun(body: OnhandRunRequest, key: string = newKey()): Promise<PackRunDetail> {
  return post("/migration/packs/onhand/runs", body, key);
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
 * POST /cutover/plans/{id}/rollback as the signed-in user. Needs two different CutoverManagers (CUT-1603): the first call
 * stores the caller's approval and returns the plan with pendingRollback (202), a second caller's call runs the rollback.
 * postings are the Passage postings since T-0 to export for re-keying; one the service rejects leaves the plan as it was.
 */
export function rollbackPlan(planId: string, postings: RekeyPosting[], version: number, key: string = newKey()): Promise<CutoverPlan> {
  return post(`/cutover/plans/${encodeURIComponent(planId)}/rollback`, { postings, version }, key);
}

/** GET /cutover/plans/{id}/go-no-go: the checklist evaluated now and the decisions so far. */
export function getReadiness(planId: string, signal?: AbortSignal): Promise<Readiness> {
  return send(`/cutover/plans/${encodeURIComponent(planId)}/go-no-go`, {}, signal);
}

/**
 * POST /cutover/plans/{id}/go-no-go as the signed-in user. A NO_GO (with a reason) is recorded at once. A GO needs
 * every checklist item met (CUT-1607) and two different CutoverManagers (CUT-1605): the first call stores the caller's
 * approval, a second caller's GO records it. The service re-evaluates the checklist when it stores the request.
 */
export function recordDecision(planId: string, input: DecisionRequest, key: string = newKey()): Promise<DecisionResponse> {
  return post(`/cutover/plans/${encodeURIComponent(planId)}/go-no-go`, input, key);
}

/** GET /cutover/plans/{id}/rekeying/{file}: the CSV download of a rolled-back plan's re-keying file. */
export function rekeyFileUrl(planId: string, file: RekeyFile): string {
  return `/api/v1/cutover/plans/${encodeURIComponent(planId)}/rekeying/${file}`;
}

/** The CSV exports of a cutover plan (9.49.15). */
export type ExportFile = "runbook.csv" | "rollback-plan.csv" | "go-no-go.csv" | "decisions.csv" | "reconciliation.csv";

/** GET /cutover/plans/{id}/export/{file}: the CSV download of the runbook, rollback plan, go/no-go checklist, decisions or reconciliation. */
export function exportFileUrl(planId: string, file: ExportFile): string {
  return `/api/v1/cutover/plans/${encodeURIComponent(planId)}/export/${file}`;
}

/** POST /migration/packs/open_orders/runs: open sales and purchase order lines (FR-49.1). */
export function startOpenOrdersRun(body: OpenOrdersRunRequest, key: string = newKey()): Promise<PackRunDetail> {
  return post("/migration/packs/open_orders/runs", body, key);
}

/** GET /migration/tolerances: the configured tolerances; a total without one compares at 0.00. */
export function listTolerances(signal?: AbortSignal): Promise<Tolerance[]> {
  return items("/migration/tolerances", signal);
}

/** PUT /migration/tolerances: sets the tolerance of a pack's control total. */
export function putTolerance(body: ToleranceRequest, key: string = newKey()): Promise<Tolerance> {
  return send("/migration/tolerances", { method: "PUT", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) });
}
