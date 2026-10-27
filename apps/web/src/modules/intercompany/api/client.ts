// Client for the intercompany service (services/intercompany/api/openapi.yaml).
// Browser calls use /api/v1/interco/..., which the web BFF rewrites to the
// service's /v1/interco/... paths and signs with the user's tenant and duties.
// Amounts are decimal strings; the browser never does arithmetic on a float.

export const base = "/api/v1/interco";

export type Side = "debit" | "credit";

/** One IC accounting matrix row (BR-40.4). */
export interface MatrixRow {
  entityA: string;
  entityB: string;
  /** Receivable account in entityA for entityB. */
  dueFrom: string;
  /** Payable account in entityB for entityA. */
  dueTo: string;
}

export interface JournalPreviewRequest {
  entity: string;
  account: string;
  side: Side;
  amount: string;
  currency: string;
  offsetEntity: string;
  offsetAccount: string;
  counterCurrency?: string;
  rate?: string;
  matrix: MatrixRow[];
}

export interface JournalLeg {
  entity: string;
  account: string;
  side: Side;
  amount: string;
  currency: string;
}

export interface JournalPreview {
  pairId: string;
  balanced: boolean;
  legs: JournalLeg[];
}

export type ReconStatus = "Matched" | "Timing" | "Mismatch";

export interface ReconItem {
  id?: string;
  icPairId?: string;
  entityA: string;
  entityB: string;
  period: string;
  currency: string;
  amountA: string;
  amountB: string;
  nextPeriodA?: string;
  nextPeriodB?: string;
  fxApplies?: boolean;
}

export interface ClassifiedItem {
  id: string;
  icPairId?: string;
  entityA: string;
  entityB: string;
  period: string;
  currency: string;
  amountA: string;
  amountB: string;
  difference: string;
  status: ReconStatus;
  toleranceApplied: boolean;
}

export interface ClassifyResult {
  fxTolerancePct: string;
  items: ClassifiedItem[];
  /** blocking: a Mismatch remains, so consolidation is blocked (CNS-0901) unless overridden (FR-40.6). */
  summary: { matched: number; timing: number; mismatch: number; blocking: boolean };
}

/** A failed request, normalised from the service's RFC 9457 problem. */
export interface Problem {
  status: number;
  code: string;
  message: string;
  /** Validation messages keyed by request path, such as matrix[0].dueTo. */
  fields: Record<string, string>;
}

export class IntercompanyApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "IntercompanyApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof IntercompanyApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<IntercompanyApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields =
    body?.errors && typeof body.errors === "object" && !Array.isArray(body.errors) ? (body.errors as Record<string, string>) : {};
  return new IntercompanyApiError(res.status, code, message, fields);
}

export interface RequestOptions {
  body?: unknown;
  headers?: Record<string, string>;
  accept?: string;
  signal?: AbortSignal;
}

/** Sends one request to the service and throws an IntercompanyApiError for a problem response. */
export async function request(method: string, path: string, opts: RequestOptions = {}): Promise<Response> {
  const headers: Record<string, string> = { Accept: opts.accept ?? "application/json", ...opts.headers };
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(`${base}${path}`, {
    method,
    headers,
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
    credentials: "same-origin",
    signal: opts.signal,
  });
  if (!res.ok) {
    throw await toError(res);
  }
  return res;
}

function send(path: string, body: unknown, accept: string, signal?: AbortSignal): Promise<Response> {
  return request("POST", path, { body, accept, signal });
}

async function post<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
  return (await (await send(path, body, "application/json", signal)).json()) as T;
}

/** Derive the due-to / due-from journal for one IC line (BR-40.4, AC-40.4). Nothing is posted. */
export function previewJournal(req: JournalPreviewRequest, signal?: AbortSignal): Promise<JournalPreview> {
  return post<JournalPreview>("/journals:preview", req, signal);
}

/** Classify IC balance pairs as Matched, Timing or Mismatch (FR-40.6, BR-40.5). Nothing is stored. */
export function classifyItems(req: { fxTolerancePct?: string; items: ReconItem[] }, signal?: AbortSignal): Promise<ClassifyResult> {
  return post<ClassifyResult>("/reconciliations:classify", req, signal);
}

export type ChargeCategory = "Expense" | "Hour" | "Item" | "Fee";
export type TransferMethod = "Cost" | "CostPlus" | "Table";

/** One ic.transferPrices row for the charge's lender and borrower, in the charge currency. */
export interface TransferPriceRow {
  category: ChargeCategory;
  resourceRole?: string;
  method: TransferMethod;
  markupPct?: string;
  unitPrice?: string;
  effectiveFrom: string;
}

export interface ChargePreviewRequest {
  lendingEntity: string;
  borrowingEntity: string;
  category: ChargeCategory;
  resourceRole?: string;
  transactionDate: string;
  quantity: string;
  unitCost: string;
  currency: string;
  transferPrices: TransferPriceRow[];
  costAccount: string;
  recoveryAccount: string;
  marginAccount: string;
  matrix: MatrixRow[];
}

