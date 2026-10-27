// Client for the productcost service (services/productcost/api/openapi.yaml).
// Browser calls use /api/v1/costing/..., which the web BFF rewrites to the
// service and signs with the user's tenant and duties. Amounts are decimal
// strings; the browser never does arithmetic on a float amount.

export const base = "/api/v1/costing";

/** Cost component code to amount, e.g. { DM: "21.2", DL: "54" }. */
export type Split = Record<string, string>;

export type ItemKind = "Purchased" | "Manufactured";
export type ExplosionMode = "Multilevel" | "SingleLevel";

export interface BOMLine {
  childId: string;
  qty: string;
  scrapPct?: string;
}

export interface Operation {
  component?: string;
  setupHours?: string;
  runHours?: string;
  rate?: string;
  lotSize?: string;
}

export interface SheetNode {
  id: string;
  type: "Surcharge" | "Rate";
  component: string;
  basis?: string[];
  percentage?: string;
  rate?: string;
  rateUnit?: "Hour" | "Unit";
  includeRolled?: boolean;
}

export interface CostItem {
  id: string;
  kind: ItemKind;
  price?: string;
  activePrice?: string;
  activeSplit?: Split;
  costingLot?: string;
  bom?: BOMLine[];
  operations?: Operation[];
  sheet?: SheetNode[];
}

export interface RollOptions {
  mode?: ExplosionMode;
  maxLevels?: number;
  workers?: number;
  fallbackToActive?: boolean;
}

export interface Diagnostic {
  code: string;
  severity: "error" | "warning";
  message: string;
  ref?: string;
}

export interface CostResult {
  itemId: string;
  status: "Pending" | "Error";
  level: number;
  unitCost: string;
  own: Split;
  rolled: Split;
  diagnostics?: Diagnostic[];
}

export interface CalculationResponse {
  result: CostResult;
  /** Own plus rolled, per component. */
  split: Split;
}

export interface RollRun {
  results: CostResult[];
  itemCount: number;
  errorCount: number;
  durationMs: number;
}

export interface OrderLine {
  component: string;
  planned: boolean;
  fixed?: boolean;
  plannedQty?: string;
  stdUnitCost?: string;
  estPerLot?: string;
  actualQty?: string;
  actualUnitCost?: string;
}

export interface Order {
  currency?: string;
  plannedQty: string;
  rafQty: string;
  lines: OrderLine[];
}

export interface VarianceTypes {
  lotSize: string;
  quantity: string;
  price: string;
  substitution: string;
}

export interface Decomposition {
  total: string;
  types: VarianceTypes;
  byComponent: { component: string; total: string; types: VarianceTypes }[];
  residual: string;
}

export interface RevalLine {
  itemId: string;
  siteId: string;
  qty: string;
  oldStd: string;
  newStd: string;
}

export interface JournalLine {
  account: string;
  debit: string;
  credit: string;
}

export interface ActivationPreview {
  currency?: string;
  total: string;
  lines: (RevalLine & { amount: string })[];
  journal: JournalLine[];
}

/** A failed request, normalised from the service's RFC 9457 problem. */
export interface Problem {
  status: number;
  code: string;
  message: string;
  /** Validation messages keyed by request path, such as items[0].bom[1].qty. */
  fields: Record<string, string>;
}

export class CostingApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "CostingApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof CostingApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<CostingApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields =
    body?.errors && typeof body.errors === "object" && !Array.isArray(body.errors) ? (body.errors as Record<string, string>) : {};
  return new CostingApiError(res.status, code, message, fields);
}

async function post<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(`${base}${path}`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(body),
    credentials: "same-origin",
    signal,
  });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

/** Roll one item from the supplied structure (FR-39.4). A failed item is a result with status Error. */
export function rollOne(
  req: { items: CostItem[]; itemId: string; options?: RollOptions },
  signal?: AbortSignal,
): Promise<CalculationResponse> {
  return post<CalculationResponse>("/calculations", req, signal);
}

/** Roll every supplied item by level, with per-item diagnostics (FR-39.5). */
export function massRoll(req: { items: CostItem[]; options?: RollOptions }, signal?: AbortSignal): Promise<RollRun> {
  return post<RollRun>("/rolls", req, signal);
}

/** Decompose an ended order's variance into lot size, quantity, price and substitution (FR-39.8). */
export function computeVariance(order: Order, signal?: AbortSignal): Promise<Decomposition> {
  return post<Decomposition>("/variances/compute", order, signal);
}

/** Preview the on-hand revaluation an activation would post (FR-39.6). Nothing is posted. */
export function previewActivation(req: { currency?: string; lines: RevalLine[] }, signal?: AbortSignal): Promise<ActivationPreview> {
  return post<ActivationPreview>("/activations:preview", req, signal);
}
