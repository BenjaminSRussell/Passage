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
}

export type Fallback = "RUNNING_AVG" | "STANDARD" | "LAST_RECEIPT";

export interface SettlePreviewRequest {
  method: CostMethod;
  currency: string;
  fallback?: Fallback;
  standardUnitCost?: string;
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
}

export interface Problem {
  title: string;
  status: number;
  code: string;
  detail?: string;
}

export class CostingApiError extends Error {
  constructor(public readonly problem: Problem) {
    super(problem.detail ?? problem.title);
    this.name = "CostingApiError";
  }
}

export async function previewSettlements(
  body: SettlePreviewRequest,
  fetchImpl: typeof fetch = fetch,
): Promise<SettlePreviewResponse> {
  const res = await fetchImpl("/api/v1/costing/settlements:preview", {
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
    });
  }
  return (await res.json()) as SettlePreviewResponse;
}
