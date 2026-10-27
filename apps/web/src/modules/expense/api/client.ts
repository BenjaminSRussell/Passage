// Typed client for services/expense (contract: services/expense/api/openapi.yaml).
// The browser calls /api/v1/...; the BFF rewrites the path and attaches the session.
// Every call here is a stateless preview: nothing is stored, so none takes an
// Idempotency-Key.

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
  code: "EXP-POL-001" | "EXP-POL-002" | "EXP-REC-001";
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

async function post<T>(path: string, body: unknown, fetchImpl: typeof fetch): Promise<T> {
  const res = await fetchImpl(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(body),
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

export function evaluateReport(submitDate: string, lines: ExpenseLineInput[], fetchImpl: typeof fetch = fetch): Promise<EvaluationResponse> {
  return post("/api/v1/expense/report-evaluations", { submitDate, lines }, fetchImpl);
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
