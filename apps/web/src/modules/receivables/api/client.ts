import { apiFetch } from "../../../shell/api/http";
// Typed client for the receivables service (services/receivables/api/openapi.yaml).
// Money stays a decimal string end to end; nothing here converts to a float.

export const base = "/api/v1/receivables";

export const BUCKETS = ["CURRENT", "1-30", "31-60", "61-90", "91-180", "OVER-180"] as const;
export type Bucket = (typeof BUCKETS)[number];

export const PAYMENT_MODES = ["CHECK", "ACH", "WIRE", "CARD", "CASH"] as const;
export type PaymentMode = (typeof PAYMENT_MODES)[number];

export const PAYMENT_STATUSES = ["RECEIVED", "SUGGESTED", "APPLIED", "ON_ACCOUNT", "REVERSED", "ADVISED", "CONFIRMED"] as const;
export type PaymentStatus = (typeof PAYMENT_STATUSES)[number];

export interface AgingTotal {
  currency: string;
  bucket: Bucket;
  amount: string;
}

export interface AgingResponse {
  asOf: string;
  totals: AgingTotal[];
}

export interface OpenItem {
  id: string;
  legalEntityId: string;
  customerId: string;
  sourceType: string;
  documentNumber: string;
  documentDate: string;
  dueDate: string;
  currency: string;
  amount: string;
  openAmount: string;
  cashDiscountDate?: string | null;
  cashDiscountAmount: string;
  dunningLevel: number;
  dunningBlock: boolean;
  status: string;
  version: number;
  /** CREDIT items (credit notes, unapplied payments) carry negative amounts. */
  side?: "DEBIT" | "CREDIT";
  /** Set when the migration loader's opening-item import created the item. */
  importSource?: string;
  legacyKey?: string;
}

export interface RemittanceLine {
  reference: string;
  amount: string;
}

export interface PaymentInput {
  legalEntityId: string;
  customerId: string | null;
  paymentMode: PaymentMode;
  source: "MANUAL";
  sourceRef: string | null;
  amount: string;
  currency: string;
  receivedDate: string;
  valueDate: string;
  payerName: string | null;
  /** The payer's IBAN or MICR account (R4); the service stores only its hash. */
  payerAccount: string | null;
  /** Explains a short payment (R7). */
  shortPayReason: string | null;
  remittance: RemittanceLine[];
}

export interface Payment {
  id: string;
  legalEntityId: string;
  customerId?: string | null;
  paymentMode: PaymentMode;
  source: string;
  sourceRef?: string | null;
  amount: string;
  unappliedAmount: string;
  currency: string;
  receivedDate: string;
  valueDate: string;
  payerName?: string | null;
  payerAccountHash?: string | null;
  shortPayReason?: string | null;
  remittance: RemittanceLine[];
  status: PaymentStatus;
  version: number;
}

export interface MatchCandidate {
  openItemIds: string[];
  score: number;
  rules: string[];
}

export interface MatchResult {
  decision: "AUTO_APPLY" | "SUGGESTED" | "ON_ACCOUNT";
  candidates: MatchCandidate[];
}

export interface SettlementInput {
  openItemId: string;
  amount: string;
  cashDiscount?: string;
  writeOff?: string;
}

export interface ApplyInput {
  settlements: SettlementInput[];
  onAccount: string;
}

export interface SettlementRecord {
  openItemId: string;
  amount: string;
  cashDiscount: string;
  writeOff: string;
  openAfter: string;
  itemStatus: string;
}

export interface ApplyResult {
  payment: Payment;
  settlements: SettlementRecord[];
}

/** Problem details (RFC 9457) surfaced to the UI with the stable service code and field errors. */
export class ReceivablesApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ReceivablesApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ReceivablesApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

/** A fresh Idempotency-Key for one user action; retries of the same action reuse it. */
export function newKey(): string {
  return crypto.randomUUID();
}

const DECIMAL = /^-?\d+(\.\d{1,6})?$/;
const DAY = /^\d{4}-\d{2}-\d{2}$/;
const CURRENCY = /^[A-Z]{3}$/;

/** Validates an aging payload; throws on any shape the OpenAPI schema forbids. */
export function parseAging(raw: unknown): AgingResponse {
  if (typeof raw !== "object" || raw === null) throw new TypeError("aging: body is not an object");
  const body = raw as Record<string, unknown>;
  if (typeof body.asOf !== "string" || !DAY.test(body.asOf)) throw new TypeError("aging: asOf is not a date");
  if (!Array.isArray(body.totals)) throw new TypeError("aging: totals is not an array");
  const totals = body.totals.map((item: unknown, i: number): AgingTotal => {
    if (typeof item !== "object" || item === null) throw new TypeError(`aging: totals[${i}] is not an object`);
    const t = item as Record<string, unknown>;
    if (typeof t.currency !== "string" || !CURRENCY.test(t.currency)) throw new TypeError(`aging: totals[${i}].currency invalid`);
    if (typeof t.bucket !== "string" || !(BUCKETS as readonly string[]).includes(t.bucket)) {
      throw new TypeError(`aging: totals[${i}].bucket invalid`);
    }
    if (typeof t.amount !== "string" || !DECIMAL.test(t.amount)) throw new TypeError(`aging: totals[${i}].amount invalid`);
    return { currency: t.currency, bucket: t.bucket as Bucket, amount: t.amount };
  });
  return { asOf: body.asOf, totals };
}

