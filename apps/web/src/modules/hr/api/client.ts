// Client for the HR service (services/hr/api/openapi.yaml). Browser calls use
// /api/v1/hr/..., which the web BFF rewrites to the service.

export const base = "/api/v1/hr";

/** Minimum reveal reason length (HCM-PII-001, PRD 8.42.6). */
export const REVEAL_REASON_MIN = 10;

export type WorkerType = "EMPLOYEE" | "CONTRACTOR";
export type EmploymentType = "FULL_TIME" | "PART_TIME" | "TEMP" | "CONTRACTOR";
export type AssignmentKind = "PRIMARY" | "CONCURRENT" | "GLOBAL_HOST";
export type EmploymentStatus = "PENDING" | "ACTIVE" | "TERMINATED";

export interface Worker {
  id: string;
  personnelNumber: string;
  workerType: WorkerType;
  partyPersonId: string;
  legalName: string;
  preferredName?: string;
  userId?: string;
  createdAt: string;
  updatedAt: string;
}

export interface PIIView {
  nationalId: string;
  bankAccount?: string;
  masked: boolean;
}

export interface WorkerDetail extends Worker {
  pii: PIIView;
}

export interface NewWorker {
  personnelNumber: string;
  workerType: WorkerType;
  partyPersonId: string;
  legalName: string;
  preferredName?: string;
  userId?: string;
  nationalId: string;
  bankAccount?: string;
}

export interface Employment {
  id: string;
  workerId: string;
  legalEntityId: string;
  employmentType: EmploymentType;
  assignmentKind: AssignmentKind;
  homeLegalEntityId?: string;
  plannedEndDate?: string;
  hireDate: string;
  terminationDate?: string;
  validFrom: string;
  validTo?: string;
  status: EmploymentStatus;
}

export interface NewEmployment {
  legalEntityId: string;
  employmentType: EmploymentType;
  assignmentKind?: AssignmentKind;
  homeLegalEntityId?: string;
  plannedEndDate?: string;
  hireDate: string;
  terminationDate?: string;
}

export interface AsOfView {
  workerId: string;
  date: string;
  employments: Employment[];
  positionId: string | null;
  positionNumber: string | null;
  fteShare: string | null;
  managerWorkerId: string | null;
}

export interface Position {
  id: string;
  positionNumber: string;
  jobCode: string;
  departmentId: string;
  legalEntityId: string;
  /** Decimal string, 0.05 to 2.00. */
  fte: string;
  allowOverfill: boolean;
  validFrom: string;
  validTo?: string;
  reportsToPositionId?: string;
}

export interface PositionAssignment {
  id: string;
  positionId: string;
  workerId: string;
  employmentId: string;
  fteShare: string;
  isPrimary: boolean;
  validFrom: string;
  validTo?: string;
}

export interface PositionDetail extends Position {
  assignments: PositionAssignment[];
}

export interface NewPosition {
  positionNumber: string;
  jobCode: string;
  departmentId: string;
  legalEntityId: string;
  fte: string;
  allowOverfill: boolean;
  validFrom: string;
  validTo?: string;
  reportsToPositionId?: string;
}

export interface NewAssignment {
  workerId: string;
  employmentId: string;
  fteShare: string;
  isPrimary: boolean;
  validFrom: string;
  validTo?: string;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class HrApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "HrApiError";
  }
}

export interface Problem {
  status?: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof HrApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<HrApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new HrApiError(res.status, code, message, fields);
}

export async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

export async function page<T>(path: string, signal?: AbortSignal): Promise<Page<T>> {
  const body = await send<Page<T>>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new HrApiError(0, "COM-0001", "The response has no items");
  return body;
}

export function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(key, value);
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

export const id = (value: string) => encodeURIComponent(value);

/** A fresh Idempotency-Key; reuse one per form so a retried submit cannot create two. */
export function newKey(): string {
  return crypto.randomUUID();
}

export function post<T>(path: string, body: unknown, key: string): Promise<T> {
  return send<T>(path, { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) });
}

export function listWorkers(filter: { q?: string; cursor?: string } = {}, signal?: AbortSignal): Promise<Page<Worker>> {
  return page<Worker>(`/workers${query({ q: filter.q?.trim(), cursor: filter.cursor })}`, signal);
}

/** The worker with PII masked; the service audits every call. */
export function getWorker(workerId: string, signal?: AbortSignal): Promise<WorkerDetail> {
  return send<WorkerDetail>(`/workers/${id(workerId)}`, {}, signal);
}

