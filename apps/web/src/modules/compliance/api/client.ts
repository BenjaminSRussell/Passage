// Client for the compliance service (services/compliance/api/openapi.yaml) and
// the localization service (services/localization/api/openapi.yaml). Browser
// calls use /api/v1/sod/..., /api/v1/compliance/..., /api/v1/security/... and
// /api/v1/localization/...; the gateway strips /api.

export const sodBase = "/api/v1/sod";
export const complianceBase = "/api/v1/compliance";
export const securityBase = "/api/v1/security";
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

export const BREACH_REGIMES = ["GDPR", "UK_GDPR", "US_STATE", "PIPEDA", "CERT_IN", "LFPDPPP"] as const;
export type BreachRegime = (typeof BREACH_REGIMES)[number];

export const CLOCK_STATUSES = ["OVERDUE", "AT_RISK", "NOT_DUE", "NO_FIXED_DEADLINE"] as const;
export type ClockStatus = (typeof CLOCK_STATUSES)[number];

export interface BreachClocksInput {
  /** RFC 3339 instant the controller became aware of the breach. */
  awareAt: string;
  personalDataBreach: boolean;
  regimes: BreachRegime[];
  /** US_STATE only, 30..60; omitted means 30. */
  usStateWindowDays?: number;
}

export interface BreachClock {
  regime: BreachRegime;
  recipient: string;
  basis: string;
  deadlineAt?: string;
  status: ClockStatus;
}

export interface BreachClocksResult {
  awareAt: string;
  asOf: string;
  items: BreachClock[];
}

export const AUDIT_RULE_TYPES = ["DUPLICATE", "KEYWORD", "LIST_SEARCH", "SAMPLING", "THRESHOLD", "WEEKEND_HOLIDAY", "AGGREGATE"] as const;
export type AuditRuleType = (typeof AUDIT_RULE_TYPES)[number];

/** One audit rule's configuration (FR-48.14); only the fields of its type are read. Amounts are decimal strings. */
export interface AuditSpec {
  type: AuditRuleType;
  keywords?: string[];
  watchList?: string[];
  /** THRESHOLD: category to limit. */
  limits?: Record<string, string>;
  currency?: string;
  /** AGGREGATE: limit per party per calendar month. */
  limit?: string;
  /** WEEKEND_HOLIDAY: YYYY-MM-DD. */
  holidays?: string[];
  sample?: { seed: string; percent?: number; everyK?: number };
}

export interface AuditDocument {
  id: string;
  partyId?: string;
  category?: string;
  amount?: string;
  currency?: string;
  /** RFC 3339 instant. */
  date?: string;
  texts?: string[];
  /** DUPLICATE: the invoice number. */
  number?: string;
}

export interface AuditFinding {
  key: string;
  ruleType: AuditRuleType;
  documentId: string;
  score: number;
  evidence: Record<string, string>;
}

export interface AuditEvaluateInput {
  policyId: string;
  version: number;
  spec: AuditSpec;
  documents: AuditDocument[];
}

export interface AuditEvaluateResult {
  count: number;
  findings: AuditFinding[];
  /** AUD-0701 when any document violates the policy. */
  code?: string;
}

export interface AuditViolation {
  id: string;
  policyId: string;
  ruleType: string;
  documentId: string;
  partyId?: string;
  caseId?: string;
}

export const CASE_CATEGORIES = ["DUPLICATE_PAYMENT", "POLICY_BREACH", "SAMPLED_REVIEW"] as const;
export type CaseCategory = (typeof CASE_CATEGORIES)[number];

export interface CaseDraft {
  key: string;
  category: CaseCategory;
  violationIds: string[];
  documentIds: string[];
}

export const CASE_STATUSES = ["OPEN", "IN_REVIEW", "RESOLVED_NO_ISSUE", "RESOLVED_ISSUE", "CLOSED", "ARCHIVED"] as const;
export type CaseStatus = (typeof CASE_STATUSES)[number];

export type CaseAction = "START_REVIEW" | "RESOLVE_NO_ISSUE" | "RESOLVE_ISSUE" | "CLOSE" | "ARCHIVE";

/** The audit case status machine (FR-48.15), as the service applies it. */
export const CASE_TRANSITIONS: Record<CaseStatus, Partial<Record<CaseAction, CaseStatus>>> = {
  OPEN: { START_REVIEW: "IN_REVIEW" },
  IN_REVIEW: { RESOLVE_NO_ISSUE: "RESOLVED_NO_ISSUE", RESOLVE_ISSUE: "RESOLVED_ISSUE" },
  RESOLVED_NO_ISSUE: { CLOSE: "CLOSED" },
  RESOLVED_ISSUE: { CLOSE: "CLOSED" },
  CLOSED: { ARCHIVE: "ARCHIVED" },
  ARCHIVED: {},
};