async function toError(res: Response): Promise<ReceivablesApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new ReceivablesApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await apiFetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [name, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(name, String(value));
  }
  const text = q.toString();
  return text ? `?${text}` : "";
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new ReceivablesApiError(0, "COM-0001", "The response has no items");
  return body.items;
}

/** GET /aging - totals per currency and bucket; asOf defaults to the server's today. */
export async function fetchAging(asOf?: string, signal?: AbortSignal): Promise<AgingResponse> {
  return parseAging(await send<unknown>(`/aging${query({ asOf })}`, {}, signal));
}

export interface OpenItemFilter {
  customer?: string;
  status?: "open" | "all";
  importSource?: string;
  limit?: number;
}

/** GET /open-items - ordered by due date. */
export function listOpenItems(filter: OpenItemFilter = {}, signal?: AbortSignal): Promise<OpenItem[]> {
  return items<OpenItem>(
    `/open-items${query({ customer: filter.customer, status: filter.status, importSource: filter.importSource, limit: filter.limit })}`,
    signal,
  );
}

/** What one import source loaded into a legal entity, per currency (GET /open-item-imports). */
export interface OpeningImportSummary {
  importSource: string;
  legalEntityId: string;
  currency: string;
  itemCount: number;
  debitCount: number;
  creditCount: number;
  debitOpen: string;
  creditOpen: string;
  netOpen: string;
  lastImportedAt?: string;
}

export function listOpeningImports(signal?: AbortSignal): Promise<OpeningImportSummary[]> {
  return items<OpeningImportSummary>("/open-item-imports", signal);
}

export interface PaymentFilter {
  status?: PaymentStatus | "";
  customer?: string;
  limit?: number;
}

/** GET /payments - newest received first. */
export function listPayments(filter: PaymentFilter = {}, signal?: AbortSignal): Promise<Payment[]> {
  return items<Payment>(`/payments${query({ status: filter.status, customer: filter.customer, limit: filter.limit })}`, signal);
}

