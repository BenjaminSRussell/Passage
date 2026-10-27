// Typed client for the pricing service contract (services/pricing/api/openapi.yaml).
// The browser calls /api/v1/pricing/...; the BFF rewrites to /v1/pricing/... on the service.

export const base = "/api/v1/pricing";

export const CONDITION_TYPES = ["PRICE", "LINE_DISC", "MULTILINE_DISC", "TOTAL_DISC", "SURCHARGE"] as const;
export type ConditionType = (typeof CONDITION_TYPES)[number];

export const RECORD_STATUSES = ["DRAFT", "ACTIVE", "EXPIRED", "WITHDRAWN"] as const;
export type RecordStatus = (typeof RECORD_STATUSES)[number];

export const CUSTOMER_KINDS = ["customer", "customer_group", "all"] as const;
export type CustomerKind = (typeof CUSTOMER_KINDS)[number];

export const PRODUCT_KINDS = ["product", "product_group", "variant", "all"] as const;
export type ProductKind = (typeof PRODUCT_KINDS)[number];

/** Fields a caller sends to create a condition record (OpenAPI ConditionRecordInput). */
export interface ConditionRecordInput {
  legalEntityId: string;
  type: ConditionType;
  customerKind: CustomerKind;
  customerKey: string;
  productKind: ProductKind;
  productKey: string;
  variant: string;
  currency: string;
  unit: string;
  fromQuantity: string;
  amount: string;
  discPct1: string;
  discPct2: string;
  validFrom: string;
  validTo: string;
  searchNext: boolean;
  exclusive: boolean;
}

export interface ConditionRecord extends ConditionRecordInput {
  id: string;
  status: RecordStatus;
  version: number;
}

export interface RecordFilter {
  legalEntityId?: string;
  status?: RecordStatus | "";
  type?: ConditionType | "";
  customer?: string;
  product?: string;
  asOf?: string;
  limit?: number;
}

export interface ResolveLineInput {
  productId: string;
  productGroupId?: string;
  variant?: string;
  unit: string;
  quantity: string;
}

export interface ResolveRequest {
  legalEntityId: string;
  customerId: string;
  customerGroupId?: string;
  currency: string;
  pricingDate: string;
  lines: ResolveLineInput[];
}

export interface AppliedCondition {
  type: ConditionType;
  recordId: string;
  access: number;
}

export interface TraceAccess {
  seq: number;
  access: string;
  evaluated: boolean;
  candidates: string[];
  winner?: string;
  reason: string;
}

export interface TraceStep {
  condition: string;
  selected?: string;
  accesses: TraceAccess[];
}

export interface ResolvedLine {
  lineNo: number;
  unit: string;
  quantity: string;
  unitPrice: string;
  netUnitPrice: string;
  lineNet: string;
  exclusive?: boolean;
  applied: AppliedCondition[];
  errorCode?: string;
  errorDetail?: string;
  trace?: TraceStep[];
}

export interface ResolveResponse {
  currency: string;
  lines: ResolvedLine[];
  subtotal: string;
  totalDiscount: string;
  /** Small-order surcharge included in orderNet (FR-26.6); "0" when none applied. */
  surcharge: string;
  orderNet: string;
  applied: AppliedCondition[];
  explainRef: string;
  explainTruncated?: boolean;
  trace?: TraceStep[];
}

/** Problem details (RFC 9457) surfaced to the UI with the stable service code and field errors. */
export class PricingApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "PricingApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof PricingApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

/** A fresh Idempotency-Key for one user action; retries of the same action reuse it. */
export function newKey(): string {
  return crypto.randomUUID();
}

async function toError(res: Response): Promise<PricingApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new PricingApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

function post<T>(path: string, body: unknown, key?: string): Promise<T> {
  const headers: Record<string, string> = {};
  if (key) headers["Idempotency-Key"] = key;
  return send<T>(path, { method: "POST", headers, body: JSON.stringify(body) });
}

