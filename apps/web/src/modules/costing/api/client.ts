import { apiFetch } from "../../../shell/api/http";
// API client for the costing service (services/costing/api/openapi.yaml).
// Browser calls use /api/v1/costing/... which the web-bff rewrites to the service.

export type CostMethod =
  | "FIFO"
  | "LIFO"
  | "LIFO_DATE"
  | "WEIGHTED_AVG_RUNNING"
  | "WEIGHTED_AVG_PERIODIC"
  | "MOVING_AVG"
  | "STANDARD";

export interface SettleTransaction {
  id: string;
  kind: "RECEIPT" | "ISSUE";
  qty: string;
  value?: string;
  financialDate: string;
  physicalDate?: string;
  /** Split valuation type (FR-20.16). */
  valuationType?: string;
  /** ISSUE only: the receipt this issue settles against first (FR-20.8). */
  markedReceiptId?: string;
}

export type Fallback = "RUNNING_AVG" | "STANDARD" | "LAST_RECEIPT";

export interface SettlePreviewRequest {
  method: CostMethod;
  currency: string;
  fallback?: Fallback;
  standardUnitCost?: string;
  defaultValuationType?: string;
  transactions: SettleTransaction[];
}

export interface SettlementRow {
  seq: number;
  issueId: string;
  receiptId?: string;
  pool?: string;
  qty: string;
  value: string;
}

export interface SettlePreviewResponse {
  method: string;
  currency: string;
  issueCosts: Record<string, string>;
  settlements: SettlementRow[];
  exceptions: { issueId: string; qty: string; value: string; fallback: string }[];
  remainingQty: string;
  remainingValue: string;
  /** Present only when stock is split by valuation type. */
  remainingByValuationType?: Record<string, string>;
}

export interface LedgerSpec {
  id: string;
  method: CostMethod;
  currency: string;
  /** Ledger currency per unit of the primary currency; exactly "1" for the primary. */
  rate: string;
  primary: boolean;
  standardUnitCost?: string;
  fallback?: Fallback;
}

export interface LedgersPreviewRequest {
  ledgers: LedgerSpec[];
  transactions: SettleTransaction[];
}

export interface LedgerResult {
  id: string;
  currency: string;
  primary: boolean;
  issueCosts: Record<string, string>;
  remainingQty: string;
  remainingValue: string;
  settlements: number;
  exceptions: number;
}

export interface LedgersPreviewResponse {
  ledgers: LedgerResult[];
}

export interface AgingPreviewRequest {
  method: CostMethod;
  currency: string;
  fallback?: Fallback;
  standardUnitCost?: string;
  defaultValuationType?: string;
  asOf: string;
  bucketBounds?: number[];
  transactions: SettleTransaction[];
}

export interface AgingBucket {
  label: string;
  qty: string;
  value: string;
}

export interface AgingTypeResult {
  valuationType: string;
  buckets: AgingBucket[];
  totalQty: string;
  totalValue: string;
  remainingValue: string;
}

export interface AgingPreviewResponse {
  method: string;
  currency: string;
  asOf: string;
  buckets: AgingBucket[];
  totalQty: string;
  totalValue: string;
  remainingValue: string;
  tiesToRemaining: boolean;
  /** Present only when stock is split by valuation type. */
  valuationTypes?: AgingTypeResult[];
}

export interface Problem {
  title: string;
  status: number;
  code: string;
  detail?: string;
  /** Field errors of a 400 (COM-0001), keyed by request path. */
  errors?: Record<string, string>;
}

export class CostingApiError extends Error {
  constructor(public readonly problem: Problem) {
    super(problem.detail ?? problem.title);
    this.name = "CostingApiError";
  }
}

async function request<T>(path: string, init: RequestInit, fetchImpl: typeof fetch): Promise<T> {
  const res = await fetchImpl(path, {
    ...init,
    headers: { accept: "application/json", ...(init.headers as Record<string, string> | undefined) },
    credentials: "same-origin",
  });
  if (!res.ok) {
    const problem = (await res.json().catch(() => ({}))) as Partial<Problem>;
    throw new CostingApiError({
      title: problem.title ?? `HTTP ${res.status}`,
      status: res.status,
      code: problem.code ?? "COM-0000",
      detail: problem.detail,
      errors: problem.errors,
    });
  }
  return (await res.json()) as T;
}

