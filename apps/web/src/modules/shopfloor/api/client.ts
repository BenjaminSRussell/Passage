// Client for the shopfloor service (services/shopfloor/api/openapi.yaml).
// Browser calls use /api/v1/shopfloor/..., which the web BFF rewrites to the service.

export const base = "/api/v1/shopfloor";

/** The server accepts at most this many registrations per batch. */
export const MAX_BATCH = 500;

/** Longest range pay:calculate and pay:approve accept, in days. */
export const MAX_PAY_RANGE_DAYS = 28;

export type RegistrationKind = "clock-in" | "clock-out" | "job-start" | "job-stop" | "absence" | "indirect";

export interface Worker {
  id: string;
  partyId: string;
  name: string;
  badgeId: string | null;
  userId: string | null;
  /** Decimal string, money per hour. */
  costRate: string;
  currency: string;
  active: boolean;
  version: number;
  createdAt: string;
  /** Time profile the worker's days are calculated with; null means the defaults (FR-25.7). */
  profileId: string | null;
  /** Pay agreement the worker's overtime follows; null means the defaults. */
  payAgreementId: string | null;
  /** Resource whose jobs make up the worker's own dispatch list ("my jobs"); null means every job. */
  defaultResourceId: string | null;
  /** When the worker was last deactivated; null or absent while active. */
  deactivatedAt?: string | null;
  deactivatedBy?: string | null;
  deactivationReason?: string | null;
}

export interface NewWorker {
  partyId: string;
  name: string;
  badgeId?: string | null;
  userId?: string | null;
  costRate: string;
  currency: string;
}

export interface RegistrationInput {
  /** Client-generated UUID; the server treats a replayed id as a duplicate. */
  id: string;
  workerId: string;
  kind: RegistrationKind;
  jobId?: string | null;
  bundleId?: string | null;
  /** Required for kind absence (an active absence code); an absence also needs stoppedAt (FR-25.2). */
  absenceCodeId?: string | null;
  qtyGood?: string | null;
  qtyScrap?: string | null;
  scrapReasonId?: string | null;
  startedAt: string;
  stoppedAt?: string | null;
  deviceId?: string | null;
  offline?: boolean;
  note?: string | null;
}

/** Review flags on a registration (BR-25.7, FR-25.3). */
export type RegistrationFlag = "job-finished-conflict" | "auto-stopped" | "auto-stop-capped" | "production-finished" | "auto-clock-out";

/** A job the server stopped because the worker started another one (FR-25.3, SF-0002). */
export interface AutoStop {
  /** The job-stop registration the server wrote. */
  registrationId: string;
  jobId: string;
  bundleId: string | null;
  startedAt: string;
  stoppedAt: string;
  /** Decimal string; a bundled job's share in 0.01 h (BR-25.1). */
  hours: string;
  flags: RegistrationFlag[];
}

export interface RegistrationWarning {
  code: "SF-0002" | "SF-0004";
  message: string;
}

export interface RegistrationResult {
  id: string;
  status: "accepted" | "duplicate";
  /** job-finished-conflict on a start for a job the server already finished (SF-0004). */
  flags: RegistrationFlag[];
  autoStopped: AutoStop[];
  warnings: RegistrationWarning[];
}

export interface Registration {
  id: string;
  workerId: string;
  kind: RegistrationKind;
  jobId: string | null;
  bundleId: string | null;
  /** The absence code of an absence registration; null for other kinds. */
  absenceCodeId?: string | null;
  qtyGood: string;
  qtyScrap: string;
  scrapReasonId: string | null;
  startedAt: string;
  stoppedAt: string | null;
  hours: string | null;
  serverReceivedAt: string;
  deviceId: string | null;
  offline: boolean;
  note: string | null;
  /** Device clock correction applied to the times, server minus device, in seconds (BR-25.7); null when none. */
  clockOffsetSeconds: number | null;
  flags: RegistrationFlag[];
}

export interface PayRange {
  workerId: string;
  /** YYYY-MM-DD, inclusive. */
  from: string;
  /** YYYY-MM-DD, inclusive, at most 28 days after from. */
  to: string;
  timeZone?: string;
}

export interface PayLine {
  type: string;
  hours: string;
  multiplier: string;
  amount: string;
  currency: string;
  /** Worker-day revision that added the line; 0 or absent for the original calculation (AC-25.8). */
  revision?: number;
  /** A line with negative hours and amount that cancels an earlier line. */
  reversal?: boolean;
}

export type DayFlag = "missing-clock-out" | "orphan-clock-out" | "absence-overlap";

export interface PayDay {
  workDate: string;
  hours: string;
  flags: DayFlag[];
  payLines: PayLine[];
}

export interface PayCalculateResponse {
  workerId: string;
  from: string;
  to: string;
  timeZone: string;
  days: PayDay[];
}

export interface ApprovedDay {
  workDate: string;
  hours: string;
  status: "calculated" | "approved";
  newlyApproved: boolean;
  payLines: PayLine[];
}

export interface PayApproveResponse {
  workerId: string;
  from: string;
  to: string;
  days: ApprovedDay[];
}

export type DayStatus = "calculated" | "approved";

