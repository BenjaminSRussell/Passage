// API client for the budgeting service (services/budgeting/api/openapi.yaml).
// Browser calls use /api/v1/budget/... which the web BFF rewrites to the service's /v1/budget/... paths.
import type { CheckLineResult, DocumentType } from "../logic";
import { apiFetch } from "../../../shell/api/http";

const BASE = "/api/v1/budget";

export type EntryType = "Original" | "Revision" | "Transfer" | "CarryForward";
export type EncumbranceStatus = "Open" | "PartiallyRelieved" | "Relieved" | "Released";

export interface Rule {
  id: string;
  legalEntityId: string;
  code: string;
  version: number;
  warnPct: string;
  blockPct: string;
  currency: string;
  effectiveFrom: string;
}

export interface CreateRuleInput {
  legalEntityId: string;
  code: string;
  warnPct: string;
  blockPct: string;
  currency: string;
  effectiveFrom: string;
}

export interface RegisterLine {
  dimensionKey: string;
  spanKey: string;
  amount: string;
}

export interface RegisterEntryInput {
  ruleVersionId: string;
  entryType: EntryType;
  lines: RegisterLine[];
}

export interface RegisterEntry {
  id: string;
  ruleVersionId: string;
  entryType: EntryType;
  status: "Posted";
  total: string;
  postedAt: string;
  lines: RegisterLine[];
}

export interface Balance {
  id: string;
  ruleVersionId: string;
  controlDimensionKey: string;
  timeSpanKey: string;
  budgetTotal: string;
  actuals: string;
  encumbrances: string;
  preEncumbrances: string;
  /** Open amounts of earmarked funds documents (FR-38.20). */
  reservations: string;
  available: string;
  version: number;
}

export interface EncumbranceHistory {
  type: "Reserve" | "Adjust" | "Relieve" | "Consume" | "Release" | "Convert";
  amount: string;
  documentId: string;
  at: string;
}

export interface Encumbrance {
  id: string;
  ruleVersionId: string;
  controlDimensionKey: string;
  timeSpanKey: string;
  sourceType: DocumentType;
  sourceDocumentId: string;
  sourceLineId: string;
  reserved: string;
  relieved: string;
  /** Open remainder given back on a PO line cancellation or PO close (FR-38.5). */
  released: string;
  /** reserved - relieved - released. */
  remaining: string;
  status: EncumbranceStatus;
  /** A requisition reserves a pre-encumbrance; the purchase order converts it into an encumbrance (FR-38.5). */
  stage?: "pre" | "encumbrance";
  history: EncumbranceHistory[];
  createdAt: string;
}

export interface CheckLineInput {
  lineId: string;
  dimensionKey: string;
  spanKey: string;
  amount: string;
}

export interface CheckInput {
  ruleVersionId: string;
  documentId: string;
  documentType: DocumentType;
  lines: CheckLineInput[];
}

export interface CheckLineOutcome {
  lineId: string;
  dimensionKey: string;
  spanKey: string;
  amount: string;
  prior: string;
  result: CheckLineResult;
  available: string;
  net: string;
  overPct: string;
  trace: string[];
  encumbranceId?: string;
}

export interface CheckOutcome {
  id: string;
  ruleVersionId: string;
  documentId: string;
  /** A reservation's creation check is recorded with document type Reservation. */
  documentType: DocumentType | "Reservation";
  result: CheckLineResult;
  overridden: boolean;
  checkedAt: string;
  lines: CheckLineOutcome[];
}

export interface ReliefInput {
  invoiceId: string;
  invoiceAmount: string;
  invoiceQuantity: string;
  poUnitPrice: string;
}

export interface ReliefOutcome {
  encumbranceId: string;
  relieved: string;
  remaining: string;
  excess: string;
  status: EncumbranceStatus;
}

/** Cancelled: the PO line was cancelled. Closed: the PO was closed with a residual. */
export type EncumbranceReleaseReason = "Cancelled" | "Closed";