export const NO_ISSUE_CODES = ["NO_ISSUE", "FALSE_POSITIVE", "SAMPLE_CLEARED"] as const;
export const ISSUE_CODES = ["DUPLICATE_RECOVERED", "POLICY_CORRECTED", "REFERRED_FRAUD"] as const;

export interface CaseTransitionInput {
  status: CaseStatus;
  action: CaseAction;
  resolutionCode?: string;
  /** Decimal string; RESOLVE_ISSUE only. */
  recoveryAmount?: string;
  currency?: string;
  /** RFC 3339; ARCHIVE only. */
  closedAt?: string;
}

export interface CaseState {
  status: CaseStatus;
  resolutionCode?: string;
  recoveryAmount: string;
  currency?: string;
  closedAt?: string;
  asOf: string;
}

export const BREAK_GLASS_REASONS = ["INCIDENT_RESPONSE", "PRODUCTION_OUTAGE", "DATA_CORRECTION_APPROVED", "PERIOD_CLOSE_BLOCKER", "AUDIT_EVIDENCE_REQUEST"] as const;
export type BreakGlassReason = (typeof BREAK_GLASS_REASONS)[number];

/** A break-glass request of the signed-in user; the target defaults to them. */
export interface ElevatedPlanInput {
  targetUserId?: string;
  roles: string[];
  reasonCode: BreakGlassReason;
  ticketRef: string;
  requestedMinutes?: number;
}

export interface ElevatedPlan {
  requesterId: string;
  targetUserId: string;
  roles: string[];
  minutes: number;
  startsAt: string;
  endsAt: string;
  reviewDueAt: string;
  needsApproval: boolean;
  approverDuty?: "BreakGlassApprove" | "PlatformAdmin";
  requiresPostReview: boolean;
}

export const TDS_SECTIONS = ["194C", "194J", "194I", "194H", "194Q"] as const;
export type TdsSection = (typeof TDS_SECTIONS)[number];

export const PAYEE_TYPES = ["INDIVIDUAL_HUF", "COMPANY"] as const;
export type PayeeType = (typeof PAYEE_TYPES)[number];

/** Amounts are INR decimal strings, never numbers. */
export interface TdsInput {
  section: TdsSection;
  payeeType?: PayeeType;
  amount: string;
  ytdPaid?: string;
  ytdTaxed?: string;
  panPresent: boolean;
  lowerRatePct?: string;
}

export interface TdsResult {
  section: string;
  ratePct: string;
  taxableAmount: string;
  tdsAmount: string;
  currency: string;
  reason: "BELOW_THRESHOLD" | "THRESHOLD_CROSSED" | "PAN_MISSING_206AA" | "LOWER_DEDUCTION_CERTIFICATE" | "EXCESS_OVER_THRESHOLD";
  returnForm: string;
  challanForm: string;
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

/** Notification clocks of a confirmed personal-data breach (FR-48.29); nothing is stored. */
export function computeBreachClocks(input: BreachClocksInput): Promise<BreachClocksResult> {
  return json<BreachClocksResult>(`${securityBase}/incidents:breach-clocks`, { method: "POST", body: JSON.stringify(input) });
}

/** India TDS for one payment (FR-48.27); a calculation, nothing is posted. */
export function withholdTds(input: TdsInput): Promise<TdsResult> {
  return json<TdsResult>(`${localizationBase}/in/tds:withhold`, { method: "POST", body: JSON.stringify(input) });
}

/** Runs one audit rule over a document window (FR-48.14); nothing is stored. */
export function evaluateAuditPolicy(input: AuditEvaluateInput): Promise<AuditEvaluateResult> {
  return json<AuditEvaluateResult>(`${complianceBase}/audit-policies:evaluate`, { method: "POST", body: JSON.stringify(input) });
}

/** Groups unassigned violations into case drafts (FR-48.15); nothing is stored. */
export async function groupViolations(violations: AuditViolation[]): Promise<CaseDraft[]> {
  return (await json<{ cases: CaseDraft[] }>(`${complianceBase}/audit-cases:group`, { method: "POST", body: JSON.stringify({ violations }) })).cases ?? [];
}

/** Applies one audit case action at the server time (FR-48.15); nothing is stored. */
export function transitionAuditCase(input: CaseTransitionInput): Promise<CaseState> {
  return json<CaseState>(`${complianceBase}/audit-cases:transition`, { method: "POST", body: JSON.stringify(input) });
}

/** Plans the break-glass session the signed-in user's request would open (FR-48.23); nothing is stored. */
export function planElevatedSession(input: ElevatedPlanInput): Promise<ElevatedPlan> {
  return json<ElevatedPlan>(`${securityBase}/elevated-sessions:plan`, { method: "POST", body: JSON.stringify(input) });
}