export interface WorkerDay {
  workerId: string;
  workerName: string;
  workDate: string;
  hours: string;
  flags: DayFlag[];
  status: DayStatus;
  timeZone: string;
  calculatedAt: string;
  approvedBy: string | null;
  approvedAt: string | null;
  /** Supervisor revisions of an approved day; its pay lines then include reversal lines (AC-25.8). */
  revision?: number;
  revisedBy?: string | null;
  revisedAt?: string | null;
  payLines: PayLine[];
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class ShopfloorApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ShopfloorApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ShopfloorApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<ShopfloorApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new ShopfloorApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new ShopfloorApiError(0, "COM-0001", "The response has no items");
  }
  return body.items;
}

function query(params: Record<string, string | number | boolean | undefined>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(key, String(value));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

export function newKey(): string {
  return crypto.randomUUID();
}

function post<T>(path: string, body: unknown, key?: string): Promise<T> {
  const headers: Record<string, string> = {};
  if (key) headers["Idempotency-Key"] = key;
  return send<T>(path, { method: "POST", headers, body: JSON.stringify(body) });
}

export function listWorkers(filter: { active?: boolean } = {}, signal?: AbortSignal): Promise<Worker[]> {
  return items<Worker>(`/workers${query({ active: filter.active })}`, signal);
}

export function getWorker(id: string, signal?: AbortSignal): Promise<Worker> {
  return send<Worker>(`/workers/${encodeURIComponent(id)}`, {}, signal);
}

/** Creates a worker; reuse one key per form so a retried submit cannot create two. */
export function createWorker(input: NewWorker, key: string = newKey()): Promise<Worker> {
  return post<Worker>("/workers", input, key);
}

export function listRegistrations(
  filter: { workerId: string; from: string; to: string; timeZone?: string },
  signal?: AbortSignal,
): Promise<Registration[]> {
  return items<Registration>(`/registrations${query(filter)}`, signal);
}

/** Every registration of one bundle, oldest first (BR-25.1). */
export function listBundle(bundleId: string, signal?: AbortSignal): Promise<Registration[]> {
  return items<Registration>(`/registrations${query({ bundleId })}`, signal);
}

export function listWorkerDays(
  filter: { from: string; to: string; workerId?: string; status?: DayStatus | "" },
  signal?: AbortSignal,
): Promise<WorkerDay[]> {
  return items<WorkerDay>(`/worker-days${query(filter)}`, signal);
}

/**
 * Sends one batch of at most MAX_BATCH registrations (each item is idempotent by its id).
 * A kiosk replaying its offline queue passes its own clock as `deviceSentAt`, so the
 * server can correct a device clock more than 5 minutes off (BR-25.7).
 */
export async function createRegistrations(batch: RegistrationInput[], deviceSentAt?: string): Promise<RegistrationResult[]> {
  if (batch.length < 1 || batch.length > MAX_BATCH) {
    throw new RangeError(`a batch holds 1 to ${MAX_BATCH} registrations`);
  }
  const res = await post<{ items: RegistrationResult[] }>("/registrations", deviceSentAt ? { deviceSentAt, items: batch } : { items: batch });
  return res.items;
}

export function calculatePay(range: PayRange, key: string = newKey()): Promise<PayCalculateResponse> {
  return post<PayCalculateResponse>("/pay:calculate", range, key);
}

export function approvePay(range: PayRange, key: string = newKey()): Promise<PayApproveResponse> {
  return post<PayApproveResponse>("/pay:approve", range, key);
}

/** Splits an offline queue into batches the server accepts (at most MAX_BATCH each). */
export function chunk<T>(list: readonly T[], size: number = MAX_BATCH): T[][] {
  if (size < 1) {
    throw new RangeError("chunk size must be at least 1");
  }
  const out: T[][] = [];
  for (let i = 0; i < list.length; i += size) {
    out.push(list.slice(i, i + size));
  }
  return out;
}

// --- Supervisor corrections and pay revision (slice 9, FR-25.8, AC-25.8) ---

/** The audit record of one supervisor edit of a registration. */
export interface Correction {
  id: string;
  registrationId: string;
  workerId: string;
  reason: string;
  oldStartedAt: string;
  oldStoppedAt: string | null;
  oldHours: string | null;
  newStartedAt: string;
  newStoppedAt: string | null;
  newHours: string | null;
  correctedBy: string;
  correctedAt: string;
}

export interface CorrectionInput {
  startedAt?: string;
  stoppedAt?: string;
  /** 1 to 500 characters, kept for audit. */
  reason: string;
}

export interface RevisedDay {
  workDate: string;
  status: DayStatus;
  revision: number;
  changed: boolean;
  /** Lines with negative hours and amount that cancel approved lines. */
  reversedLines: PayLine[];
  addedLines: PayLine[];
}

export interface PayRevision {
  workerId: string;
  from: string;
  to: string;
  reason: string;
  days: RevisedDay[];
}

export function correctRegistration(id: string, input: CorrectionInput, key: string = newKey()): Promise<Correction> {
  return post<Correction>(`/registrations/${encodeURIComponent(id)}:correct`, input, key);
}

export function listCorrections(
  filter: { from: string; to: string; workerId?: string; timeZone?: string },
  signal?: AbortSignal,
): Promise<Correction[]> {
  return items<Correction>(`/registration-corrections${query(filter)}`, signal);
}

/** Recalculates a range and revises approved days with reversal lines (SF-0129 when nothing differs). */
export function revisePay(input: PayRange & { reason: string }, key: string = newKey()): Promise<PayRevision> {
  return post<PayRevision>("/pay:revise", input, key);
}

// --- Auto clock-out (slice 10, D2h) ---

/** Hours a clock-in may stay open before the sweep closes it (SF-0130 outside the bounds). */
export const AUTO_CLOCK_OUT_HOURS = { min: 4, max: 24, default: 16 } as const;

/** A clock-out the auto clock-out sweep wrote, for supervisor review. */
export interface AutoClockOutReview {
  registrationId: string;
  workerId: string;
  workerName: string;
  clockedInAt: string | null;
  clockedOutAt: string;
  closedAt: string;
  /** A supervisor confirmed or corrected the clock-out; until then its day cannot be approved (BR-25.3). */
  reviewed: boolean;
}

export interface AutoClosed {
  registrationId: string;
  workerId: string;
  clockInId: string;
  clockedInAt: string;
  clockedOutAt: string;
}

export interface AutoClockOutRun {
  afterHours: number;
  items: AutoClosed[];
}

export function listAutoClockOuts(
  filter: { from: string; to: string; workerId?: string; timeZone?: string },
  signal?: AbortSignal,
): Promise<AutoClockOutReview[]> {
  return items<AutoClockOutReview>(`/auto-clock-outs${query(filter)}`, signal);
}

/** Closes the clock-ins left open for more than afterHours (the hourly job, run by hand). */
export function runAutoClockOut(afterHours: number): Promise<AutoClockOutRun> {
  return post<AutoClockOutRun>(`/registrations:auto-clock-out${query({ afterHours })}`, {});
}

// --- Machines, downtime, andon, OEE and payroll export (slice 2) ---

/** Longest window GET /oee accepts. */
export const MAX_OEE_SPAN_HOURS = 7 * 24;

/** Longest range pay:export accepts, in days. */
export const MAX_EXPORT_RANGE_DAYS = 31;

export type MachineState = "RUNNING" | "IDLE" | "DOWN" | "SETUP" | "OFF";

export interface NewMachine {
  /** Edge machine code, unique per tenant (e.g. M-101). */
  machineId: string;
  resourceId: string;
  siteId?: string | null;
  /** Decimal seconds per unit, above 0. */
  idealCycleSeconds: string;
  /** RUNNING with no count for longer than this is idle (BR-25.6); 30 to 3600, default 300. */
  idleThresholdSeconds?: number;
}

/** Bounds and default of the machine idle threshold, in seconds (BR-25.6). */
export const IDLE_THRESHOLD = { min: 30, max: 3600, default: 300 } as const;

export interface Machine {
  id: string;
  machineId: string;
  resourceId: string;
  siteId: string | null;
  idealCycleSeconds: string;
  idleThresholdSeconds: number;
  lastSeq: number;
  createdAt: string;
}

export type DowntimeCategory = "planned" | "unplanned";

export interface Downtime {
  id: string;
  machineId: string;
  resourceId: string;
  startedAt: string;
  endedAt: string | null;
  reasonCode: string | null;
  category: DowntimeCategory;
  /** "down" for a reported downtime, "idle" for idle time the andon sweep found (BR-25.6). */
  cause: "down" | "idle";
  andonRaised: boolean;
}

export interface Andon {
  id: string;
  resourceId: string;
  siteId: string | null;
  kind: AndonKind;
  severity: "info" | "warning" | "error";
  downtimeId: string | null;
  /** The job whose failed check raised a quality-fail alert. */
  jobId: string | null;
  /** One line on what raised a scrap-rate or quality-fail alert. */
  detail: string | null;
  startedAt: string;
}

/** One open alert per resource and kind (BR-25.10). */
export type AndonKind = "downtime" | "scrap-rate" | "quality-fail";

/**
 * Clears a quality-fail alert with a reason: the shop floor supervisor's authority to override a
 * failed check (FR-25.5). Enforced by the service; the andon sweep is the scheduler's job and has
 * no page action.
 */
export const DUTY_CHECK_OVERRIDE = "shopfloor.check.override";

export type OeeBucketSize = "5m" | "15m" | "60m";

export interface OeeBucket {
  resourceId: string;
  bucketStart: string;
  plannedMinutes: string;
  runMinutes: string;
  downMinutes: string;
  totalCount: number;
  goodCount: number;
  availability: string;
  performance: string;
  quality: string;
  oee: string;
  overRate: boolean;
  sourceEvents: number;
}

export interface PayExport {
  batchId: string;
  fromDate: string;
  toDate: string;
  lineCount: number;
  createdBy: string;
  createdAt: string;
  /** Present on create and when one batch is read. */
  csv?: string;
}

export function listMachines(signal?: AbortSignal): Promise<Machine[]> {
  return items<Machine>("/machines", signal);
}

/** Registers a machine; reuse one key per form so a retried submit cannot register twice. */
export function createMachine(input: NewMachine, key: string = newKey()): Promise<Machine> {
  return post<Machine>("/machines", input, key);
}

export function listDowntimes(filter: { open?: boolean; resourceId?: string } = {}, signal?: AbortSignal): Promise<Downtime[]> {
  return items<Downtime>(`/downtimes${query({ open: filter.open, resourceId: filter.resourceId })}`, signal);
}

/** Assigns a reason code; the andon for the downtime stays open until it clears. */
export function codeDowntime(id: string, input: { reasonCode: string; category?: DowntimeCategory }, key: string = newKey()): Promise<Downtime> {
  return post<Downtime>(`/downtimes/${encodeURIComponent(id)}:code`, input, key);
}

/** Clears a quality-fail alert (SF-0162 unknown or cleared, SF-0163 another kind). Reuse one key per form. */
export function clearAndon(id: string, reason: string, key: string = newKey()): Promise<Andon> {
  return post<Andon>(`/andon/${encodeURIComponent(id)}:clear`, { reason }, key);
}

export function listAndon(filter: { siteId?: string } = {}, signal?: AbortSignal): Promise<Andon[]> {
  return items<Andon>(`/andon${query(filter)}`, signal);
}

export function getOee(
  filter: { resourceIds: string[]; from: string; to: string; bucket?: OeeBucketSize },
  signal?: AbortSignal,
): Promise<OeeBucket[]> {
  return items<OeeBucket>(
    `/oee${query({ resourceIds: filter.resourceIds.join(","), from: filter.from, to: filter.to, bucket: filter.bucket })}`,
    signal,
  );
}

/** Exports approved, not yet exported pay lines in the range as one batch. */
export function exportPay(range: { from: string; to: string }, key: string = newKey()): Promise<PayExport> {
  return post<PayExport>("/pay:export", range, key);
}

export function listPayExports(signal?: AbortSignal): Promise<PayExport[]> {
  return items<PayExport>("/pay-exports", signal);
}

export function getPayExport(id: string, signal?: AbortSignal): Promise<PayExport> {
  return send<PayExport>(`/pay-exports/${encodeURIComponent(id)}`, {}, signal);
}

// --- Process messages (slice 4, FR-25.17) ---

export type ProcessSource = "kiosk" | "edge" | "manual";

/** A category code: a built-in one (phase-start, quality-result, ...) or one the tenant defined (slice 21). */
export type ProcessCategory = string;

export type ProcessDestination = "ebr" | "journal" | "quality" | "kafka" | "webhook";

export type ProcessStatus = "received" | "delivered" | "partially-delivered" | "error";

export type CharacteristicType = "numeric" | "text" | "boolean";

export interface Characteristic {
  name: string;
  type: CharacteristicType;
  /** Decimal string for numeric, "true" or "false" for boolean. */
  value: string;
  /** Decimal string limits, numeric only. */
  min?: string | null;
  max?: string | null;
  unit?: string | null;
  /** Set by the server when a numeric value is outside its limits (BR-25.13). */
  outOfLimit?: boolean;
}

export interface NewProcessMessage {
  source: ProcessSource;
  /** Unique per source; a replay returns the stored message. */
  sourceMessageId: string;
  categoryCode: ProcessCategory;
  orderId: string;
  phasePath?: string;
  occurredAt: string;
  values: Characteristic[];
}

export interface ProcessDelivery {
  destination: ProcessDestination;
  status: "pending" | "delivered" | "error";
  attempts: number;
  lastError: string | null;
  nextAttemptAt: string | null;
  deliveredAt: string | null;
}

export interface ProcessMessage {
  id: string;
  source: ProcessSource;
  sourceMessageId: string;
  categoryCode: ProcessCategory;
  orderId: string;
  phasePath: string;
  occurredAt: string;
  receivedAt: string;
  status: ProcessStatus;
  outOfLimit: boolean;
  values: Characteristic[];
  deliveries: ProcessDelivery[];
  /** Set once a supervisor deleted the message (slice 5); it then leaves the monitor. */
  deletedAt: string | null;
  deletedBy: string | null;
  deleteReason: string | null;
}

export interface ProcessIngest {
  message: ProcessMessage;
  duplicate: boolean;
}

export interface ProcessFilter {
  status?: ProcessStatus | "";
  category?: ProcessCategory | "";
  destination?: ProcessDestination | "";
  orderId?: string;
  limit?: number;
  /** Opaque position from the previous page's nextCursor. */
  cursor?: string;
}

/** One page of the monitor, newest first. nextCursor is null on the last page. */
export interface ProcessMessagePage {
  items: ProcessMessage[];
  nextCursor: string | null;
}

export async function listProcessMessages(filter: ProcessFilter = {}, signal?: AbortSignal): Promise<ProcessMessagePage> {
  const body = await send<{ items?: ProcessMessage[]; nextCursor?: string | null }>(
    `/process-messages${query({
      status: filter.status,
      category: filter.category,
      destination: filter.destination,
      orderId: filter.orderId,
      limit: filter.limit,
      cursor: filter.cursor,
    })}`,
    {},
    signal,
  );
  if (!Array.isArray(body.items)) {
    throw new ShopfloorApiError(0, "COM-0001", "The response has no items");
  }
  return { items: body.items, nextCursor: typeof body.nextCursor === "string" ? body.nextCursor : null };
}

export function getProcessMessage(id: string, signal?: AbortSignal): Promise<ProcessMessage> {
  return send<ProcessMessage>(`/process-messages/${encodeURIComponent(id)}`, {}, signal);
}

/** Records a process message. No Idempotency-Key: (source, sourceMessageId) makes a resend a replay. */
export function createProcessMessage(input: NewProcessMessage): Promise<ProcessIngest> {
  return post<ProcessIngest>("/process-messages", input);
}

/** Sends a destination in error again; keep one key per form so a retried submit replays. */
export function reprocessProcessMessage(
  id: string,
  input: { destination: ProcessDestination; reason: string },
  key: string = newKey(),
): Promise<ProcessMessage> {
  return post<ProcessMessage>(`/process-messages/${encodeURIComponent(id)}:reprocess`, input, key);
}

/**
 * Deletes a message with a destination in error, with a reason kept for audit
 * (SF-0119 otherwise, SF-0120 when already deleted). Keep one key per form.
 */
export function deleteProcessMessage(id: string, input: { reason: string }, key: string = newKey()): Promise<ProcessMessage> {
  return post<ProcessMessage>(`/process-messages/${encodeURIComponent(id)}:delete`, input, key);
}

// --- Kiosk login and who is clocked in (slice 6, FR-25.1, FR-25.14) ---

/** The worker a badge resolves to on a shared kiosk. */
export interface KioskSession {
  workerId: string;
  partyId: string;
  name: string;
  userId: string | null;
  /** The open clock-in, or null when the worker is clocked out. */
  clockedInAt: string | null;
  /** The worker's home resource, whose jobs the kiosk lists; null means the list holds every job. */
  defaultResourceId: string | null;
  /** The worker's open job-starts: one job, or the jobs of one bundle (FR-25.3). */
  openJobs: CurrentJob[];
  /** Sign the worker out after this many minutes without input. */
  idleTimeoutMinutes: number;
}

export interface CurrentJob {
  jobId: string;
  bundleId: string | null;
  startedAt: string;
  /** When production finished the job's operation while this start stays open (a start dated before the finish but stored after it); absent on a start the kiosk just made. */
  productionFinishedAt?: string | null;
}

export interface ClockedWorker {
  workerId: string;
  name: string;
  badgeId: string | null;
  active: boolean;
  clockedInAt: string;
  /** Jobs started since the clock-in and not yet stopped (every job of a bundle). */
  jobs: CurrentJob[];
}

/** Resolves a scanned badge; an unknown or deactivated badge is SF-0001. Nothing is stored, so there is no key. */
export function badgeLogin(badgeId: string): Promise<KioskSession> {
  return post<KioskSession>("/sessions:badge", { badgeId });
}

export function listClockedIn(signal?: AbortSignal): Promise<ClockedWorker[]> {
  return items<ClockedWorker>("/clocked-in", signal);
}

// --- Job dispatch list, feedback and quality checks (slice 7, FR-25.3 to FR-25.5) ---

export type JobStatus = "released" | "started" | "finished";

export type CheckKind = "numeric" | "pass-fail" | "text";

/** A quality check attached to the operation. */
export interface JobCheck {
  code: string;
  name: string;
  kind: CheckKind;
  /** Decimal strings, inclusive; numeric checks only. */
  min?: string | null;
  max?: string | null;
  /** A mandatory check without a passing result blocks finished (SF-0005). */
  mandatory: boolean;
}

export interface JobCheckResult {
  code: string;
  value: string;
  pass: boolean;
}

export interface Job {
  id: string;
  orderRef: string;
  operationRef: string;
  resourceId: string;
  status: JobStatus;
  /** Decimal strings. */
  plannedQuantity: string;
  goodQuantity: string;
  scrapQuantity: string;
  sequenceStart: string;
  checks: JobCheck[];
  /** Latest value per check; empty in the dispatch list. */
  results: JobCheckResult[];
  finishedAt: string | null;
  /**
   * When production (w21) finished the job's operation; null while it is open or after production
   * reopened it. An online start at or after it is refused (SF-0157); an offline replay is flagged job-finished-conflict.
   */
  productionFinishedAt?: string | null;
  version: number;
  updatedAt: string;
}

export interface JobFilter {
  resourceId?: string;
  /** The worker's own list: the jobs of the worker's home resource. Not with resourceId. */
  workerId?: string;
  /** Omit to hide finished jobs (the dispatch view). */
  status?: JobStatus | "";
}

export interface JobFeedbackInput {
  /** Client UUID; a resend with the same id changes nothing on the server. */
  feedbackId: string;
  /** Decimal increments. */
  goodQuantity?: string;
  scrapQuantity?: string;
  scrapReasonId?: string;
  finished: boolean;
  checks?: { code: string; value: string }[];
  /** Supervisor override of failed mandatory checks; only with finished. */
  override?: { reason: string };
}

export interface JobFeedbackResult {
  job: Job;
  duplicate: boolean;
}

/** Dispatch list in start order; finished jobs are hidden unless status asks for them. */
export function listJobs(filter: JobFilter = {}, signal?: AbortSignal): Promise<Job[]> {
  return items<Job>(`/jobs${query({ resourceId: filter.resourceId, workerId: filter.workerId, status: filter.status })}`, signal);
}

export function getJob(id: string, signal?: AbortSignal): Promise<Job> {
  return send<Job>(`/jobs/${encodeURIComponent(id)}`, {}, signal);
}

/**
 * Reports quantities, check values and finished. No Idempotency-Key: feedbackId is the
 * key, so keep one id per form and a retried submit is a replay. SF-0005 refuses a
 * finish while a mandatory check has no passing result.
 */
export function recordJobFeedback(jobId: string, input: JobFeedbackInput): Promise<JobFeedbackResult> {
  return post<JobFeedbackResult>(`/jobs/${encodeURIComponent(jobId)}:feedback`, input);
}

/** Reopens a finished job (FR-25.3). Held by the shop floor supervisor; enforced by the service. */
export const DUTY_JOB_REOPEN = "shopfloor.job.reopen";

/** Read access to the shop floor (supervisor lists such as the reopenings of a job). */
export const DUTY_VIEW = "shopfloor.view";

/** One supervisor reopening of a finished job (slice 15). */
export interface JobReopening {
  id: string;
  jobId: string;
  reason: string;
  reopenedBy: string;
  reopenedAt: string;
  /** The finish that no longer stands. */
  previousFinishedAt: string;
  /** Decimal strings: the job totals at the reopening. */
  goodQuantity: string;
  scrapQuantity: string;
}

/**
 * Puts a finished job back to started with a reason. The job version is the If-Match entity tag
 * (SF-0143, 412 when stale); a job that is not finished is SF-0155. Reuse one key per form.
 */
export function reopenJob(jobId: string, version: number, reason: string, key: string = newKey()): Promise<Job> {
  return send<Job>(`/jobs/${encodeURIComponent(jobId)}:reopen`, {
    method: "POST",
    headers: { "Idempotency-Key": key, "If-Match": `"${version}"` },
    body: JSON.stringify({ reason }),
  });
}

/** The audit list of a job's reopenings, newest first. */
export function listJobReopenings(jobId: string, signal?: AbortSignal): Promise<JobReopening[]> {
  return items<JobReopening>(`/jobs/${encodeURIComponent(jobId)}/reopenings`, signal);
}

// --- Time profiles and pay agreements (slice 11, D2f) ---

export type DayType = "weekday" | "saturday" | "sunday";

export const DAY_TYPES: readonly DayType[] = ["weekday", "saturday", "sunday"];

/** An unpaid break as local HH:MM times. */
export interface BreakWindow {
  from: string;
  to: string;
}

/** The day rules of one day type (ProfileDay). Shift start and end are both set or both null. */
export interface ProfileDay {
  dayType: DayType;
  shiftStart: string | null;
  shiftEnd: string | null;
  breaks: BreakWindow[];
  roundingMinutes: number;
  toleranceBeforeMin: number;
  toleranceAfterMin: number;
  flexAllowed: boolean;
}

export interface TimeProfile {
  id: string;
  code: string;
  name: string;
  active: boolean;
  version: number;
  days: ProfileDay[];
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewTimeProfile {
  code: string;
  name: string;
  active?: boolean;
  days: ProfileDay[];
}

/** A premium window: hours between from and to (local HH:MM; to not after from crosses midnight) earn the premium (0.15 is 15% extra). */
export interface ShiftPremium {
  name: string;
  from: string;
  to: string;
  premium: string;
}

/** Decimal fields are strings; weekStart is 0 (Sunday) to 6 (Saturday). */
export interface PayAgreement {
  id: string;
  code: string;
  name: string;
  dailyThreshold: string;
  weeklyThreshold: string;
  overtimeMultiplier: string;
  sundayMultiplier: string;
  weekStart: number;
  shiftPremiums: ShiftPremium[];
  active: boolean;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewPayAgreement {
  code: string;
  name: string;
  active?: boolean;
  dailyThreshold: string;
  weeklyThreshold: string;
  overtimeMultiplier: string;
  sundayMultiplier: string;
  weekStart: number;
  shiftPremiums?: ShiftPremium[];
}

export function listTimeProfiles(filter: { active?: boolean } = {}, signal?: AbortSignal): Promise<TimeProfile[]> {
  return items<TimeProfile>(`/time-profiles${query({ active: filter.active })}`, signal);
}

/** Creates a profile; reuse one key per form so a retried submit cannot create two. */
export function createTimeProfile(input: NewTimeProfile, key: string = newKey()): Promise<TimeProfile> {
  return post<TimeProfile>("/time-profiles", input, key);
}

export function listPayAgreements(filter: { active?: boolean } = {}, signal?: AbortSignal): Promise<PayAgreement[]> {
  return items<PayAgreement>(`/pay-agreements${query({ active: filter.active })}`, signal);
}

export function createPayAgreement(input: NewPayAgreement, key: string = newKey()): Promise<PayAgreement> {
  return post<PayAgreement>("/pay-agreements", input, key);
}

/** One entry of the downtime reason code master: a code with its planned or unplanned category (FR-25.11). */
export interface DowntimeReason {
  id: string;
  code: string;
  name: string;
  category: DowntimeCategory;
  active: boolean;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewDowntimeReason {
  code: string;
  name: string;
  category: DowntimeCategory;
  active?: boolean;
}

export function listDowntimeReasons(filter: { active?: boolean } = {}, signal?: AbortSignal): Promise<DowntimeReason[]> {
  return items<DowntimeReason>(`/downtime-reasons${query({ active: filter.active })}`, signal);
}

/** Creates a reason; reuse one key per form so a retried submit cannot create two. A used code is SF-0142. */
export function createDowntimeReason(input: NewDowntimeReason, key: string = newKey()): Promise<DowntimeReason> {
  return post<DowntimeReason>("/downtime-reasons", input, key);
}

/**
 * Replaces a reason's name, category and status (the code never changes). The version is the If-Match
 * entity tag; another change in between is SF-0143 (412).
 */
export function updateDowntimeReason(reason: DowntimeReason, changes: { name?: string; category?: DowntimeCategory; active?: boolean }): Promise<DowntimeReason> {
  return send<DowntimeReason>(`/downtime-reasons/${encodeURIComponent(reason.id)}`, {
    method: "PUT",
    headers: { "If-Match": `"${reason.version}"` },
    body: JSON.stringify({ name: reason.name, category: reason.category, active: reason.active, ...changes }),
  });
}

/** A characteristic a process category defines: its type, unit and limits (FR-25.17, BR-25.13). */
export interface CharacteristicDefinition {
  name: string;
  type: CharacteristicType;
  unit?: string | null;
  /** Decimal string limits, numeric only. */
  min?: string | null;
  max?: string | null;
  /** A message of the category without a value of this name is SF-0115. */
  required: boolean;
}

/** One process message category: a row of the tenant's master, or a built-in default (builtIn, no id or version). */
export interface ProcessCategoryDef {
  id?: string;
  code: string;
  name: string;
  destinations: ProcessDestination[];
  rejectOutOfLimit: boolean;
  characteristics: CharacteristicDefinition[];
  active: boolean;
  builtIn: boolean;
  version?: number;
  createdBy?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface NewProcessCategory {
  code: string;
  name: string;
  destinations: ProcessDestination[];
  rejectOutOfLimit: boolean;
  characteristics: CharacteristicDefinition[];
  active?: boolean;
}

export type ProcessCategoryChanges = Partial<Omit<NewProcessCategory, "code">>;

export function listProcessCategories(filter: { active?: boolean } = {}, signal?: AbortSignal): Promise<ProcessCategoryDef[]> {
  return items<ProcessCategoryDef>(`/process-categories${query({ active: filter.active })}`, signal);
}

export function getProcessCategory(id: string, signal?: AbortSignal): Promise<ProcessCategoryDef> {
  return send<ProcessCategoryDef>(`/process-categories/${encodeURIComponent(id)}`, {}, signal);
}

/** Creates a category; a row for a built-in code replaces that default. A used code is SF-0142. */
export function createProcessCategory(input: NewProcessCategory, key: string = newKey()): Promise<ProcessCategoryDef> {
  return post<ProcessCategoryDef>("/process-categories", input, key);
}

/**
 * Replaces a category's name, destinations, reject flag, definitions and status (the code never
 * changes). The version is the If-Match entity tag; another change in between is SF-0143 (412).
 */
export function updateProcessCategory(category: ProcessCategoryDef, changes: ProcessCategoryChanges): Promise<ProcessCategoryDef> {
  return send<ProcessCategoryDef>(`/process-categories/${encodeURIComponent(category.id ?? "")}`, {
    method: "PUT",
    headers: { "If-Match": `"${category.version ?? 0}"` },
    body: JSON.stringify({
      name: category.name,
      destinations: category.destinations,
      rejectOutOfLimit: category.rejectOutOfLimit,
      characteristics: category.characteristics,
      active: category.active,
      ...changes,
    }),
  });
}

/** An absence code: whether absence time is paid and at what share of the worker's rate (FR-25.2). */
export interface AbsenceCode {
  id: string;
  code: string;
  name: string;
  paid: boolean;
  /** Decimal string: the share of the worker's rate for a paid code, "0" for an unpaid one. Fixed once an absence names the code. */
  payMultiplier: string;
  active: boolean;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewAbsenceCode {
  code: string;
  name: string;
  paid: boolean;
  /** Paid codes only (defaults to 1); an unpaid code has none. */
  payMultiplier?: string;
  active?: boolean;
}

export function listAbsenceCodes(filter: { active?: boolean } = {}, signal?: AbortSignal): Promise<AbsenceCode[]> {
  return items<AbsenceCode>(`/absence-codes${query({ active: filter.active })}`, signal);
}

/** Creates an absence code; reuse one key per form so a retried submit cannot create two. */
export function createAbsenceCode(input: NewAbsenceCode, key: string = newKey()): Promise<AbsenceCode> {
  return post<AbsenceCode>("/absence-codes", input, key);
}

/**
 * Links a worker to a time profile and a pay agreement (null clears the link). The worker version
 * is the If-Match entity tag; another change in between is SF-0143 (412).
 */
export function assignWorkerSetup(
  workerId: string,
  version: number,
  input: { profileId: string | null; payAgreementId: string | null },
  key: string = newKey(),
): Promise<Worker> {
  return send<Worker>(`/workers/${encodeURIComponent(workerId)}:assign-setup`, {
    method: "POST",
    headers: { "Idempotency-Key": key, "If-Match": `"${version}"` },
    body: JSON.stringify(input),
  });
}

/**
 * Sets or clears (null) the worker's home resource; the worker version is the If-Match entity tag (SF-0143, 412).
 * A resource with no registered machine and no dispatch jobs is SF-0156 (422).
 */
export function assignWorkerResource(workerId: string, version: number, resourceId: string | null, key: string = newKey()): Promise<Worker> {
  return send<Worker>(`/workers/${encodeURIComponent(workerId)}:assign-resource`, {
    method: "POST",
    headers: { "Idempotency-Key": key, "If-Match": `"${version}"` },
    body: JSON.stringify({ resourceId }),
  });
}

/** The editable fields of a worker; the party link never changes. */
export type WorkerEdit = Omit<NewWorker, "partyId">;

/**
 * Replaces a worker's name, badge, sign-in user, cost rate and currency. The worker version is the
 * If-Match entity tag (SF-0143, 412); a badge held by another worker is SF-0105 (409).
 */
export function updateWorker(workerId: string, version: number, input: WorkerEdit, key: string = newKey()): Promise<Worker> {
  return send<Worker>(`/workers/${encodeURIComponent(workerId)}`, {
    method: "PUT",
    headers: { "Idempotency-Key": key, "If-Match": `"${version}"` },
    body: JSON.stringify({ ...input, badgeId: input.badgeId ?? null, userId: input.userId ?? null }),
  });
}

/**
 * Deactivates a worker with a reason. A worker who is clocked in or has an open job is SF-0166 (409);
 * an inactive worker is SF-0167 (409).
 */
export function deactivateWorker(workerId: string, version: number, reason: string, key: string = newKey()): Promise<Worker> {
  return send<Worker>(`/workers/${encodeURIComponent(workerId)}:deactivate`, {
    method: "POST",
    headers: { "Idempotency-Key": key, "If-Match": `"${version}"` },
    body: JSON.stringify({ reason }),
  });
}

/** Reactivates a deactivated worker; an active worker is SF-0168 (409). */
export function reactivateWorker(workerId: string, version: number, key: string = newKey()): Promise<Worker> {
  return send<Worker>(`/workers/${encodeURIComponent(workerId)}:reactivate`, {
    method: "POST",
    headers: { "Idempotency-Key": key, "If-Match": `"${version}"` },
  });
}

/** One dated cost rate of a worker: per hour, in the worker's currency, from effectiveFrom until the next one. */
export interface CostRate {
  id: string;
  workerId: string;
  /** Calendar date, YYYY-MM-DD. */
  effectiveFrom: string;
  /** Decimal string. */
  costRate: string;
  note: string | null;
  createdBy: string;
  createdAt: string;
}

/** The worker's dated cost rates, newest effective date first; empty when the worker has none. */
export function listWorkerCostRates(workerId: string, signal?: AbortSignal): Promise<CostRate[]> {
  return items<CostRate>(`/workers/${encodeURIComponent(workerId)}/cost-rates`, signal);
}

/**
 * Sets the worker's cost rate from a date. One rate per date (SF-0170, 409): a change takes effect
 * on a later date. The worker's own user cannot set it (SF-0106, 403).
 */
export function addWorkerCostRate(workerId: string, input: { effectiveFrom: string; costRate: string; note: string | null }, key: string = newKey()): Promise<CostRate> {
  return post<CostRate>(`/workers/${encodeURIComponent(workerId)}/cost-rates`, input, key);
}

/** Registers and changes edge nodes and publishes their configuration (with the write allowlist). */
export const DUTY_EDGE_CONFIGURE = "shopfloor.edge.configure";

export type EdgePlatform = "k3s" | "windows" | "linux";
export type EdgeRing = "canary" | "ring1" | "all";
export type EdgeStatusValue = "pending" | "online" | "offline" | "inactive";

export interface EdgeMachineStatus {
  machineCode: string;
  state: string;
}

/** One iot-edge connector (FR-25.19); the status is derived by the service from the last heartbeat. */
export interface EdgeNode {
  id: string;
  siteId: string | null;
  name: string;
  platform: EdgePlatform;
  ring: EdgeRing;
  /** The identity the connector authenticates as; only it may pull the configuration and report. */
  connectorSubject: string | null;
  status: EdgeStatusValue;
  connectorVersion: string | null;
  certSerial: string | null;
  certExpiresAt: string | null;
  lastHeartbeatAt: string | null;
  /** Decimal string, 0 to 100. */
  bufferFillPct: string | null;
  machines: EdgeMachineStatus[];
  appliedConfigVersion: number | null;
  latestConfigVersion: number | null;
  active: boolean;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewEdgeNode {
  name: string;
  platform: EdgePlatform;
  ring?: EdgeRing;
  connectorSubject?: string | null;
  active?: boolean;
}

/** A PUT replaces the node: an omitted site or connector identity is cleared. */
export interface EdgeNodeChange {
  name: string;
  ring: EdgeRing;
  siteId: string | null;
  connectorSubject: string | null;
  active: boolean;
}

export interface EdgeConfigVersion {
  id: string;
  edgeId: string;
  version: number;
  document: Record<string, unknown>;
  /** SHA-256 of the stored document, also on the published event. */
  documentSha256: string;
  publishedBy: string;
  publishedAt: string;
  appliedAt: string | null;
}

export function listEdgeNodes(filter: { ring?: EdgeRing; status?: EdgeStatusValue } = {}, signal?: AbortSignal): Promise<EdgeNode[]> {
  return items<EdgeNode>(`/edge-nodes${query({ ring: filter.ring, status: filter.status })}`, signal);
}

export function getEdgeNode(id: string, signal?: AbortSignal): Promise<EdgeNode> {
  return send<EdgeNode>(`/edge-nodes/${encodeURIComponent(id)}`, {}, signal);
}

/** Registers a connector; reuse one key per form so a retried submit cannot register two. */
export function createEdgeNode(input: NewEdgeNode, key: string = newKey()): Promise<EdgeNode> {
  return post<EdgeNode>("/edge-nodes", input, key);
}

/** Replaces name, ring, identity and status. The node version is the If-Match entity tag (SF-0143, 412 when stale). */
export function updateEdgeNode(id: string, version: number, input: EdgeNodeChange): Promise<EdgeNode> {
  return send<EdgeNode>(`/edge-nodes/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "If-Match": `"${version}"` },
    body: JSON.stringify(input),
  });
}

export function listEdgeConfigs(id: string, signal?: AbortSignal): Promise<EdgeConfigVersion[]> {
  return items<EdgeConfigVersion>(`/edge-nodes/${encodeURIComponent(id)}/config-versions`, signal);
}

/** Publishes the next configuration version; reuse one key per form. */
export function publishEdgeConfig(id: string, document: Record<string, unknown>, key: string = newKey()): Promise<EdgeConfigVersion> {
  return post<EdgeConfigVersion>(`/edge-nodes/${encodeURIComponent(id)}/config-versions`, { document }, key);
}