export interface EncumbranceReleaseInput {
  /** The cancelled or closed purchase order. */
  documentId: string;
  reason: EncumbranceReleaseReason;
}

export interface EncumbranceReleaseOutcome {
  encumbranceId: string;
  reason: EncumbranceReleaseReason;
  released: string;
  relieved: string;
  remaining: string;
  status: "Released";
}

export type ReservationType = "FundsReservation" | "Precommitment" | "FundsBlocking" | "RevenueForecast";
export type ReservationStatus = "Open" | "PartiallyConsumed" | "Consumed" | "Closed" | "Expired";

export interface ReservationInput {
  ruleVersionId: string;
  type: ReservationType;
  description: string;
  /** YYYY-MM-DD, the first day the document no longer holds funds; omitted for no expiry. */
  expiryDate?: string;
  lines: RegisterLine[];
}

export interface ReservationLine {
  lineId: string;
  dimensionKey: string;
  spanKey: string;
  amount: string;
  consumed: string;
  released: string;
  open: string;
}

export interface Consumption {
  id: string;
  lineId: string;
  encumbranceId: string;
  documentType: DocumentType;
  documentId: string;
  documentLineId: string;
  amount: string;
  consumedAt: string;
}

export interface Reservation {
  id: string;
  legalEntityId: string;
  ruleVersionId: string;
  type: ReservationType;
  number: string;
  description: string;
  expiryDate?: string;
  status: ReservationStatus;
  total: string;
  consumed: string;
  released: string;
  open: string;
  checkId?: string;
  version: number;
  createdAt: string;
  closedAt?: string;
  lines: ReservationLine[];
  consumptions: Consumption[];
  check?: CheckOutcome;
}

export interface ConsumeInput {
  lineId: string;
  documentType: DocumentType;
  documentId: string;
  documentLineId: string;
  amount: string;
}

export interface ConsumeOutcome {
  reservationId: string;
  lineId: string;
  documentId: string;
  encumbranceId: string;
  consumed: string;
  lineOpen: string;
  open: string;
  status: ReservationStatus;
}

export interface ReleaseOutcome {
  reservationId: string;
  number: string;
  released: string;
  status: ReservationStatus;
}

export interface Problem {
  title: string;
  status: number;
  code: string;
  detail?: string;
  /** Field errors of a 400 (COM-0001), keyed by request path. */
  errors?: Record<string, string>;
}

/** API error with the stable code from the problem details body. */
export class BudgetApiError extends Error {
  constructor(public readonly problem: Problem) {
    super(problem.detail ?? problem.title);
    this.name = "BudgetApiError";
  }
}

/** One readable sentence for a failed call: the code, the detail and any field errors. */
export function describeError(err: unknown, fallback: string): string {
  if (!(err instanceof BudgetApiError)) return fallback;
  const fields = Object.entries(err.problem.errors ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([field, msg]) => `${field} ${msg}`);
  return [`${err.problem.code}: ${err.message}`, ...fields].join("; ");
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await apiFetch(path, {
    ...init,
    credentials: "same-origin",
    headers: { accept: "application/json", ...(init.headers as Record<string, string> | undefined) },
  });
  if (!res.ok) {
    const problem = (await res.json().catch(() => ({}))) as Partial<Problem>;
    throw new BudgetApiError({
      title: problem.title ?? `HTTP ${res.status}`,
      status: res.status,
      code: problem.code ?? "COM-0000",
      detail: problem.detail,
      errors: problem.errors,
    });
  }
  return (await res.json()) as T;
}

