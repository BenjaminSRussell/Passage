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
  /** Optional campaign (FR-26.10); the record prices only while the campaign is live. */
  campaignId?: string;
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
  campaignId?: string;
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

// ---- Rebate agreements and accruals (FR-26.11, BR-26.8; vendor rebates FR-26.15).

export type RebateBasis = "NET_AMOUNT" | "QUANTITY";
export type RebatePeriod = "MONTH" | "QUARTER" | "YEAR";
/** CUSTOMER rebates accrue on sales invoices; VENDOR rebates on vendor invoices and settle by claims. */
export type RebatePartyRole = "CUSTOMER" | "VENDOR";

export interface RebateTier {
  from: string;
  ratePct?: string;
  amountPerUnit?: string;
}

export interface RebateAgreementInput {
  legalEntityId: string;
  name: string;
  partyRole?: RebatePartyRole;
  customerId?: string;
  customerGroupId?: string;
  vendorId?: string;
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
  partyRole: RebatePartyRole;
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

export function listRebateAgreements(legalEntityId = "", signal?: AbortSignal, partyRole: RebatePartyRole | "" = ""): Promise<RebateAgreement[]> {
  return items<RebateAgreement>(`/rebates/agreements${query({ legalEntityId, partyRole })}`, signal);
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

// ---- Vendor rebate claims (FR-26.15, BR-26.11, AC-26.11).

/** CREDIT_NOTE and PAYMENT_DEDUCTION are chosen; a negative claim settles by VENDOR_INVOICE on its own. */
export type ClaimSettlementMode = "CREDIT_NOTE" | "PAYMENT_DEDUCTION" | "VENDOR_INVOICE";
export type ClaimStatus = "OPEN" | "APPROVED";

export interface VendorClaimPreview {
  agreementId: string;
  vendorId: string;
  periodId: string;
  /** The period's accruals. */
  accrued: string;
  /** What the period's earlier claims already claimed. */
  priorClaimed: string;
  /** accrued - priorClaimed: what a new claim would settle; "0" when the period is fully claimed. */
  amount: string;
  currency: string;
  settlementMode: ClaimSettlementMode;
  claims: number;
}

export interface VendorClaim {
  id: string;
  number: string;
  legalEntityId: string;
  agreementId: string;
  vendorId: string;
  periodId: string;
  accruedTotal: string;
  priorClaimed: string;
  amount: string;
  currency: string;
  settlementMode: ClaimSettlementMode;
  status: ClaimStatus;
  version: number;
  createdBy?: string;
  approvedBy?: string;
  createdAt: string;
  approvedAt?: string;
}

export interface VendorClaimFilter {
  agreementId?: string;
  vendorId?: string;
  period?: string;
  status?: ClaimStatus | "";
}

/** GET /rebates/agreements/{id}/claim-preview - the claim a period of a VENDOR agreement would produce; writes nothing. */
export function previewVendorClaim(agreementId: string, period: string, signal?: AbortSignal): Promise<VendorClaimPreview> {
  return send<VendorClaimPreview>(`/rebates/agreements/${encodeURIComponent(agreementId)}/claim-preview${query({ period })}`, {}, signal);
}

/** POST /rebates/vendor-claims - claim one period (RC-n); 409 PRC-0015 when it is fully claimed. */
export function createVendorClaim(
  body: { agreementId: string; period: string; settlementMode?: "CREDIT_NOTE" | "PAYMENT_DEDUCTION" },
  key: string = newKey(),
): Promise<VendorClaim> {
  return post<VendorClaim>("/rebates/vendor-claims", body, key);
}

/** GET /rebates/vendor-claims - the Vendor rebate claims inquiry, newest first, at most 100. */
export function listVendorClaims(filter: VendorClaimFilter = {}, signal?: AbortSignal): Promise<VendorClaim[]> {
  return items<VendorClaim>(`/rebates/vendor-claims${query({ ...filter })}`, signal);
}

export function getVendorClaim(id: string, signal?: AbortSignal): Promise<VendorClaim> {
  return send<VendorClaim>(`/rebates/vendor-claims/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /rebates/vendor-claims/{id}:approve at the claim version; the claim's creator cannot approve it (PRC-0046). */
export function approveVendorClaim(id: string, version: number, key: string = newKey()): Promise<VendorClaim> {
  return post<VendorClaim>(`/rebates/vendor-claims/${encodeURIComponent(id)}:approve`, { version }, key);
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

// ---- Trade promotion funds (FR-26.19, BR-26.15) and promotion planning (FR-26.20).

export const FUND_SOURCES = ["CAMPAIGN_ACCRUAL", "REBATE_ACCRUAL", "CLAIM", "DEDUCTION"] as const;
export type FundSource = (typeof FUND_SOURCES)[number];

export interface FundInput {
  legalEntityId: string;
  name: string;
  type: string;
  currency: string;
  customerScope: string;
  owner: string;
  budgetPerPeriod: string;
  allowOverspend: boolean;
}

export interface Fund extends FundInput {
  id: string;
  closed: boolean;
  version: number;
}

export interface FundAttributionInput {
  /** Fiscal period such as 2027-03. */
  period: string;
  source: FundSource;
  reference: string;
  amount: string;
}

/** Budget, usage and remaining budget of one fund in one period; remaining is negative only with overspend. */
export interface FundPosition {
  fundId: string;
  period: string;
  budget: string;
  used: string;
  remaining: string;
  closed: boolean;
}

export interface FundUsageLine {
  period: string;
  source: FundSource;
  amount: string;
}

export interface FundUsage {
  fund: Fund;
  items: FundUsageLine[];
  periods: FundPosition[];
}

export interface PromotionInput {
  legalEntityId: string;
  name: string;
  eventCategory: string;
  eventType: string;
  customerScope: string;
  fundId?: string;
  productId: string;
  currency: string;
  startDate: string;
  endDate: string;
  /** Units sold in each of the 13 weeks before the promotion, oldest first. */
  baselineWeeklyUnits: string[];
  liftPct: string;
  plannedSpend: string;
}

export interface PromotionActuals {
  units: string;
  weeks: number;
  spend: string;
}

export interface Promotion extends Omit<PromotionInput, "fundId"> {
  id: string;
  fundId?: string;
  baseline: string;
  forecastUnits: string;
  actuals?: PromotionActuals;
  effectivenessPct?: string;
}

export function listFunds(legalEntityId = "", signal?: AbortSignal): Promise<Fund[]> {
  return items<Fund>(`/funds${query({ legalEntityId })}`, signal);
}

export function getFund(id: string, signal?: AbortSignal): Promise<Fund> {
  return send<Fund>(`/funds/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /funds - an open fund at version 1. */
export function createFund(body: FundInput, key: string = newKey()): Promise<Fund> {
  return post<Fund>("/funds", body, key);
}

/** POST /funds/{id}:attribute - 409 PRC-0011 when closed, 422 PRC-0012 when over the period budget. */
export function attributeFund(id: string, body: FundAttributionInput, key: string = newKey()): Promise<FundPosition> {
  return post<FundPosition>(`/funds/${encodeURIComponent(id)}:attribute`, body, key);
}

/** POST /funds/{id}:revise - new budget per period and overspend flag at the expected version. */
export function reviseFund(id: string, body: { version: number; budgetPerPeriod: string; allowOverspend: boolean }, key: string = newKey()): Promise<Fund> {
  return post<Fund>(`/funds/${encodeURIComponent(id)}:revise`, body, key);
}

/** POST /funds/{id}:close - freezes usage; new attribution fails with PRC-0011. */
export function closeFund(id: string, version: number, key: string = newKey()): Promise<Fund> {
  return post<Fund>(`/funds/${encodeURIComponent(id)}:close`, { version }, key);
}

/** POST /funds/{id}:reopen - lifts the close. */
export function reopenFund(id: string, version: number, key: string = newKey()): Promise<Fund> {
  return post<Fund>(`/funds/${encodeURIComponent(id)}:reopen`, { version }, key);
}

/** GET /funds/{id}/usage - the Fund usage inquiry: usage per period and source, and the position per period. */
export async function getFundUsage(id: string, signal?: AbortSignal): Promise<FundUsage> {
  const body = await send<Partial<FundUsage>>(`/funds/${encodeURIComponent(id)}/usage`, {}, signal);
  if (!body.fund || !Array.isArray(body.items) || !Array.isArray(body.periods)) {
    throw new PricingApiError(0, "COM-0001", "The fund usage response is incomplete");
  }
  return body as FundUsage;
}

export function listPromotions(legalEntityId = "", signal?: AbortSignal): Promise<Promotion[]> {
  return items<Promotion>(`/promotions${query({ legalEntityId })}`, signal);
}

export function getPromotion(id: string, signal?: AbortSignal): Promise<Promotion> {
  return send<Promotion>(`/promotions/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /promotions - the fund, when named, must be open and in the same legal entity and currency. */
export function createPromotion(body: PromotionInput, key: string = newKey()): Promise<Promotion> {
  return post<Promotion>("/promotions", body, key);
}

/** POST /promotions/{id}:actuals - overwrites the window's units, weeks and spend; returns the effectiveness. */
export function recordPromotionActuals(id: string, body: PromotionActuals): Promise<Promotion> {
  return post<Promotion>(`/promotions/${encodeURIComponent(id)}:actuals`, body);
}

// ---- Campaigns (FR-26.10): validity, target customer group, budget and kill switch.

export interface CampaignInput {
  legalEntityId: string;
  name: string;
  validFrom: string;
  validTo: string;
  /** Customer group the campaign is for; empty means every customer. */
  targetCustomerGroup: string;
  budget: string;
  currency: string;
}

export interface Campaign extends CampaignInput {
  id: string;
  killed: boolean;
  version: number;
}

export function listCampaigns(legalEntityId = "", signal?: AbortSignal): Promise<Campaign[]> {
  return items<Campaign>(`/campaigns${query({ legalEntityId })}`, signal);
}

export function getCampaign(id: string, signal?: AbortSignal): Promise<Campaign> {
  return send<Campaign>(`/campaigns/${encodeURIComponent(id)}`, {}, signal);
}

export function createCampaign(body: CampaignInput, key: string = newKey()): Promise<Campaign> {
  return post<Campaign>("/campaigns", body, key);
}

/** POST /campaigns/{id}:kill - the campaign's records drop out of the next resolve. */
export function killCampaign(id: string, version: number, key: string = newKey()): Promise<Campaign> {
  return post<Campaign>(`/campaigns/${encodeURIComponent(id)}:kill`, { version }, key);
}

/** POST /campaigns/{id}:restore - the records rejoin the next resolve inside the campaign validity. */
export function restoreCampaign(id: string, version: number, key: string = newKey()): Promise<Campaign> {
  return post<Campaign>(`/campaigns/${encodeURIComponent(id)}:restore`, { version }, key);
}

// ---- Pricing procedures (FR-26.1): versioned step sequences; ACTIVE and SUPERSEDED versions are immutable.

export const PROCEDURE_STEP_TYPES = [
  "PRICE",
  "LINE_DISC",
  "MULTILINE_DISC",
  "TOTAL_DISC",
  "SURCHARGE",
  "FREIGHT",
  "REBATE_ACCRUAL",
  "ROUNDING",
  "FREE_GOODS",
] as const;
export type ProcedureStepType = (typeof PROCEDURE_STEP_TYPES)[number];
export type ProcedureStatus = "DRAFT" | "ACTIVE" | "SUPERSEDED";
export type ExclusionMode = "" | "BEST" | "FIRST";

export interface ProcedureStep {
  stepNo: number;
  conditionType: ProcedureStepType;
  requirementCel: string;
  exclusionGroup: string;
  exclusionMode: ExclusionMode;
  statistical: boolean;
  accountKey: string;
}

export interface ProcedureInput {
  legalEntityId: string;
  name: string;
  steps: ProcedureStep[];
}

export interface Procedure extends ProcedureInput {
  id: string;
  version: number;
  status: ProcedureStatus;
  /** Optimistic version of a DRAFT's steps. */
  revision: number;
}

export function listProcedures(legalEntityId = "", signal?: AbortSignal): Promise<Procedure[]> {
  return items<Procedure>(`/procedures${query({ legalEntityId })}`, signal);
}

export function getProcedure(id: string, signal?: AbortSignal): Promise<Procedure> {
  return send<Procedure>(`/procedures/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /procedures - saves a DRAFT with the next version number for the legal entity and name. */
export function createProcedure(body: ProcedureInput, key: string = newKey()): Promise<Procedure> {
  return post<Procedure>("/procedures", body, key);
}

/** POST /procedures/{id}:revise - replaces a DRAFT's steps; 409 PRC-0008 once activated, 412 on a stale revision. */
export function reviseProcedure(id: string, revision: number, steps: ProcedureStep[], key: string = newKey()): Promise<Procedure> {
  return post<Procedure>(`/procedures/${encodeURIComponent(id)}:revise`, { revision, steps }, key);
}

/** POST /procedures/{id}:activate - supersedes the ACTIVE version of the same name; 409 PRC-0008 when not DRAFT. */
export function activateProcedure(id: string, key: string = newKey()): Promise<Procedure> {
  return post<Procedure>(`/procedures/${encodeURIComponent(id)}:activate`, {}, key);
}