/** Unmasked PII (hr.pii.read); the reason is recorded in the audit log. */
export function revealPII(workerId: string, reason: string): Promise<PIIView> {
  return send<PIIView>(`/workers/${id(workerId)}/pii${query({ reason: reason.trim() })}`);
}

export function createWorker(input: NewWorker, key: string = newKey()): Promise<Worker> {
  return post<Worker>("/workers", input, key);
}

export async function listEmployments(workerId: string, signal?: AbortSignal): Promise<Employment[]> {
  return (await page<Employment>(`/workers/${id(workerId)}/employments`, signal)).items;
}

export function createEmployment(workerId: string, input: NewEmployment, key: string = newKey()): Promise<Employment> {
  return post<Employment>(`/workers/${id(workerId)}/employments`, input, key);
}

export function workerAsOf(workerId: string, date: string, signal?: AbortSignal): Promise<AsOfView> {
  return send<AsOfView>(`/workers/${id(workerId)}/as-of${query({ date })}`, {}, signal);
}

export function listPositions(filter: { cursor?: string } = {}, signal?: AbortSignal): Promise<Page<Position>> {
  return page<Position>(`/positions${query({ cursor: filter.cursor })}`, signal);
}

export function getPosition(positionId: string, signal?: AbortSignal): Promise<PositionDetail> {
  return send<PositionDetail>(`/positions/${id(positionId)}`, {}, signal);
}

export function createPosition(input: NewPosition, key: string = newKey()): Promise<Position> {
  return post<Position>("/positions", input, key);
}

export function createAssignment(positionId: string, input: NewAssignment, key: string = newKey()): Promise<PositionAssignment> {
  return post<PositionAssignment>(`/positions/${id(positionId)}/assignments`, input, key);
}

// Absence (slice 2): plans, accrual, year end, balances and requests.

export type AbsenceUnit = "HOURS" | "DAYS";
export type AccrualFrequency = "MONTHLY" | "BIWEEKLY" | "ANNUAL" | "PER_HOUR_WORKED";
export type AccrualDay = "LAST_DAY" | "FIRST_DAY" | "PAYDATE";
export type Proration = "DAYS_IN_PERIOD" | "NONE";
export type AbsenceRequestStatus = "SUBMITTED" | "APPROVED" | "REJECTED" | "CANCELED";

export interface NewAbsencePlan {
  code: string;
  unit: AbsenceUnit;
  accrualFrequency: AccrualFrequency;
  /** Decimal strings, never numbers (no float reaches the ledger). */
  accrualRate: string;
  accrualDay: AccrualDay;
  proration: Proration;
  maxBalance: string;
  carryOverMax: string;
  carryOverExpiryMonths: number;
  /** MM-DD. */
  planYearStart: string;
  negativeBalanceAllowed: boolean;
  minServiceDays: number;
}

export interface AbsencePlan extends NewAbsencePlan {
  id: string;
  createdAt: string;
}

export interface AccrualPeriod {
  periodStart: string;
  periodEnd: string;
  workers: number;
  accrued: string;
  discarded: string;
}

export interface PlanClose {
  planYearStart: string;
  kind: "YEAR_END" | "CARRY_EXPIRY";
  workers: number;
  carried: string;
  expired: string;
}

export interface AbsencePlanDetail extends AbsencePlan {
  periods: AccrualPeriod[];
  closes: PlanClose[];
}

export interface AccrualRun {
  planId: string;
  periodStart: string;
  periodEnd: string;
  planYearStart: string;
  credited: number;
  alreadyAccrued: number;
  accrued: string;
  discarded: string;
  replayed: boolean;
}

export interface CloseRun {
  planId: string;
  planYearStart: string;
  kind: "YEAR_END" | "CARRY_EXPIRY";
  workers: number;
  alreadyClosed: number;
  carried: string;
  expired: string;
}

export interface BucketBalance {
  accrued: string;
  used: string;
  adjusted: string;
  expired: string;
  balance: string;
}

export interface AbsenceBalance {
  planId: string;
  planCode: string;
  unit: AbsenceUnit;
  planYearStart: string;
  carryOver: BucketBalance;
  current: BucketBalance;
  total: string;
}

export interface AbsenceBalances {
  workerId: string;
  asOf: string;
  balances: AbsenceBalance[];
}

