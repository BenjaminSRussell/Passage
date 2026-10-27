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

export function previewSettlements(body: SettlePreviewRequest, fetchImpl: typeof fetch = fetch): Promise<SettlePreviewResponse> {
  return post("/api/v1/costing/settlements:preview", body, fetchImpl);
}

export function previewValuationLedgers(body: LedgersPreviewRequest, fetchImpl: typeof fetch = fetch): Promise<LedgersPreviewResponse> {
  return post("/api/v1/costing/valuation-ledgers:preview", body, fetchImpl);
}

export function previewAging(body: AgingPreviewRequest, fetchImpl: typeof fetch = fetch): Promise<AgingPreviewResponse> {
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

export function listVersions(filter: VersionFilter = {}, signal?: AbortSignal, fetchImpl: typeof fetch = fetch): Promise<Page<CostingVersion>> {
  const q = query({ legalEntityId: filter.legalEntityId, status: filter.status, type: filter.type, cursor: filter.cursor });
  return request(`${versionsPath}${q}`, { method: "GET", signal }, fetchImpl);
}

export function getVersion(id: string, signal?: AbortSignal, fetchImpl: typeof fetch = fetch): Promise<CostingVersion> {
  return request(versionPath(id), { method: "GET", signal }, fetchImpl);
}

export function listItemCosts(id: string, cursor?: string, signal?: AbortSignal, fetchImpl: typeof fetch = fetch): Promise<Page<ItemCostRow>> {
  return request(`${versionPath(id)}/item-costs${query({ cursor })}`, { method: "GET", signal }, fetchImpl);
}

/** Creates a DRAFT version; the Idempotency-Key makes a retried submit safe. */
export function createVersion(body: CreateVersionRequest, idempotencyKey: string, fetchImpl: typeof fetch = fetch): Promise<CostingVersion> {
  return post(versionsPath, body, fetchImpl, { "Idempotency-Key": idempotencyKey });
}

/** Imports a CSV file (header productId,siteId,unitCost) into a DRAFT version, all or nothing. */
export function importItemCostsCsv(id: string, csv: string, fetchImpl: typeof fetch = fetch): Promise<{ versionId: string; imported: number }> {
  return request(`${versionPath(id)}/item-costs:import`, { method: "POST", headers: { "content-type": "text/csv" }, body: csv }, fetchImpl);
}

export function copyItemCosts(
  targetId: string,
  sourceVersionId: string,
  percentChange: string,
  fetchImpl: typeof fetch = fetch,
): Promise<{ versionId: string; copied: number }> {
  return post(`${versionPath(targetId)}/item-costs:copy`, { sourceVersionId, percentChange }, fetchImpl);
}

export function previewActivation(id: string, onHand: OnHandLine[], fetchImpl: typeof fetch = fetch): Promise<ActivationResponse> {
  return post(`${versionPath(id)}:preview-activation`, { onHand }, fetchImpl);
}

/** Activates at the version the preview returned; a change since then is 412 conflict_version. */
export function activateVersion(id: string, version: number, onHand: OnHandLine[], fetchImpl: typeof fetch = fetch): Promise<ActivationResponse> {
  return post(`${versionPath(id)}:activate`, { onHand }, fetchImpl, { "If-Match": `"${version}"` });
}
