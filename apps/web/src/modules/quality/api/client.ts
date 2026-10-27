// Typed client for the quality service (services/quality/api/openapi.yaml).
// Browser calls use /api/v1/quality/...; the BFF rewrites them to the service.
// Decimals travel as strings, never numbers.

export const base = "/api/v1/quality";

export type SamplingMethod = "FIXED" | "PERCENT" | "AQL";
export type Severity = "NORMAL" | "TIGHTENED" | "REDUCED";
export type Trigger =
  | "PURCHASE_RECEIPT"
  | "PRODUCTION_RAF"
  | "SALES_PICK"
  | "TRANSFER_RECEIPT"
  | "QUARANTINE"
  | "MANUAL"
  | "SOURCE_INSPECTION";
export type OrderStatus = "CREATED" | "OPEN" | "RESULTS_IN_PROGRESS" | "PENDING_DECISION" | "CLOSED" | "CANCELLED";
export type DecisionKind = "ACCEPT" | "REJECT" | "ACCEPT_WITH_DEVIATION" | "REWORK" | "RETURN_TO_VENDOR" | "SCRAP";
export type CalibrationStatus = "IN_CALIBRATION" | "DUE_SOON" | "OVERDUE";

/** The AQL columns of Z1.4 Table II-A loaded in this slice (BR-33.1). */
export const AQL_VALUES = ["1.0", "2.5", "4.0"] as const;

/** Most results one batch may carry. */
export const MAX_RESULTS_PER_BATCH = 1000;

export interface SamplingPlan {
  method: SamplingMethod;
  fixedQty?: string;
  percent?: string;
  aql?: string;
  severity?: Severity;
  inspectionLevel?: "II";
}

export interface SampleDraw {
  codeLetter: string;
  sampleQty: string;
  acceptNo: number;
  rejectNo: number;
  severity: Severity;
  inspectionLevel: string;
}

export interface TestLine {
  testId: string;
  name: string;
  kind: "VARIABLE" | "ATTRIBUTE";
  required: boolean;
  decimals: number;
  min?: string;
  max?: string;
  passingOutcomeIds?: string[];
  destructive: boolean;
}

export interface TestGroup {
  id: string;
  code: string;
  version: number;
  tests: TestLine[];
}

export interface NewTestGroup {
  code: string;
  tests: TestLine[];
}

export interface Instrument {
  id: string;
  code: string;
  name: string;
  calibrationIntervalDays: number;
  lastCalibratedAt: string;
  dueAt: string;
  calibrationStatus?: CalibrationStatus;
}

export interface NewInstrument {
  code: string;
  name: string;
  calibrationIntervalDays: number;
  lastCalibratedAt: string;
}

export interface Association {
  id: string;
  trigger: Exclude<Trigger, "MANUAL">;
  itemScope: "ITEM" | "ITEM_GROUP" | "ALL";
  itemRef?: string;
  partyScope: "PARTY" | "ALL";
  partyRef?: string;
  siteScope: "SITE" | "ALL";
  siteRef?: string;
  sequence: number;
  testGroupId: string;
  samplingPlan: SamplingPlan;
}

export type NewAssociation = Omit<Association, "id">;

export interface SourceRef {
  type: string;
  id: string;
  line?: string;
}

export interface Order {
  id: string;
  trigger: Trigger;
  sourceRef: SourceRef;
  itemId: string;
  siteId: string;
  warehouseId: string;
  dims: Record<string, string>;
  lotQty: string;
  sampleQty: string;
  uom: string;
  testGroupId: string;
  testGroupVersion: number;
  samplingMethod: SamplingMethod;
  severity: Severity;
  codeLetter: string;
  acceptNo: number;
  rejectNo: number;
  status: OrderStatus;
  version: number;
  createdAt: string;
  closedAt?: string;
}

export interface NewOrder {
  trigger: Trigger;
  sourceRef: SourceRef;
  itemId: string;
  itemGroupId?: string;
  partyId?: string;
  siteId: string;
  warehouseId: string;
  lotQty: string;
  uom: string;
  testGroupId?: string;
  samplingPlan?: SamplingPlan;
}

export interface Result {
  testId: string;
  sampleNo: number;
  retestNo: number;
  value?: string;
  outcomeId?: string;
  passed: boolean;
  instrumentId?: string;
  recordedBy: string;
  recordedAt: string;
}

export interface ResultInput {
  testId: string;
  sampleNo: number;
  retestNo?: number;
  value?: string;
  outcomeId?: string;
  instrumentId?: string;
}

