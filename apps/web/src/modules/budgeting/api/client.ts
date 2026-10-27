// API client for the budgeting service (services/budgeting/api/openapi.yaml).
// Browser calls use /api/v1/budget/... which the web BFF rewrites to the service's /v1/budget/... paths.
import type { CheckLineResult, DocumentType } from "../logic";

const BASE = "/api/v1/budget";

export type EntryType = "Original" | "Revision" | "Transfer" | "CarryForward";
export type EncumbranceStatus = "Open" | "PartiallyRelieved" | "Relieved";

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
  available: string;
  version: number;
}

export interface EncumbranceHistory {
  type: "Reserve" | "Adjust" | "Relieve";
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
  remaining: string;
  status: EncumbranceStatus;
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
  documentType: DocumentType;
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

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(path, {
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
function command<T>(path: string, body: unknown, idempotencyKey: string): Promise<T> {
  return request<T>(path, {
    method: "POST",
    headers: { "content-type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}

function query(params: Record<string, string | undefined>): string {
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