export function getPayment(id: string, signal?: AbortSignal): Promise<Payment> {
  return send<Payment>(`/payments/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /payments - records a manual payment; the key makes a retry return the same payment. */
export function createPayment(input: PaymentInput, key: string = newKey()): Promise<Payment> {
  return send<Payment>("/payments", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

/** POST /payments/{id}:match - scores candidate settlements; changes nothing. */
export function matchPayment(id: string): Promise<MatchResult> {
  return send<MatchResult>(`/payments/${encodeURIComponent(id)}:match`, { method: "POST" });
}

/** POST /payments/{id}:apply - If-Match carries the payment version; 412 when it changed. */
export function applyPayment(id: string, version: number, input: ApplyInput, key: string = newKey()): Promise<ApplyResult> {
  return send<ApplyResult>(`/payments/${encodeURIComponent(id)}:apply`, {
    method: "POST",
    headers: { "If-Match": `"${version}"`, "Idempotency-Key": key },
    body: JSON.stringify(input),
  });
}

// ---- Collections (FR-30.6, FR-30.7, FR-30.8, FR-30.9, FR-30.10) and lockbox (FR-30.3) ----

export const DISPUTE_STATUSES = ["OPEN", "UNDER_REVIEW", "RESOLVED_CREDIT", "RESOLVED_PAID", "RESOLVED_WRITE_OFF", "REJECTED"] as const;
export type DisputeStatus = (typeof DISPUTE_STATUSES)[number];

export const RESOLUTIONS = ["CREDIT", "PAID", "WRITE_OFF", "REJECTED"] as const;
export type Resolution = (typeof RESOLUTIONS)[number];

export const RUN_STATUSES = ["DRAFT", "REVIEWED", "ISSUED", "CANCELLED"] as const;
export type RunStatus = (typeof RUN_STATUSES)[number];

export type ExclusionReason = "BLOCKED" | "DISPUTE" | "PROMISE" | "BELOW_MINIMUM" | "INTERVAL";

export interface Dispute {
  id: string;
  legalEntityId: string;
  customerId: string;
  openItemId: string;
  documentNumber: string;
  reasonCode: string;
  disputedAmount: string;
  currency: string;
  owner: string;
  status: DisputeStatus;
  resolutionNote?: string | null;
  slaDue: string;
  createdAt: string;
  resolvedAt?: string | null;
  version: number;
}

export interface DisputeInput {
  legalEntityId: string;
  openItemId: string;
  reasonCode: string;
  disputedAmount: string;
  owner: string;
}

/** A promise to pay; named so it does not shadow the global Promise. */
export interface PaymentPromise {
  id: string;
  legalEntityId: string;
  customerId: string;
  openItemId: string;
  documentNumber: string;
  currency: string;
  amount: string;
  promiseDate: string;
  createdAt: string;
}

export interface PromiseInput {
  legalEntityId: string;
  openItemId: string;
  amount: string;
  promiseDate: string;
}

export interface Exclusion {
  openItemId: string;
  customerId: string;
  reason: ExclusionReason;
}

export interface LetterItem {
  openItemId: string;
  documentNumber: string;
  dueDate: string;
  openAmount: string;
  daysOverdue: number;
  fromLevel: number;
  toLevel: number;
}

export interface DunningLetter {
  id: string;
  customerId: string;
  currency: string;
  level: number;
  priorLevel: number;
  amount: string;
  feeAmount: string;
  status: "DRAFT" | "ISSUED" | "SKIPPED";
  documentRef?: string | null;
  items: LetterItem[];
}

export interface DunningRun {
  id: string;
  legalEntityId: string;
  runDate: string;
  graceDays: number;
  status: RunStatus;
  exclusions: Exclusion[];
  letters: DunningLetter[];
  issuedAt?: string | null;
  version: number;
}

export interface DunningRunSummary {
  id: string;
  legalEntityId: string;
  runDate: string;
  graceDays: number;
  status: RunStatus;
  letterCount: number;
  issuedAt?: string | null;
  version: number;
}

export interface DunningRunInput {
  legalEntityId: string;
  runDate: string;
  graceDays: number;
}

export type InterestBasis = "ACT_365" | "ACT_360";

export interface InterestInput {
  legalEntityId: string;
  calculationDate: string;
  annualRate: string;
  basis: InterestBasis;
  graceDays: number;
}

export interface InterestCandidate {
  openItemId: string;
  customerId: string;
  documentNumber: string;
  currency: string;
  principal: string;
  days: number;
  amount: string;
}

export interface InterestNote {
  id: string;
  legalEntityId: string;
  customerId: string;
  openItemId: string;
  documentNumber: string;
  interestItemId: string;
  period: string;
  calculationDate: string;
  days: number;
  annualRate: string;
  basis: InterestBasis;
  currency: string;
  principal: string;
  amount: string;
}

export interface InterestResult {
  candidates: InterestCandidate[];
  exclusions: Exclusion[];
  posted: InterestNote[];
}

export interface CustomerRisk {
  customerId: string;
  currency: string;
  totalOpen: string;
  overdue: string;
  oldestDays: number;
  avgDaysLate: number;
  dunningLevel: number;
  openDisputes: number;
  brokenPromises: number;
  riskScore: number;
}

export interface WorklistResponse {
  asOf: string;
  items: CustomerRisk[];
}

export interface LockboxImportInput {
  legalEntityId: string;
  receivedDate: string;
  content: string;
  typeCodes?: string[];
}

export interface LockboxBatch {
  id: string;
  legalEntityId: string;
  fileSha256: string;
  receivedDate: string;
  accountCount: number;
  paymentCount: number;
  importedAt?: string | null;
  payments?: Payment[];
}

export interface DisputeFilter {
  status?: DisputeStatus | "active" | "";
  customer?: string;
  openItem?: string;
  limit?: number;
}

/** GET /disputes - nearest SLA first; status "active" keeps OPEN and UNDER_REVIEW. */
export function listDisputes(filter: DisputeFilter = {}, signal?: AbortSignal): Promise<Dispute[]> {
  return items<Dispute>(
    `/disputes${query({ status: filter.status, customer: filter.customer, openItem: filter.openItem, limit: filter.limit })}`,
    signal,
  );
}

export function getDispute(id: string, signal?: AbortSignal): Promise<Dispute> {
  return send<Dispute>(`/disputes/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /disputes - excludes the item from dunning and interest while the dispute is active. */
export function openDispute(input: DisputeInput, key: string = newKey()): Promise<Dispute> {
  return send<Dispute>("/disputes", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

/** POST /disputes/{id}:review - OPEN to UNDER_REVIEW; If-Match carries the dispute version. */
export function reviewDispute(id: string, version: number): Promise<Dispute> {
  return send<Dispute>(`/disputes/${encodeURIComponent(id)}:review`, { method: "POST", headers: { "If-Match": `"${version}"` } });
}

/** POST /disputes/{id}:resolve - CREDIT, PAID or WRITE_OFF from review; REJECTED from OPEN or review. */
export function resolveDispute(id: string, version: number, resolution: Resolution, note: string | null): Promise<Dispute> {
  return send<Dispute>(`/disputes/${encodeURIComponent(id)}:resolve`, {
    method: "POST",
    headers: { "If-Match": `"${version}"` },
    body: JSON.stringify({ resolution, note }),
  });
}

export interface PromiseFilter {
  customer?: string;
  openItem?: string;
  limit?: number;
}

export function listPromises(filter: PromiseFilter = {}, signal?: AbortSignal): Promise<PaymentPromise[]> {
  return items<PaymentPromise>(`/promises${query({ customer: filter.customer, openItem: filter.openItem, limit: filter.limit })}`, signal);
}

/** POST /promises - holds the item out of dunning and interest until the promise date plus two days. */
export function createPromise(input: PromiseInput, key: string = newKey()): Promise<PaymentPromise> {
  return send<PaymentPromise>("/promises", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

export function listDunningRuns(status: RunStatus | "" = "", signal?: AbortSignal): Promise<DunningRunSummary[]> {
  return items<DunningRunSummary>(`/dunning-runs${query({ status })}`, signal);
}

export function getDunningRun(id: string, signal?: AbortSignal): Promise<DunningRun> {
  return send<DunningRun>(`/dunning-runs/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /dunning-runs - proposes letters as a DRAFT run; nothing is raised or posted until issue. */
export function createDunningRun(input: DunningRunInput, key: string = newKey()): Promise<DunningRun> {
  return send<DunningRun>("/dunning-runs", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

/** POST /dunning-runs/{id}:review|:issue|:cancel with If-Match. */
export function moveDunningRun(id: string, version: number, action: "review" | "issue" | "cancel"): Promise<DunningRun> {
  return send<DunningRun>(`/dunning-runs/${encodeURIComponent(id)}:${action}`, { method: "POST", headers: { "If-Match": `"${version}"` } });
}

/** POST /interest-notes:calculate - a preview; writes nothing. */
export function calculateInterest(input: InterestInput): Promise<InterestResult> {
  return send<InterestResult>("/interest-notes:calculate", { method: "POST", body: JSON.stringify(input) });
}

/** POST /interest-notes:post - posts INTEREST open items once per item and month. */
export function postInterest(input: InterestInput, key: string = newKey()): Promise<InterestResult> {
  return send<InterestResult>("/interest-notes:post", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

export function listInterestNotes(customer = "", signal?: AbortSignal): Promise<InterestNote[]> {
  return items<InterestNote>(`/interest-notes${query({ customer })}`, signal);
}

/** GET /worklist - customers ranked by risk score, riskiest first. */
export function fetchWorklist(legalEntityId: string, asOf?: string, signal?: AbortSignal): Promise<WorklistResponse> {
  return send<WorklistResponse>(`/worklist${query({ legalEntityId, asOf })}`, {}, signal);
}

export function listLockboxBatches(signal?: AbortSignal): Promise<LockboxBatch[]> {
  return items<LockboxBatch>("/lockbox/imports", signal);
}

/** POST /lockbox/imports - a repeated file is 409 AR-0003; a malformed one 400 AR-0004 with the line. */
export function importLockbox(input: LockboxImportInput): Promise<LockboxBatch> {
  return send<LockboxBatch>("/lockbox/imports", { method: "POST", body: JSON.stringify(input) });
}

// ---- Collection cases (FR-30.18), balance confirmations (FR-30.20), down payments and deposits (FR-30.21) ----

export const CASE_CATEGORIES = ["COLLECTION", "DISPUTE", "DEDUCTION"] as const;
export type CaseCategory = (typeof CASE_CATEGORIES)[number];
export const CASE_PRIORITIES = ["LOW", "NORMAL", "HIGH"] as const;
export type CasePriority = (typeof CASE_PRIORITIES)[number];
export type CaseStatus = "OPEN" | "CLOSED";

export interface CollectionCase {
  id: string;
  legalEntityId: string;
  customerId: string;
  category: CaseCategory;
  owner: string;
  priority: CasePriority;
  status: CaseStatus;
  reason: string;
  currency: string;
  openItemId?: string | null;
  documentNumber?: string | null;
  attachedAmount?: string | null;
  slaHours: number;
  slaDue: string;
  slaBreached: boolean;
  closeNote?: string | null;
  closedAt?: string | null;
  createdAt: string;
  version: number;
}

export interface CaseInput {
  legalEntityId: string;
  customerId: string;
  category: CaseCategory;
  owner: string;
  priority: CasePriority;
  reason: string;
  currency?: string;
  openItemId?: string | null;
  attachedAmount?: string | null;
}

export interface CaseFilter {
  status?: CaseStatus | "";
  category?: CaseCategory | "";
  customer?: string;
  limit?: number;
}

/** GET /cases - open cases first, nearest SLA first. */
export function listCases(filter: CaseFilter = {}, signal?: AbortSignal): Promise<CollectionCase[]> {
  return items<CollectionCase>(
    `/cases${query({ status: filter.status, category: filter.category, customer: filter.customer, limit: filter.limit })}`,
    signal,
  );
}

export function getCase(id: string, signal?: AbortSignal): Promise<CollectionCase> {
  return send<CollectionCase>(`/cases/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /cases - the SLA comes from the category. */
export function createCase(input: CaseInput, key: string = newKey()): Promise<CollectionCase> {
  return send<CollectionCase>("/cases", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

/** POST /cases/{id}:assign with If-Match. */
export function assignCase(id: string, version: number, owner: string): Promise<CollectionCase> {
  return send<CollectionCase>(`/cases/${encodeURIComponent(id)}:assign`, {
    method: "POST",
    headers: { "If-Match": `"${version}"` },
    body: JSON.stringify({ owner }),
  });
}

/** POST /cases/{id}:close with If-Match and an optional note. */
export function closeCase(id: string, version: number, note: string | null): Promise<CollectionCase> {
  return send<CollectionCase>(`/cases/${encodeURIComponent(id)}:close`, {
    method: "POST",
    headers: { "If-Match": `"${version}"` },
    body: JSON.stringify({ note }),
  });
}

export type ReplyStatus = "NO_REPLY" | "AGREED" | "DISAGREED";

export interface ConfirmationCounts {
  selected: number;
  agreed: number;
  disagreed: number;
  noReply: number;
}

export interface Confirmation {
  id: string;
  runId: string;
  legalEntityId: string;
  customerId: string;
  currency: string;
  ourBalance: string;
  status: ReplyStatus;
  customerBalance?: string | null;
  difference?: string | null;
  comment?: string | null;
  repliedAt?: string | null;
  caseId?: string | null;
}

export interface ConfirmationRun {
  id: string;
  legalEntityId: string;
  asOfDate: string;
  threshold: string;
  sampleSize?: number | null;
  includeZeroBalances: boolean;
  reminderDays: number;
  createdAt: string;
  counts: ConfirmationCounts;
  confirmations?: Confirmation[];
}

export interface ConfirmationRunInput {
  legalEntityId: string;
  asOfDate: string;
  threshold: string;
  sampleSize: number | null;
  includeZeroBalances: boolean;
  reminderDays: number;
}

export interface ReplyInput {
  confirmationId: string;
  reply: "AGREED" | "DISAGREED";
  customerBalance: string | null;
  comment: string | null;
}

export function listConfirmationRuns(signal?: AbortSignal): Promise<ConfirmationRun[]> {
  return items<ConfirmationRun>("/balance-confirmations", signal);
}

export function getConfirmationRun(id: string, signal?: AbortSignal): Promise<ConfirmationRun> {
  return send<ConfirmationRun>(`/balance-confirmations/${encodeURIComponent(id)}`, {}, signal);
}

/** GET /balance-confirmations/{id}/differences - the DISAGREED replies with both balances. */
export function listConfirmationDifferences(id: string, signal?: AbortSignal): Promise<Confirmation[]> {
  return items<Confirmation>(`/balance-confirmations/${encodeURIComponent(id)}/differences`, signal);
}

/** POST /balance-confirmations - the key also seeds the sample, so a retry selects the same customers. */
export function createConfirmationRun(input: ConfirmationRunInput, key: string = newKey()): Promise<ConfirmationRun> {
  return send<ConfirmationRun>("/balance-confirmations", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

/** POST /balance-confirmations/{id}:reply - replies are immutable; DISAGREED opens a DISPUTE case. */
export function recordReply(runId: string, input: ReplyInput): Promise<Confirmation> {
  return send<Confirmation>(`/balance-confirmations/${encodeURIComponent(runId)}:reply`, { method: "POST", body: JSON.stringify(input) });
}

export const DOWN_PAYMENT_KINDS = ["DOWN_PAYMENT", "SECURITY_DEPOSIT"] as const;
export type DownPaymentKind = (typeof DOWN_PAYMENT_KINDS)[number];
export const DOWN_PAYMENT_STATUSES = ["REQUESTED", "RECEIVED", "CLEARED", "RELEASED", "CANCELLED"] as const;
export type DownPaymentStatus = (typeof DOWN_PAYMENT_STATUSES)[number];

export interface DownPaymentClearing {
  id: string;
  openItemId: string;
  documentNumber: string;
  amount: string;
  clearedAt: string;
}

export interface DownPayment {
  id: string;
  legalEntityId: string;
  customerId: string;
  salesOrderId?: string | null;
  kind: DownPaymentKind;
  currency: string;
  amount: string;
  clearedAmount: string;
  remaining: string;
  status: DownPaymentStatus;
  receivedDate?: string | null;
  createdAt: string;
  version: number;
  clearings?: DownPaymentClearing[];
}

export interface DownPaymentInput {
  legalEntityId: string;
  customerId: string;
  salesOrderId: string | null;
  kind: DownPaymentKind;
  currency: string;
  amount: string;
}

export interface DownPaymentFilter {
  kind?: DownPaymentKind | "";
  status?: DownPaymentStatus | "";
  customer?: string;
  limit?: number;
}

export function listDownPayments(filter: DownPaymentFilter = {}, signal?: AbortSignal): Promise<DownPayment[]> {
  return items<DownPayment>(
    `/down-payments${query({ kind: filter.kind, status: filter.status, customer: filter.customer, limit: filter.limit })}`,
    signal,
  );
}

export function getDownPayment(id: string, signal?: AbortSignal): Promise<DownPayment> {
  return send<DownPayment>(`/down-payments/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /down-payments - a request; nothing posts until it is received. */
export function createDownPayment(input: DownPaymentInput, key: string = newKey()): Promise<DownPayment> {
  return send<DownPayment>("/down-payments", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

export type DownPaymentAction =
  | { action: "receive"; receivedDate?: string }
  | { action: "cancel" }
  | { action: "clear"; openItemId: string; amount?: string }
  | { action: "release" };

/** POST /down-payments/{id}:receive|:cancel|:clear|:release with If-Match. */
export function moveDownPayment(id: string, version: number, command: DownPaymentAction): Promise<DownPayment> {
  const { action, ...body } = command;
  return send<DownPayment>(`/down-payments/${encodeURIComponent(id)}:${action}`, {
    method: "POST",
    headers: { "If-Match": `"${version}"` },
    body: JSON.stringify(body),
  });
}


// ---- camt.053 bank statements (FR-30.5) and the deductions workbench (FR-30.17, BR-30.12) ----

export interface BankStatementImportInput {
  legalEntityId: string;
  content: string;
}

export interface BankStatementBatch {
  id: string;
  legalEntityId: string;
  fileSha256: string;
  statementCount: number;
  paymentCount: number;
  duplicateCount: number;
  skippedCount: number;
  importedAt?: string;
  payments?: Payment[];
}

export function listBankStatementImports(signal?: AbortSignal): Promise<BankStatementBatch[]> {
  return items<BankStatementBatch>("/bank-statements/imports", signal);
}

/** POST /bank-statements/imports - a repeated file is 409 AR-0003; a malformed one 400 AR-0004 with the line. */
export function importBankStatement(input: BankStatementImportInput): Promise<BankStatementBatch> {
  return send<BankStatementBatch>("/bank-statements/imports", { method: "POST", body: JSON.stringify(input) });
}

export const DEDUCTION_TYPES = ["PROMO", "PRICE", "SHORT_SHIP", "DAMAGE", "UNKNOWN"] as const;
export type DeductionType = (typeof DEDUCTION_TYPES)[number];
export const DEDUCTION_STATUSES = ["OPEN", "MATCHED", "APPROVED", "DENIED", "SPLIT", "WRITTEN_OFF"] as const;
export type DeductionStatus = (typeof DEDUCTION_STATUSES)[number];
export const CLAIM_TYPES = ["REBATE_ACCRUAL", "PROMOTION_FUND", "CHARGEBACK"] as const;
export type ClaimType = (typeof CLAIM_TYPES)[number];
export const DENIAL_REASONS = ["NOT_AUTHORIZED", "EXPIRED", "DUPLICATE"] as const;
export type DenialReason = (typeof DENIAL_REASONS)[number];

export interface Deduction {
  id: string;
  legalEntityId: string;
  customerId: string;
  openItemId: string;
  documentNumber: string;
  paymentId?: string | null;
  parentId?: string | null;
  type: DeductionType;
  reasonCode: string;
  currency: string;
  amount: string;
  status: DeductionStatus;
  claimType?: ClaimType | null;
  claimRef?: string | null;
  claimRemaining?: string | null;
  denialReason?: DenialReason | null;
  decisionNote?: string | null;
  rebillItemId?: string | null;
  createdAt: string;
  resolvedAt?: string | null;
  version: number;
  children?: Deduction[];
}

export interface DeductionInput {
  legalEntityId: string;
  openItemId: string;
  paymentId: string | null;
  type: DeductionType;
  reasonCode: string;
  amount: string;
}

export interface DeductionFilter {
  status?: DeductionStatus | "";
  type?: DeductionType | "";
  customer?: string;
  limit?: number;
}

export function listDeductions(filter: DeductionFilter = {}, signal?: AbortSignal): Promise<Deduction[]> {
  return items<Deduction>(
    `/deductions${query({ status: filter.status, type: filter.type, customer: filter.customer, limit: filter.limit })}`,
    signal,
  );
}

export function getDeduction(id: string, signal?: AbortSignal): Promise<Deduction> {
  return send<Deduction>(`/deductions/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /deductions - moves the amount off the short-paid item; the key makes a retry return the same deduction. */
export function createDeduction(input: DeductionInput, key: string = newKey()): Promise<Deduction> {
  return send<Deduction>("/deductions", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

export type DeductionAction =
  | { action: "match"; claimType: ClaimType; claimRef: string; claimRemaining: string }
  | { action: "approve"; note?: string }
  | { action: "deny"; reason: DenialReason; note?: string }
  | { action: "split"; parts: string[] }
  | { action: "write-off"; note?: string };

/** POST /deductions/{id}:match|:approve|:deny|:split|:write-off with If-Match; 412 when it changed. */
export function moveDeduction(id: string, version: number, command: DeductionAction): Promise<Deduction> {
  const { action, ...body } = command;
  return send<Deduction>(`/deductions/${encodeURIComponent(id)}:${action}`, {
    method: "POST",
    headers: { "If-Match": `"${version}"` },
    body: JSON.stringify(body),
  });
}

// ---- Customer statements (FR-30.13) and the aging snapshot job (§5.30.16) ----
export interface StatementLine {
  /** The open item's source, DEDUCTION for a deduction being worked, PAYMENT for unapplied cash. */
  sourceType: string;
  documentNumber: string;
  documentDate: string;
  dueDate: string | null;
  amount: string;
  openAmount: string;
  runningBalance: string;
}

export interface StatementSection {
  currency: string;
  lines: StatementLine[];
  closingBalance: string;
  aging: AgingTotal[];
}

export interface Statement {
  legalEntityId: string;
  customerId: string;
  asOf: string;
  sections: StatementSection[];
}

/** GET /statements - what the customer had open at the end of asOf (today by default); reads only. */
export function fetchStatement(legalEntityId: string, customer: string, asOf?: string, signal?: AbortSignal): Promise<Statement> {
  return send<Statement>(`/statements${query({ legalEntityId, customer, asOf })}`, {}, signal);
}

export interface BalanceSnapshot {
  legalEntityId: string;
  customerId: string;
  currency: string;
  asOf: string;
  totalOpen: string;
  overdue: string;
  oldestItemDays: number;
  dunningLevel: number;
  riskScore: number;
  takenAt: string;
}

export interface SnapshotFilter {
  legalEntityId: string;
  asOf?: string;
  customer?: string;
}

export interface SnapshotResult {
  legalEntityId: string;
  asOf: string;
  rows: number;
  changed: number;
}

/** GET /aging-snapshots - stored balances for a key date (the latest when omitted), riskiest first. */
export function listBalanceSnapshots(filter: SnapshotFilter, signal?: AbortSignal): Promise<BalanceSnapshot[]> {
  return items<BalanceSnapshot>(`/aging-snapshots${query({ legalEntityId: filter.legalEntityId, asOf: filter.asOf, customer: filter.customer })}`, signal);
}

/** POST /aging-snapshots - runs the nightly snapshot now; a re-run for the date announces only moved balances. */
export function runAgingSnapshot(legalEntityId: string, asOf?: string): Promise<SnapshotResult> {
  return send<SnapshotResult>("/aging-snapshots", { method: "POST", body: JSON.stringify(asOf ? { legalEntityId, asOf } : { legalEntityId }) });
}


// ---- Ledger journals (30-2) and remittance advices (FR-30.4, 30-4) ----

export type JournalStatus = "PENDING" | "POSTED";

export interface GLJournalLine {
  account: string;
  side: "D" | "C";
  amount: string;
  text?: string;
}

export interface GLJournal {
  id: string;
  legalEntityId: string;
  sourceType: string;
  sourceId: string;
  documentId: string;
  postingDate: string;
  currency: string;
  status: JournalStatus;
  journalPostingId?: string | null;
  attempts: number;
  nextAttemptAt?: string | null;
  parked: boolean;
  lastError?: string | null;
  createdAt: string;
  debit: string;
  lines: GLJournalLine[];
}

export interface GLJournalFilter {
  legalEntityId?: string;
  status?: JournalStatus | "";
  parked?: boolean;
  limit?: number;
}

/** GET /gl-journals - the journals receivables owes the ledger or has posted, newest first. */
export function listGLJournals(filter: GLJournalFilter = {}, signal?: AbortSignal): Promise<GLJournal[]> {
  return items<GLJournal>(`/gl-journals${query({ legalEntityId: filter.legalEntityId, status: filter.status, parked: filter.parked ? "true" : undefined, limit: filter.limit })}`, signal);
}

export interface JournalSweepResult {
  posted: number;
  failed: number;
  parked: number;
}

/** POST /gl-journals:sweep - posts the due journals of the caller's legal entity. */
export function sweepGLJournals(): Promise<JournalSweepResult> {
  return send<JournalSweepResult>("/gl-journals:sweep", { method: "POST" });
}

/** POST /gl-journals/{id}:retry - a parked journal goes back to pending, optionally under a new posting date. */
export function retryGLJournal(id: string, postingDate?: string): Promise<GLJournal> {
  return send<GLJournal>(`/gl-journals/${id}:retry`, { method: "POST", body: postingDate ? JSON.stringify({ postingDate }) : undefined });
}

export const ADVICE_FORMATS = ["CSV", "JSON", "EDI820"] as const;
export type AdviceFormat = (typeof ADVICE_FORMATS)[number];

export interface AdviceInput {
  legalEntityId: string;
  format: AdviceFormat;
  content: string;
  autoApply?: boolean;
  createMissing?: boolean;
  currency?: string;
  valueDate?: string;
  paymentMode?: PaymentMode;
}

export type AdviceOutcome = "ATTACHED" | "UNMATCHED" | "AMBIGUOUS" | "ALREADY_APPLIED" | "INVALID" | "ADVISED";

export interface AdviceLineResult {
  paymentRef: string;
  outcome: AdviceOutcome;
  paymentId?: string;
  detail?: string;
}

export interface AdviceResult {
  lines: AdviceLineResult[];
  attached: number;
  created: number;
  skipped: number;
  autoApply?: { applied: number; suggested: number; onAccount: number; failed: number };
}

/** POST /remittance-advices - 400 AR-ADVICE-MALFORMED names the row or segment. */
export function importAdvice(input: AdviceInput): Promise<AdviceResult> {
  return send<AdviceResult>("/remittance-advices", { method: "POST", body: JSON.stringify(input) });
}

export interface BankAccountMapping {
  legalEntityId: string;
  currency: string;
  accountRef: string;
  glAccount: string;
  name: string;
}

/** GET /bank-accounts - the ledger account each bank account's cash is booked to (1000 where none is mapped). */
export function listBankAccounts(legalEntityId: string, signal?: AbortSignal): Promise<BankAccountMapping[]> {
  return items<BankAccountMapping>(`/bank-accounts${query({ legalEntityId })}`, signal);
}

/** PUT /bank-accounts - creates or replaces a mapping. */
export function putBankAccount(input: BankAccountMapping): Promise<BankAccountMapping> {
  return send<BankAccountMapping>("/bank-accounts", { method: "PUT", body: JSON.stringify(input) });
}

/** DELETE /bank-accounts - removes a mapping; its cash books to 1000 again. */
export async function deleteBankAccount(legalEntityId: string, currency: string, accountRef: string): Promise<void> {
  const res = await apiFetch(`${base}/bank-accounts${query({ legalEntityId, currency, accountRef })}`, { method: "DELETE", credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!res.ok) throw await toError(res);
}

export interface FXSetting {
  legalEntityId: string;
  crossCurrencyEnabled: boolean;
  functionalCurrency: string;
  gainAccount: string;
  lossAccount: string;
  /** How far a cross-currency settlement may miss the item amount: the larger of this amount and tolerancePercent of the item. Both absent: 5.00 and 0.5%. */
  toleranceAmount?: string | null;
  tolerancePercent?: string | null;
}

/** GET /fx-settings - how a legal entity settles across currencies (the default when none is stored). */
export function getFXSetting(legalEntityId: string, signal?: AbortSignal): Promise<FXSetting> {
  return send<FXSetting>(`/fx-settings${query({ legalEntityId })}`, {}, signal);
}

/** PUT /fx-settings - sets cross-currency settlement, the functional currency and the realized gain and loss accounts. */
export function putFXSetting(input: FXSetting): Promise<FXSetting> {
  return send<FXSetting>("/fx-settings", { method: "PUT", body: JSON.stringify(input) });
}

export interface BackfillInput {
  legalEntityId: string;
  openingAccount?: string;
  openingDate?: string;
}

export interface BackfillResult {
  receipts: number;
  applications: number;
  openingItems: number;
  openingAlreadyQueued: number;
  dunningFees: number;
  interest: number;
  downPayments: number;
  deductions: number;
  more: boolean;
}

/** POST /gl-journals:backfill - queues the journals owed for what happened before receivables posted to the ledger. */
export function backfillGLJournals(input: BackfillInput): Promise<BackfillResult> {
  return send<BackfillResult>("/gl-journals:backfill", { method: "POST", body: JSON.stringify(input) });
}

export interface UnappliedSettlement {
  settlementId: string;
  openItemId: string;
  amount: string;
  itemAmount: string;
  openAfter: string;
  itemStatus: string;
}

export interface UnapplyResult {
  payment: Payment;
  reversed: UnappliedSettlement[];
}

/** POST /payments/{id}:unapply - reverses every live application of a payment: its items open again by what it took off them. */
export function unapplyPayment(id: string, version: number, reason: string): Promise<UnapplyResult> {
  return send<UnapplyResult>(`/payments/${id}:unapply`, { method: "POST", headers: { "If-Match": `"${version}"` }, body: JSON.stringify(reason ? { reason } : {}) });
}

/** POST /payments/{id}:reverse - undoes the cash receipt of a payment that was never applied. */
export function reversePayment(id: string, version: number): Promise<unknown> {
  return send<unknown>(`/payments/${id}:reverse`, { method: "POST", headers: { "If-Match": `"${version}"` } });
}
