// Typed client for services/expense (contract: services/expense/api/openapi.yaml).
// The browser calls /api/v1/...; the BFF rewrites the path and attaches the session.
// The report calls (slice 4) and the cash advance, travel requisition and card feed
// calls (slice 5) persist: creating POSTs and custom methods send an Idempotency-Key,
// and custom methods and line removal send If-Match with the version acted on.
// Every other call is a stateless preview that stores nothing and takes no key.

export interface ExpenseLineInput {
  id: string;
  category: string;
  txnDate: string;
  amount: string;
  merchant: string;
  attendees: number;
  hasReceipt: boolean;
  justification: string;
  paidBy: "EMPLOYEE" | "COMPANY_CARD";
}

export type Outcome = "WARN" | "BLOCK" | "REQUIRE_JUSTIFICATION" | "REQUIRE_RECEIPT";

export interface PolicyResult {
  lineId: string;
  ruleId: string;
  ruleVersion: number;
  outcome: Outcome;
  code: "EXP-POL-001" | "EXP-POL-002" | "EXP-REC-001" | "EXP-TR-002";
  message: string;
  satisfied: boolean;
}

export interface EvaluationResponse {
  submittable: boolean;
  results: PolicyResult[];
  totals: { total: string; reimbursable: string; companyPaid: string };
}

/** BR-43.6 per diem input: one trip of consecutive days. */
export interface PerDiemInput {
  rate: { country?: string; city?: string; currency: string; lodging: string; mealsIncidentals: string };
  days: { date: string; lodging: boolean; breakfastProvided: boolean; lunchProvided: boolean; dinnerProvided: boolean }[];
}

export interface PerDiemLine {
  date: string;
  partial: boolean;
  lodging: string;
  mie: string;
  total: string;
}

export interface PerDiemResult {
  currency: string;
  lines: PerDiemLine[];
  totals: { lodging: string; mie: string; total: string };
}

/** FR-43.15 aging input: one paid advance and what has cleared it. */
export interface AgingAdvanceInput {
  id: string;
  tripEnd: string;
  paid: string;
  settled?: string;
  returned?: string;
  recovered?: string;
  writtenOff?: string;
}

export interface AgingInput {
  asOf: string;
  limitDays?: number;
  advances: AgingAdvanceInput[];
}

export interface OverdueAdvance {
  id: string;
  dueDate: string;
  daysPastDue: number;
  open: string;
}

export interface AgingResult {
  newAdvancesBlocked: boolean;
  overdue: OverdueAdvance[];
  code?: "EXP-ADV-002";
}

/** BR-43.13 window and closure input. */
export interface RequisitionCheckInput {
  trip: { from: string; to: string };
  travelDates: string[];
  lastTravelDate?: string;
  asOf: string;
  reportPosted: boolean;
}

export interface RequisitionCheckResult {
  outOfRangeDates: string[];
  closeDate: string;
  closeDue: boolean;
}

export type FeedFormat = "CSV" | "OFX";

export interface CardTransaction {
  line: number;
  cardLast4: string;
  txnDate: string;
  postDate: string;
  merchant: string;
  mcc: string;
  amount: string;
  currency: string;
}

export interface RejectedRow {
  line: number;
  reason: string;
}

export interface CardFeedResult {
  format: FeedFormat;
  fileHash: string;
  transactions: CardTransaction[];
  rejected: RejectedRow[];
}

/** FR-43.16 travel requisition gate sent with a report evaluation. */
export interface RequisitionGate {
  requisitionCategories: string[];
  hasRequisition: boolean;
}

/** BR-43.5 lodging cap input: one stay and the per-diem lodging rate per night. */
export interface LodgingInput {
  lineId?: string;
  amount: string;
  nights: number;
  lodgingRate: string;
  justification?: string;
}

export interface LodgingResult {
  capPerNight: string;
  cap: string;
  overCap: boolean;
  satisfied: boolean;
  result?: PolicyResult;
}

export interface MileageInput {
  distance: string;
  rate: string;
}

export interface MileageResult {
  amount: string;
}

/** FR-43.13 card transaction or receipt to match. */
export interface MatchItem {
  id: string;
  workerId: string;
  date: string;
  merchant: string;
  amount: string;
}

export interface CardMatchInput {
  cards: MatchItem[];
  receipts: MatchItem[];
  tolerancePct?: string;
  toleranceDays?: number;
}

export interface CardMatchPair {
  cardId: string;
  receiptId: string;
}

