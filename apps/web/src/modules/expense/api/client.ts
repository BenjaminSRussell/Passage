// Typed client for services/expense (contract: services/expense/api/openapi.yaml).
// The browser calls /api/v1/...; the BFF rewrites the path and attaches the session.
// The report calls persist (slice 4): creating POSTs and submit send an
// Idempotency-Key, and submit and line removal send If-Match with the report
// version. Every other call is a stateless preview that stores nothing and takes
// no key.

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

