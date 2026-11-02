// Client for the consolidation service (services/consolidation/api/openapi.yaml).
// Browser calls use /api/v1/consol/..., which the web BFF rewrites to the
// service's /v1/consol/... paths and signs with the user's tenant and duties.
// Amounts are decimal strings; the browser never does arithmetic on a float.

export const base = "/api/v1/consol";

/** Subsidiary-to-group rates for one period (IAS 21). */
export interface Rates {
  closing?: string;
  average?: string;
  opening?: string;
  historical?: string;
}

export interface EliminationRequest {
  currency: string;
  ownershipPct: string;
  rates: Rates;
  icSales?: string;
  icCost?: string;
  unsoldShare?: string;
  icReceivable?: string;
  payableGroup?: string;
  shareCapital?: string;
  preAcqRe?: string;
  investmentCost?: string;
  netIncome?: string;
  sellerIsSubsidiary?: boolean;
  icNetInvestment?: boolean;
  fxGainLossAccount?: string;
}

export type RuleCode = "E1" | "E2" | "E3" | "E4" | "E5";

export interface EliminationEntry {
  rule: RuleCode;
  account: string;
  debit: string;
  credit: string;
}

export interface Totals {
  debit: string;
  credit: string;
}

export interface EliminationPreview {
  currency: string;
  entries: EliminationEntry[];
  rules: Partial<Record<RuleCode, Totals>>;
  totals: Totals;
  balanced: boolean;
}

export interface TranslationRequest {
  currency: string;
  ownershipPct: string;
  rates: Rates;
  openingNetAssets: string;
  netIncome: string;
  closingNetAssets: string;
}

export interface TranslationPreview {
  currency: string;
  openingNetAssets: string;
  netIncome: string;
  closingNetAssets: string;
  cta: string;
  nciShareOfCta: string;
  groupShareOfCta: string;
}

/** A failed request, normalised from the service's RFC 9457 problem. */
export interface Problem {
  status: number;
  code: string;
  message: string;
  /** Validation messages keyed by request path, such as rates.closing. */
  fields: Record<string, string>;
}

export class ConsolidationApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "ConsolidationApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ConsolidationApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<ConsolidationApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields =
    body?.errors && typeof body.errors === "object" && !Array.isArray(body.errors) ? (body.errors as Record<string, string>) : {};
  return new ConsolidationApiError(res.status, code, message, fields);
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

/** Build the E1–E5 elimination entries for one parent and subsidiary (FR-40.9, AC-40.7). Nothing is posted. */
export function previewEliminations(req: EliminationRequest, signal?: AbortSignal): Promise<EliminationPreview> {
  return post<EliminationPreview>("/eliminations:preview", req, signal);
}

/** Translate a unit under IAS 21 and derive the CTA with its NCI share (FR-40.8, AC-40.6). */
export function previewTranslation(req: TranslationRequest, signal?: AbortSignal): Promise<TranslationPreview> {
  return post<TranslationPreview>("/translations:preview", req, signal);
}

/** One consolidation line with its rule code (E2 for IC differences, EQ for the equity method). */
export interface Entry {
  rule: string;
  account: string;
  debit: string;
  credit: string;
}

export interface Warning {
  code: string;
  message: string;
}

export interface ProfitInInventoryRequest {
  currency: string;
  sellerMarginPct?: string;
  layers: { id?: string; quantity: string; legalUnitCost: string; groupUnitCost?: string }[];
}

export interface ProfitInInventoryPreview {
  currency: string;
  layers: { id: string; basis: "GroupCost" | "MarginFallback"; profit: string }[];
  total: string;
  fallbackCount: number;
  warnings: Warning[];
}

export interface EquityMethodRequest {
  currency: string;
  ownershipPct: string;
  openingCarrying: string;
  netIncome: string;
  dividendsReceived?: string;
}

export interface EquityMethodPreview {
  currency: string;
  shareOfProfit: string;
  unrecognizedLoss: string;
  closingCarrying: string;
  entries: Entry[];
  balanced: boolean;
}

export interface DivestitureRequest {
  currency: string;
  proceeds: string;
  netAssetsCarrying: string;
  goodwill?: string;
  nciCarrying?: string;
  ctaRecycled?: string;
}

export interface DivestiturePreview {
  currency: string;
  carryingAmount: string;
  gain: string;
}

export interface IcDifferenceRequest {
  currency: string;
  threshold?: string;
  pairs: { pair: string; receivable: string; payable: string }[];
}

export interface IcDifferencePreview {
  currency: string;
  threshold: string;
  taskStatus: "Completed" | "Error";
  pairs: {
    pair: string;
    receivable: string;
    payable: string;
    difference: string;
    status: "Eliminated" | "Error";
    code?: string;
    message?: string;
    entries: Entry[];
  }[];
}

export interface UnitStatement {
  unitId: string;
  totalAssets: string;
  totalLiabilities: string;
  totalEquity: string;
  openingRetained: string;
  netIncome: string;
  dividends?: string;
  closingRetained: string;
  icItemsWithoutPartner?: number;
}

export type TaskStatus = "NotStarted" | "Running" | "Completed" | "CompletedWithWarnings" | "Error" | "Blocked";