export interface ChargePreview {
  pairId: string;
  currency: string;
  transferPrice: { method: TransferMethod; markupPct?: string; unitPrice?: string; effectiveFrom: string };
  cost: string;
  price: string;
  margin: string;
  balanced: boolean;
  legs: JournalLeg[];
}

export type PaymentDirection = "Pay" | "Receive";

export interface CentralPaymentRequest {
  payingEntity: string;
  owningEntity: string;
  paymentMethod: string;
  direction: PaymentDirection;
  amount: string;
  currency: string;
  bankAccount: string;
  owningAccount: string;
  matrix: MatrixRow[];
}

export interface PlanningRequest {
  members: { entityId: string; sequence?: number }[];
  edges: { buyer: string; supplier: string; leadTimeDays?: number }[];
  plannedOrders: { buyer: string; supplier: string; item: string; quantity: string; requestedDate: string }[];
  replannedBuyer?: string;
}

export interface PlanningSequence {
  order: string[];
  demand: { buyer: string; supplier: string; item: string; quantity: string; dueDate: string }[];
  staleSuppliers: string[];
  warnings: { code: string; message: string }[];
}

/** Price an IC charge by its effective transfer price and derive its Journal-mode legs (FR-40.13, AC-40.11). Nothing is posted. */
export function previewCharge(req: ChargePreviewRequest, signal?: AbortSignal): Promise<ChargePreview> {
  return post<ChargePreview>("/charges:preview", req, signal);
}

/** Derive the legs of one pay- or receive-on-behalf payment (FR-40.12, AC-40.10). Nothing is posted. */
export function previewCentralPayment(req: CentralPaymentRequest, signal?: AbortSignal): Promise<JournalPreview> {
  return post<JournalPreview>("/centralized-payments:preview", req, signal);
}

/** Run order, IC demand and stale suppliers of a planning group (FR-40.15, AC-40.16). Nothing is planned. */
export function sequencePlanning(req: PlanningRequest, signal?: AbortSignal): Promise<PlanningSequence> {
  return post<PlanningSequence>("/planning-groups:sequence", req, signal);
}

/** One document of a direct delivery chain (FR-40.3). */
export interface ChainLeg {
  docId: string;
  entity: string;
  quantity: string;
}

export interface ChainPreviewRequest {
  chainId: string;
  partner: { sellerEntity: string; buyerEntity: string; active: boolean };
  customerOrder: ChainLeg;
  icPurchase: ChainLeg;
  icSales: ChainLeg;
  /** Packing slip quantity; defaults to the IC sales order quantity. */
  shippedQuantity?: string;
}

export type ChainPostingKind = "ProductReceipt" | "CustomerPackingSlip";

export interface ChainPreview {
  chainId: string;
  postings: { kind: ChainPostingKind; entity: string; docId: string; quantity: string }[];
}

/** What the seller's packing slip posts in the buyer entity in one saga (FR-40.3, AC-40.3). Nothing is posted. */
export function previewChain(req: ChainPreviewRequest, signal?: AbortSignal): Promise<ChainPreview> {
  return post<ChainPreview>("/chains:preview", req, signal);
}

/** One priced IC line of the transfer-pricing evidence; price and cost are line totals. */
export interface EvidenceRow {
  item: string;
  quantity: string;
  price: string;
  cost: string;
  basis: string;
}

export interface EvidenceRequest {
  sellerEntity: string;
  buyerEntity: string;
  period: string;
  currency: string;
  rows: EvidenceRow[];
}

/** The transfer-pricing evidence file as CSV text, with the file name the service suggests. */
export interface EvidenceFile {
  fileName: string;
  csv: string;
}

/** Render the transfer-pricing evidence file for one entity pair and period (FR-40.4). Nothing is stored. */
export async function exportEvidence(req: EvidenceRequest, signal?: AbortSignal): Promise<EvidenceFile> {
  const res = await send("/transfer-pricing/evidence:export", req, "text/csv", signal);
  const disposition = res.headers.get("Content-Disposition") ?? "";
  const named = /filename="([^"]+)"/.exec(disposition);
  return { fileName: named ? named[1] : `tp-evidence-${req.period}.csv`, csv: await res.text() };
}

export interface CentralPaymentReversalRequest extends CentralPaymentRequest {
  /** Pair id the original payment posted under. */
  pairId: string;
}

/** Reverse a centralized payment returned by the bank in both entities under its pair id (BR-40.11). Nothing is posted. */
export function previewReversal(req: CentralPaymentReversalRequest, signal?: AbortSignal): Promise<JournalPreview> {
  return post<JournalPreview>("/centralized-payments/reversals:preview", req, signal);
}