export interface NewAbsenceRequest {
  workerId: string;
  planId: string;
  from: string;
  to: string;
  quantity: string;
  reason?: string;
}

export interface AbsenceRequest {
  id: string;
  workerId: string;
  personnelNumber: string;
  workerName: string;
  planId: string;
  planCode: string;
  unit: AbsenceUnit;
  from: string;
  to: string;
  quantity: string;
  status: AbsenceRequestStatus;
  reason?: string;
  requestedBy?: string;
  decidedBy?: string;
  decidedAt?: string;
  createdAt: string;
}

export interface RuleWarning {
  code: string;
  detail: string;
}

export interface AbsenceRequestResult extends AbsenceRequest {
  warnings: RuleWarning[];
}

function command<T>(path: string, body?: unknown): Promise<T> {
  return send<T>(path, { method: "POST", body: body === undefined ? undefined : JSON.stringify(body) });
}

export function listAbsencePlans(filter: { cursor?: string } = {}, signal?: AbortSignal): Promise<Page<AbsencePlan>> {
  return page<AbsencePlan>(`/absence/plans${query({ cursor: filter.cursor })}`, signal);
}

export function getAbsencePlan(planId: string, signal?: AbortSignal): Promise<AbsencePlanDetail> {
  return send<AbsencePlanDetail>(`/absence/plans/${id(planId)}`, {}, signal);
}

export function createAbsencePlan(input: NewAbsencePlan, key: string = newKey()): Promise<AbsencePlan> {
  return post<AbsencePlan>("/absence/plans", input, key);
}

/** Accrues one whole period. Keyed per (plan, period, worker) by the service, so a retry needs no Idempotency-Key. */
export function runAccrual(planId: string, periodStart: string, periodEnd: string): Promise<AccrualRun> {
  return command<AccrualRun>("/absence/accrue", { planId, periodStart, periodEnd });
}

export function closePlanYear(planId: string, planYearStart: string): Promise<CloseRun> {
  return command<CloseRun>(`/absence/plans/${id(planId)}/year-end`, { planYearStart });
}

export function expireCarryOver(planId: string, planYearStart: string): Promise<CloseRun> {
  return command<CloseRun>(`/absence/plans/${id(planId)}/carry-over-expiry`, { planYearStart });
}

export function getAbsenceBalances(workerId: string, asOf: string, signal?: AbortSignal): Promise<AbsenceBalances> {
  return send<AbsenceBalances>(`/workers/${id(workerId)}/absence/balances${query({ asOf })}`, {}, signal);
}

export function listAbsenceRequests(
  filter: { status?: AbsenceRequestStatus | ""; workerId?: string; cursor?: string } = {},
  signal?: AbortSignal,
): Promise<Page<AbsenceRequest>> {
  return page<AbsenceRequest>(`/absence/requests${query({ status: filter.status, workerId: filter.workerId, cursor: filter.cursor })}`, signal);
}

export function createAbsenceRequest(input: NewAbsenceRequest, key: string = newKey()): Promise<AbsenceRequestResult> {
  return post<AbsenceRequestResult>("/absence/requests", input, key);
}

/** Replaying the decision a request already has is a no-op on the service. */
export function decideAbsenceRequest(requestId: string, decision: "APPROVE" | "REJECT"): Promise<AbsenceRequest> {
  return command<AbsenceRequest>(`/absence/requests/${id(requestId)}/decide`, { decision });
}

export function cancelAbsenceRequest(requestId: string): Promise<AbsenceRequest> {
  return command<AbsenceRequest>(`/absence/requests/${id(requestId)}/cancel`);
}

// Benefits (FR-42.8, BR-42.6). Rates are decimal strings in the plan currency.

export type BenefitPlanType = "MEDICAL" | "DENTAL" | "VISION" | "LIFE" | "RETIREMENT_401K" | "HSA" | "FSA" | "OTHER";
export type RateBasis = "PER_PAY_PERIOD" | "MONTHLY";
export type LifeEventKind = "MARRIAGE" | "BIRTH" | "ADOPTION" | "DIVORCE" | "LOSS_OF_COVERAGE" | "OTHER";
export type EnrollmentEndReason = "TERMINATION" | "LIFE_EVENT" | "PLAN_END";
export type EnrollmentStatus = "PENDING" | "ACTIVE" | "ENDED";

export interface BenefitOption {
  code: string;
  coverageLevel: string;
  employeeRate: string;
  employerRate: string;
  rateBasis: RateBasis;
}