export interface ValidationPreview {
  units: {
    unitId: string;
    status: TaskStatus;
    rules: { rule: string; severity: "Error" | "Warning"; tolerance: string; difference: string; failed: boolean; code?: string; message?: string }[];
  }[];
  groupTranslation: TaskStatus;
}

/** Unrealized profit per IC receipt layer with the margin fallback (FR-40.16, AC-40.12). Nothing is posted. */
export function previewProfitInInventory(req: ProfitInInventoryRequest, signal?: AbortSignal): Promise<ProfitInInventoryPreview> {
  return post<ProfitInInventoryPreview>("/profit-in-inventory:preview", req, signal);
}

/** Share of profit and carrying amount of an associate under the equity method (FR-40.18, AC-40.14). */
export function previewEquityMethod(req: EquityMethodRequest, signal?: AbortSignal): Promise<EquityMethodPreview> {
  return post<EquityMethodPreview>("/equity-method:preview", req, signal);
}

/** Deconsolidation gain or loss of a full divestiture (FR-40.18). */
export function previewDivestiture(req: DivestitureRequest, signal?: AbortSignal): Promise<DivestiturePreview> {
  return post<DivestiturePreview>("/divestitures:preview", req, signal);
}

/** Two-sided IC elimination per partner pair with the difference threshold (BR-40.15). */
export function previewIcDifferences(req: IcDifferenceRequest, signal?: AbortSignal): Promise<IcDifferencePreview> {
  return post<IcDifferencePreview>("/ic-differences:preview", req, signal);
}

/** Evaluate the seeded validation rules per unit and the group translation gate (FR-40.20, AC-40.15). */
export function previewValidations(req: { units: UnitStatement[] }, signal?: AbortSignal): Promise<ValidationPreview> {
  return post<ValidationPreview>("/validations:preview", req, signal);
}

export interface DividendRequest {
  currency: string;
  ownershipPct: string;
  /** Subsidiary-to-group rate on the dividend date. */
  rate: string;
  /** Dividend declared, in the subsidiary's currency. */
  amount: string;
}

export interface DividendPreview {
  currency: string;
  entries: Entry[];
  totals: Totals;
  /** Signed debit of the ROUNDING line (BR-40.9); "0.00" when none was needed. */
  rounding: string;
  balanced: boolean;
}

/** Build the E6 elimination of a dividend within the group with its rounding line (FR-40.9, BR-40.9). Nothing is posted. */
export function previewDividends(req: DividendRequest, signal?: AbortSignal): Promise<DividendPreview> {
  return post<DividendPreview>("/dividends:preview", req, signal);
}

export type ReclassLevel = "Entity" | "Group";
export type ReclassType = "DebitBalanceVendors" | "CreditBalanceCustomers" | "RemainingTerm" | "SignBased";

export interface ReclassRule {
  code: string;
  level: ReclassLevel;
  type: ReclassType;
  order?: number;
  version?: number;
  sourceAccount: string;
  targetAccount: string;
  threshold?: string;
  termMonths?: number;
}

export interface OpenItem {
  party: string;
  account: string;
  dueDate?: string;
  /** Signed: positive is a debit balance. */
  amount: string;
}

export interface ReclassificationRequest {
  currency: string;
  periodEnd: string;
  rules: ReclassRule[];
  openItems?: OpenItem[];
  balances?: { account: string; amount: string }[];
}

export interface ReclassLine {
  rule: string;
  level: ReclassLevel;
  account: string;
  party?: string;
  debit: string;
  credit: string;
  reverses: boolean;
}

export interface ReclassificationPreview {
  currency: string;
  periodEnd: string;
  dryRun: true;
  applied: string[];
  reversalDate?: string;
  lines: ReclassLine[];
  totals: Totals;
}

/** Dry run of reclassification and regrouping rules (FR-40.17, BR-40.14, AC-40.13). Nothing is posted. */
export function previewReclassification(req: ReclassificationRequest, signal?: AbortSignal): Promise<ReclassificationPreview> {
  return post<ReclassificationPreview>("/reclassification-runs:preview", req, signal);
}

export type DocumentType = "Adjustment" | "Reclassification" | "Elimination" | "CoI" | "Cumulative";

export interface GroupJournalLine {
  fsItem: string;
  debit?: string;
  credit?: string;
  partnerUnit?: string;
}

export interface GroupJournalRequest {
  periodKey: string;
  documentType: DocumentType;
  unitId?: string;
  currency: string;
  reason: string;
  attachmentRef: string;
  carryForward: boolean;
  fiscalYearStartMonth?: number;
  approvalThreshold?: string;
  lines: GroupJournalLine[];
}

export interface GroupJournalPreview {
  currency: string;
  level: "Group" | "Unit";
  total: string;
  balanced: boolean;
  approvalThreshold: string;
  approvalRequired: boolean;
  periods: string[];
  continuesPastYearEnd: boolean;
  warnings: Warning[];
}

/** Check a top-side group journal: approval at the threshold (CNS-0909) and the periods it applies to (FR-40.19, BR-40.18). Nothing is posted. */
export function previewGroupJournal(req: GroupJournalRequest, signal?: AbortSignal): Promise<GroupJournalPreview> {
  return post<GroupJournalPreview>("/group-journals:preview", req, signal);
}
