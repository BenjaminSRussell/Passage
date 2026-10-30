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
export type RegistrationFlag = "job-finished-conflict" | "auto-stopped" | "auto-stop-capped";

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

export type DayFlag = "missing-clock-out" | "orphan-clock-out";

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
  andonRaised: boolean;
}

export interface Andon {
  id: string;
  resourceId: string;
  siteId: string | null;
  kind: "downtime";
  severity: "info" | "warning" | "error";
  downtimeId: string | null;
  startedAt: string;
}

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

export type ProcessCategory =
  | "phase-start"
  | "phase-end"
  | "consumption"
  | "yield"
  | "process-data"
  | "quality-result"
  | "deviation"
  | "order-status"
  | "custom";

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
  /** The worker's open job-starts: one job, or the jobs of one bundle (FR-25.3). */
  openJobs: CurrentJob[];
  /** Sign the worker out after this many minutes without input. */
  idleTimeoutMinutes: number;
}

export interface CurrentJob {
  jobId: string;
  bundleId: string | null;
  startedAt: string;
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
  version: number;
  updatedAt: string;
}

export interface JobFilter {
  resourceId?: string;
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
  return items<Job>(`/jobs${query({ resourceId: filter.resourceId, status: filter.status })}`, signal);
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
