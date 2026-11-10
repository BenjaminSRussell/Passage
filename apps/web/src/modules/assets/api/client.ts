import { apiFetch } from "../../../shell/api/http";
// Client for the assets service (services/assets/api/openapi.yaml).
// Browser calls use /api/v1/..., which the web BFF rewrites to /v1/....

export const base = "/api/v1";

/** Decimal values travel as strings (never floats). */
export type Decimal = string;

export const ASSET_STATUSES = ["NotYetAcquired", "Open", "Sold", "Scrapped", "Transferred"] as const;
export type AssetStatus = (typeof ASSET_STATUSES)[number];

export const METHODS = ["StraightLine", "DecliningBalance"] as const;
export type Method = (typeof METHODS)[number];

/** Conventions each method accepts (services/assets/internal/domain/depreciation.go). */
export const CONVENTIONS: Record<Method, readonly string[]> = {
  StraightLine: ["None", "FullMonth", "MidMonth15"],
  DecliningBalance: ["None", "HalfYear", "MidQuarter"],
};

export const PAYMENTS_PER_YEAR = [1, 2, 4, 12] as const;

export interface Asset {
  id: string;
  legalEntityId: string;
  assetNumber: string;
  name: string;
  classCode: string;
  barcode: string | null;
  quantity: Decimal;
  status: AssetStatus;
  acquiredDate: string | null;
  bookCount: number;
  version: number;
  createdAt: string;
}

export interface Book {
  id: string;
  assetId: string;
  legalEntityId: string;
  bookCode: string;
  currency: string;
  method: Method;
  factor: Decimal;
  switchToStraightLine: boolean;
  serviceLifePeriods: number;
  salvageValue: Decimal;
  convention: string;
  inServiceDate: string;
  cost: Decimal;
  accumulatedDepreciation: Decimal;
  nbv: Decimal;
  status: "Open" | "Sold" | "Scrapped" | "Transferred";
  /** Last period a posted depreciation run charged ("2026-03" or "2026"). */
  lastDepreciatedPeriod?: string | null;
  /** The group asset that depreciates this book (FR-37.16); set on the asset read. */
  groupAssetId?: string;
  groupCode?: string;
}