/** GET /condition-records with the optional filters; at most 100 rows, ordered by id. */
export async function listConditionRecords(filter: RecordFilter = {}, signal?: AbortSignal): Promise<ConditionRecord[]> {
  const q = new URLSearchParams();
  for (const [name, value] of Object.entries(filter)) {
    if (value !== undefined && value !== "") q.set(name, String(value));
  }
  const query = q.toString();
  const body = await send<{ items?: ConditionRecord[] }>(`/condition-records${query ? `?${query}` : ""}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new PricingApiError(0, "COM-0001", "The response has no items");
  }
  return body.items;
}

export function getConditionRecord(id: string, signal?: AbortSignal): Promise<ConditionRecord> {
  return send<ConditionRecord>(`/condition-records/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /condition-records - creates a DRAFT; the overlap check runs at activation. */
export function createConditionRecord(input: ConditionRecordInput, key: string = newKey()): Promise<ConditionRecord> {
  return post<ConditionRecord>("/condition-records", input, key);
}

/** POST /condition-records/{id}:activate - 409 PRC-0002 names an overlapping active record. */
export function activateConditionRecord(id: string, version: number, key: string = newKey()): Promise<ConditionRecord> {
  return post<ConditionRecord>(`/condition-records/${encodeURIComponent(id)}:activate`, { version }, key);
}

export function withdrawConditionRecord(id: string, version: number, reason: string, key: string = newKey()): Promise<ConditionRecord> {
  return post<ConditionRecord>(`/condition-records/${encodeURIComponent(id)}:withdraw`, { version, reason }, key);
}

/** POST /resolve - price a document against the stored ACTIVE records, with the explain trace. */
export function resolvePrice(body: ResolveRequest, explain = true): Promise<ResolveResponse> {
  return post<ResolveResponse>(`/resolve?explain=${explain ? "true" : "false"}`, body);
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
  if (!Array.isArray(body.items)) {
    throw new PricingApiError(0, "COM-0001", "The response has no items");
  }
  return body.items;
}

// ---- Price journals (FR-26.9): mass create, update and delete; validate, post atomically, reverse.

export const JOURNAL_STATUSES = ["OPEN", "VALIDATED", "POSTED", "REVERSED"] as const;
export type JournalStatus = (typeof JOURNAL_STATUSES)[number];
export type JournalOp = "CREATE" | "UPDATE" | "DELETE";

/** One journal line as sent (OpenAPI JournalLineInput). UPDATE takes adjustPct or record. */
export interface JournalLineInput {
  op: JournalOp;
  recordId?: string;
  expectedVersion?: number;
  adjustPct?: string;
  record?: ConditionRecordInput;
}

export interface JournalRequest {
  legalEntityId: string;
  name: string;
  smartRound: boolean;
  lines: JournalLineInput[];
}

export interface JournalLineError {
  lineNo: number;
  code: string;
  detail: string;
}

export interface JournalLine {
  lineNo: number;
  op: JournalOp | "RESTORE";
  recordId: string;
  expectedVersion: number;
  adjustPct: string;
  before?: ConditionRecord;
  after?: ConditionRecord;
  errors: JournalLineError[];
}

export interface Journal {
  id: string;
  legalEntityId: string;
  name: string;
  status: JournalStatus;
  smartRound: boolean;
  lineCount: number;
  errorCount: number;
  reversesJournalId?: string;
  reversedByJournalId?: string;
  postedAt?: string;
  createdAt: string;
  errors: JournalLineError[];
  lines?: JournalLine[];
  linesTruncated?: boolean;
}

export interface JournalFilter {
  legalEntityId?: string;
  status?: JournalStatus | "";
  limit?: number;
}

/** GET /journals - the price journal log, newest first, without lines. */
export function listPriceJournals(filter: JournalFilter = {}, signal?: AbortSignal): Promise<Journal[]> {
  return items<Journal>(`/journals${query({ ...filter })}`, signal);
}

/** GET /journals/{id} - the journal with its first 1,000 lines and every line error. */
export function getPriceJournal(id: string, signal?: AbortSignal): Promise<Journal> {
  return send<Journal>(`/journals/${encodeURIComponent(id)}`, {}, signal);
}

export function createPriceJournal(body: JournalRequest, key: string = newKey()): Promise<Journal> {
  return post<Journal>("/journals", body, key);
}

/** POST /journals/{id}:validate - checks every line; the journal stays OPEN with line errors when any fail. */
export function validatePriceJournal(id: string): Promise<Journal> {
  return post<Journal>(`/journals/${encodeURIComponent(id)}:validate`, {});
}

/** POST /journals/{id}:post - 422 PRC-0005 names the lines when something changed since validation. */
export function postPriceJournal(id: string, key: string = newKey()): Promise<Journal> {
  return post<Journal>(`/journals/${encodeURIComponent(id)}:post`, {}, key);
}

/** POST /journals/{id}:reverse - returns the new reversing journal. */
export function reversePriceJournal(id: string, key: string = newKey()): Promise<Journal> {
  return post<Journal>(`/journals/${encodeURIComponent(id)}:reverse`, {}, key);
}

// ---- Rebate agreements and accruals (FR-26.11, BR-26.8).

export type RebateBasis = "NET_AMOUNT" | "QUANTITY";
export type RebatePeriod = "MONTH" | "QUARTER" | "YEAR";

export interface RebateTier {
  from: string;
  ratePct?: string;
  amountPerUnit?: string;
}

export interface RebateAgreementInput {
  legalEntityId: string;
  name: string;
  customerId?: string;
  customerGroupId?: string;
  basis: RebateBasis;
  period: RebatePeriod;
  tiers: RebateTier[];
  currency: string;
  validFrom: string;
  validTo: string;
  accrualAccount?: string;
  expenseAccount?: string;
}

export interface RebateAgreement extends RebateAgreementInput {
  id: string;
  status: "ACTIVE";
  accrualAccount: string;
  expenseAccount: string;
}

export interface RebateAccrual {
  id: string;
  agreementId: string;
  periodId: string;
  invoiceLineId: string;
  invoiceDate: string;
  basisValue: string;
  basisQuantity: string;
  amount: string;
  currency: string;
  status: "ACCRUED" | "ADJUSTED" | "SETTLED" | "REVERSED";
  /** A replayed invoice line (amount 0) or the per-period delta of a retroactive recompute (FR-26.23). */
  retroactive?: boolean;
}

export function listRebateAgreements(legalEntityId = "", signal?: AbortSignal): Promise<RebateAgreement[]> {
  return items<RebateAgreement>(`/rebates/agreements${query({ legalEntityId })}`, signal);
}

export function getRebateAgreement(id: string, signal?: AbortSignal): Promise<RebateAgreement> {
  return send<RebateAgreement>(`/rebates/agreements/${encodeURIComponent(id)}`, {}, signal);
}

export function createRebateAgreement(body: RebateAgreementInput, key: string = newKey()): Promise<RebateAgreement> {
  return post<RebateAgreement>("/rebates/agreements", body, key);
}

/** GET /rebates/accruals - accrual rows of an agreement, optionally for one period (2027-03, 2027-Q1, 2027). */
export function listRebateAccruals(filter: { agreementId?: string; period?: string } = {}, signal?: AbortSignal): Promise<RebateAccrual[]> {
  return items<RebateAccrual>(`/rebates/accruals${query(filter)}`, signal);
}

// ---- Retroactive rebate recompute (FR-26.23, BR-26.14, AC-26.14).

/** One posted invoice line replayed by a retroactive recompute. */
export interface RetroLineInput {
  invoiceLineId: string;
  invoiceDate: string;
  value: string;
  quantity?: string;
  currency: string;
}

export interface RetroDelta {
  periodId: string;
  target: string;
  posted: string;
  delta: string;
}

export interface RetroResult {
  agreementId: string;
  activatedOn: string;
  reaccruedLines: number;
  replayed: boolean;
  deltas: RetroDelta[];
  notice?: { code: string; message: string };
}

/** POST /rebates/agreements/{id}:retro-recompute - the Idempotency-Key names the run and is required. */
export function retroRecompute(agreementId: string, lines: RetroLineInput[], key: string = newKey()): Promise<RetroResult> {
  return post<RetroResult>(`/rebates/agreements/${encodeURIComponent(agreementId)}:retro-recompute`, { lines }, key);
}

// ---- Commissions (FR-26.12, FR-26.18, BR-26.12).

export type CommissionBasis = "REVENUE" | "MARGIN";

export interface RepShare {
  repId: string;
  sharePct: string;
}

export interface SalesGroupInput {
  legalEntityId: string;
  name: string;
  reps: RepShare[];
}

export interface SalesGroup extends SalesGroupInput {
  id: string;
}

export interface CommissionRuleInput {
  salesGroupId: string;
  customerGroup: string;
  itemGroup: string;
  basis: CommissionBasis;
  ratePct: string;
}

export interface CommissionRule extends CommissionRuleInput {
  id: string;
  legalEntityId: string;
}

export interface CommissionTransaction {
  id: string;
  legalEntityId: string;
  invoiceLineId: string;
  reversesInvoiceLineId?: string;
  invoiceDate: string;
  periodId: string;
  customerId?: string;
  customerGroup: string;
  itemGroup: string;
  salesGroupId: string;
  repId: string;
  sharePct: string;
  basis: CommissionBasis;
  lineNet: string;
  costSnapshot?: string;
  basisAmount: string;
  ratePct: string;
  amount: string;
  currency: string;
  debitAccount: string;
  creditAccount: string;
  status: "POSTED";
}

export interface CommissionFilter {
  legalEntityId?: string;
  salesGroupId?: string;
  repId?: string;
  customerId?: string;
  invoiceLineId?: string;
  /** Invoice month such as 2027-03. */
  period?: string;
}

export function listSalesGroups(legalEntityId = "", signal?: AbortSignal): Promise<SalesGroup[]> {
  return items<SalesGroup>(`/commission/sales-groups${query({ legalEntityId })}`, signal);
}

export function getSalesGroup(id: string, signal?: AbortSignal): Promise<SalesGroup> {
  return send<SalesGroup>(`/commission/sales-groups/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /commission/sales-groups - shares sum to 100; the first rep takes the rounding residual. */
export function createSalesGroup(body: SalesGroupInput, key: string = newKey()): Promise<SalesGroup> {
  return post<SalesGroup>("/commission/sales-groups", body, key);
}

/** GET /commission/rules - most specific first; one rule per sales group, customer group and item group. */
export function listCommissionRules(salesGroupId = "", signal?: AbortSignal): Promise<CommissionRule[]> {
  return items<CommissionRule>(`/commission/rules${query({ salesGroupId })}`, signal);
}

/** POST /commission/rules - 409 PRC-0038 when the group already has a rule for the same customer and item groups. */
export function createCommissionRule(body: CommissionRuleInput, key: string = newKey()): Promise<CommissionRule> {
  return post<CommissionRule>("/commission/rules", body, key);
}

/** GET /commission/transactions - the commission inquiry, newest invoice first, at most 100 rows. */
export function listCommissionTransactions(filter: CommissionFilter = {}, signal?: AbortSignal): Promise<CommissionTransaction[]> {
  return items<CommissionTransaction>(`/commission/transactions${query({ ...filter })}`, signal);
}