export interface NewBenefitPlan {
  code: string;
  planType: BenefitPlanType;
  providerPartyId?: string;
  currency: string;
  eligibilityRule: string;
  enrollmentStart: string;
  enrollmentEnd: string;
  waitingDays: number;
  preTax: boolean;
  payrollDeductionCode?: string;
  options: BenefitOption[];
}

export interface BenefitPlan extends NewBenefitPlan {
  id: string;
  createdAt: string;
}

export interface BenefitSimulationRow {
  workerId: string;
  personnelNumber: string;
  legalName: string;
  employmentId?: string;
  legalEntityId?: string;
  employmentType?: EmploymentType;
  serviceDays?: number;
  fte?: string;
  eligible: boolean;
  reason: string;
}

export interface BenefitSimulation {
  planId: string;
  planCode: string;
  rule: string;
  asOf: string;
  total: number;
  eligible: number;
  rows: BenefitSimulationRow[];
}

export interface LifeEvent {
  id: string;
  workerId: string;
  kind: LifeEventKind;
  eventDate: string;
  /** The last effective date an enrollment may take because of this event. */
  windowEnd: string;
  recordedBy?: string;
  createdAt: string;
}

export interface NewLifeEvent {
  workerId: string;
  kind: LifeEventKind;
  eventDate: string;
}

export interface BenefitEnrollment {
  id: string;
  workerId: string;
  planId: string;
  planCode: string;
  planType: BenefitPlanType;
  optionCode: string;
  coverageLevel: string;
  validFrom: string;
  validTo?: string;
  status: EnrollmentStatus;
  lifeEventId?: string;
  dependentCount: number;
  endReason?: EnrollmentEndReason;
  cobraQualifyingEvent: boolean;
  employeeRate: string;
  employerRate: string;
  currency: string;
  rateBasis: RateBasis;
  preTax: boolean;
  payrollDeductionCode?: string;
}

export interface NewBenefitEnrollment {
  workerId: string;
  planId: string;
  optionCode: string;
  effectiveDate: string;
  lifeEventId?: string;
  dependentCount: number;
}

export interface WorkerBenefits {
  workerId: string;
  enrollments: BenefitEnrollment[];
  lifeEvents: LifeEvent[];
}

export function listBenefitPlans(filter: { cursor?: string } = {}, signal?: AbortSignal): Promise<Page<BenefitPlan>> {
  return page<BenefitPlan>(`/benefits/plans${query({ cursor: filter.cursor })}`, signal);
}

export function getBenefitPlan(planId: string, signal?: AbortSignal): Promise<BenefitPlan> {
  return send<BenefitPlan>(`/benefits/plans/${id(planId)}`, {}, signal);
}

export function createBenefitPlan(input: NewBenefitPlan, key: string = newKey()): Promise<BenefitPlan> {
  return post<BenefitPlan>("/benefits/plans", input, key);
}

/** Evaluates the plan for the whole workforce as of a date; writes nothing. */
export function simulateBenefitPlan(planId: string, asOf: string, signal?: AbortSignal): Promise<BenefitSimulation> {
  return send<BenefitSimulation>(`/benefits/plans/${id(planId)}/simulate${query({ asOf })}`, {}, signal);
}

export function getWorkerBenefits(workerId: string, signal?: AbortSignal): Promise<WorkerBenefits> {
  return send<WorkerBenefits>(`/workers/${id(workerId)}/benefits`, {}, signal);
}

export function recordLifeEvent(input: NewLifeEvent, key: string = newKey()): Promise<LifeEvent> {
  return post<LifeEvent>("/benefits/life-events", input, key);
}

export function createBenefitEnrollment(input: NewBenefitEnrollment, key: string = newKey()): Promise<BenefitEnrollment> {
  return post<BenefitEnrollment>("/benefits/enrollments", input, key);
}

/** Ending again with the same last day and reason is a no-op on the service. */
export function endBenefitEnrollment(enrollmentId: string, lastDay: string, reason: EnrollmentEndReason): Promise<BenefitEnrollment> {
  return command<BenefitEnrollment>(`/benefits/enrollments/${id(enrollmentId)}/end`, { lastDay, reason });
}

// Skills and certifications (FR-42.10), skill mapping and gap analysis (FR-42.18).

