/** Browser client for the warehouse service, through the web BFF at /api/v1/warehouse (service routes /v1/warehouse/...). */
export const base = "/api/v1/warehouse";

export type Direction = "PUT" | "PICK";

/** The work line to place (BR-31.1). Quantities are decimal strings, never floats. */
export interface SimulateLine {
  itemId: string;
  workType: string;
  direction: Direction;
  warehouseId: string;
  qty: string;
  weight?: string;
  volume?: string;
  lps?: number;
  attributes?: Record<string, string>;
}

export type HazmatRel = "allowed" | "separate" | "prohibited";

/** Hazmat reference data for storage compatibility (FR-31.19, BR-31.15). Limits are decimal strings in kg. */
export interface Compliance {
  hazmatProfiles?: { itemId: string; storageClass: string; unNumber?: string; shippingName?: string; class?: string }[];
  segregation?: { a: string; b: string; rel: HazmatRel }[];
  sectionLimitsKg?: Record<string, string>;
}

/** Directives and locations travel as the service's JSON shapes (see services/warehouse/api/openapi.yaml). */
export interface SimulateRequest {
  line: SimulateLine;
  directives: unknown[];
  locations: unknown[];
  compliance?: Compliance;
}

export interface TraceStep {
  directive: string;
  band?: number;
  action?: number;
  outcome: "hit" | "miss" | "skip";
  reason: string;
}

/** Result of a directive simulation. A blocked line is an answer, not an error (WMS-0101). */
export interface SimulateResponse {
  blocked: boolean;
  code?: string;
  locationId?: string;
  location?: string;
  directive?: string;
  trace: TraceStep[];
}

/** Problem details (RFC 9457) with the stable service code and field errors. */
export class WarehouseApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "WarehouseApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof WarehouseApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "NETWORK", message: error instanceof Error ? error.message : "The request failed.", fields: {} };
}

async function toError(res: Response): Promise<WarehouseApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new WarehouseApiError(res.status, code, message, fields);
}

/** POST /v1/warehouse/location-directives:simulate (duty warehouse.setup.view). Nothing is saved. */
export async function simulateDirectives(request: SimulateRequest, signal?: AbortSignal): Promise<SimulateResponse> {
  const res = await fetch(`${base}/location-directives:simulate`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(request),
    signal,
  });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as SimulateResponse;
}

/* ---------- Persisted setup and work (slice 3) ---------- */

export type LocationStatus = "ACTIVE" | "BLOCKED_IN" | "BLOCKED_OUT" | "BLOCKED" | "INACTIVE";
export const LOCATION_STATUSES: LocationStatus[] = ["ACTIVE", "BLOCKED_IN", "BLOCKED_OUT", "BLOCKED", "INACTIVE"];

export const WORK_TYPES = [
  "PURCHASE_PUTAWAY",
  "SALES_PICK",
  "TRANSFER_ISSUE",
  "TRANSFER_RECEIPT",
  "PRODUCTION_PICK",
  "RAF_PUTAWAY",
  "REPLENISHMENT",
  "MOVEMENT",
  "COUNT",
  "RETURN",
] as const;
export type WorkType = (typeof WORK_TYPES)[number];

export const WORK_STATUSES = ["OPEN", "ASSIGNED", "IN_PROGRESS", "COMPLETED", "PARTIALLY_COMPLETED", "CANCELLED", "BLOCKED_NO_LOCATION"] as const;
export type WorkStatus = (typeof WORK_STATUSES)[number];

export const STRATEGIES = ["consolidate", "fifo", "lifo", "fefo", "empty_location", "match_packing_quantity", "round_up_to_lp", "outbound_staging"] as const;

/** Capacity limits; a missing limit is unlimited. Weights (kg) and volumes (m³) are decimal strings. */
export interface Capacity {
  maxWeight?: string;
  maxVolume?: string;
  maxLps?: number;
  maxItems?: number;
  allowMixedItems?: boolean;
}

export interface Location {
  id: string;
  name: string;
  zone: string;
  pickSequence: number;
  status: LocationStatus;
  capacity: Capacity;
  used: { weight: string; volume: string; lps: number };
  stock?: Record<string, string>;
  fireSection?: string;
  rackGroup?: string;
  allowedStorageClasses?: string[];
  version?: number;
}

export interface LocationInput {
  name: string;
  zone: string;
  pickSequence: number;
  status: LocationStatus;
  capacity: Capacity;
}

export interface DirectiveAction {
  seq: number;
  strategy: string;
  zone?: string;
}

export interface DirectiveLine {
  seq: number;
  fromQty: string;
  toQty?: string;
  actions: DirectiveAction[];
}

export interface Directive {
  id: string;
  sequence: number;
  workType: string;
  direction: Direction;
  warehouseId: string;
  query?: Record<string, string>;
  lines: DirectiveLine[];
}

export type DirectiveInput = Omit<Directive, "id">;

export type LineStatus = "OPEN" | "COMPLETED" | "SHORT" | "CANCELLED";

