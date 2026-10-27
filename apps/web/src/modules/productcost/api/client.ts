// Client for the productcost service (services/productcost/api/openapi.yaml).
// Browser calls use /api/v1/costing/..., which the web BFF rewrites to the
// service and signs with the user's tenant and duties. Amounts are decimal
// strings; the browser never does arithmetic on a float amount.

export const base = "/api/v1/costing";

/** Cost component code to amount, e.g. { DM: "21.2", DL: "54" }. */
export type Split = Record<string, string>;

export type ItemKind = "Purchased" | "Manufactured";
export type ExplosionMode = "Multilevel" | "SingleLevel" | "MakeToOrder" | "ByLineType";

export interface BOMLine {
  childId: string;
  qty: string;
  scrapPct?: string;
  /** Phantom line, exploded in place in every mode (BR-39.3). */
  phantom?: boolean;
  /** Make-to-order sub-assembly, exploded by the MakeToOrder mode. */
  makeToOrder?: boolean;
}

export interface Operation {
  component?: string;
  setupHours?: string;
  runHours?: string;
  rate?: string;
  lotSize?: string;
  /** Operation scrap in percent; compounds backward through the route (BR-39.6). */
  scrapPct?: string;
}

/**
 * A costing sheet node, evaluated in sheet order (FR-39.2). Surcharge and Rate
 * charge `component`; Basis and Total report subtotals and charge nothing.
 */
export interface SheetNode {
  id: string;
  type: "Basis" | "Surcharge" | "Rate" | "Total";
  component?: string;
  /** Components summed by a Basis node, or a Surcharge's basis. */
  basis?: string[];
  /** Surcharge: an earlier Basis or Total node used as the basis instead of `basis`. */
  basisRef?: string;
  /** Total: earlier Basis or Total nodes to add. */
  refs?: string[];
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
  /** Purchased from an intercompany partner (FR-39.17). */
  intercompany?: boolean;
  /** The supplier's Legal-view unit cost, which the Group view rolls (BR-39.15). */
  legalCost?: string;
}

/** Valuation view of a roll (FR-39.17). ProfitCenter is not built yet. */
export type ValuationView = "Legal" | "Group";