/** Skill and certificate codes: 1 to 20 of A-Z, 0-9, hyphen and underscore. */
export const SKILL_CODE = /^[A-Z0-9][A-Z0-9_-]{0,19}$/;
/** The widest rating scale a skill may declare. */
export const MAX_SKILL_SCALE = 10;

export interface Skill {
  id: string;
  code: string;
  name: string;
  scaleMax: number;
  createdAt: string;
}

export interface NewSkill {
  code: string;
  name: string;
  scaleMax: number;
}

export interface WorkerSkill {
  workerId: string;
  skillId: string;
  skillCode: string;
  skillName: string;
  scaleMax: number;
  rating: number;
  assessedOn: string;
  updatedAt: string;
}

export interface NewRating {
  skillCode: string;
  rating: number;
  assessedOn: string;
}

export interface Certification {
  id: string;
  workerId: string;
  code: string;
  name: string;
  issuer?: string;
  issuedOn: string;
  /** Exclusive: the certificate is no longer valid on this day. Absent when it never expires. */
  expiresOn?: string;
  valid: boolean;
}

export interface NewCertification {
  code: string;
  name: string;
  issuer?: string;
  issuedOn: string;
  expiresOn?: string;
  validityMonths?: number;
}

export interface CertificationList {
  asOf: string;
  items: Certification[];
}

export interface ExpiringCertification extends Certification {
  personnelNumber: string;
  legalName: string;
  daysLeft: number;
}

export interface ExpiringCertificationPage extends Page<ExpiringCertification> {
  asOf: string;
  withinDays: number;
}

export interface SkillRequirement {
  positionId: string;
  skillId: string;
  skillCode: string;
  skillName: string;
  scaleMax: number;
  minRating: number;
}

/** One requirement next to the worker's rating; delta = actual - required, negative is a shortfall. */
export interface SkillGapRow {
  skill: string;
  required: number;
  actual: number;
  delta: number;
}

export interface SkillGap {
  workerId: string;
  positionId?: string;
  jobId?: string;
  met: boolean;
  comparison: SkillGapRow[];
  gaps: SkillGapRow[];
}

export interface SkillMatch {
  workerId: string;
  personnelNumber: string;
  legalName: string;
  skills: SkillGapRow[];
}

export interface SkillSearchResult {
  asOf: string;
  items: SkillMatch[];
  truncated: boolean;
}

export interface SkillSearchQuery {
  /** Comma-separated CODE or CODE:min terms. */
  skills?: string;
  minRating?: string;
  certs?: string;
  asOf?: string;
}

export function listSkills(filter: { cursor?: string } = {}, signal?: AbortSignal): Promise<Page<Skill>> {
  return page<Skill>(`/skills${query({ cursor: filter.cursor })}`, signal);
}

/** Every skill in the catalog, following the cursor (the catalog is short; pickers need all of it). */
export async function listAllSkills(signal?: AbortSignal): Promise<Skill[]> {
  const out: Skill[] = [];
  let cursor: string | undefined;
  do {
    const next = await listSkills({ cursor }, signal);
    out.push(...next.items);
    cursor = next.nextCursor;
  } while (cursor);
  return out;
}

export function createSkill(input: NewSkill, key: string = newKey()): Promise<Skill> {
  return post<Skill>("/skills", input, key);
}

export async function listWorkerSkills(workerId: string, signal?: AbortSignal): Promise<WorkerSkill[]> {
  return (await page<WorkerSkill>(`/workers/${id(workerId)}/skills`, signal)).items;
}

/** Recording the same rating and date again is a no-op on the service, so no key is needed. */
export function rateWorkerSkill(workerId: string, input: NewRating): Promise<WorkerSkill> {
  return command<WorkerSkill>(`/workers/${id(workerId)}/skills`, input);
}

export function listCertifications(workerId: string, asOf?: string, signal?: AbortSignal): Promise<CertificationList> {
  return send<CertificationList>(`/workers/${id(workerId)}/certifications${query({ asOf })}`, {}, signal);
}

export function issueCertification(workerId: string, input: NewCertification, key: string = newKey()): Promise<Certification> {
  return post<Certification>(`/workers/${id(workerId)}/certifications`, input, key);
}

export function listExpiringCertifications(
  filter: { withinDays?: string; asOf?: string; cursor?: string } = {},
  signal?: AbortSignal,
): Promise<ExpiringCertificationPage> {
  return send<ExpiringCertificationPage>(
    `/certifications/expiring${query({ withinDays: filter.withinDays, asOf: filter.asOf, cursor: filter.cursor })}`,
    {},
    signal,
  );
}