export interface WorkLine {
  lineNo: number;
  itemId: string;
  direction: Direction;
  qty: string;
  confirmedQty: string;
  weight: string;
  volume: string;
  lps: number;
  fromLocationId?: string;
  toLocationId?: string;
  status: LineStatus;
  reason?: string;
  attributes?: Record<string, string>;
}

export interface Work {
  id: string;
  warehouseId: string;
  workType: string;
  status: WorkStatus;
  priority: number;
  parentWorkId?: string;
  assignedUser?: string;
  assignedAt?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
  lines: WorkLine[];
}

export interface WorkLineInput {
  itemId: string;
  direction: Direction;
  qty: string;
  weight?: string;
  volume?: string;
  lps?: number;
}

export interface WorkInput {
  warehouseId: string;
  workType: string;
  priority?: number;
  lines: WorkLineInput[];
}

export interface Planned {
  work: Work;
  traces: { directive?: string; blocked: boolean; code?: string; trace: TraceStep[] }[];
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

/** A fresh Idempotency-Key for one user action; a retry of the same action reuses it. */
export function newKey(): string {
  return crypto.randomUUID();
}

async function send<T>(path: string, init: RequestInit & { key?: string; version?: number } = {}): Promise<T> {
  const { key, version, headers, ...rest } = init;
  const res = await fetch(`${base}${path}`, {
    ...rest,
    headers: {
      Accept: "application/json",
      ...(rest.body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(key ? { "Idempotency-Key": key } : {}),
      ...(version ? { "If-Match": `"${version}"` } : {}),
      ...headers,
    },
    credentials: "same-origin",
  });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") q.set(k, String(v));
  }
  return q.toString();
}

/** GET /v1/warehouse/locations (duty warehouse.setup.view): one page by name, with stock. */
export function listLocations(p: { warehouseId: string; zone?: string; cursor?: string; limit?: number }, signal?: AbortSignal) {
  return send<Page<Location>>(`/locations?${query(p)}`, { signal });
}

/** POST /v1/warehouse/locations:bulk (duty warehouse.setup.maintain). */
export function createLocations(warehouseId: string, locations: LocationInput[], key: string) {
  return send<Page<Location>>("/locations:bulk", { method: "POST", key, body: JSON.stringify({ warehouseId, locations }) });
}

/** GET /v1/warehouse/location-directives (duty warehouse.setup.view). */
export function listDirectives(p: { warehouseId: string; workType?: string; direction?: string }, signal?: AbortSignal) {
  return send<Page<Directive>>(`/location-directives?${query(p)}`, { signal });
}

/** POST /v1/warehouse/location-directives (duty warehouse.setup.maintain). */
export function createDirective(input: DirectiveInput, key: string) {
  return send<Directive>("/location-directives", { method: "POST", key, body: JSON.stringify(input) });
}

/** GET /v1/warehouse/work (duty warehouse.inquiry.view): headers only, best priority then oldest first. */
export function listWork(p: { warehouseId: string; status?: string; cursor?: string; limit?: number }, signal?: AbortSignal) {
  return send<Page<Work>>(`/work?${query(p)}`, { signal });
}

/** GET /v1/warehouse/work/{id} (duty warehouse.inquiry.view). */
export function getWork(id: string, signal?: AbortSignal) {
  return send<Work>(`/work/${encodeURIComponent(id)}`, { signal });
}

/** POST /v1/warehouse/work (duty warehouse.work.supervise): the directives choose each line's location. */
export function createWork(input: WorkInput, key: string) {
  return send<Planned>("/work", { method: "POST", key, body: JSON.stringify(input) });
}

/** POST /v1/warehouse/work:assign (duty warehouse.work.execute): work is null when nothing is assignable. */
export function assignNextWork(warehouseId: string, key: string) {
  return send<{ work: Work | null }>("/work:assign", { method: "POST", key, body: JSON.stringify({ warehouseId }) });
}

/** POST /v1/warehouse/work/{id}/lines/{n}:confirm (duty warehouse.work.execute). */
export function confirmLine(work: Pick<Work, "id" | "version">, lineNo: number, qty: string, key: string) {
  return send<Work>(`/work/${encodeURIComponent(work.id)}/lines/${lineNo}:confirm`, {
    method: "POST",
    key,
    version: work.version,
    body: JSON.stringify({ qty }),
  });
}

export type WorkException = { action: "SHORT_PICK"; lineNo: number; confirmedQty: string; reason: string } | { action: "CANCEL"; reason: string };

/** POST /v1/warehouse/work/{id}:exception (duty warehouse.work.execute): short pick a line or cancel the work. */
export function workException(work: Pick<Work, "id" | "version">, exception: WorkException, key: string) {
  return send<{ work: Work; remainder: Work | null }>(`/work/${encodeURIComponent(work.id)}:exception`, {
    method: "POST",
    key,
    version: work.version,
    body: JSON.stringify(exception),
  });
}

/* ---------- Dock scheduling and ASN receiving (slice 4) ---------- */

export interface DockDoor {
  id: string;
  warehouseId: string;
  code: string;
  createdAt: string;
}

export type AppointmentDirection = "INBOUND" | "OUTBOUND";

/** A door booked for the half-open window [start, end); times are UTC RFC 3339 strings on the 15-minute grid. */
export interface Appointment {
  id: string;
  warehouseId: string;
  doorId: string;
  doorCode: string;
  direction: AppointmentDirection;
  reference: string;
  start: string;
  end: string;
  createdAt: string;
}

export type AppointmentInput = Pick<Appointment, "warehouseId" | "doorId" | "direction" | "reference" | "start" | "end">;

export const RECEIPT_STATUSES = ["OPEN", "PARTIALLY_RECEIVED", "RECEIVED"] as const;
export type ReceiptStatus = (typeof RECEIPT_STATUSES)[number];

/** One ASN pallet (license plate). Quantities and the variance are decimal strings. */
export interface ReceiptLine {
  lineNo: number;
  itemId: string;
  lpId: string;
  poLineRef?: string;
  expectedQty: string;
  weight: string;
  volume: string;
  status: "EXPECTED" | "RECEIVED";
  receivedQty: string;
  varianceQty: string;
  variancePct: string;
  discrepancy: boolean;
  reason?: string;
  workId?: string;
  receivedBy?: string;
  receivedAt?: string;
}

export interface Receipt {
  id: string;
  warehouseId: string;
  asnRef: string;
  tolerancePct: string;
  status: ReceiptStatus;
  version: number;
  createdAt: string;
  updatedAt: string;
  lines: ReceiptLine[];
}

export interface ReceiptSummary {
  id: string;
  warehouseId: string;
  asnRef: string;
  tolerancePct: string;
  status: ReceiptStatus;
  lines: number;
  received: number;
  discrepancies: number;
  createdAt: string;
  updatedAt: string;
}

export interface AsnLineInput {
  lineNo: number;
  itemId: string;
  lpId: string;
  poLineRef?: string;
  expectedQty: string;
  weight?: string;
  volume?: string;
}

export interface AsnInput {
  warehouseId: string;
  asnRef: string;
  tolerancePct?: string;
  lines: AsnLineInput[];
}

export interface Received {
  receipt: Receipt;
  work: Work;
  traces: Planned["traces"];
  warnings: { code: string; detail: string }[];
}

/** GET /v1/warehouse/dock-doors (duty warehouse.inquiry.view). */
export function listDoors(warehouseId: string, signal?: AbortSignal) {
  return send<Page<DockDoor>>(`/dock-doors?${query({ warehouseId })}`, { signal });
}

/** POST /v1/warehouse/dock-doors (duty warehouse.setup.maintain). */
export function createDoor(warehouseId: string, code: string, key: string) {
  return send<DockDoor>("/dock-doors", { method: "POST", key, body: JSON.stringify({ warehouseId, code }) });
}

/** GET /v1/warehouse/dock-appointments (duty warehouse.inquiry.view): appointments overlapping [from, to). */
export function listAppointments(p: { warehouseId: string; doorId?: string; from?: string; to?: string }, signal?: AbortSignal) {
  return send<Page<Appointment>>(`/dock-appointments?${query(p)}`, { signal });
}

/** POST /v1/warehouse/dock-appointments (INBOUND: warehouse.receipt.register, OUTBOUND: warehouse.shipment.confirm). */
export function createAppointment(input: AppointmentInput, key: string) {
  return send<Appointment>("/dock-appointments", { method: "POST", key, body: JSON.stringify(input) });
}

/** GET /v1/warehouse/receipts (duty warehouse.inquiry.view): one page by ASN reference, with pallet counts. */
export function listReceipts(p: { warehouseId: string; status?: string; cursor?: string; limit?: number }, signal?: AbortSignal) {
  return send<Page<ReceiptSummary>>(`/receipts?${query(p)}`, { signal });
}

/** GET /v1/warehouse/receipts/{id} (duty warehouse.inquiry.view). */
export function getReceipt(id: string, signal?: AbortSignal) {
  return send<Receipt>(`/receipts/${encodeURIComponent(id)}`, { signal });
}

/** POST /v1/warehouse/receipts:from-asn (duty warehouse.receipt.register). An ASN already registered in the warehouse is 409 WMS-DUPLICATE. */
export function registerAsn(input: AsnInput, key: string) {
  return send<Receipt>("/receipts:from-asn", { method: "POST", key, body: JSON.stringify(input) });
}

/** POST /v1/warehouse/receipts/{id}/lines/{n}:receive (duty warehouse.receipt.register): creates the pallet's put-away work. */
export function receivePallet(receipt: Pick<Receipt, "id" | "version">, lineNo: number, qty: string, key: string) {
  return send<Received>(`/receipts/${encodeURIComponent(receipt.id)}/lines/${lineNo}:receive`, {
    method: "POST",
    key,
    version: receipt.version,
    body: JSON.stringify({ qty }),
  });
}