export interface AssetDetail extends Asset {
  books: Book[];
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export interface ScheduleLine {
  index: number;
  period: string;
  amount: Decimal;
  accumulated: Decimal;
  nbv: Decimal;
}

export interface Schedule {
  bookId: string;
  bookCode: string;
  currency: string;
  total: Decimal;
  lines: ScheduleLine[];
}

export interface Transaction {
  id: string;
  assetBookId: string;
  bookCode: string;
  currency: string;
  type: TransactionType;
  transactionDate: string;
  costAmount: Decimal;
  accumulatedAmount: Decimal;
  proceedsAmount: Decimal | null;
  gainLoss: Decimal | null;
  impairmentAmount?: Decimal;
  sourceType: string;
  sourceId?: string | null;
  runId?: string | null;
  createdAt: string;
}

export type TransactionType =
  | "Acquisition"
  | "AcquisitionAdj"
  | "Depreciation"
  | "DepreciationAdj"
  | "Revaluation"
  | "Impairment"
  | "DisposalSale"
  | "DisposalScrap"
  | "Transfer"
  | "Split";

export const ADJUSTMENT_TYPES = ["AcquisitionAdj", "DepreciationAdj", "Revaluation", "Impairment"] as const;
export type AdjustmentType = (typeof ADJUSTMENT_TYPES)[number];

export interface AdjustInput {
  bookId: string;
  type: AdjustmentType;
  transactionDate: string;
  /** Signed: a negative Impairment is a reversal. */
  amount: Decimal;
}

export interface AdjustResult {
  book: Book;
  transaction: Transaction;
  /** An impairment reversal was capped (AST-0512). */
  limited: boolean;
  requested: Decimal;
  warnings: string[];
}

export const ADDITION_TYPES = ["Improvement", "Component", "Accessory"] as const;
export type AdditionType = (typeof ADDITION_TYPES)[number];

export interface NewAddition {
  type: AdditionType;
  description: string;
  quantity?: Decimal;
  serialNumber?: string;
  addedDate: string;
  financial: boolean;
  cost?: Decimal;
  currency?: string;
  bookIds?: string[];
}

export interface Addition {
  id: string;
  assetId: string;
  type: AdditionType;
  description: string;
  quantity: Decimal;
  serialNumber: string | null;
  addedDate: string;
  financial: boolean;
  cost: Decimal;
  currency: string | null;
  createdAt: string;
  transactions: Transaction[];
}

export interface RunInput {
  legalEntityId: string;
  bookCode: string;
  /** "2026-03" for monthly (straight-line) books, "2026" for yearly ones. */
  period: string;
}

export interface RunLine {
  assetBookId: string;
  assetId: string;
  assetNumber: string;
  status: "Due" | "Posted" | "Exception";
  amount: Decimal;
  scheduledAmount: Decimal;
  exceptionCode: string | null;
  detail: string | null;
}

export interface DepreciationRun {
  id: string;
  legalEntityId: string;
  bookCode: string;
  period: string;
  currency: string | null;
  status: "Proposed" | "Posted";
  assetCount: number;
  exceptionCount: number;
  totalAmount: Decimal;
  journalEntryId: string | null;
  version: number;
  createdAt: string;
  postedAt: string | null;
  lines?: RunLine[];
}

export interface IndexPoint {
  year: number;
  value: Decimal;
}

export interface IndexSeries {
  code: string;
  country: string;
  classCode: string;
  values: IndexPoint[];
  updatedAt: string;
}

export interface IndexSeriesInput {
  country: string;
  classCode: string;
  values: IndexPoint[];
}

export const INSURANCE_TYPES = ["Property", "MachineryBreakdown", "Transit", "Fleet"] as const;
export const INSURANCE_BASES = ["Replacement", "NBV", "Agreed"] as const;

export interface NewPolicy {
  insurerPartyId: string;
  policyNumber: string;
  insuranceType: (typeof INSURANCE_TYPES)[number];
  coverageFrom: string;
  coverageTo: string;
  premium?: Decimal;
  currency: string;
  basis: (typeof INSURANCE_BASES)[number];
  baseValue: Decimal;
  sumInsured: Decimal;
  indexSeriesCode?: string;
  indexBaseYear?: number;
  valuationYear?: number;
}

export interface InsurancePolicy extends Required<Omit<NewPolicy, "indexSeriesCode" | "indexBaseYear" | "valuationYear">> {
  id: string;
  assetId: string;
  legalEntityId: string;
  indexSeriesCode: string | null;
  indexBaseYear: number | null;
  valuationYear: number | null;
  indexYearUsed: number | null;
  indexedValue: Decimal | null;
  /** Sum insured below 90% of the indexed value (AST-0532). */
  underInsured: boolean;
  warnings: string[];
  createdAt: string;
}

export const CAPEX_STATUSES = ["Draft", "Submitted", "InApproval", "Approved", "Released", "Closed", "Rejected", "Postponed"] as const;
export type CapexStatus = (typeof CAPEX_STATUSES)[number];

export interface NewCapexVariant {
  description: string;
  amountsByYear: Record<string, Decimal>;
  inServiceDate: string;
  profileAssumption: string;
}

export interface CapexVariant extends NewCapexVariant {
  id: string;
  position: number;
  totalAmount: Decimal;
}

export interface NewCapexRequest {
  legalEntityId: string;
  number: string;
  title: string;
  requesterId: string;
  currency: string;
  variants: NewCapexVariant[];
}

export interface CapexRequest {
  id: string;
  legalEntityId: string;
  number: string;
  title: string;
  requesterId: string;
  status: CapexStatus;
  currency: string;
  selectedVariantId: string | null;
  approvalLevel: "PlantManager" | "CFO" | "Board" | null;
  requestedAmount: Decimal | null;
  approvedAmount: Decimal | null;
  /** User that created the request; that user may not approve, reject or postpone it. */
  createdBy?: string | null;
  version: number;
  createdAt: string;
  variants?: CapexVariant[];
  /** Decision trail, oldest first (read and decisions only). */
  decisions?: CapexDecisionRecord[];
}

/** Decisions on a submitted request (BR-37.12); each is its own POST route. */
export const CAPEX_DECISIONS = ["approve", "reject", "postpone", "reopen", "release", "close"] as const;
export type CapexDecision = (typeof CAPEX_DECISIONS)[number];

export interface CapexDecisionRecord {
  id: string;
  decision: CapexDecision;
  fromStatus: CapexStatus;
  toStatus: CapexStatus;
  approvalLevel: "PlantManager" | "CFO" | "Board" | null;
  amount: Decimal | null;
  comment: string | null;
  decidedBy: string;
  decidedAt: string;
}

export interface MidQuarterBook {
  assetId: string;
  assetNumber: string;
  bookId: string;
  method: Method;
  convention: string;
  inServiceDate: string;
  quarter: number;
  cost: Decimal;
}

/** BR-37.5 mid-quarter test for one tax book code and year. */
export interface MidQuarterReport {
  legalEntityId: string;
  bookCode: string;
  currency: string | null;
  year: number;
  bookCount: number;
  totalBasis: Decimal;
  quarterBasis: [Decimal, Decimal, Decimal, Decimal];
  q4Share: Decimal;
  applies: boolean;
  warnings: string[];
  reviewCount: number;
  review: MidQuarterBook[];
}

export interface MidQuarterFilter {
  legalEntityId: string;
  bookCode: string;
  year: number;
}

export interface Disposal {
  asset: Asset;
  books: Book[];
  transactions: Transaction[];
}

export interface LeaseLine {
  periodNo: number;
  opening: Decimal;
  /** Modification adjustment added to the opening before the period's payment and interest (BR-37.8). */
  remeasurement?: Decimal;
  interest: Decimal;
  payment: Decimal;
  closing: Decimal;
  rouDepreciation: Decimal;
}

export interface Lease {
  id: string;
  legalEntityId: string;
  leaseNumber: string;
  commencementDate: string;
  termMonths: number;
  paymentsPerYear: number;
  paymentAmount: Decimal;
  paymentTiming: "arrears" | "advance";
  ibrRate: Decimal;
  rateMethod?: RateMethod;
  /** Recognition exemption; a lease under an election has a liability and ROU asset of 0. */
  election?: LeaseElection;
  /** The lease contains a purchase option, which rules out the short-term election. */
  purchaseOption?: boolean;
  /** Value of the underlying asset when new, for a low-value election, in underlyingAssetCurrency. */
  underlyingAssetValue?: Decimal;
  underlyingAssetCurrency?: LowValueCurrency;
  currency: string;
  /** Liability at initial recognition. */
  liability: Decimal;
  /** ROU asset at initial recognition. */
  rouAsset: Decimal;
  status: "Draft" | "Active" | "Modified" | "Terminated";
  version: number;
  createdAt: string;
  schedule?: LeaseLine[];
  modifications?: LeaseModification[];
  /** Set once the lease was terminated early (D4c). */
  termination?: LeaseTermination;
}

/** The derecognition of an early-terminated lease; gainLoss is positive for a gain. journalId is null until booked. */
export interface LeaseTermination {
  id: string;
  leaseId: string;
  afterPeriod: number;
  terminationDate: string;
  reason: string;
  currency: string;
  liabilityDerecognized: Decimal;
  rouGross: Decimal;
  rouAccumulated: Decimal;
  rouCarrying: Decimal;
  terminationPayment: Decimal;
  gainLoss: Decimal;
  journalId: string | null;
  createdBy: string;
  createdAt: string;
}

export interface TerminateLeaseInput {
  afterPeriod: number;
  terminationPayment?: Decimal;
  reason?: string;
}

export interface LeaseTerminationResult {
  termination: LeaseTermination;
  lease: Lease;
  /** The AST-0036 info message. */
  messages: string[];
}

/** Which journals of a lease are in the general ledger (D4d); null means not booked yet. */
export interface LeasePostings {
  leaseId: string;
  recognitionJournalId: string | null;
  terminationJournalId?: string | null;
  periods: { periodNo: number; periodEnd: string; postedJournalId: string | null }[];
}

export interface LeasePostInput {
  legalEntityId: string;
  ledgerId: string;
  postThrough: string;
  leaseId?: string;
}

/** Period 0 is the recognition journal at commencement. */
export interface LeasePostResult {
  booked: { leaseId: string; leaseNumber: string; period: number; entryId: string; lines: number; placeholderProfile?: boolean }[];
  failed: { leaseId: string; leaseNumber: string; period: number; code: string }[];
  more: boolean;
}

/** Accounts of the lease journals of a legal entity; placeholder is true until the accountant saves a profile. */
export interface LeasePostingProfile {
  legalEntityId: string;
  rouAssetAccount: string;
  rouAccumulatedDepreciationAccount: string;
  liabilityAccount: string;
  interestExpenseAccount: string;
  depreciationExpenseAccount: string;
  paymentClearingAccount: string;
  remeasurementGainAccount: string;
  /** Gain (credit) or loss (debit) of an early termination. */
  terminationGainLossAccount: string;
  /** Debited with each payment of a lease under a short-term or low-value election. */
  exemptLeaseExpenseAccount: string;
  version?: number;
  placeholder?: boolean;
}

/** IFRS 16.5-8 recognition exemptions: None recognizes the lease, the others expense its payments. */
export const LEASE_ELECTIONS = ["None", "ShortTerm", "LowValue"] as const;
export type LeaseElection = (typeof LEASE_ELECTIONS)[number];

/** lease.lowValueThreshold (domain.LowValueThresholds): the most an underlying asset may be worth when new, by the currency that value is stated in. */
export const LOW_VALUE_THRESHOLDS = { USD: 5000, EUR: 5000 } as const;
export type LowValueCurrency = keyof typeof LOW_VALUE_THRESHOLDS;

export const RATE_METHODS = ["Nominal", "Effective"] as const;
export type RateMethod = (typeof RATE_METHODS)[number];

/** One remeasurement of a lease (FR-37.10, BR-37.8). */
export interface LeaseModification {
  id: string;
  leaseId: string;
  effectivePeriod: number;
  termMonths: number;
  paymentAmount: Decimal;
  ibrRate: Decimal;
  scopeChange: boolean;
  liabilityBefore: Decimal;
  liabilityAfter: Decimal;
  adjustment: Decimal;
  rouBefore: Decimal;
  rouAfter: Decimal;
  rouAdjustment: Decimal;
  profitLossExcess: Decimal;
  createdAt: string;
  /** A later modification taking effect in an earlier period replaced this one's schedule; its adjustments are not booked. */
  superseded?: boolean;
}

export interface ModifyLeaseInput {
  effectivePeriod: number;
  paymentAmount: Decimal;
  /** Revised full term; only with scopeChange. */
  termMonths?: number;
  scopeChange: boolean;
  /** Revised rate; required with scopeChange, refused without. */
  ibrRate?: Decimal;
}

export interface LeaseModificationResult {
  modification: LeaseModification;
  lease: Lease;
  /** The AST-0516 info message. */
  messages: string[];
}

export type CountOutcome = "Found" | "Missing" | "Unknown";

export interface CountLine {
  barcode: string;
  outcome: CountOutcome;
  assetId: string | null;
  assetNumber: string | null;
  assetName: string | null;
}

/** A physical inventory count session (FR-37.11). */
export interface CountSession {
  id: string;
  legalEntityId: string;
  location: string;
  countedDate: string;
  expectedCount: number;
  foundCount: number;
  missingCount: number;
  unknownCount: number;
  duplicateCount: number;
  createdAt: string;
  lines?: CountLine[];
  /** AST-0525 per unknown barcode. */
  warnings?: string[];
}

export interface NewCountSession {
  legalEntityId: string;
  location: string;
  countedDate?: string;
  barcodes: string[];
}

export interface BookValue {
  bookCode: string;
  currency: string;
  nbv: Decimal;
}

export interface InsuranceReportRow extends InsurancePolicy {
  assetNumber: string;
  assetName: string;
  assetStatus: AssetStatus;
  books: BookValue[];
  /** Cover ends within 30 days of the report date. */
  expiringSoon: boolean;
}

export interface InsuranceReport {
  asOf: string;
  items: InsuranceReportRow[];
  nextCursor?: string;
}

export interface InsuranceReportFilter {
  legalEntityId: string;
  asOf?: string;
  insuranceType?: (typeof INSURANCE_TYPES)[number] | "";
  expiringWithinDays?: number;
  after?: string;
}

export interface NewAsset {
  legalEntityId: string;
  assetNumber: string;
  name: string;
  classCode: string;
  barcode?: string;
  quantity?: Decimal;
}

export interface NewBook {
  bookCode: string;
  currency: string;
  method: Method;
  factor?: Decimal;
  switchToStraightLine: boolean;
  serviceLifePeriods: number;
  salvageValue?: Decimal;
  convention: string;
  inServiceDate: string;
  cost: Decimal;
}

export interface DisposeInput {
  kind: "sale" | "scrap";
  percentage: Decimal;
  proceeds?: Decimal;
  transactionDate?: string;
}

export interface NewLease {
  legalEntityId: string;
  leaseNumber: string;
  commencementDate: string;
  termMonths: number;
  paymentsPerYear: number;
  paymentAmount: Decimal;
  paymentTiming: "arrears" | "advance";
  ibrRate: Decimal;
  rateMethod?: RateMethod;
  currency: string;
  election?: LeaseElection;
  purchaseOption?: boolean;
  underlyingAssetValue?: string;
  underlyingAssetCurrency?: LowValueCurrency;
}

/** A value read together with the ETag that If-Match must echo back. */
export const TRANSFER_KINDS = ["transfer", "split"] as const;
export type TransferKind = (typeof TRANSFER_KINDS)[number];

/** One target asset of a transfer or split (FR-37.7). */
export interface NewTransferPart {
  assetNumber: string;
  name: string;
  /** Share of every book, two decimals at most; the parts total 100.00. */
  percentage: Decimal;
  /** Transfer only: another legal entity of the tenant. */
  legalEntityId?: string;
  classCode?: string;
  barcode?: string;
  quantity?: Decimal;
}

export interface TransferInput {
  kind: TransferKind;
  transferDate: string;
  parts: NewTransferPart[];
}

export interface TransferTarget {
  lineNo: number;
  percentage: Decimal;
  asset: Asset;
  books: Book[];
}

export interface Transfer {
  id: string;
  kind: TransferKind;
  transferDate: string;
  legalEntityId: string;
  sourceAssetId: string;
  sourceAssetNumber: string;
  createdBy: string | null;
  createdAt: string;
  /** On the command response: the source asset after the transfer. */
  source?: Asset;
  targets: TransferTarget[];
  transactions?: Transaction[];
}

export interface NewGroupAsset {
  legalEntityId: string;
  groupCode: string;
  name: string;
  classCode: string;
  bookCode: string;
  taxYear: number;
  method: Method;
  factor?: Decimal;
  switchToStraightLine?: boolean;
  serviceLifePeriods: number;
  convention: string;
  memberAssetIds: string[];
}

export interface GroupMember {
  assetId: string;
  assetNumber: string;
  name: string;
  assetBookId: string;
  bookStatus: string;
  inServiceDate: string;
  cost: Decimal;
}

export interface GroupYear {
  year: string;
  amount: Decimal;
  accumulated: Decimal;
  nbv: Decimal;
}

/** A group asset for pooled tax depreciation (FR-37.16). */
export interface GroupAsset {
  id: string;
  legalEntityId: string;
  groupCode: string;
  name: string;
  classCode: string;
  bookCode: string;
  taxYear: number;
  currency: string;
  method: Method;
  factor: Decimal;
  switchToStraightLine: boolean;
  serviceLifePeriods: number;
  convention: string;
  inServiceDate: string;
  status: "Open";
  /** Sum of the members' cost on the group's book. */
  basis: Decimal;
  memberCount: number;
  version: number;
  createdAt: string;
  members?: GroupMember[];
  schedule?: GroupYear[];
}

export interface Versioned<T> {
  value: T;
  etag: string;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class AssetsApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "AssetsApiError";
  }
}

