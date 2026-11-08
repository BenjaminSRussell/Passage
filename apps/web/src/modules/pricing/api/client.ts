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
  /** Last valid day on or after this date; with validToTo, the list comes soonest-ending first. */
  validToFrom?: string;
  /** Last valid day on or before this date. */
  validToTo?: string;
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
  /** Selects the channel's procedure assignment; empty uses the legal entity's default (PRD 5.26.13). */
  salesChannel?: string;
  /** Name of a procedure to price with instead of the assigned one. */
  procedure?: string;
  lines: ResolveLineInput[];
}

export interface AppliedCondition {
  type: ConditionType;
  recordId: string;
  access: number;
  /** Procedure step that applied the condition; absent under the reference order. */
  stepNo?: number;
  accountKey?: string;
  /** A statistical step is reported with amount and not added to the net. */
  statistical?: boolean;
  amount?: string;
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
  stepNo?: number;
  /** Why a condition that found a record did not apply: an exclusion group kept another member (FR-26.8). */
  reason?: string;
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
  /** The procedure version the prices came from (FR-26.4); absent under the reference order. */
  procedure?: { id: string; name: string; version: number };
  explainRef: string;
  explainTruncated?: boolean;
  trace?: TraceStep[];
  /** Zero-priced free goods line proposals (FR-26.22); empty when no live rule applies. */
  freeGoods: FreeGoodsProposal[];
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

/** The CSV file of the trade agreements export, as the service wrote it. */
export interface RecordsExport {
  csv: string;
  filename: string;
  /** Rows in the file, without the header. */
  count: number;
  /** True when more records matched than the file holds. */
  truncated: boolean;
}

/** GET /condition-records:export - the records that match the filter as CSV (up to 50000 rows). */
export async function exportConditionRecords(filter: RecordFilter = {}, signal?: AbortSignal): Promise<RecordsExport> {
  const q = new URLSearchParams();
  for (const [name, value] of Object.entries(filter)) {
    if (value !== undefined && value !== "" && name !== "limit") q.set(name, String(value));
  }
  const text = q.toString();
  const res = await sendRaw(`/condition-records:export${text ? `?${text}` : ""}`, { headers: { Accept: "text/csv" } }, signal);
  const csv = await res.text();
  const disposition = /filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") ?? "");
  return {
    csv,
    filename: disposition?.[1] ?? "trade-agreements.csv",
    count: Math.max(parseCsv(csv).filter((row) => row.some((cell) => cell !== "")).length - 1, 0),
    truncated: res.headers.get("X-Passage-Truncated") === "true",
  };
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

export interface CompareRequest extends ResolveRequest {
  /** The baseline procedure version. */
  procedureIdA: string;
  /** The version compared with the baseline. */
  procedureIdB: string;
}

export interface LineComparison {
  lineNo: number;
  netUnitPriceA: string;
  netUnitPriceB: string;
  netUnitPriceDelta: string;
  lineNetA: string;
  lineNetB: string;
  lineNetDelta: string;
  errorCodeA?: string;
  errorCodeB?: string;
  appliedOnlyA: string[];
  appliedOnlyB: string[];
  changed: boolean;
}

export interface Comparison {
  identical: boolean;
  subtotalDelta: string;
  totalDiscountDelta: string;
  surchargeDelta: string;
  orderNetDelta: string;
  changedLines: number;
  lines: LineComparison[];
}

export interface CompareResponse {
  a: ResolveResponse;
  b: ResolveResponse;
  /** Deltas are version B minus version A. */
  comparison: Comparison;
}

/** POST /compare - price one request under two procedure versions (FR-26.13); DRAFT versions are allowed. */
export function comparePrices(body: CompareRequest): Promise<CompareResponse> {
  return post<CompareResponse>("/compare?explain=false", body);
}

export type PriceListScope = "CUSTOMER" | "GROUP" | "ALL";

export interface PriceListItem extends ConditionRecord {
  scope: PriceListScope;
}

export interface PriceListQuery {
  legalEntityId: string;
  customerId: string;
  customerGroupId?: string;
  currency: string;
  asOf: string;
  type?: ConditionType | "";
  productId?: string;
  productGroupId?: string;
  /** Entries to skip: the nextOffset of the previous page. */
  offset?: number;
}

export interface NetPriceQuery {
  legalEntityId: string;
  customerId: string;
  customerGroupId?: string;
  currency: string;
  asOf: string;
  productId?: string;
  productGroupId?: string;
  /** Prices with the procedure assigned to this channel (the default assignment when absent). */
  salesChannel?: string;
}

export interface NetPriceApplied {
  type: ConditionType;
  recordId: string;
  /** A statistical step: reported, but it did not change the net. */
  statistical?: boolean;
}

export interface NetPriceBreak {
  quantity: string;
  unitPrice: string;
  netUnitPrice: string;
  lineNet: string;
  priceRecordId?: string;
  applied: NetPriceApplied[];
  errorCode?: string;
}

export interface NetPriceRow {
  productKind: ConditionRecord["productKind"];
  productKey: string;
  variant: string;
  unit: string;
  breaks: NetPriceBreak[];
}

export interface NetPriceList {
  rows: NetPriceRow[];
  /** True when more rows apply than the service prices (500 rows and 2,000 breaks per response). */
  truncated: boolean;
}

export interface PriceList {
  items: PriceListItem[];
  /** True when more entries follow this page (the service returns at most 1000 per page). */
  truncated: boolean;
  /** Every entry that matches, whatever the page. */
  total: number;
  /** Offset of the next page; set only when truncated. */
  nextOffset?: number;
}

/** GET /price-list - the ACTIVE records that apply to a customer on a date, most specific first. */
export async function getPriceList(q: PriceListQuery, signal?: AbortSignal): Promise<PriceList> {
  const body = await send<{ items?: PriceListItem[]; truncated?: boolean; total?: number; nextOffset?: number }>(`/price-list${query({ ...q })}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new PricingApiError(0, "COM-0001", "The response has no items");
  }
  return { items: body.items, truncated: body.truncated === true, total: body.total ?? body.items.length, nextOffset: body.nextOffset };
}

/** GET /price-list/net-prices - the net price at each quantity break of the customer's price list rows. */
export async function getNetPriceList(q: NetPriceQuery, signal?: AbortSignal): Promise<NetPriceList> {
  const body = await send<{ rows?: NetPriceRow[]; truncated?: boolean }>(`/price-list/net-prices${query({ ...q })}`, {}, signal);
  if (!Array.isArray(body.rows)) {
    throw new PricingApiError(0, "COM-0001", "The response has no rows");
  }
  return { rows: body.rows, truncated: body.truncated === true };
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
  /** The maker and the checker; with journalFourEyes on they must differ (PRC-0057). */
  createdBy?: string;
  postedBy?: string;
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
  status: "ACTIVE" | "CLOSED";
  /** Optimistic lock: 1 on create, incremented by every revise and close. */
  version: number;
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

/** One accrual row of a rebate statement (OpenAPI RebateStatementLine). */
export interface RebateStatementLine {
  invoiceLineId: string;
  invoiceDate: string;
  /** Absent on a RETRO_DELTA row, which has no basis of its own. */
  basisValue?: string;
  basisQuantity?: string;
  amount: string;
  status: RebateAccrual["status"];
  retroactive: boolean;
  source: "INVOICE" | "REPLAYED" | "RETRO_DELTA";
}

/** GET /rebates/agreements/{id}/statement (PRD §5.26.15): one period's header, lines and footer. */
export interface RebateStatement {
  agreementId: string;
  name: string;
  partyRole: RebatePartyRole;
  customerId?: string;
  customerGroupId?: string;
  vendorId?: string;
  basis: "NET_AMOUNT" | "QUANTITY";
  currency: string;
  period: string;
  /** The tier in force at the period's cumulative basis; absent below the first threshold. */
  tier?: RebateTier;
  basisValue: string;
  basisQuantity: string;
  targetAmount: string;
  accruedAmount: string;
  /** The sum of every accrual row of the period, listed or not. */
  linesAccruedAmount: string;
  settledAmount: string;
  openAmount: string;
  /** The tier's amount on the whole period, the accrued total and the sum of the rows agree, and the rows' basis adds up. */
  reconciled: boolean;
  lineCount: number;
  linesTruncated: boolean;
  lines: RebateStatementLine[];
}

export function getRebateStatement(id: string, period: string, signal?: AbortSignal): Promise<RebateStatement> {
  return send<RebateStatement>(`/rebates/agreements/${encodeURIComponent(id)}/statement${query({ period })}`, {}, signal);
}

/** A CSV document as the service wrote it, with the file name it suggested. */
export interface CsvDocument {
  csv: string;
  filename: string;
}

async function csvDocument(path: string, fallbackName: string, signal?: AbortSignal): Promise<CsvDocument> {
  const res = await sendRaw(path, { headers: { Accept: "text/csv" } }, signal);
  const csv = await res.text();
  const disposition = /filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") ?? "");
  return { csv, filename: disposition?.[1] ?? fallbackName };
}

/** GET /rebates/agreements/{id}/statement?format=csv - every accrual row of the period (streamed, never cut) and the footer rows. */
export function exportRebateStatement(id: string, period: string, signal?: AbortSignal): Promise<CsvDocument> {
  return csvDocument(`/rebates/agreements/${encodeURIComponent(id)}/statement${query({ period, format: "csv" })}`, `rebate-statement-${id}-${period}.csv`, signal);
}

/** GET /journals/{id}/audit-extract - every line with before and after values; 409 PRC-0064 until the journal is posted. */
export function exportJournalAudit(id: string, signal?: AbortSignal): Promise<CsvDocument> {
  return csvDocument(`/journals/${encodeURIComponent(id)}/audit-extract${query({ format: "csv" })}`, `price-journal-${id}-audit.csv`, signal);
}

export function createRebateAgreement(body: RebateAgreementInput, key: string = newKey()): Promise<RebateAgreement> {
  return post<RebateAgreement>("/rebates/agreements", body, key);
}

/** POST /rebates/agreements/{id}:revise - change the name, end date or (before any accrual) tiers of an ACTIVE agreement. */
export function reviseRebateAgreement(id: string, body: { version: number; name?: string; validTo?: string; tiers?: RebateTier[] }, key: string = newKey()): Promise<RebateAgreement> {
  return post<RebateAgreement>(`/rebates/agreements/${encodeURIComponent(id)}:revise`, body, key);
}

/** POST /rebates/agreements/{id}:close - end an ACTIVE agreement on closeDate (the last day invoices accrue). */
export function closeRebateAgreement(id: string, version: number, closeDate: string, key: string = newKey()): Promise<RebateAgreement> {
  return post<RebateAgreement>(`/rebates/agreements/${encodeURIComponent(id)}:close`, { version, closeDate }, key);
}

/** POST /rebates/agreements/{id}:reopen - make a CLOSED agreement ACTIVE again with a later last day. */
export function reopenRebateAgreement(id: string, version: number, validTo: string, key: string = newKey()): Promise<RebateAgreement> {
  return post<RebateAgreement>(`/rebates/agreements/${encodeURIComponent(id)}:reopen`, { version, validTo }, key);
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

/** One currency of a commission statement footer (OpenAPI CommissionStatement.totals). */
export interface CommissionStatementTotal {
  currency: string;
  lineCount: number;
  earnedAmount: string;
  creditedAmount: string;
  netAmount: string;
}

/** GET /commission/reps/{repId}/statement (PRD §5.26.15): one rep's month, the footer summed over every transaction. */
export interface CommissionStatement {
  legalEntityId: string;
  repId: string;
  period: string;
  lineCount: number;
  linesTruncated: boolean;
  totals: CommissionStatementTotal[];
  lines: CommissionTransaction[];
}

export function getCommissionStatement(filter: { legalEntityId: string; repId: string; period: string }, signal?: AbortSignal): Promise<CommissionStatement> {
  const { repId, ...rest } = filter;
  return send<CommissionStatement>(`/commission/reps/${encodeURIComponent(repId)}/statement${query({ ...rest })}`, {}, signal);
}

/** GET /commission/reps/{repId}/statement?format=csv - every transaction of the month, then the footer rows. */
export function exportCommissionStatement(filter: { legalEntityId: string; repId: string; period: string }, signal?: AbortSignal): Promise<CsvDocument> {
  const { repId, ...rest } = filter;
  return csvDocument(`/commission/reps/${encodeURIComponent(repId)}/statement${query({ ...rest, format: "csv" })}`, `commission-statement-${filter.period}.csv`, signal);
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
  /** The campaign the promotion belongs to (FR-26.10): same legal entity and currency, window inside its validity. */
  campaignId?: string;
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

export interface Promotion extends Omit<PromotionInput, "fundId" | "campaignId"> {
  id: string;
  fundId?: string;
  campaignId?: string;
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

/** GET /promotions - optionally only the plans of one campaign. */
export function listPromotions(legalEntityId = "", signal?: AbortSignal, campaignId = ""): Promise<Promotion[]> {
  return items<Promotion>(`/promotions${query({ legalEntityId, campaignId })}`, signal);
}

export function getPromotion(id: string, signal?: AbortSignal): Promise<Promotion> {
  return send<Promotion>(`/promotions/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /promotions - the fund, when named, must be open and in the same legal entity and currency; the campaign, when named, must be live-able (not killed), in the same entity and currency, and span the window. */
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

/** The terms POST /campaigns/{id}:revise may change; an omitted field stays as it is. */
export interface CampaignRevision {
  name?: string;
  validFrom?: string;
  validTo?: string;
  /** An empty string means every customer. */
  targetCustomerGroup?: string;
  budget?: string;
}

/** POST /campaigns/{id}:revise - change name, validity, target group or budget; the next resolve uses them. */
export function reviseCampaign(id: string, version: number, change: CampaignRevision, key: string = newKey()): Promise<Campaign> {
  return post<Campaign>(`/campaigns/${encodeURIComponent(id)}:revise`, { version, ...change }, key);
}

/** POST /campaigns/{id}:restore - the records rejoin the next resolve inside the campaign validity. */
export function restoreCampaign(id: string, version: number, key: string = newKey()): Promise<Campaign> {
  return post<Campaign>(`/campaigns/${encodeURIComponent(id)}:restore`, { version }, key);
}

// ---- Free goods rules (FR-26.22): free units per N ordered, inclusive or exclusive.

export const FREE_GOODS_KINDS = ["INCLUSIVE", "EXCLUSIVE"] as const;
export type FreeGoodsKind = (typeof FREE_GOODS_KINDS)[number];
export const FREE_GOODS_CALC_RULES = ["PROPORTIONAL", "UNIT_REFERENCE", "WHOLE_UNITS"] as const;
export type FreeGoodsCalcRule = (typeof FREE_GOODS_CALC_RULES)[number];
export type FreeGoodsCustomerKind = "customer" | "customer_group" | "all";

export interface FreeGoodsRuleInput {
  legalEntityId: string;
  customerKind: FreeGoodsCustomerKind;
  /** Customer or customer group id; empty when customerKind is all. */
  customerKey: string;
  productId: string;
  unit: string;
  kind: FreeGoodsKind;
  /** Left out, the legal entity's freeGoodsCalcRule parameter applies (WHOLE_UNITS unless changed). */
  calcRule?: FreeGoodsCalcRule;
  everyQuantity: string;
  freeQuantity: string;
  minQuantity: string;
  validFrom: string;
  validTo: string;
}

export interface FreeGoodsRule extends FreeGoodsRuleInput {
  calcRule: FreeGoodsCalcRule;
  id: string;
  withdrawn: boolean;
  version: number;
}

/** A zero-priced supplementary line proposal that sales adds to the order (FR-27.14). */
export interface FreeGoodsProposal {
  lineNo: number;
  productId: string;
  unit: string;
  ruleId: string;
  kind: FreeGoodsKind;
  orderedQuantity: string;
  pricedQuantity: string;
  freeQuantity: string;
  /** PRC-0014 when the line is already priced at nothing. */
  warning?: string;
}

export function listFreeGoodsRules(filter: { legalEntityId?: string; productId?: string } = {}, signal?: AbortSignal): Promise<FreeGoodsRule[]> {
  return items<FreeGoodsRule>(`/free-goods-rules${query({ legalEntityId: filter.legalEntityId, productId: filter.productId })}`, signal);
}

export function getFreeGoodsRule(id: string, signal?: AbortSignal): Promise<FreeGoodsRule> {
  return send<FreeGoodsRule>(`/free-goods-rules/${encodeURIComponent(id)}`, {}, signal);
}

export function createFreeGoodsRule(body: FreeGoodsRuleInput, key: string = newKey()): Promise<FreeGoodsRule> {
  return post<FreeGoodsRule>("/free-goods-rules", body, key);
}

/** The values a revise replaces; the rule's customers, product, unit and variant never change. */
export interface FreeGoodsRuleRevision {
  calcRule: FreeGoodsCalcRule;
  everyQuantity: string;
  freeQuantity: string;
  minQuantity: string;
  validFrom: string;
  validTo: string;
}

/** POST /free-goods-rules/{id}:revise - the next resolve uses the revised values (FR-26.22). */
export function reviseFreeGoodsRule(id: string, version: number, body: FreeGoodsRuleRevision, key: string = newKey()): Promise<FreeGoodsRule> {
  return post<FreeGoodsRule>(`/free-goods-rules/${encodeURIComponent(id)}:revise`, { version, ...body }, key);
}

/** POST /free-goods-rules/{id}:withdraw - the rule leaves the next resolve. */
export function withdrawFreeGoodsRule(id: string, version: number, key: string = newKey()): Promise<FreeGoodsRule> {
  return post<FreeGoodsRule>(`/free-goods-rules/${encodeURIComponent(id)}:withdraw`, { version }, key);
}

// ---- Commodity pricing (FR-26.21): cost basis types, dated index values, price calculations and runs.

export interface CommodityBasisInput {
  legalEntityId: string;
  /** Capitals, digits, - and _, unique in the legal entity. */
  code: string;
  name: string;
  currency: string;
  unit: string;
}

export interface CommodityBasis extends CommodityBasisInput {
  id: string;
  version: number;
}

export interface CommodityIndexValueInput {
  asOf: string;
  value: string;
  source?: string;
}

export interface CommodityIndexValue extends CommodityIndexValueInput {
  basisId: string;
  /** 1 for the first quote of a day; a correction is the next revision. */
  revision: number;
  /** Quotes inquiry only: ACTIVE price records the basis's live calculations would reprice from this quote. */
  affectedAgreements?: number;
}

export const MARGIN_KINDS = ["PERCENT", "AMOUNT"] as const;
export type MarginKind = (typeof MARGIN_KINDS)[number];

export interface CommodityCalculationInput {
  legalEntityId: string;
  name: string;
  basisId: string;
  factor: string;
  marginKind: MarginKind;
  margin: string;
  rounding: number;
  maxIndexAgeDays: number;
  customerKind: FreeGoodsCustomerKind;
  customerKey: string;
  productId: string;
  unit: string;
  currency: string;
}

export interface CommodityCalculation extends CommodityCalculationInput {
  id: string;
  withdrawn: boolean;
  version: number;
}

export interface CommodityWarning {
  code: string;
  detail: string;
}

export interface CommodityRunPreview {
  calculationId: string;
  asOf: string;
  index: CommodityIndexValue;
  price: string;
  stale: boolean;
  indexAgeDays: number;
  warnings: CommodityWarning[];
  changes: { recordId: string; version: number; currentAmount: string; newAmount: string }[];
  unchangedCount: number;
}

export interface CommodityRun {
  id: string;
  legalEntityId: string;
  calculationId: string;
  asOf: string;
  indexAsOf: string;
  indexRevision: number;
  indexValue: string;
  price: string;
  changedCount: number;
  unchangedCount: number;
  stale: boolean;
  /** The VALIDATED price journal awaiting review; absent when no record changed price. */
  journalId?: string;
  /** The user who ran it; that user cannot post the journal (PRC-0056). */
  runBy: string;
  warnings: CommodityWarning[];
  createdAt: string;
}

const COMMODITY = "/commodity";

export function listCommodityBases(legalEntityId = "", signal?: AbortSignal): Promise<CommodityBasis[]> {
  return items<CommodityBasis>(`${COMMODITY}/bases${query({ legalEntityId })}`, signal);
}

export function getCommodityBasis(id: string, signal?: AbortSignal): Promise<CommodityBasis> {
  return send<CommodityBasis>(`${COMMODITY}/bases/${encodeURIComponent(id)}`, {}, signal);
}

export function createCommodityBasis(body: CommodityBasisInput, key: string = newKey()): Promise<CommodityBasis> {
  return post<CommodityBasis>(`${COMMODITY}/bases`, body, key);
}

export function listCommodityIndexValues(basisId: string, signal?: AbortSignal): Promise<CommodityIndexValue[]> {
  return items<CommodityIndexValue>(`${COMMODITY}/bases/${encodeURIComponent(basisId)}/index-values`, signal);
}

export function recordCommodityIndexValue(basisId: string, body: CommodityIndexValueInput, key: string = newKey()): Promise<CommodityIndexValue> {
  return post<CommodityIndexValue>(`${COMMODITY}/bases/${encodeURIComponent(basisId)}/index-values`, body, key);
}

export function listCommodityCalculations(filter: { legalEntityId?: string; basisId?: string } = {}, signal?: AbortSignal): Promise<CommodityCalculation[]> {
  return items<CommodityCalculation>(`${COMMODITY}/calculations${query({ legalEntityId: filter.legalEntityId, basisId: filter.basisId })}`, signal);
}

export function getCommodityCalculation(id: string, signal?: AbortSignal): Promise<CommodityCalculation> {
  return send<CommodityCalculation>(`${COMMODITY}/calculations/${encodeURIComponent(id)}`, {}, signal);
}

export function createCommodityCalculation(body: CommodityCalculationInput, key: string = newKey()): Promise<CommodityCalculation> {
  return post<CommodityCalculation>(`${COMMODITY}/calculations`, body, key);
}

export function withdrawCommodityCalculation(id: string, version: number, key: string = newKey()): Promise<CommodityCalculation> {
  return post<CommodityCalculation>(`${COMMODITY}/calculations/${encodeURIComponent(id)}:withdraw`, { version }, key);
}

/** POST :preview - plans the run and writes nothing. */
export function previewCommodityRun(id: string, asOf: string): Promise<CommodityRunPreview> {
  return post<CommodityRunPreview>(`${COMMODITY}/calculations/${encodeURIComponent(id)}:preview`, { asOf });
}

/** POST :run - creates the price journal that stays VALIDATED for review; posting it is a separate act. */
export function runCommodityCalculation(id: string, asOf: string, key: string = newKey()): Promise<CommodityRun> {
  return post<CommodityRun>(`${COMMODITY}/calculations/${encodeURIComponent(id)}:run`, { asOf }, key);
}

export function listCommodityRuns(calculationId: string, signal?: AbortSignal): Promise<CommodityRun[]> {
  return items<CommodityRun>(`${COMMODITY}/calculations/${encodeURIComponent(calculationId)}/runs`, signal);
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

// ---- Workspace tiles, record expiry, price journal CSV import and commission settlement export.

/** Rebate accrued on invoices dated in the month, per party role and currency (never added across them). */
export interface AccrualTotal {
  partyRole: RebatePartyRole;
  currency: string;
  amount: string;
}

/** GET /workspace (PRD §5.26.6): exact tile counts for one legal entity or every entity in scope. */
export interface PricingWorkspace {
  legalEntityId?: string;
  asOf: string;
  month: string;
  activeRecords: number;
  draftRecords: number;
  expiringIn30Days: number;
  journalsToPost: number;
  liveCampaigns: number;
  claimsAwaitingApproval: number;
  accrualsThisMonth: AccrualTotal[];
}

export function getPricingWorkspace(filter: { legalEntityId?: string; asOf?: string } = {}, signal?: AbortSignal): Promise<PricingWorkspace> {
  return send<PricingWorkspace>(`/workspace${query({ ...filter })}`, {}, signal);
}

export interface ExpiryResult {
  legalEntityId: string;
  asOf: string;
  expired: number;
  recordIds: string[];
}

/** POST /condition-records:expire - the nightly expiry job (BR-26.9) run on demand; a rerun expires nothing more. */
export function expireConditionRecords(legalEntityId: string, asOf: string): Promise<ExpiryResult> {
  return post<ExpiryResult>("/condition-records:expire", { legalEntityId, asOf });
}

async function sendRaw(path: string, init: RequestInit, signal?: AbortSignal): Promise<Response> {
  const res = await fetch(`${base}${path}`, { ...init, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return res;
}

/**
 * POST /journals:import-csv - creates an OPEN journal from a CSV file (FR-26.9). Row faults come
 * back keyed rows[n].column (n = data row from 1); journal faults keep the JSON keys lines[i].field.
 */
export async function importPriceJournalCsv(
  header: { legalEntityId: string; name: string; smartRound: boolean },
  csv: string,
  key: string = newKey(),
): Promise<Journal> {
  const res = await sendRaw(`/journals:import-csv${query({ legalEntityId: header.legalEntityId, name: header.name, smartRound: header.smartRound ? "true" : "" })}`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "text/csv", "Idempotency-Key": key },
    body: csv,
  });
  return (await res.json()) as Journal;
}

export const SETTLEMENT_TARGETS = ["AP", "PAYROLL"] as const;
export type SettlementTarget = (typeof SETTLEMENT_TARGETS)[number];

export interface SettlementRow {
  repId: string;
  currency: string;
  amount: string;
  transactions: number;
}

export interface SettlementExport {
  /** The file exactly as the service wrote it, for download. */
  csv: string;
  filename: string;
  rows: SettlementRow[];
}

/** Splits RFC 4180 CSV text into records (quoted fields, doubled quotes, CRLF or LF). */
export function parseCsv(text: string): string[][] {
  const records: string[][] = [];
  let record: string[] = [];
  let field = "";
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (quoted) {
      if (ch === '"' && text[i + 1] === '"') {
        field += '"';
        i++;
      } else if (ch === '"') {
        quoted = false;
      } else {
        field += ch;
      }
    } else if (ch === '"') {
      quoted = true;
    } else if (ch === ",") {
      record.push(field);
      field = "";
    } else if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && text[i + 1] === "\n") i++;
      record.push(field);
      records.push(record);
      record = [];
      field = "";
    } else {
      field += ch;
    }
  }
  if (field !== "" || record.length > 0) {
    record.push(field);
    records.push(record);
  }
  return records;
}

/**
 * GET /commission/settlement-export (FR-26.18): commission per rep and currency for invoices dated
 * from..to, as the CSV payroll (w43) or AP (w19) reads, plus the parsed rows for a preview.
 */
export async function exportCommissionSettlement(
  filter: { legalEntityId: string; from: string; to: string; target: SettlementTarget },
  signal?: AbortSignal,
): Promise<SettlementExport> {
  const res = await sendRaw(`/commission/settlement-export${query({ ...filter })}`, { headers: { Accept: "text/csv" } }, signal);
  const csv = await res.text();
  const [head, ...data] = parseCsv(csv);
  const col = (name: string) => head?.indexOf(name) ?? -1;
  const [rep, currency, amount, count] = [col("rep_id"), col("currency"), col("amount"), col("transactions")];
  if (rep < 0 || currency < 0 || amount < 0 || count < 0) {
    throw new PricingApiError(0, "COM-0001", "The settlement file has an unexpected header");
  }
  const rows = data.map((r) => ({ repId: r[rep] ?? "", currency: r[currency] ?? "", amount: r[amount] ?? "", transactions: Number(r[count] ?? 0) }));
  const disposition = /filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") ?? "");
  return { csv, filename: disposition?.[1] ?? `commission-settlement-${filter.target}-${filter.from}-${filter.to}.csv`, rows };
}

// ---- Recorded commission settlement exports (FR-26.18, PRD 5.26.15).

export type SettlementExportStatus = "ISSUED" | "VOIDED";

export interface SettlementExportTotal {
  currency: string;
  amount: string;
}

/** One recorded hand-over of a period's commission to AP or payroll; lines are frozen at record time. */
export interface SettlementExportRecord {
  id: string;
  legalEntityId: string;
  target: SettlementTarget;
  from: string;
  to: string;
  status: SettlementExportStatus;
  lineCount: number;
  totals: SettlementExportTotal[];
  csvSha256: string;
  exportedBy: string;
  voidReason: string;
  voidedBy: string;
  version: number;
  createdAt: string;
  /** Present on create, read and void; absent in the list. */
  lines?: SettlementRow[];
}

export interface SettlementExportFilter {
  legalEntityId?: string;
  target?: SettlementTarget | "";
  status?: SettlementExportStatus | "";
}

/** POST /commission/settlement-exports - 409 PRC-0065 when the period overlaps an ISSUED export; 422 PRC-0066 when it holds no commission. */
export function recordCommissionSettlementExport(
  input: { legalEntityId: string; from: string; to: string; target: SettlementTarget },
  key: string = newKey(),
): Promise<SettlementExportRecord> {
  return post<SettlementExportRecord>("/commission/settlement-exports", input, key);
}

/** GET /commission/settlement-exports - newest period first, at most 100. */
export function listCommissionSettlementExports(filter: SettlementExportFilter = {}, signal?: AbortSignal): Promise<SettlementExportRecord[]> {
  return items<SettlementExportRecord>(`/commission/settlement-exports${query({ ...filter })}`, signal);
}

export function getCommissionSettlementExport(id: string, signal?: AbortSignal): Promise<SettlementExportRecord> {
  return send<SettlementExportRecord>(`/commission/settlement-exports/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /commission/settlement-exports/{id}:void - the reason is required; the period can then be exported again. */
export function voidCommissionSettlementExport(id: string, version: number, reason: string, key: string = newKey()): Promise<SettlementExportRecord> {
  return post<SettlementExportRecord>(`/commission/settlement-exports/${encodeURIComponent(id)}:void`, { version, reason }, key);
}

/** GET /commission/settlement-exports/{id}/file - the CSV built from the frozen rows, with the service's digest. */
export async function downloadCommissionSettlementExport(id: string, signal?: AbortSignal): Promise<{ csv: string; filename: string; sha256: string }> {
  const res = await sendRaw(`/commission/settlement-exports/${encodeURIComponent(id)}/file`, { headers: { Accept: "text/csv" } }, signal);
  const disposition = /filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") ?? "");
  return { csv: await res.text(), filename: disposition?.[1] ?? "commission-settlement.csv", sha256: res.headers.get("X-Content-SHA256") ?? "" };
}

// ---- Pricing parameters per legal entity (PRD 5.26.13).

export interface PricingParameters {
  legalEntityId: string;
  /** When true, the user who created a price journal cannot post it (PRC-0057). */
  journalFourEyes: boolean;
  /** Document date that prices a resolve sent without a pricingDate (BR-26.2). */
  pricingDateBasis: PricingDateBasis;
  /** What TOTAL_DISC applies to: the line nets, or the line amounts at the unit price. */
  totalDiscountBasis: TotalDiscountBasis;
  /** Smart rounding table of journal adjustments, lowest band first; the last band has no upTo. */
  smartRoundingBands: SmartRoundingBand[];
  /** Default of searchNext on a new condition record that leaves it out (BR-26.1 step 5). */
  priceSearchMode?: PriceSearchMode;
  /** SURCHARGE threshold per currency (FR-26.6); a currency not named keeps the reference threshold (250 USD, 200 EUR). */
  smallOrderThresholds?: Record<string, string>;
  /** Calculation rule a new free goods rule takes when its create request names none (BR-26.13); WHOLE_UNITS by default. */
  freeGoodsCalcRule?: FreeGoodsCalcRule;
  /** 0 when the legal entity has no stored parameters. */
  version: number;
  updatedBy?: string;
  updatedAt?: string;
}

export type PriceSearchMode = "FIRST_MATCH" | "SEARCH_NEXT";
export type PricingDateBasis = "SHIP_DATE" | "ORDER_DATE" | "DELIVERY_DATE";
export type TotalDiscountBasis = "AFTER_LINE_DISC" | "GROSS";
export interface SmartRoundingBand {
  upTo?: string;
  ending: string;
}

export function getPricingParameters(legalEntityId: string, signal?: AbortSignal): Promise<PricingParameters> {
  return send<PricingParameters>(`/parameters${query({ legalEntityId })}`, {}, signal);
}

export type PricingParametersInput = Omit<PricingParameters, "updatedBy" | "updatedAt" | "priceSearchMode" | "smallOrderThresholds" | "freeGoodsCalcRule"> & {
  priceSearchMode: PriceSearchMode;
  freeGoodsCalcRule: FreeGoodsCalcRule;
  smallOrderThresholds: Record<string, string>;
};

export function setPricingParameters(body: PricingParametersInput, key: string = newKey()): Promise<PricingParameters> {
  return send<PricingParameters>("/parameters", { method: "PUT", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) });
}

// ---- Pricing parameters per tenant (PRD 5.26.13, scope tenant).

export type CampaignKillSwitchEffect = "NEXT_RESOLVE" | "IMMEDIATE_CACHE_FLUSH";

export interface PricingTenantParameters {
  /** Lifetime of the tenant's record snapshots, 10 to 600 seconds; 0 follows the deployment. */
  cacheTtlSeconds: number;
  campaignKillSwitchEffect: CampaignKillSwitchEffect;
  /** 0 when the tenant has no stored parameters. */
  version: number;
  updatedBy?: string;
  updatedAt?: string;
}

export function getPricingTenantParameters(signal?: AbortSignal): Promise<PricingTenantParameters> {
  return send<PricingTenantParameters>("/tenant-parameters", {}, signal);
}

export type PricingTenantParametersInput = Omit<PricingTenantParameters, "updatedBy" | "updatedAt">;

export function setPricingTenantParameters(body: PricingTenantParametersInput, key: string = newKey()): Promise<PricingTenantParameters> {
  return send<PricingTenantParameters>("/tenant-parameters", { method: "PUT", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) });
}

// ---- Procedure determination (PRD 5.26.13, reference_procedure): which procedure each sales channel prices with.

export interface ProcedureAssignment {
  legalEntityId: string;
  /** Empty for the legal entity's default. */
  salesChannel: string;
  procedureName: string;
  version: number;
  updatedBy?: string;
  updatedAt?: string;
}

export function listProcedureAssignments(legalEntityId: string, signal?: AbortSignal): Promise<ProcedureAssignment[]> {
  return items<ProcedureAssignment>(`/procedure-assignments${query({ legalEntityId })}`, signal);
}

/** PUT /procedure-assignments - version is the one read, 0 when the channel has no assignment. */
export function setProcedureAssignment(
  body: { legalEntityId: string; salesChannel: string; procedureName: string; version: number },
  key: string = newKey(),
): Promise<ProcedureAssignment> {
  return send<ProcedureAssignment>("/procedure-assignments", { method: "PUT", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) });
}

/** DELETE /procedure-assignments - removes the assignment at the version read (204). */
export async function clearProcedureAssignment(legalEntityId: string, salesChannel: string, version: number, key: string = newKey()): Promise<void> {
  await sendRaw(`/procedure-assignments${query({ legalEntityId, salesChannel, version })}`, { method: "DELETE", headers: { Accept: "application/json", "Idempotency-Key": key } });
}

// ---- Broker contracts and royalty agreements (FR-26.16, FR-26.17).

export type ContractKind = "BROKER" | "ROYALTY";
export type ContractStatus = "ACTIVE" | "CLOSED";
export type BrokerRateKind = "PERCENT_NET" | "PER_UNIT";
export type RoyaltyBasis = "NET_SALES" | "QUANTITY";
export type AccrualKind = "ACCRUAL" | "CREDIT" | "SHORTFALL";

export interface BrokerContractInput {
  legalEntityId: string;
  name: string;
  brokerVendorId: string;
  customerId?: string;
  customerGroupId?: string;
  productId?: string;
  productGroupId?: string;
  rateKind: BrokerRateKind;
  rate: string;
  currency: string;
  validFrom: string;
  validTo: string;
  payableAccount?: string;
  expenseAccount?: string;
}

export interface BrokerContract extends BrokerContractInput {
  id: string;
  payableAccount: string;
  expenseAccount: string;
  status: ContractStatus;
  version: number;
}

export interface RoyaltyAgreementInput {
  legalEntityId: string;
  name: string;
  licensorVendorId: string;
  productId?: string;
  productGroupId?: string;
  basis: RoyaltyBasis;
  period: RebatePeriod;
  tiers: RebateTier[];
  minimumRoyalty?: string;
  currency: string;
  validFrom: string;
  validTo: string;
  payableAccount?: string;
  expenseAccount?: string;
}

export interface RoyaltyAgreement extends RoyaltyAgreementInput {
  id: string;
  minimumRoyalty: string;
  payableAccount: string;
  expenseAccount: string;
  status: ContractStatus;
  version: number;
}

export interface ContractFilter {
  legalEntityId?: string;
  status?: ContractStatus | "";
}

/** One accrual row of a contract; amounts are negative on a credit note line. */
export interface ContractAccrual {
  id: string;
  contractKind: ContractKind;
  contractId: string;
  periodId: string;
  lineKey: string;
  kind: AccrualKind;
  invoiceDate: string;
  basisValue: string;
  basisQuantity: string;
  amount: string;
  currency: string;
  reversesLineKey?: string;
}

export interface ContractClaimPreview {
  contractKind: ContractKind;
  contractId: string;
  vendorId: string;
  periodId: string;
  accrued: string;
  shortfall: string;
  priorClaimed: string;
  /** accrued + shortfall - priorClaimed. */
  amount: string;
  currency: string;
  claims: number;
  /** A royalty period can be claimed only when closed. */
  closed: boolean;
}

export interface ContractClaim {
  id: string;
  number: string;
  legalEntityId: string;
  contractKind: ContractKind;
  contractId: string;
  vendorId: string;
  periodId: string;
  accruedTotal: string;
  priorClaimed: string;
  amount: string;
  currency: string;
  status: ClaimStatus;
  version: number;
  createdBy?: string;
  approvedBy?: string;
  createdAt: string;
  approvedAt?: string;
}

export interface ContractClaimFilter {
  contractKind?: ContractKind | "";
  contractId?: string;
  vendorId?: string;
  period?: string;
  status?: ClaimStatus | "";
}

export interface RoyaltyPeriodClose {
  agreementId: string;
  periodId: string;
  accrued: string;
  minimumRoyalty: string;
  shortfall: string;
  currency: string;
  alreadyClosed: boolean;
  /** PRC-0017 when a shortfall posted at close. */
  notice?: string;
  accrual?: ContractAccrual;
}

const brokerPath = "/broker-contracts";
const royaltyPath = "/royalty-agreements";
export const contractPath = (kind: ContractKind) => (kind === "BROKER" ? brokerPath : royaltyPath);

export function listBrokerContracts(filter: ContractFilter = {}, signal?: AbortSignal): Promise<BrokerContract[]> {
  return items<BrokerContract>(`${brokerPath}${query({ ...filter })}`, signal);
}

export function getBrokerContract(id: string, signal?: AbortSignal): Promise<BrokerContract> {
  return send<BrokerContract>(`${brokerPath}/${encodeURIComponent(id)}`, {}, signal);
}

export function createBrokerContract(body: BrokerContractInput, key: string = newKey()): Promise<BrokerContract> {
  return post<BrokerContract>(brokerPath, body, key);
}

/** POST /broker-contracts/{id}:close at the contract version; a closed contract takes no more accruals. */
export function closeBrokerContract(id: string, version: number, key: string = newKey()): Promise<BrokerContract> {
  return post<BrokerContract>(`${brokerPath}/${encodeURIComponent(id)}:close`, { version }, key);
}

export function listRoyaltyAgreements(filter: ContractFilter = {}, signal?: AbortSignal): Promise<RoyaltyAgreement[]> {
  return items<RoyaltyAgreement>(`${royaltyPath}${query({ ...filter })}`, signal);
}

export function getRoyaltyAgreement(id: string, signal?: AbortSignal): Promise<RoyaltyAgreement> {
  return send<RoyaltyAgreement>(`${royaltyPath}/${encodeURIComponent(id)}`, {}, signal);
}

export function createRoyaltyAgreement(body: RoyaltyAgreementInput, key: string = newKey()): Promise<RoyaltyAgreement> {
  return post<RoyaltyAgreement>(royaltyPath, body, key);
}

export function closeRoyaltyAgreement(id: string, version: number, key: string = newKey()): Promise<RoyaltyAgreement> {
  return post<RoyaltyAgreement>(`${royaltyPath}/${encodeURIComponent(id)}:close`, { version }, key);
}

/** GET /{broker-contracts|royalty-agreements}/{id}/accruals - the accrual rows of a contract, optionally for one period. */
export function listContractAccruals(kind: ContractKind, id: string, period = "", signal?: AbortSignal): Promise<ContractAccrual[]> {
  return items<ContractAccrual>(`${contractPath(kind)}/${encodeURIComponent(id)}/accruals${query({ period })}`, signal);
}

/** GET .../claim-preview - the claim a period would produce; writes nothing. */
export function previewContractClaim(kind: ContractKind, id: string, period: string, signal?: AbortSignal): Promise<ContractClaimPreview> {
  return send<ContractClaimPreview>(`${contractPath(kind)}/${encodeURIComponent(id)}/claim-preview${query({ period })}`, {}, signal);
}

/** POST /royalty-agreements/{id}:close-period - posts the minimum royalty shortfall, once. */
export function closeRoyaltyPeriod(id: string, period: string): Promise<RoyaltyPeriodClose> {
  return post<RoyaltyPeriodClose>(`${royaltyPath}/${encodeURIComponent(id)}:close-period`, { period });
}

/** POST /contract-claims - claim one period (BC-n or RY-n); 409 PRC-0015 when it is fully claimed. */
export function createContractClaim(body: { contractKind: ContractKind; contractId: string; period: string }, key: string = newKey()): Promise<ContractClaim> {
  return post<ContractClaim>("/contract-claims", body, key);
}

/** GET /contract-claims - the Broker and royalty claims inquiry, newest first, at most 100. */
export function listContractClaims(filter: ContractClaimFilter = {}, signal?: AbortSignal): Promise<ContractClaim[]> {
  return items<ContractClaim>(`/contract-claims${query({ ...filter })}`, signal);
}

export function getContractClaim(id: string, signal?: AbortSignal): Promise<ContractClaim> {
  return send<ContractClaim>(`/contract-claims/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /contract-claims/{id}:approve at the claim version; the claim's creator cannot approve it (PRC-0046). */
export function approveContractClaim(id: string, version: number, key: string = newKey()): Promise<ContractClaim> {
  return post<ContractClaim>(`/contract-claims/${encodeURIComponent(id)}:approve`, { version }, key);
}