export async function listSkillRequirements(positionId: string, signal?: AbortSignal): Promise<SkillRequirement[]> {
  return (await page<SkillRequirement>(`/positions/${id(positionId)}/skill-requirements`, signal)).items;
}

/** Sets or replaces one skill's minimum, so a retry is safe without a key. */
export function setSkillRequirement(positionId: string, skillCode: string, minRating: number): Promise<SkillRequirement> {
  return command<SkillRequirement>(`/positions/${id(positionId)}/skill-requirements`, { skillCode, minRating });
}

export async function removeSkillRequirement(positionId: string, skillCode: string): Promise<void> {
  const res = await fetch(`${base}/positions/${id(positionId)}/skill-requirements/${id(skillCode)}`, {
    method: "DELETE",
    headers: { Accept: "application/json" },
    credentials: "same-origin",
  });
  if (!res.ok) throw await toError(res);
}

export function skillGap(workerId: string, positionId: string, signal?: AbortSignal): Promise<SkillGap> {
  return send<SkillGap>(`/workers/${id(workerId)}/skill-gap${query({ positionId })}`, {}, signal);
}

/** The worker against a job's skill profile (FR-42.18); a skill the worker is not rated on counts as 0. */
export function skillGapForJob(workerId: string, jobId: string, signal?: AbortSignal): Promise<SkillGap> {
  return send<SkillGap>(`/workers/${id(workerId)}/skill-gap${query({ jobId })}`, {}, signal);
}

export interface JobSkillRequirement {
  jobId: string;
  skillId: string;
  skillCode: string;
  skillName: string;
  scaleMax: number;
  minRating: number;
}

export interface JobWorkerGap {
  workerId: string;
  personnelNumber: string;
  legalName: string;
  met: boolean;
  gaps: SkillGapRow[];
}

/** Everyone who holds the job on asOf, each with only the unmet requirements; truncated means more exist. */
export interface JobSkillGaps {
  jobId: string;
  jobCode: string;
  asOf: string;
  items: JobWorkerGap[];
  truncated: boolean;
}

export async function listJobSkillRequirements(jobId: string, signal?: AbortSignal): Promise<JobSkillRequirement[]> {
  return (await page<JobSkillRequirement>(`/jobs/${id(jobId)}/skill-requirements`, signal)).items;
}

/** Sets or replaces one skill's minimum, so a retry is safe without a key. */
export function setJobSkillRequirement(jobId: string, skillCode: string, minRating: number): Promise<JobSkillRequirement> {
  return command<JobSkillRequirement>(`/jobs/${id(jobId)}/skill-requirements`, { skillCode, minRating });
}

export async function removeJobSkillRequirement(jobId: string, skillCode: string): Promise<void> {
  const res = await fetch(`${base}/jobs/${id(jobId)}/skill-requirements/${id(skillCode)}`, {
    method: "DELETE",
    headers: { Accept: "application/json" },
    credentials: "same-origin",
  });
  if (!res.ok) throw await toError(res);
}

export function jobSkillGaps(jobId: string, asOf?: string, signal?: AbortSignal): Promise<JobSkillGaps> {
  return send<JobSkillGaps>(`/jobs/${id(jobId)}/skill-gaps${query({ asOf })}`, {}, signal);
}

export function searchSkills(q: SkillSearchQuery, signal?: AbortSignal): Promise<SkillSearchResult> {
  return send<SkillSearchResult>(
    `/skills/search${query({ skills: q.skills?.trim(), minRating: q.minRating?.trim(), certs: q.certs?.trim(), asOf: q.asOf })}`,
    {},
    signal,
  );
}

// Courses (slice 5): catalog, sessions, registrations and completion (FR-42.14, BR-42.9).

export type RegistrationStatus = "REQUESTED" | "CONFIRMED" | "WAITLISTED" | "ATTENDED" | "NO_SHOW" | "CANCELED";
export type CourseResult = "PASS" | "FAIL";

/** Waitlist size a session takes when the form leaves it empty (8.42.13 default). */
export const DEFAULT_WAITLIST = 5;
export const MAX_WAITLIST = 100;
export const MAX_SEATS = 10000;
export const MAX_VALIDITY_MONTHS = 240;

export interface CourseSkillOutcome {
  skillId: string;
  skillCode: string;
  skillName: string;
  rating: number;
}