export interface Problem {
  status: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof AssetsApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

export function newKey(): string {
  return crypto.randomUUID();
}

async function toError(res: Response): Promise<AssetsApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new AssetsApiError(res.status, code, message, fields);
}

async function send(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await apiFetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return res;
}

async function json<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  return (await (await send(path, init, signal)).json()) as T;
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

function post<T>(path: string, body: unknown, key: string, extra: Record<string, string> = {}): Promise<T> {
  return json<T>(path, { method: "POST", body: JSON.stringify(body), headers: { "Idempotency-Key": key, ...extra } });
}

async function versioned<T>(res: Response): Promise<Versioned<T>> {
  return { value: (await res.json()) as T, etag: res.headers.get("ETag") ?? "" };
}

const id = encodeURIComponent;

export interface AssetFilter {
  status?: AssetStatus | "";
  legalEntityId?: string;
  after?: string;
}

export function listAssets(filter: AssetFilter = {}, signal?: AbortSignal): Promise<Page<Asset>> {
  return json(`/assets${query({ status: filter.status, legalEntityId: filter.legalEntityId, after: filter.after })}`, {}, signal);
}

export async function getAsset(assetId: string, signal?: AbortSignal): Promise<Versioned<AssetDetail>> {
  const res = await send(`/assets/${id(assetId)}`, {}, signal);
  return { value: (await res.json()) as AssetDetail, etag: res.headers.get("ETag") ?? "" };
}

