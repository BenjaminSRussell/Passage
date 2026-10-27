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

export interface RegistrationResult {
  id: string;
  status: "accepted" | "duplicate";
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
}

export interface Machine {
  id: string;
  machineId: string;
  resourceId: string;
  siteId: string | null;
  idealCycleSeconds: string;
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
}

export function listProcessMessages(filter: ProcessFilter = {}, signal?: AbortSignal): Promise<ProcessMessage[]> {
  return items<ProcessMessage>(
    `/process-messages${query({
      status: filter.status,
      category: filter.category,
      destination: filter.destination,
      orderId: filter.orderId,
      limit: filter.limit,
    })}`,
    signal,
  );
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