function post<T>(path: string, body: unknown, fetchImpl: typeof fetch, headers: Record<string, string> = {}): Promise<T> {
  return request<T>(path, { method: "POST", headers: { "content-type": "application/json", ...headers }, body: JSON.stringify(body) }, fetchImpl);
}

export function previewSettlements(body: SettlePreviewRequest, fetchImpl: typeof fetch = apiFetch): Promise<SettlePreviewResponse> {
  return post("/api/v1/costing/settlements:preview", body, fetchImpl);
}

export function previewValuationLedgers(body: LedgersPreviewRequest, fetchImpl: typeof fetch = apiFetch): Promise<LedgersPreviewResponse> {
  return post("/api/v1/costing/valuation-ledgers:preview", body, fetchImpl);
}

export function previewAging(body: AgingPreviewRequest, fetchImpl: typeof fetch = apiFetch): Promise<AgingPreviewResponse> {
  return post("/api/v1/costing/aging:preview", body, fetchImpl);
}

/** One-line description of a failed call: the stable code, the detail and any field errors. */
export function describeError(err: unknown, fallback: string): string {
  if (!(err instanceof CostingApiError)) {
    return fallback;
  }
  const fields = Object.entries(err.problem.errors ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([field, msg]) => `${field} ${msg}`);
  return [`${err.problem.code}: ${err.message}`, ...fields].join("; ");
}

// ---- costing versions (FR-20.5) ----

export type VersionType = "STANDARD" | "PLANNED";
export type VersionStatus = "DRAFT" | "ACTIVE" | "ARCHIVED";

export interface CostingVersion {
  id: string;
  legalEntityId: string;
  code: string;
  type: VersionType;
  status: VersionStatus;
  currency: string;
  effectiveFrom: string;
  blockActivation: boolean;
  /** Optimistic version: send it as If-Match to activate. */
  version: number;
  createdAt: string;
  activatedAt?: string;
  itemCount: number;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export interface VersionFilter {
  legalEntityId?: string;
  status?: VersionStatus | "";
  type?: VersionType | "";
  cursor?: string;
}

export interface CreateVersionRequest {
  legalEntityId: string;
  code: string;
  type: VersionType;
  currency: string;
  effectiveFrom: string;
  blockActivation: boolean;
}

export type ItemCostStatus = "PENDING" | "ACTIVE" | "SUPERSEDED";

export interface ItemCostRow {
  productId: string;
  siteId: string;
  unitCost: string;
  status: ItemCostStatus;
  source: "MANUAL" | "IMPORT" | "COPY" | "ROLLUP";
  /** Standard active for this product and site in another version. */
  activeCost?: string;
  previousUnitCost?: string;
  bookValue?: string;
  onHandQty?: string;
  revaluationAmount?: string;
  activatedAt?: string;
}

export interface OnHandLine {
  productId: string;
  siteId: string;
  qty: string;
  /** Current inventory value; needed where the product and site has no standard yet. */
  bookValue?: string;
}

export interface ActivationDelta {
  productId: string;
  siteId: string;
  previousCost?: string;
  bookValue?: string;
  newCost: string;
  onHandQty: string;
  revaluation: string;
}

/** Revaluation preview (PREVIEW), committed activation (ACTIVATED) or a replay (ALREADY_ACTIVE). */
export interface ActivationResponse {
  versionId: string;
  versionCode: string;
  version: number;
  status: "PREVIEW" | "ACTIVATED" | "ALREADY_ACTIVE";
  currency: string;
  lineCount: number;
  revaluationTotal: string;
  sites: { siteId: string; revaluation: string }[];
  topDeltas: ActivationDelta[];
}

const versionsPath = "/api/v1/costing/versions";
const versionPath = (id: string) => `${versionsPath}/${encodeURIComponent(id)}`;

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : "";
}