export function createAsset(input: NewAsset, key: string = newKey()): Promise<Asset> {
  return post(`/assets`, input, key);
}

export function addBook(assetId: string, input: NewBook, key: string = newKey()): Promise<Book> {
  return post(`/assets/${id(assetId)}/books`, input, key);
}

export function schedulePath(assetId: string, bookId: string): string {
  return `${base}/assets/${id(assetId)}/schedule?book=${id(bookId)}`;
}

export function getSchedule(assetId: string, bookId: string, signal?: AbortSignal): Promise<Schedule> {
  return json(`/assets/${id(assetId)}/schedule${query({ book: bookId })}`, {}, signal);
}

export async function listTransactions(assetId: string, signal?: AbortSignal): Promise<Transaction[]> {
  return (await json<{ items: Transaction[] }>(`/assets/${id(assetId)}/transactions`, {}, signal)).items;
}

export function disposeAsset(assetId: string, etag: string, input: DisposeInput, key: string = newKey()): Promise<Disposal> {
  return post(`/assets/${id(assetId)}/dispose`, input, key, { "If-Match": etag });
}

export function listLeases(after?: string, signal?: AbortSignal): Promise<Page<Lease>> {
  return json(`/leases${query({ after })}`, {}, signal);
}

export async function getLease(leaseId: string, signal?: AbortSignal): Promise<Versioned<Lease>> {
  return versioned(await send(`/leases/${id(leaseId)}`, {}, signal));
}