export interface CardMatchResult {
  matches: CardMatchPair[];
  unmatchedCards: string[];
  unmatchedReceipts: string[];
  autoMatchRate: string;
}

/** A RFC 9457 problem returned by the service, with its stable code and field errors. */
export class ExpenseProblem extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ExpenseProblem";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

/** Turns any thrown value into what a page shows: the service problem, or a network failure. */
export function describeProblem(error: unknown): Problem {
  if (error instanceof ExpenseProblem) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "NETWORK", message: "The expense service could not be reached. Check your connection and try again.", fields: {} };
}

async function request<T>(method: string, path: string, body: unknown, fetchImpl: typeof fetch, headers: Record<string, string> = {}): Promise<T> {
  const res = await fetchImpl(path, {
    method,
    headers: { ...(body === undefined ? {} : { "Content-Type": "application/json" }), Accept: "application/json", ...headers },
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const json = (await res.json().catch(() => null)) as Record<string, unknown> | null;
  if (!res.ok) {
    const code = typeof json?.code === "string" && json.code !== "" ? json.code : `HTTP-${res.status}`;
    const message = typeof json?.detail === "string" ? json.detail : `Request failed (HTTP ${res.status})`;
    const fields = json?.errors && typeof json.errors === "object" ? (json.errors as Record<string, string>) : {};
    throw new ExpenseProblem(res.status, code, message, fields);
  }
  return json as T;
}

function post<T>(path: string, body: unknown, fetchImpl: typeof fetch): Promise<T> {
  return request("POST", path, body, fetchImpl);
}

export function evaluateReport(
  submitDate: string,
  lines: ExpenseLineInput[],
  gate?: RequisitionGate,
  fetchImpl: typeof fetch = fetch,
): Promise<EvaluationResponse> {
  return post("/api/v1/expense/report-evaluations", { submitDate, lines, ...gate }, fetchImpl);
}

export function computePerDiem(input: PerDiemInput, fetchImpl: typeof fetch = fetch): Promise<PerDiemResult> {
  return post("/api/v1/expense/per-diem-computations", input, fetchImpl);
}

export function checkAdvanceAging(input: AgingInput, fetchImpl: typeof fetch = fetch): Promise<AgingResult> {
  return post("/api/v1/expense/cash-advance-aging", input, fetchImpl);
}

export function checkRequisition(input: RequisitionCheckInput, fetchImpl: typeof fetch = fetch): Promise<RequisitionCheckResult> {
  return post("/api/v1/expense/travel-requisition-checks", input, fetchImpl);
}

export function parseCardFeed(format: FeedFormat, content: string, fetchImpl: typeof fetch = fetch): Promise<CardFeedResult> {
  return post("/api/v1/expense/card-transactions:parse", { format, content }, fetchImpl);
}

export function checkLodging(input: LodgingInput, fetchImpl: typeof fetch = fetch): Promise<LodgingResult> {
  return post("/api/v1/expense/lodging-checks", input, fetchImpl);
}

export function computeMileage(input: MileageInput, fetchImpl: typeof fetch = fetch): Promise<MileageResult> {
  return post("/api/v1/expense/mileage-computations", input, fetchImpl);
}

export function matchCards(input: CardMatchInput, fetchImpl: typeof fetch = fetch): Promise<CardMatchResult> {
  return post("/api/v1/expense/card-matches", input, fetchImpl);
}

// ---- Expense reports (slice 4, persisted) ----

export type ReportStatus = "DRAFT" | "SUBMITTED" | "APPROVED" | "REJECTED" | "RETURNED" | "POSTED" | "PAID";
export type PaidBy = "EMPLOYEE" | "COMPANY_CARD";

export interface ReportLine {
  id: string;
  lineNo: number;
  category: string;
  txnDate: string;
  amount: string;
  merchant: string;
  attendees: number;
  hasReceipt: boolean;
  justification: string;
  paidBy: PaidBy;
  policyResults: PolicyResult[];
}

export interface ExpenseReport {
  id: string;
  legalEntityId: string;
  workerId: string;
  purpose: string;
  currency: string;
  status: ReportStatus;
  total: string;
  reimbursable: string;
  companyPaid: string;
  /** DRAFT, at least one line and no open stored result; submit re-checks at its own date. */
  submittable: boolean;
  version: number;
  createdAt: string;
  submittedAt: string | null;
  lines: ReportLine[];
}

export interface ReportPage {
  items: ExpenseReport[];
  nextCursor: string;
}

export interface ReportQuery {
  status?: ReportStatus;
  workerId?: string;
  cursor?: string;
  limit?: number;
}

export interface NewReportInput {
  legalEntityId: string;
  workerId: string;
  purpose: string;
  currency: string;
}

export interface NewLineInput {
  category: string;
  txnDate: string;
  amount: string;
  merchant: string;
  attendees: number;
  hasReceipt: boolean;
  justification: string;
  paidBy: PaidBy;
}

const REPORTS = "/api/v1/expense/reports";

/** A fresh Idempotency-Key for one user action; a retry of that action reuses it. */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

export function listReports(query: ReportQuery = {}, fetchImpl: typeof fetch = fetch): Promise<ReportPage> {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v !== undefined && v !== "") params.set(k, String(v));
  }
  const qs = params.toString();
  return request("GET", qs ? `${REPORTS}?${qs}` : REPORTS, undefined, fetchImpl);
}

