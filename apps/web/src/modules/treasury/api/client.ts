// Typed client for the treasury service (services/treasury/api/openapi.yaml).
// Browser calls use /api/v1/treasury/..., which the web BFF rewrites to /v1/treasury/....
// Money stays a decimal string end to end; nothing here converts to a float.

export const base = "/api/v1/treasury";

export const ACCOUNT_STATUSES = ["draft", "active", "inactive", "closed"] as const;
export type AccountStatus = (typeof ACCOUNT_STATUSES)[number];

export const SOURCE_TYPES = ["Manual", "AP", "AR", "PO", "SO", "Recurring", "Payroll", "Project"] as const;
export type SourceType = (typeof SOURCE_TYPES)[number];

export const LAYOUTS = ["daily", "weekly", "monthly"] as const;
export type Layout = (typeof LAYOUTS)[number];

export interface BankAccount {
  id: string;
  legalEntityId: string;
  accountCode: string;
  ibanMasked: string;
  ibanLast4: string;
  bic?: string | null;
  currency: string;
  mainAccountId: string;
  status: AccountStatus;
  version: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface BankAccountInput {
  legalEntityId: string;
  accountCode: string;
  iban: string;
  bic?: string;
  currency: string;
  mainAccountId: string;
}

export interface Statement {
  id: string;
  bankAccountId: string;
  legalEntityId: string;
  statementId: string;
  sequence?: number | null;
  format: string;
  currency: string;
  openingDate: string;
  openingBalance: string;
  closingDate: string;
  closingBalance: string;
  lineCount: number;
  createdAt: string;
}

export interface Warning {
  code: string;
  detail: string;
}

export interface ImportedStatement extends Statement {
  warnings: Warning[];
}

/** Outcome of an upload: 201 stored a new statement, 200 replayed one already imported (TRS-0405). */
export interface ImportResult {
  statement: ImportedStatement;
  replayed: boolean;
}

export interface StatementLine {
  lineNo: number;
  bookingDate: string;
  valueDate: string;
  amount: string;
  currency: string;
  btcCode?: string;
  endToEndRef?: string;
  reference?: string;
  counterpartyName?: string;
  status: string;
}

export interface LinePage {
  items: StatementLine[];
  nextAfterLineNo: number | null;
}

export interface ForecastEntryInput {
  legalEntityId: string;
  sourceType: SourceType;
  sourceId?: string;
  expectedDate: string;
  amount: string;
  currency: string;
  confidence?: string;
}

export interface ForecastEntry extends ForecastEntryInput {
  id: string;
  sourceId: string;
  confidence: string;
}

export interface ForecastBucket {
  key: string;
  currency: string;
  inflow: string;
  outflow: string;
  net: string;
  cumulative: string;
}

export interface Forecast {
  layout: Layout;
  asOf: string;
  buckets: ForecastBucket[];
  outOfHorizon: number;
}

/** Problem details (RFC 9457) surfaced to the UI with the stable service code and field errors. */
export class TreasuryApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "TreasuryApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof TreasuryApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

/** A fresh Idempotency-Key for one user action; retries of the same action reuse it. */
export function newKey(): string {
  return crypto.randomUUID();
}

async function toError(res: Response): Promise<TreasuryApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new TreasuryApiError(res.status, code, message, fields);
}

async function request(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  // A FormData body sets its own multipart boundary.
  if (init.body !== undefined && !(init.body instanceof FormData)) headers.set("Content-Type", "application/json");
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return res;
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  return (await (await request(path, init, signal)).json()) as T;
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
  if (!Array.isArray(body.items)) throw new TreasuryApiError(0, "COM-0001", "The response has no items");
  return body.items;
}

/** GET /bank-accounts?legalEntityId= - accounts of one legal entity, by code. */
export function listBankAccounts(legalEntityId: string, signal?: AbortSignal): Promise<BankAccount[]> {
  return items<BankAccount>(`/bank-accounts${query({ legalEntityId })}`, signal);
}

export function getBankAccount(id: string, signal?: AbortSignal): Promise<BankAccount> {
  return send<BankAccount>(`/bank-accounts/${encodeURIComponent(id)}`, {}, signal);
}

/** POST /bank-accounts - creates a Draft account; the key makes a retry return the same account. */
export function createBankAccount(input: BankAccountInput, key: string = newKey()): Promise<BankAccount> {
  return send<BankAccount>("/bank-accounts", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

export type AccountAction = "activate" | "deactivate" | "close";

/** POST /bank-accounts/{id}:activate|:deactivate|:close - If-Match carries the version; 412 when it changed. */
export function transitionBankAccount(id: string, action: AccountAction, version: number): Promise<BankAccount> {
  return send<BankAccount>(`/bank-accounts/${encodeURIComponent(id)}:${action}`, { method: "POST", headers: { "If-Match": `"${version}"` } });
}

export function listStatements(bankAccountId: string, signal?: AbortSignal): Promise<Statement[]> {
  return items<Statement>(`/bank-accounts/${encodeURIComponent(bankAccountId)}/statements`, signal);
}

export function getStatement(id: string, signal?: AbortSignal): Promise<Statement> {
  return send<Statement>(`/statements/${encodeURIComponent(id)}`, {}, signal);
}

export function listStatementLines(id: string, afterLineNo = 0, limit = 100, signal?: AbortSignal): Promise<LinePage> {
  return send<LinePage>(`/statements/${encodeURIComponent(id)}/lines${query({ afterLineNo, limit })}`, {}, signal);
}

/** POST /statements/import (multipart). The file hash makes a re-upload a replay, so no key is needed. */
export async function importStatement(bankAccountId: string, file: Blob, fileName: string): Promise<ImportResult> {
  const form = new FormData();
  form.set("bankAccountId", bankAccountId);
  form.set("file", file, fileName);
  const res = await request("/statements/import", { method: "POST", body: form });
  const statement = (await res.json()) as ImportedStatement;
  return { statement: { ...statement, warnings: statement.warnings ?? [] }, replayed: res.status === 200 };
}

/** POST /cash-forecast-entries - a manual or source-fed expected cash flow (inflow positive). */
export function createForecastEntry(input: ForecastEntryInput, key: string = newKey()): Promise<ForecastEntry> {
  return send<ForecastEntry>("/cash-forecast-entries", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
}

export interface ForecastQuery {
  legalEntityId: string;
  layout: Layout;
  asOf?: string;
  currency?: string;
}

/** GET /cash-forecast - buckets of one legal entity; each bucket's net is the sum of its contributions. */
export function getCashForecast(q: ForecastQuery, signal?: AbortSignal): Promise<Forecast> {
  return send<Forecast>(`/cash-forecast${query({ legalEntityId: q.legalEntityId, layout: q.layout, asOf: q.asOf, currency: q.currency })}`, {}, signal);
}