export interface Course {
  id: string;
  code: string;
  name: string;
  courseGroup?: string;
  skillOutcomes: CourseSkillOutcome[];
  certificationCode?: string;
  certificationName?: string;
  /** 0: the certificate does not expire. */
  validityMonths: number;
  requiresManagerApproval: boolean;
  createdAt: string;
}

export interface NewCourse {
  code: string;
  name: string;
  courseGroup?: string;
  skillOutcomes: { skillCode: string; rating: number }[];
  certificationCode?: string;
  certificationName?: string;
  validityMonths?: number;
  requiresManagerApproval: boolean;
}

export interface CourseSession {
  id: string;
  courseId: string;
  courseCode: string;
  courseName: string;
  startsAt: string;
  endsAt: string;
  location?: string;
  virtualUrl?: string;
  instructorWorkerId?: string;
  instructorPartyId?: string;
  capacity: number;
  waitlistCapacity: number;
  /** Decimal string at the currency's minor units. */
  costPerAttendee?: string;
  currency?: string;
  confirmed: number;
  waitlisted: number;
  requested: number;
  seatsLeft: number;
  createdAt: string;
}

export interface NewCourseSession {
  startsAt: string;
  endsAt: string;
  location?: string;
  virtualUrl?: string;
  instructorWorkerId?: string;
  capacity: number;
  waitlistCapacity?: number;
  costPerAttendee?: string;
  currency?: string;
}

export interface CourseRegistration {
  id: string;
  sessionId: string;
  workerId: string;
  personnelNumber: string;
  legalName: string;
  status: RegistrationStatus;
  result?: CourseResult;
  score?: string;
  completedOn?: string;
  queuedAt: string;
}

export interface CourseRegistrationResult extends CourseRegistration {
  warnings: RuleWarning[];
}

export interface CompletionLine {
  workerId: string;
  outcome: "ATTENDED" | "NO_SHOW";
  result?: CourseResult;
  score?: string;
}

export interface CourseCompletion {
  items: CourseRegistration[];
  changed: number;
  passed: number;
}

export function listCourses(filter: { cursor?: string } = {}, signal?: AbortSignal): Promise<Page<Course>> {
  return page<Course>(`/courses${query({ cursor: filter.cursor })}`, signal);
}

export function getCourse(courseId: string, signal?: AbortSignal): Promise<Course> {
  return send<Course>(`/courses/${id(courseId)}`, {}, signal);
}

export function createCourse(input: NewCourse, key: string = newKey()): Promise<Course> {
  return post<Course>("/courses", input, key);
}

export async function listCourseSessions(courseId: string, signal?: AbortSignal): Promise<CourseSession[]> {
  return (await page<CourseSession>(`/courses/${id(courseId)}/sessions`, signal)).items;
}

export function createCourseSession(courseId: string, input: NewCourseSession, key: string = newKey()): Promise<CourseSession> {
  return post<CourseSession>(`/courses/${id(courseId)}/sessions`, input, key);
}

export function getCourseSession(sessionId: string, signal?: AbortSignal): Promise<CourseSession> {
  return send<CourseSession>(`/courses/sessions/${id(sessionId)}`, {}, signal);
}

export async function listCourseRegistrations(sessionId: string, signal?: AbortSignal): Promise<CourseRegistration[]> {
  return (await page<CourseRegistration>(`/courses/sessions/${id(sessionId)}/registrations`, signal)).items;
}

/** One registration per worker and session, so registering again returns it; no key is needed. */
export function registerForCourseSession(sessionId: string, workerId: string): Promise<CourseRegistrationResult> {
  return command<CourseRegistrationResult>(`/courses/sessions/${id(sessionId)}/registrations`, { workerId });
}

export function approveCourseRegistration(registrationId: string): Promise<CourseRegistration> {
  return command<CourseRegistration>(`/courses/registrations/${id(registrationId)}/approve`);
}

export function cancelCourseRegistration(registrationId: string): Promise<CourseRegistration> {
  return command<CourseRegistration>(`/courses/registrations/${id(registrationId)}/cancel`);
}

/** The same outcome again changes nothing on the service, so a retry is safe without a key. */
export function completeCourseSession(sessionId: string, results: CompletionLine[]): Promise<CourseCompletion> {
  return command<CourseCompletion>(`/courses/sessions/${id(sessionId)}/complete`, { results });
}