export function listVersions(filter: VersionFilter = {}, signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<Page<CostingVersion>> {
  const q = query({ legalEntityId: filter.legalEntityId, status: filter.status, type: filter.type, cursor: filter.cursor });
  return request(`${versionsPath}${q}`, { method: "GET", signal }, fetchImpl);
}

export function getVersion(id: string, signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<CostingVersion> {
  return request(versionPath(id), { method: "GET", signal }, fetchImpl);
}

export function listItemCosts(id: string, cursor?: string, signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<Page<ItemCostRow>> {
  return request(`${versionPath(id)}/item-costs${query({ cursor })}`, { method: "GET", signal }, fetchImpl);
}

/** Creates a DRAFT version; the Idempotency-Key makes a retried submit safe. */
export function createVersion(body: CreateVersionRequest, idempotencyKey: string, fetchImpl: typeof fetch = apiFetch): Promise<CostingVersion> {
  return post(versionsPath, body, fetchImpl, { "Idempotency-Key": idempotencyKey });
}

/** Imports a CSV file (header productId,siteId,unitCost) into a DRAFT version, all or nothing. */
export function importItemCostsCsv(id: string, csv: string, fetchImpl: typeof fetch = apiFetch): Promise<{ versionId: string; imported: number }> {
  return request(`${versionPath(id)}/item-costs:import`, { method: "POST", headers: { "content-type": "text/csv" }, body: csv }, fetchImpl);
}

export function copyItemCosts(
  targetId: string,
  sourceVersionId: string,
  percentChange: string,
  fetchImpl: typeof fetch = apiFetch,
): Promise<{ versionId: string; copied: number }> {
  return post(`${versionPath(targetId)}/item-costs:copy`, { sourceVersionId, percentChange }, fetchImpl);
}

export function previewActivation(id: string, onHand: OnHandLine[], fetchImpl: typeof fetch = apiFetch): Promise<ActivationResponse> {
  return post(`${versionPath(id)}:preview-activation`, { onHand }, fetchImpl);
}

/** Activates at the version the preview returned; a change since then is 412 conflict_version. */
export function activateVersion(id: string, version: number, onHand: OnHandLine[], fetchImpl: typeof fetch = apiFetch): Promise<ActivationResponse> {
  return post(`${versionPath(id)}:activate`, { onHand }, fetchImpl, { "If-Match": `"${version}"` });
}

// ---- valuation setup (FR-20.15, FR-20.16) ----

export type RateType = "SPOT" | "AVERAGE" | "HISTORICAL";

export interface ValuationLedgerRequest {
  legalEntityId: string;
  /** The wave 12 ledger group the ledger posts to; one valuation ledger per group. */
  ledgerGroupId: string;
  code: string;
  method: CostMethod;
  currency: string;
  rateType: RateType;
  primary: boolean;
}

export interface ValuationLedger extends ValuationLedgerRequest {
  id: string;
  createdAt: string;
}

export interface ValuationType {
  code: string;
  /** Inventory account of the type (the valuation class equivalent). */
  account: string;
}

export interface ValuationCategoryRequest {
  code: string;
  /** A batch category lists no types: each batch becomes its own type at receipt. */
  batch: boolean;
  types: ValuationType[];
}

export interface ValuationCategory extends ValuationCategoryRequest {
  id: string;
  createdAt: string;
}

export function listValuationLedgers(legalEntityId = "", signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<{ items: ValuationLedger[] }> {
  return request(`/api/v1/costing/valuation-ledgers${query({ legalEntityId })}`, { method: "GET", signal }, fetchImpl);
}

export function createValuationLedger(body: ValuationLedgerRequest, fetchImpl: typeof fetch = apiFetch): Promise<ValuationLedger> {
  return post("/api/v1/costing/valuation-ledgers", body, fetchImpl);
}

export function listValuationCategories(signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<{ items: ValuationCategory[] }> {
  return request("/api/v1/costing/valuation-categories", { method: "GET", signal }, fetchImpl);
}

export function createValuationCategory(body: ValuationCategoryRequest, fetchImpl: typeof fetch = apiFetch): Promise<ValuationCategory> {
  return post("/api/v1/costing/valuation-categories", body, fetchImpl);
}

// ---- aging bucket sets and month-end snapshots (FR-20.17) ----

export interface BucketSet {
  id: string;
  legalEntityId: string;
  name: string;
  bounds: number[];
  createdAt: string;
}

export interface SnapshotLineRequest {
  productId: string;
  siteId: string;
  standardUnitCost?: string;
  defaultValuationType?: string;
  transactions: SettleTransaction[];
}

export interface SnapshotRequest {
  legalEntityId: string;
  /** With a ledger the snapshot takes its method and currency. */
  valuationLedgerId?: string;
  asOf: string;
  method?: CostMethod;
  currency?: string;
  fallback?: Fallback;
  bucketSetId?: string;
  bucketBounds?: number[];
  lines: SnapshotLineRequest[];
}

export interface SnapshotLine {
  productId: string;
  siteId: string;
  buckets: AgingBucket[];
  totalQty: string;
  totalValue: string;
  remainingValue: string;
  tiesToRemaining: boolean;
  valuationTypes?: AgingTypeResult[];
}

export interface AgingSnapshot {
  id: string;
  legalEntityId: string;
  valuationLedgerId?: string;
  asOf: string;
  method: string;
  currency: string;
  bucketSetId?: string;
  buckets: AgingBucket[];
  totalQty: string;
  totalValue: string;
  remainingValue: string;
  tiesToRemaining: boolean;
  lineCount: number;
  /** Absent from list results. */
  lines?: SnapshotLine[];
  createdAt: string;
}

export function listBucketSets(legalEntityId = "", signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<{ items: BucketSet[] }> {
  return request(`/api/v1/costing/aging/bucket-sets${query({ legalEntityId })}`, { method: "GET", signal }, fetchImpl);
}

export function createBucketSet(body: { legalEntityId: string; name: string; bounds: number[] }, fetchImpl: typeof fetch = apiFetch): Promise<BucketSet> {
  return post("/api/v1/costing/aging/bucket-sets", body, fetchImpl);
}

export function listAgingSnapshots(
  filter: { legalEntityId?: string; cursor?: string } = {},
  signal?: AbortSignal,
  fetchImpl: typeof fetch = apiFetch,
): Promise<Page<AgingSnapshot>> {
  return request(`/api/v1/costing/aging/snapshots${query({ legalEntityId: filter.legalEntityId, cursor: filter.cursor })}`, { method: "GET", signal }, fetchImpl);
}

export function getAgingSnapshot(id: string, signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<AgingSnapshot> {
  return request(`/api/v1/costing/aging/snapshots/${encodeURIComponent(id)}`, { method: "GET", signal }, fetchImpl);
}

/** Stores a month-end snapshot; the Idempotency-Key makes a retried submit a replay, not a second snapshot. */
export function createAgingSnapshot(body: SnapshotRequest, idempotencyKey: string, fetchImpl: typeof fetch = apiFetch): Promise<AgingSnapshot> {
  return post("/api/v1/costing/aging/snapshots", body, fetchImpl, { "Idempotency-Key": idempotencyKey });
}

// ---- cost transactions, inventory value report and GL tie-out (FR-20.4) ----

export const REPORT_DIMENSIONS = ["product", "site", "warehouse", "valuationType"] as const;
export type ReportDimension = (typeof REPORT_DIMENSIONS)[number];

export const COST_KINDS = ["PHYSICAL", "FINANCIAL", "INVOICE_DIFF", "SETTLEMENT_ADJ", "PPV", "REVALUATION", "MANUAL_ADJ", "RECALC"] as const;
export type CostKind = (typeof COST_KINDS)[number];

export interface ValueReportParams {
  legalEntityId: string;
  asOf: string;
  /** A valuation ledger of the legal entity; omitted for the primary valuation. */
  valuationLedgerId?: string;
  siteId?: string;
  /** Empty is one total row. */
  groupBy: ReportDimension[];
}

export interface ValueAmounts {
  qty: string;
  /** PHYSICAL rows: received or shipped, not yet invoiced. */
  physicalValue: string;
  financialValue: string;
  /** physicalValue + financialValue, which the tie-out compares with the ledger. */
  value: string;
}

export interface ValueReportRow extends ValueAmounts {
  key: Partial<Record<ReportDimension, string>>;
}

interface ReportHeader {
  legalEntityId: string;
  asOf: string;
  valuationLedgerId?: string;
  valuationLedgerCode?: string;
  siteId?: string;
  /** Empty when neither a ledger nor any row names one. */
  currency: string;
  groupBy: ReportDimension[];
}

export interface ValueReport extends ReportHeader {
  rows: ValueReportRow[];
  total: ValueAmounts;
}

export interface LedgerBalance {
  dimensions: Partial<Record<ReportDimension, string>>;
  balance: string;
}

export interface TieOutRow {
  key: Partial<Record<ReportDimension, string>>;
  inventory: string;
  ledger: string;
  diff: string;
}

export interface TieOut extends ReportHeader {
  rows: TieOutRow[];
  differences: number;
  totalDiff: string;
  ties: boolean;
}

export interface CostTransaction {
  id: string;
  sourceKey: string;
  legalEntityId: string;
  inventoryTransactionId?: string;
  ledgerEntryId?: string;
  productId: string;
  siteId: string;
  warehouseId?: string;
  valuationType?: string;
  valuationLedgerId?: string;
  kind: CostKind;
  qty: string;
  amount: string;
  currency: string;
  financialDate: string;
  createdAt: string;
}

export interface CostTransactionFilter {
  legalEntityId: string;
  valuationLedgerId?: string;
  productId?: string;
  siteId?: string;
  kind?: CostKind | "";
  asOf?: string;
  cursor?: string;
}

/** The inventory value at a date, grouped by the given dimensions, for one valuation ledger. */
export function getValueReport(params: ValueReportParams, signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<ValueReport> {
  const q = new URLSearchParams({ legalEntityId: params.legalEntityId, asOf: params.asOf, groupBy: params.groupBy.join(",") });
  if (params.valuationLedgerId) q.set("valuationLedgerId", params.valuationLedgerId);
  if (params.siteId) q.set("siteId", params.siteId);
  return request(`/api/v1/costing/value-report?${q.toString()}`, { method: "GET", signal }, fetchImpl);
}

/** Compares the value report with ledger balances grouped the same way. It only reads. */
export function tieOutValueReport(body: ValueReportParams & { glBalances: LedgerBalance[] }, fetchImpl: typeof fetch = apiFetch): Promise<TieOut> {
  return post("/api/v1/costing/value-report:tie-out", body, fetchImpl);
}

/** The cost transactions behind the report, newest financial date first. */
export function listCostTransactions(filter: CostTransactionFilter, signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<Page<CostTransaction>> {
  const q = query({
    legalEntityId: filter.legalEntityId,
    valuationLedgerId: filter.valuationLedgerId,
    productId: filter.productId,
    siteId: filter.siteId,
    kind: filter.kind || undefined,
    asOf: filter.asOf,
    cursor: filter.cursor,
  });
  return request(`/api/v1/costing/cost-transactions${q}`, { method: "GET", signal }, fetchImpl);
}

// ---- manual cost adjustments (FR-20.6) ----

export type AdjustmentKind = "ON_HAND" | "RECEIPT";
export type AdjustmentMode = "UNIT_COST" | "AMOUNT";

/**
 * ON_HAND names the product and site (warehouse and valuation type optional);
 * RECEIPT names receiptTransactionId and is by AMOUNT. unitCost is for
 * UNIT_COST, amount for AMOUNT; expectedValue is the value the preview showed.
 */
export interface AdjustmentRequest {
  legalEntityId: string;
  kind: AdjustmentKind;
  mode: AdjustmentMode;
  productId?: string;
  siteId?: string;
  warehouseId?: string;
  valuationType?: string;
  valuationLedgerId?: string;
  receiptTransactionId?: string;
  financialDate: string;
  unitCost?: string;
  amount?: string;
  expectedValue?: string;
  reason?: string;
}

export interface Adjustment {
  /** Absent on a preview. */
  id?: string;
  legalEntityId: string;
  kind: AdjustmentKind;
  mode: AdjustmentMode;
  productId: string;
  siteId: string;
  warehouseId?: string;
  valuationType?: string;
  /** Absent for the primary valuation. */
  valuationLedgerId?: string;
  receiptTransactionId?: string;
  currency: string;
  financialDate: string;
  reason?: string;
  unitCost?: string;
  amount?: string;
  qty: string;
  oldValue: string;
  newValue: string;
  /** newValue − oldValue: the MANUAL_ADJ cost transaction's amount. */
  delta: string;
  oldUnitCost?: string;
  newUnitCost?: string;
  costTransactionId?: string;
  createdAt?: string;
}

export interface AdjustmentFilter {
  legalEntityId?: string;
  kind?: AdjustmentKind | "";
  productId?: string;
  cursor?: string;
}

const adjustmentsPath = "/api/v1/costing/adjustments";

/** What the adjustment would post, without posting it. */
export function previewAdjustment(body: AdjustmentRequest, fetchImpl: typeof fetch = apiFetch): Promise<Adjustment> {
  return post(`${adjustmentsPath}:preview`, body, fetchImpl);
}

/** Posts an adjustment; the Idempotency-Key makes a retried submit a replay, not a second adjustment. */
export function createAdjustment(body: AdjustmentRequest, idempotencyKey: string, fetchImpl: typeof fetch = apiFetch): Promise<Adjustment> {
  return post(adjustmentsPath, body, fetchImpl, { "Idempotency-Key": idempotencyKey });
}

export function listAdjustments(filter: AdjustmentFilter = {}, signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<Page<Adjustment>> {
  const q = query({ legalEntityId: filter.legalEntityId, kind: filter.kind || undefined, productId: filter.productId, cursor: filter.cursor });
  return request(`${adjustmentsPath}${q}`, { method: "GET", signal }, fetchImpl);
}

export function getAdjustment(id: string, signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<Adjustment> {
  return request(`${adjustmentsPath}/${encodeURIComponent(id)}`, { method: "GET", signal }, fetchImpl);
}

// ---- stored reconciliation runs (FR-20.4) ----

export interface ReconciliationRun extends ReportHeader {
  id: string;
  differences: number;
  totalDiff: string;
  ties: boolean;
  rowCount: number;
  /** Absent from list results. */
  rows?: TieOutRow[];
  createdAt: string;
}

const reconciliationsPath = "/api/v1/costing/reconciliations";

/** Runs the tie-out and stores it; each differing group emits reconciliation.diff. */
export function createReconciliation(
  body: ValueReportParams & { glBalances: LedgerBalance[] },
  idempotencyKey: string,
  fetchImpl: typeof fetch = apiFetch,
): Promise<ReconciliationRun> {
  return post(reconciliationsPath, body, fetchImpl, { "Idempotency-Key": idempotencyKey });
}

export function listReconciliations(
  filter: { legalEntityId?: string; cursor?: string } = {},
  signal?: AbortSignal,
  fetchImpl: typeof fetch = apiFetch,
): Promise<Page<ReconciliationRun>> {
  return request(`${reconciliationsPath}${query({ legalEntityId: filter.legalEntityId, cursor: filter.cursor })}`, { method: "GET", signal }, fetchImpl);
}

export function getReconciliation(id: string, signal?: AbortSignal, fetchImpl: typeof fetch = apiFetch): Promise<ReconciliationRun> {
  return request(`${reconciliationsPath}/${encodeURIComponent(id)}`, { method: "GET", signal }, fetchImpl);
}