export function modifyLease(leaseId: string, etag: string, input: ModifyLeaseInput, key: string = newKey()): Promise<LeaseModificationResult> {
  return post(`/leases/${id(leaseId)}/modify`, input, key, { "If-Match": etag });
}

export function terminateLease(leaseId: string, etag: string, input: TerminateLeaseInput, key: string = newKey()): Promise<LeaseTerminationResult> {
  return post(`/leases/${id(leaseId)}/terminate`, input, key, { "If-Match": etag });
}

export function getLeasePostings(leaseId: string, signal?: AbortSignal): Promise<LeasePostings> {
  return json(`/leases/${id(leaseId)}/gl-postings`, {}, signal);
}

export function postLeases(input: LeasePostInput): Promise<LeasePostResult> {
  return json(`/leases/gl-postings`, { method: "POST", body: JSON.stringify(input) });
}

export async function getLeasePostingProfile(legalEntityId: string, signal?: AbortSignal): Promise<Versioned<LeasePostingProfile>> {
  return versioned(await send(`/leases/posting-profile${query({ legalEntityId })}`, {}, signal));
}

/** Creates the profile (no etag) or replaces it under its etag. */
export async function saveLeasePostingProfile(input: LeasePostingProfile, etag?: string): Promise<Versioned<LeasePostingProfile>> {
  return versioned(await send(`/leases/posting-profile`, { method: "PUT", body: JSON.stringify(input), headers: etag ? { "If-Match": etag } : {} }));
}