export function getReport(id: string, fetchImpl: typeof fetch = fetch): Promise<ExpenseReport> {
  return request("GET", `${REPORTS}/${encodeURIComponent(id)}`, undefined, fetchImpl);
}

export function createReport(input: NewReportInput, key: string, fetchImpl: typeof fetch = fetch): Promise<ExpenseReport> {
  return request("POST", REPORTS, input, fetchImpl, { "Idempotency-Key": key });
}

export function addReportLine(reportId: string, line: NewLineInput, key: string, fetchImpl: typeof fetch = fetch): Promise<ExpenseReport> {
  return request("POST", `${REPORTS}/${encodeURIComponent(reportId)}/lines`, line, fetchImpl, { "Idempotency-Key": key });
}

export function deleteReportLine(reportId: string, lineId: string, version: number, fetchImpl: typeof fetch = fetch): Promise<ExpenseReport> {
  return request("DELETE", `${REPORTS}/${encodeURIComponent(reportId)}/lines/${encodeURIComponent(lineId)}`, undefined, fetchImpl, {
    "If-Match": `"${version}"`,
  });
}

export function submitReport(reportId: string, version: number, key: string, fetchImpl: typeof fetch = fetch): Promise<ExpenseReport> {
  return request("POST", `${REPORTS}/${encodeURIComponent(reportId)}/submit`, undefined, fetchImpl, {
    "Idempotency-Key": key,
    "If-Match": `"${version}"`,
  });
}


// ---- Cash advances, travel requisitions and card feeds (slice 5, persisted) ----
// Creating POSTs and every custom method send an Idempotency-Key; the custom methods
// also send If-Match with the version the user acted on.

export type AdvanceStatus = "DRAFT" | "SUBMITTED" | "APPROVED" | "REJECTED" | "PAID" | "PARTIALLY_SETTLED" | "SETTLED";
export type RequisitionStatus = "DRAFT" | "SUBMITTED" | "APPROVED" | "REJECTED" | "CLOSED";
export type CardTxnStatus = "UNASSIGNED" | "ASSIGNED" | "DISPUTED" | "PERSONAL";
export type Decision = "APPROVE" | "REJECT";

export interface CashAdvance {
  id: string;
  legalEntityId: string;
  workerId: string;
  amount: string;
  currency: string;
  purpose: string;
  travelRequisitionId: string | null;
  tripEnd: string;
  dueDate: string;
  overdue: boolean;
  status: AdvanceStatus;
  paid: string;
  settled: string;
  returned: string;
  recovered: string;
  open: string;
  apPrepaymentId: string | null;
  version: number;
  createdAt: string;
}

export interface NewAdvanceInput {
  legalEntityId: string;
  workerId: string;
  amount: string;
  currency: string;
  purpose: string;
  travelRequisitionId?: string;
  tripEnd: string;
}

export interface Destination {
  country: string;
  city: string;
}

export interface Estimate {
  category: string;
  amount: string;
}

export interface TravelRequisition {
  id: string;
  legalEntityId: string;
  workerId: string;
  purpose: string;
  currency: string;
  status: RequisitionStatus;
  tripFrom: string;
  tripTo: string;
  destinations: Destination[];
  estimates: Estimate[];
  estimatedTotal: string;
  version: number;
  createdAt: string;
  submittedAt: string | null;
  decidedAt: string | null;
}

export interface NewRequisitionInput {
  legalEntityId: string;
  workerId: string;
  purpose: string;
  currency: string;
  tripFrom: string;
  tripTo: string;
  destinations: Destination[];
  estimates: Estimate[];
}

/** An imported card transaction in its worker's pool (the parsed row plus where it is stored). */
export interface StoredCardTransaction extends CardTransaction {
  id: string;
  importId: string;
  workerId: string;
  status: CardTxnStatus;
}

