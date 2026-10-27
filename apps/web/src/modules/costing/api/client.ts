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

async function post<T>(path: string, body: unknown, fetchImpl: typeof fetch): Promise<T> {
  const res = await fetchImpl(path, {
    method: "POST",
    headers: { "content-type": "application/json", accept: "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(body),
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