export function createLease(input: NewLease, key: string = newKey()): Promise<Lease> {
  return post(`/leases`, input, key);
}

export function adjustBook(assetId: string, input: AdjustInput, key: string = newKey()): Promise<AdjustResult> {
  return post(`/assets/${id(assetId)}/transactions`, input, key);
}

export async function listAdditions(assetId: string, signal?: AbortSignal): Promise<Addition[]> {
  return (await json<{ items?: Addition[] }>(`/assets/${id(assetId)}/additions`, {}, signal)).items ?? [];
}

export function addAddition(assetId: string, input: NewAddition, key: string = newKey()): Promise<Addition> {
  return post(`/assets/${id(assetId)}/additions`, input, key);
}

export function listRuns(after?: string, signal?: AbortSignal): Promise<Page<DepreciationRun>> {
  return json(`/assets/depreciation-runs${query({ after })}`, {}, signal);
}

export async function getRun(runId: string, signal?: AbortSignal): Promise<Versioned<DepreciationRun>> {
  return versioned(await send(`/assets/depreciation-runs/${id(runId)}`, {}, signal));
}

export function proposeRun(input: RunInput, key: string = newKey()): Promise<DepreciationRun> {
  return post(`/assets/depreciation-runs`, input, key);
}

export async function postRun(runId: string, etag: string): Promise<Versioned<DepreciationRun>> {
  return versioned(await send(`/assets/depreciation-runs/${id(runId)}/post`, { method: "POST", headers: { "If-Match": etag } }));
}