/** Commands carry an Idempotency-Key; the page keeps one key per form content so a retry is a replay. */
export function command<T>(path: string, body: unknown, idempotencyKey: string): Promise<T> {
  return request<T>(path, {
    method: "POST",
    headers: { "content-type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

export function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : "";
}

interface Items<T> {
  items: T[];
}

export function listRules(legalEntityId?: string, signal?: AbortSignal): Promise<Items<Rule>> {
  return request(`${BASE}/control-rules${query({ legalEntityId })}`, { signal });
}

export function getRule(id: string, signal?: AbortSignal): Promise<Rule> {
  return request(`${BASE}/control-rules/${encodeURIComponent(id)}`, { signal });
}

export function createRule(input: CreateRuleInput, idempotencyKey: string): Promise<Rule> {
  return command(`${BASE}/control-rules`, input, idempotencyKey);
}

export function listRegisterEntries(rule: string, signal?: AbortSignal): Promise<Items<RegisterEntry>> {
  return request(`${BASE}/register-entries${query({ rule })}`, { signal });
}

export function postRegisterEntry(input: RegisterEntryInput, idempotencyKey: string): Promise<RegisterEntry> {
  return command(`${BASE}/register-entries`, input, idempotencyKey);
}

export function listAvailable(rule: string, signal?: AbortSignal): Promise<Items<Balance>> {
  return request(`${BASE}/available${query({ rule })}`, { signal });
}

export function listEncumbrances(rule: string, status?: EncumbranceStatus | "", signal?: AbortSignal): Promise<Items<Encumbrance>> {
  return request(`${BASE}/encumbrances${query({ rule, status: status || undefined })}`, { signal });
}

export function getEncumbrance(id: string, signal?: AbortSignal): Promise<Encumbrance> {
  return request(`${BASE}/encumbrances/${encodeURIComponent(id)}`, { signal });
}

/** Relieves an encumbrance from a matched invoice (FR-38.5). */
export function relieveEncumbrance(id: string, input: ReliefInput, idempotencyKey: string): Promise<ReliefOutcome> {
  return command(`${BASE}/encumbrances/${encodeURIComponent(id)}:relieve`, input, idempotencyKey);
}

/** Gives an encumbrance's open remainder back to available funds (FR-38.5). The line is final afterwards. */
export function releaseEncumbrance(id: string, input: EncumbranceReleaseInput, idempotencyKey: string): Promise<EncumbranceReleaseOutcome> {
  return command(`${BASE}/encumbrances/${encodeURIComponent(id)}:release`, input, idempotencyKey);
}

export function listChecks(rule: string, signal?: AbortSignal): Promise<Items<CheckOutcome>> {
  return request(`${BASE}/checks${query({ rule })}`, { signal });
}

export function getCheck(id: string, signal?: AbortSignal): Promise<CheckOutcome> {
  return request(`${BASE}/checks/${encodeURIComponent(id)}`, { signal });
}

/** Runs a budget check for a document's lines (FR-38.4). */
export function checkBudget(input: CheckInput, idempotencyKey: string): Promise<CheckOutcome> {
  return command(`${BASE}/checks`, input, idempotencyKey);
}

export interface ReservationFilter {
  rule?: string;
  type?: ReservationType | "";
  status?: ReservationStatus | "";
}

/** Earmarked funds list (PRD 7.38.14), newest first. */
export function listReservations(f: ReservationFilter, signal?: AbortSignal): Promise<Items<Reservation>> {
  return request(`${BASE}/reservations${query({ rule: f.rule, type: f.type || undefined, status: f.status || undefined })}`, { signal });
}

export function getReservation(id: string, signal?: AbortSignal): Promise<Reservation> {
  return request(`${BASE}/reservations/${encodeURIComponent(id)}`, { signal });
}

/** Creates an earmarked funds document; fund-holding types run the budget check first (FR-38.20). */
export function createReservation(input: ReservationInput, idempotencyKey: string): Promise<Reservation> {
  return command(`${BASE}/reservations`, input, idempotencyKey);
}

/** A follow-on document line draws on a reservation line and becomes an encumbrance (AC-38.12). */
export function consumeReservation(id: string, input: ConsumeInput, idempotencyKey: string): Promise<ConsumeOutcome> {
  return command(`${BASE}/reservations/${encodeURIComponent(id)}:consume`, input, idempotencyKey);
}

/** Closes the document and releases its residual (BR-38.14). */
export function closeReservation(id: string, idempotencyKey: string): Promise<ReleaseOutcome> {
  return command(`${BASE}/reservations/${encodeURIComponent(id)}:close`, {}, idempotencyKey);
}

export type PositionCategory = "Capex" | "Opex-related" | "Contingency";
export type AssignedObjectType = "CapexRequest" | "AssetUnderConstruction" | "Project";

/** An investment program for one approval year (FR-38.21). */
export interface Program {
  id: string;
  legalEntityId: string;
  code: string;
  name: string;
  approvalYear: number;
  programType: "Capex";
  currency: string;
  budget: string;
  /** Σ budgets of the top-level positions. */
  distributed: string;
  status: "Open" | "Closed";
  version: number;
  createdAt: string;
}

export interface ProgramAssignment {
  id: string;
  positionId: string;
  objectType: AssignedObjectType;
  objectRef: string;
  plan: string;
  actual: string;
  commitment: string;
  updatedAt: string;
}

/** A program position; plan, actual and commitment roll up the whole subtree. */
export interface ProgramPosition {
  id: string;
  programId: string;
  parentId?: string;
  code: string;
  level: number;
  category: PositionCategory;
  budget: string;
  distributed: string;
  undistributed: string;
  plan: string;
  actual: string;
  commitment: string;
  available: string;
  carriedForward: string;
  assignments: ProgramAssignment[];
}

export interface ProgramView extends Program {
  plan: string;
  actual: string;
  commitment: string;
  available: string;
  carriedForward: string;
  /** Parents before children. */
  positions: ProgramPosition[];
}

export interface CreateProgramInput {
  legalEntityId: string;
  code: string;
  name: string;
  approvalYear: number;
  currency: string;
  budget: string;
}

export interface AllocationInput {
  positionCode: string;
  /** Absent keeps an existing position's category; a new position defaults to Capex. */
  category?: PositionCategory;
  budget: string;
}

export interface DistributeInput {
  /** Absent distributes the program budget to top-level positions. */
  parentPositionId?: string;
  allocations: AllocationInput[];
}

export interface AssignInput {
  positionId: string;
  objectType: AssignedObjectType;
  objectRef: string;
  plan: string;
  actual: string;
  commitment: string;
}

export interface CarryForwardOutcome {
  programId: string;
  fromYear: number;
  toYear: number;
  amount: string;
  positions: { positionId: string; code: string; amount: string }[];
}

export function listPrograms(legalEntityId?: string, signal?: AbortSignal): Promise<Items<Program>> {
  return request(`${BASE}/investment-programs${query({ legalEntityId })}`, { signal });
}

export function getProgram(id: string, signal?: AbortSignal): Promise<ProgramView> {
  return request(`${BASE}/investment-programs/${encodeURIComponent(id)}`, { signal });
}

export function createProgram(input: CreateProgramInput, idempotencyKey: string): Promise<ProgramView> {
  return command(`${BASE}/investment-programs`, input, idempotencyKey);
}

/** Sets child budgets under one parent; Σ children never exceed the parent (BR-38.15, CO-0641). */
export function distributeProgram(id: string, input: DistributeInput, idempotencyKey: string): Promise<ProgramView> {
  return command(`${BASE}/investment-programs/${encodeURIComponent(id)}:distribute`, input, idempotencyKey);
}

/** Puts a capex request, asset under construction or project on a position with its amounts. */
export function assignToProgram(id: string, input: AssignInput, idempotencyKey: string): Promise<ProgramView> {
  return command(`${BASE}/investment-programs/${encodeURIComponent(id)}:assign`, input, idempotencyKey);
}

/** Carries each position's unused budget into the next approval year, once (FR-38.21). */
export function carryForwardProgram(id: string, toYear: number, idempotencyKey: string): Promise<CarryForwardOutcome> {
  return command(`${BASE}/investment-programs/${encodeURIComponent(id)}:carry-forward`, { toYear }, idempotencyKey);
}
