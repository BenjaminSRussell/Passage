// Client for the compliance service (services/compliance/api/openapi.yaml) and
// the localization service (services/localization/api/openapi.yaml). Browser
// calls use /api/v1/sod/... and /api/v1/localization/...; the gateway strips /api.

export const sodBase = "/api/v1/sod";
export const localizationBase = "/api/v1/localization";

export const SEVERITIES = ["HIGH", "MEDIUM", "LOW"] as const;
export type Severity = (typeof SEVERITIES)[number];

export const CONFLICT_STATES = ["OPEN", "MITIGATED", "RESOLVED", "ACCEPTED_RISK"] as const;
export type ConflictState = (typeof CONFLICT_STATES)[number];

export interface Rule {
  ruleId: string;
  name: string;
  dutyA: string;
  dutyB: string;
  severity: Severity;
  riskId: string;
  description?: string;
  mitigationAllowed: boolean;
  maxMitigationDays: number;
  status: "ACTIVE" | "RETIRED";
  version: number;
}

export interface Role {
  name: string;
  duties: string[];
}

export interface SimulatedConflict {
  userId: string;
  ruleId: string;
  severity: Severity;
  riskId: string;
  dutyA: string;
  dutyB: string;
  rolesA: string[];
  rolesB: string[];
  state: ConflictState;
}

export interface SimulateResult {
  newConflicts: SimulatedConflict[];
  blocked: boolean;
  blockingRuleIds: string[];
  code?: string;
}

export interface Conflict {
  conflictId: string;
  userId: string;
  ruleId: string;
  riskId: string;
  severity: Severity;
  dutyA: string;
  dutyB: string;
  rolesA: string[];
  rolesB: string[];
  state: ConflictState;
  mitigationId?: string;
  expiresAt?: string;
  decidedBy?: string;
  decisionReason?: string;
  detectedAt: string;
  resolvedAt?: string;
  updatedAt: string;
  version: number;
}

export interface Mitigation {
  mitigationId: string;
  conflictId: string;
  controlRef: string;
  reason: string;
  approverId: string;
  approvedAt: string;
  expiresAt: string;
}

export interface ConflictDetail extends Conflict {
  mitigations: Mitigation[];
}

export interface ConflictPage {
  items: Conflict[];
  nextCursor?: string;
}

export interface ConflictFilter {
  state?: ConflictState | "";
  severity?: Severity | "";
  userId?: string;
  cursor?: string;
}

export interface MitigationInput {
  controlRef: string;
  reason: string;
  /** RFC 3339 timestamp. */
  expiresAt: string;
}

export interface StatutoryReport {
  code: string;
  format: string;
  mandate: string;
  requiredFields: string[];
}

export interface Pack {
  code: string;
  version: string;
  name: string;
  country: string;
  currency: string;
  locale: string;
  taxRegime: string;
  fiscalYearStartMonth: number;
  holidayRegion: string;
  bankFormats: string[];
  taxIdScheme: string;
  retentionMinYears: Record<string, number>;
  documentLayouts: string[];
  statutoryReports: StatutoryReport[];
}

export interface InstallCheckInput {
  entityCountry: string;
  primaryInstalled: number;
  calendarOpenPeriodsOk: boolean;
}

export interface InstallCheckResult {
  installable: boolean;
  pack: string;
  version: string;
}

export interface Versioned<T> {
  value: T;
  etag: string;
}

export interface Problem {
  status: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export class ComplianceApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ComplianceApiError";
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ComplianceApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<ComplianceApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new ComplianceApiError(res.status, code, message, fields);
}

async function send(url: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(url, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return res;
}

async function json<T>(url: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  return (await (await send(url, init, signal)).json()) as T;
}

async function versioned<T>(url: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Versioned<T>> {
  const res = await send(url, init, signal);
  return { value: (await res.json()) as T, etag: res.headers.get("ETag") ?? "" };
}

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") q.set(k, v);
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

/** A fresh Idempotency-Key for one user intent. */
export function newKey(): string {
  return crypto.randomUUID();
}

export async function listRules(signal?: AbortSignal): Promise<Rule[]> {
  return (await json<{ items: Rule[] }>(`${sodBase}/rules`, {}, signal)).items ?? [];
}

export function listConflicts(filter: ConflictFilter = {}, signal?: AbortSignal): Promise<ConflictPage> {
  return json<ConflictPage>(
    `${sodBase}/conflicts${query({ state: filter.state, severity: filter.severity, userId: filter.userId, cursor: filter.cursor, limit: "50" })}`,
    {},
    signal,
  );
}

export function getConflict(id: string, signal?: AbortSignal): Promise<Versioned<ConflictDetail>> {
  return versioned<ConflictDetail>(`${sodBase}/conflicts/${encodeURIComponent(id)}`, {}, signal);
}

/** Approves a mitigation at the given ETag; the caller is the approver (FR-48.5). */
export function mitigateConflict(id: string, etag: string, input: MitigationInput, key: string): Promise<Versioned<ConflictDetail>> {
  return versioned<ConflictDetail>(`${sodBase}/conflicts/${encodeURIComponent(id)}/mitigations`, {
    method: "POST",
    body: JSON.stringify(input),
    headers: { "If-Match": etag, "Idempotency-Key": key },
  });
}

/** Accepts the risk of an OPEN LOW or MEDIUM conflict (BR-48.1). */
export function acceptRisk(id: string, etag: string, reason: string): Promise<Versioned<ConflictDetail>> {
  return versioned<ConflictDetail>(`${sodBase}/conflicts/${encodeURIComponent(id)}/accept-risk`, {
    method: "POST",
    body: JSON.stringify({ reason }),
    headers: { "If-Match": etag },
  });
}

export function simulate(input: { userId: string; currentRoles: Role[]; proposedRoles: Role[] }): Promise<SimulateResult> {
  return json<SimulateResult>(`${sodBase}/simulate`, { method: "POST", body: JSON.stringify(input) });
}

export function evaluate(input: { userId: string; roles: Role[] }): Promise<{ userId: string; conflicts: Conflict[] }> {
  return json<{ userId: string; conflicts: Conflict[] }>(`${sodBase}/evaluations`, { method: "POST", body: JSON.stringify(input) });
}

export async function listPacks(signal?: AbortSignal): Promise<Pack[]> {
  return (await json<{ items: Pack[] }>(`${localizationBase}/packs`, {}, signal)).items ?? [];
}

export function getPack(code: string, signal?: AbortSignal): Promise<Pack> {
  return json<Pack>(`${localizationBase}/packs/${encodeURIComponent(code)}`, {}, signal);
}

export function checkInstall(code: string, input: InstallCheckInput): Promise<InstallCheckResult> {
  return json<InstallCheckResult>(`${localizationBase}/packs/${encodeURIComponent(code)}/install:check`, { method: "POST", body: JSON.stringify(input) });
}

export function validateTaxId(scheme: string, value: string): Promise<{ valid: boolean; scheme: string }> {
  return json<{ valid: boolean; scheme: string }>(`${localizationBase}/tax-ids:validate`, { method: "POST", body: JSON.stringify({ scheme, value }) });
}