export async function listIndexSeries(signal?: AbortSignal): Promise<IndexSeries[]> {
  return (await json<{ items?: IndexSeries[] }>(`/assets/index-series`, {}, signal)).items ?? [];
}

export function putIndexSeries(code: string, input: IndexSeriesInput): Promise<IndexSeries> {
  return json(`/assets/index-series/${id(code)}`, { method: "PUT", body: JSON.stringify(input) });
}

export async function listPolicies(assetId: string, signal?: AbortSignal): Promise<InsurancePolicy[]> {
  return (await json<{ items?: InsurancePolicy[] }>(`/assets/${id(assetId)}/insurance-policies`, {}, signal)).items ?? [];
}

export function addPolicy(assetId: string, input: NewPolicy, key: string = newKey()): Promise<InsurancePolicy> {
  return post(`/assets/${id(assetId)}/insurance-policies`, input, key);
}

export interface CapexFilter {
  status?: CapexStatus | "";
  after?: string;
}

export function listCapex(filter: CapexFilter = {}, signal?: AbortSignal): Promise<Page<CapexRequest>> {
  return json(`/assets/capex-requests${query({ status: filter.status, after: filter.after })}`, {}, signal);
}

export async function getCapex(requestId: string, signal?: AbortSignal): Promise<Versioned<CapexRequest>> {
  return versioned(await send(`/assets/capex-requests/${id(requestId)}`, {}, signal));
}