export interface Evaluation {
  defectiveSamples: number;
  passed: boolean;
  missingRequired: string[];
}

export interface DecisionLine {
  decision: DecisionKind;
  quantity: string;
  reason?: string;
}

export interface Decision {
  id: string;
  orderId: string;
  lines: DecisionLine[];
  split: { acceptQty: string; rejectQty: string; deviationQty: string };
  decidedBy: string;
  decidedAt: string;
}

export interface OrderView extends Order {
  testGroup: TestGroup;
  results: Result[];
  evaluation: Evaluation;
  decision?: Decision;
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

/** Thrown for every non-2xx response; carries the stable problem code. */
export class QualityApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "QualityApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

/** The stable code, the message and any field errors of a failure. */
export function describeProblem(error: unknown): Problem {
  if (error instanceof QualityApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<QualityApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new QualityApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new QualityApiError(0, "COM-0001", "The response has no items");
  return body.items;
}

function post<T>(path: string, body: unknown, headers: Record<string, string> = {}): Promise<T> {
  return send<T>(path, { method: "POST", headers, body: JSON.stringify(body) });
}

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value) q.set(key, value);
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

/** A fresh Idempotency-Key. Forms keep one per attempt so a retried submit cannot post twice. */
export function newKey(): string {
  return crypto.randomUUID();
}

/** Computes the sample draw for a lot without side effects (QM_VIEW). */
export function sizeSample(lotQty: string, plan: SamplingPlan): Promise<SampleDraw> {
  return post<SampleDraw>("/sampling-plans:size", { lotQty, ...plan });
}

export function listTestGroups(signal?: AbortSignal): Promise<TestGroup[]> {
  return items<TestGroup>("/test-groups", signal);
}

export function getTestGroup(id: string, signal?: AbortSignal): Promise<TestGroup> {
  return send<TestGroup>(`/test-groups/${encodeURIComponent(id)}`, {}, signal);
}

/** Creates the next version of a test group code (QM_SETUP_MAINTAIN). */
export function createTestGroup(input: NewTestGroup, key: string = newKey()): Promise<TestGroup> {
  return post<TestGroup>("/test-groups", input, { "Idempotency-Key": key });
}

export function listInstruments(signal?: AbortSignal): Promise<Instrument[]> {
  return items<Instrument>("/instruments", signal);
}

export function createInstrument(input: NewInstrument, key: string = newKey()): Promise<Instrument> {
  return post<Instrument>("/instruments", input, { "Idempotency-Key": key });
}

/** Records a calibration; the due date moves on by the interval (BR-33.8). */
export function calibrateInstrument(id: string, calibratedAt: string): Promise<Instrument> {
  return post<Instrument>(`/instruments/${encodeURIComponent(id)}:calibrate`, { calibratedAt });
}

export function listAssociations(signal?: AbortSignal): Promise<Association[]> {
  return items<Association>("/associations", signal);
}

export function createAssociation(input: NewAssociation, key: string = newKey()): Promise<Association> {
  return post<Association>("/associations", input, { "Idempotency-Key": key });
}

export function listOrders(filter: { status?: OrderStatus | ""; siteId?: string }, signal?: AbortSignal): Promise<Order[]> {
  return items<Order>(`/quality-orders${query({ status: filter.status, siteId: filter.siteId })}`, signal);
}

/** Reads a quality order with its test group, results, evaluation and decision (QM_VIEW). */
export function getOrder(id: string, signal?: AbortSignal): Promise<OrderView> {
  return send<OrderView>(`/quality-orders/${encodeURIComponent(id)}`, {}, signal);
}

export function createOrder(input: NewOrder, key: string = newKey()): Promise<Order> {
  return post<Order>("/quality-orders", input, { "Idempotency-Key": key });
}

export function recordResults(orderId: string, results: ResultInput[], key: string = newKey()): Promise<OrderView> {
  return post<OrderView>(`/quality-orders/${encodeURIComponent(orderId)}/results`, { results }, { "Idempotency-Key": key });
}

/** Posts the usage decision; version is the order's ETag from its last read. */
export function postDecision(
  orderId: string,
  version: number,
  lines: DecisionLine[],
  key: string = newKey(),
): Promise<{ order: OrderView; decision: Decision }> {
  return post(`/quality-orders/${encodeURIComponent(orderId)}:usage-decision`, { lines }, { "Idempotency-Key": key, "If-Match": `"${version}"` });
}