export interface RollOptions {
  mode?: ExplosionMode;
  maxLevels?: number;
  workers?: number;
  fallbackToActive?: boolean;
  view?: ValuationView;
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
  /** Basis and Total sheet node values by node id. */
  subtotals?: Record<string, string>;
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

// Slice 2: mixed cost, component postings, sales order estimates, comparison and reconciliation.

export interface Alternative {
  /** Vendor, production version or source site. */
  source: string;
  /** Mixing ratio in percent; 0 keeps it for reporting but leaves it out (BR-39.12). */
  percent: string;
  /** Catalog item rolled for this alternative. */
  itemId: string;
}

export interface AlternativeRoll extends Alternative {
  status: "Pending" | "Error" | "Excluded";
  unitCost: string;
  split: Split;
  diagnostics?: Diagnostic[];
}

export interface MixedRoll {
  itemId: string;
  status: "Pending" | "Error";
  unitCost: string;
  split: Split;
  alternatives: AlternativeRoll[];
  diagnostics?: Diagnostic[];
}

export interface PostingPreview {
  currency?: string;
  total: string;
  journal: JournalLine[];
}

export interface SalesEstimate {
  itemId: string;
  calculationType: "SalesOrder";
  status: "Pending" | "Error";
  quantity: string;
  unitCost: string;
  cost: string;
  split: Split;
  netAmount: string;
  margin: string;
  marginPct: string;
  diagnostics?: Diagnostic[];
}

export interface ComparisonRow {
  itemId: string;
  status: "Compared" | "BaselineOnly" | "CandidateOnly" | "Error";
  baseline: string;
  candidate: string;
  delta: string;
  deltaPct: string;
  byComponent: Split;
}

export interface Comparison {
  rows: ComparisonRow[];
  totalDelta: string;
}

export interface StandardOnHand {
  siteId: string;
  itemId: string;
  qty: string;
  stdUnit: string;
}

export interface SiteBalance {
  siteId: string;
  amount: string;
}

export interface SiteReconciliation {
  siteId: string;
  lines: number;
  standardValue: string;
  glBalance: string;
  difference: string;
  status: "Reconciled" | "Difference";
}

export interface Reconciliation {
  currency?: string;
  sites: SiteReconciliation[];
  differences: number;
}

/** Cost an item mixed from procurement alternatives by ratio (FR-39.14). */
export function mixedCost(
  req: { items: CostItem[]; itemId: string; alternatives: Alternative[]; options?: RollOptions },
  signal?: AbortSignal,
): Promise<MixedRoll> {
  return post<MixedRoll>("/mixed-costs", req, signal);
}

/** Preview the COGS of an issue by cost component (FR-39.15). Nothing is posted. */
export function cogsPosting(req: { currency?: string; quantity: string; unitCost: Split }, signal?: AbortSignal): Promise<PostingPreview> {
  return post<PostingPreview>("/postings:cogs", req, signal);
}

/** Preview a purchase price variance split by the standard's components (FR-39.7, FR-39.15). Nothing is posted. */
export function priceDifferencePosting(
  req: { currency?: string; quantity: string; receiptUnitCost: string; standard: Split },
  signal?: AbortSignal,
): Promise<PostingPreview> {
  return post<PostingPreview>("/postings:price-difference", req, signal);
}

/** Cost a configured or make-to-order line at its order quantity and show the margin (FR-39.16). */
export function salesOrderEstimate(
  req: { currency?: string; items: CostItem[]; itemId: string; quantity: string; netAmount: string; options?: RollOptions },
  signal?: AbortSignal,
): Promise<SalesEstimate> {
  return post<SalesEstimate>("/sales-order-estimates", req, signal);
}

/** Roll a baseline and a candidate selection and compare them item by item (FR-39.10). */
export function compareRuns(
  req: { baseline: { items: CostItem[]; options?: RollOptions }; candidate: { items: CostItem[]; options?: RollOptions } },
  signal?: AbortSignal,
): Promise<Comparison> {
  return post<Comparison>("/comparisons", req, signal);
}

/** Reconcile on-hand at standard to the GL inventory balance per site (FR-39.11). */
export function reconcileStandard(
  req: { currency?: string; onHand: StandardOnHand[]; glBalances: SiteBalance[] },
  signal?: AbortSignal,
): Promise<Reconciliation> {
  return post<Reconciliation>("/reconciliations", req, signal);
}

// Slice 3: costing version lifecycle and the standard cost conversion checklist.

export type VersionType = "Standard" | "Planned" | "Simulation" | "Conversion";
export type VersionStatus = "Open" | "Blocked" | "Active" | "Closed";
export type FallbackPrinciple = "None" | "Active" | "SpecifiedVersion";

export interface CostingVersion {
  id: string;
  type: VersionType;
  status: VersionStatus;
  fallback: FallbackPrinciple;
  fallbackVersionId?: string;
  siteIds?: string[];
}

/** Lifecycle actions under CostingMaintain; activation is separate (CostingActivate). */
export type VersionAction = "block" | "unblock" | "close";

/** Block, unblock or close a version (BR-39.2). The service is stateless: it returns the next state. */
export function transitionVersion(
  req: { version: CostingVersion; action: VersionAction },
  signal?: AbortSignal,
): Promise<{ version: CostingVersion }> {
  return post<{ version: CostingVersion }>("/versions:transition", req, signal);
}

/** Activate an Open or Blocked version (BR-39.2). Simulation versions are refused with CST-0711. */
export function activateVersion(req: { version: CostingVersion }, signal?: AbortSignal): Promise<{ version: CostingVersion }> {
  return post<{ version: CostingVersion }>("/versions:activate", req, signal);
}

export type ConversionBlocker = "NegativeOnHand" | "InTransit" | "StartedOrders";

export const CONVERSION_STEPS = ["select", "roll", "validate", "preview", "close", "activate"] as const;
export type ConversionStep = (typeof CONVERSION_STEPS)[number];

export interface ConversionPosition {
  itemId: string;
  siteId: string;
  onHand: string;
  standardUnit: string;
  actualValue: string;
  inTransit?: number;
  startedOrders?: number;
}

export interface ConversionInput {
  conversionDate: string;
  closeThrough?: string;
  currency?: string;
  blockers: ConversionBlocker[];
  positions: ConversionPosition[];
}

export interface ConversionReport {
  currency?: string;
  blocked: boolean;
  closeComplete: boolean;
  diagnostics?: Diagnostic[];
  lines: { itemId: string; siteId: string; onHand: string; actualValue: string; standardUnit: string; amount: string }[];
  revaluation: string;
  journal: JournalLine[];
}

export interface ChecklistStep {
  step: ConversionStep;
  status: "Done";
  user: string;
  at: string;
}

export interface ConversionRun {
  status: "Draft" | "Validated" | "Closed" | "Activated";
  checklist: ChecklistStep[];
}

/** Validate a standard cost conversion and preview its revaluation (FR-39.12). Nothing is posted. */
export function validateConversion(input: ConversionInput, signal?: AbortSignal): Promise<ConversionReport> {
  return post<ConversionReport>("/conversions:validate", input, signal);
}

/** Record the next checklist step for the signed-in user (FR-39.12). */
export function advanceConversion(
  req: { run: ConversionRun; step: ConversionStep; input: ConversionInput },
  signal?: AbortSignal,
): Promise<{ run: ConversionRun; report: ConversionReport }> {
  return post<{ run: ConversionRun; report: ConversionReport }>("/conversions:advance", req, signal);
}