export function createCapex(input: NewCapexRequest, key: string = newKey()): Promise<CapexRequest> {
  return post(`/assets/capex-requests`, input, key);
}

export async function submitCapex(requestId: string, etag: string, variantId: string): Promise<Versioned<CapexRequest>> {
  return versioned(
    await send(`/assets/capex-requests/${id(requestId)}/submit`, { method: "POST", body: JSON.stringify({ variantId }), headers: { "If-Match": etag } }),
  );
}

/** Applies one decision at the version the caller read; reject and postpone need a comment. */
export async function decideCapex(requestId: string, etag: string, decision: CapexDecision, comment?: string): Promise<Versioned<CapexRequest>> {
  const body = comment && comment.trim() !== "" ? JSON.stringify({ comment: comment.trim() }) : undefined;
  return versioned(await send(`/assets/capex-requests/${id(requestId)}/${decision}`, { method: "POST", body, headers: { "If-Match": etag } }));
}

export function getMidQuarterTest(filter: MidQuarterFilter, signal?: AbortSignal): Promise<MidQuarterReport> {
  return json(`/assets/mid-quarter-test${query({ legalEntityId: filter.legalEntityId, bookCode: filter.bookCode, year: String(filter.year) })}`, {}, signal);
}

export function listCountSessions(after?: string, signal?: AbortSignal): Promise<Page<CountSession>> {
  return json(`/assets/count-sessions${query({ after })}`, {}, signal);
}

export function getCountSession(sessionId: string, signal?: AbortSignal): Promise<CountSession> {
  return json(`/assets/count-sessions/${id(sessionId)}`, {}, signal);
}

export function createCountSession(input: NewCountSession, key: string = newKey()): Promise<CountSession> {
  return post(`/assets/count-sessions`, input, key);
}

export function getInsuranceReport(filter: InsuranceReportFilter, signal?: AbortSignal): Promise<InsuranceReport> {
  return json(
    `/assets/insurance-report${query({
      legalEntityId: filter.legalEntityId,
      asOf: filter.asOf,
      insuranceType: filter.insuranceType,
      expiringWithinDays: filter.expiringWithinDays,
      after: filter.after,
    })}`,
    {},
    signal,
  );
}

/** Transfers the whole asset to one new asset, or splits it, at the version the caller read (If-Match). */
export async function transferAsset(assetId: string, etag: string, input: TransferInput, key: string = newKey()): Promise<Versioned<Transfer>> {
  return versioned(
    await send(`/assets/${id(assetId)}/transfer`, { method: "POST", body: JSON.stringify(input), headers: { "Idempotency-Key": key, "If-Match": etag } }),
  );
}

export async function listTransfers(assetId: string, signal?: AbortSignal): Promise<Transfer[]> {
  return (await json<{ items?: Transfer[] }>(`/assets/${id(assetId)}/transfers`, {}, signal)).items ?? [];
}

export function listGroupAssets(after?: string, signal?: AbortSignal): Promise<Page<GroupAsset>> {
  return json(`/assets/group-assets${query({ after })}`, {}, signal);
}

export function getGroupAsset(groupId: string, signal?: AbortSignal): Promise<GroupAsset> {
  return json(`/assets/group-assets/${id(groupId)}`, {}, signal);
}

export function createGroupAsset(input: NewGroupAsset, key: string = newKey()): Promise<GroupAsset> {
  return post(`/assets/group-assets`, input, key);
}