export interface CardImportInput {
  legalEntityId: string;
  format: FeedFormat;
  content: string;
  /** Last four card digits to the worker id holding the card. */
  cardholders: Record<string, string>;
}

export interface CardImport {
  id: string;
  legalEntityId: string;
  format: FeedFormat;
  fileHash: string;
  imported: number;
  quarantined: { line: number; reason: string }[];
  createdAt: string;
}

export interface Page<T> {
  items: T[];
  nextCursor: string;
}

export interface ListQuery<S extends string> {
  status?: S;
  workerId?: string;
  cursor?: string;
  limit?: number;
}

const ADVANCES = "/api/v1/expense/cash-advances";
const REQUISITIONS = "/api/v1/expense/travel-requisitions";
const CARDS = "/api/v1/expense/card-transactions";

function withQuery(path: string, query: object): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v !== undefined && v !== "") params.set(k, String(v));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

function command<T>(path: string, body: unknown, key: string, version: number | undefined, fetchImpl: typeof fetch): Promise<T> {
  const headers: Record<string, string> = { "Idempotency-Key": key };
  if (version !== undefined) headers["If-Match"] = `"${version}"`;
  return request("POST", path, body, fetchImpl, headers);
}

export function listAdvances(query: ListQuery<AdvanceStatus> = {}, fetchImpl: typeof fetch = fetch): Promise<Page<CashAdvance>> {
  return request("GET", withQuery(ADVANCES, query), undefined, fetchImpl);
}

export function getAdvance(id: string, fetchImpl: typeof fetch = fetch): Promise<CashAdvance> {
  return request("GET", `${ADVANCES}/${encodeURIComponent(id)}`, undefined, fetchImpl);
}

export function requestAdvance(input: NewAdvanceInput, key: string, fetchImpl: typeof fetch = fetch): Promise<CashAdvance> {
  return command(ADVANCES, input, key, undefined, fetchImpl);
}

export function decideAdvance(id: string, decision: Decision, version: number, key: string, fetchImpl: typeof fetch = fetch): Promise<CashAdvance> {
  return command(`${ADVANCES}/${encodeURIComponent(id)}/decide`, { decision }, key, version, fetchImpl);
}

export function payAdvance(id: string, prepaymentId: string, version: number, key: string, fetchImpl: typeof fetch = fetch): Promise<CashAdvance> {
  return command(`${ADVANCES}/${encodeURIComponent(id)}/pay`, { prepaymentId }, key, version, fetchImpl);
}

export function returnAdvance(id: string, amount: string, version: number, key: string, fetchImpl: typeof fetch = fetch): Promise<CashAdvance> {
  return command(`${ADVANCES}/${encodeURIComponent(id)}/return`, { amount }, key, version, fetchImpl);
}

export function listRequisitions(query: ListQuery<RequisitionStatus> = {}, fetchImpl: typeof fetch = fetch): Promise<Page<TravelRequisition>> {
  return request("GET", withQuery(REQUISITIONS, query), undefined, fetchImpl);
}

export function getRequisition(id: string, fetchImpl: typeof fetch = fetch): Promise<TravelRequisition> {
  return request("GET", `${REQUISITIONS}/${encodeURIComponent(id)}`, undefined, fetchImpl);
}

export function createRequisition(input: NewRequisitionInput, key: string, fetchImpl: typeof fetch = fetch): Promise<TravelRequisition> {
  return command(REQUISITIONS, input, key, undefined, fetchImpl);
}

export function submitRequisition(id: string, version: number, key: string, fetchImpl: typeof fetch = fetch): Promise<TravelRequisition> {
  return command(`${REQUISITIONS}/${encodeURIComponent(id)}/submit`, undefined, key, version, fetchImpl);
}

export function decideRequisition(id: string, decision: Decision, version: number, key: string, fetchImpl: typeof fetch = fetch): Promise<TravelRequisition> {
  return command(`${REQUISITIONS}/${encodeURIComponent(id)}/decide`, { decision }, key, version, fetchImpl);
}

export function listCardTransactions(query: ListQuery<CardTxnStatus> = {}, fetchImpl: typeof fetch = fetch): Promise<Page<StoredCardTransaction>> {
  return request("GET", withQuery(CARDS, query), undefined, fetchImpl);
}

export function importCardFeed(input: CardImportInput, key: string, fetchImpl: typeof fetch = fetch): Promise<CardImport> {
  return command(`${CARDS}/import`, input, key, undefined, fetchImpl);
}
