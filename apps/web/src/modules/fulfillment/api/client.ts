// Client for the fulfillment service (services/fulfillment/api/openapi.yaml).
// Browser calls use /api/v1/fulfillment/..., which the web BFF routes to the service.
// Commands send an Idempotency-Key; shipment commands also send If-Match with the
// shipment version from the last read.

export const base = "/api/v1/fulfillment";

export type ShipmentStatus = "OPEN" | "RELEASED" | "PICKING" | "PICKED" | "PACKED" | "SHIPPED" | "DELIVERED" | "CANCELLED";
export type PackingSlipStatus = "PENDING" | "POSTED" | "CANCELLED" | "INVOICED";

export const SHIPMENT_STATUSES: ShipmentStatus[] = ["RELEASED", "PICKING", "PICKED", "PACKED", "SHIPPED", "DELIVERED", "CANCELLED"];

export interface ShipmentLine {
  id: string;
  orderLineId: string;
  productId: string;
  unit: string;
  /** Decimal strings. */
  qtyPlanned: string;
  qtyPicked: string;
  qtyPacked: string;
  qtyShipped: string;
  pickConfirmed: boolean;
}

export interface Container {
  id: string;
  containerType: string;
  sscc: string;
  status: "OPEN" | "CLOSED" | "SHIPPED";
  lines: { shipmentLineId: string; qty: string }[];
}

export interface ShipmentSummary {
  id: string;
  shipmentNumber: string;
  legalEntityId: string;
  warehouseId: string;
  orderId: string;
  customerId: string;
  status: ShipmentStatus;
  plannedShipDate: string;
  actualShipDate: string | null;
  version: number;
  lineCount: number;
}

export interface Shipment extends Omit<ShipmentSummary, "lineCount"> {
  lines: ShipmentLine[];
  containers: Container[];
}

export interface JournalEntry {
  account: string;
  /** Signed decimal: debits positive, credits negative. */
  amount: string;
  currency: string;
  orderLineId: string;
}

export interface PackingSlipSummary {
  id: string;
  packingSlipNumber: string;
  shipmentId: string;
  orderId: string;
  status: PackingSlipStatus;
  postingDate: string;
  currency: string;
  totalCost: string;
  reversalOfId: string | null;
}

export interface PackingSlip extends PackingSlipSummary {
  lines: { orderLineId: string; shipmentLineId: string; qty: string; costAmount: string; costSnapshotId: string }[];
  journal: JournalEntry[];
}

export interface ReleaseLineInput {
  orderId: string;
  orderLineId: string;
  customerId: string;
  legalEntityId: string;
  productId: string;
  unit: string;
  qty: string;
  confirmedShipDate: string;
  atpOk: boolean;
  held: boolean;
}

export interface ReleaseInput {
  warehouseId: string;
  horizonDays?: number;
  requireFullAtp?: boolean;
  lines: ReleaseLineInput[];
}

export interface ReleaseSkip {
  orderLineId: string;
  reason: "held" | "beyond_horizon" | "atp_short" | "non_positive_qty" | "already_released";
}

export interface ReleaseResult {
  shipments: Shipment[];
  skipped: ReleaseSkip[];
}

export interface PostInput {
  postingDate: string;
  currency: string;
  cogsAtShipment: boolean;
  costs: { shipmentLineId: string; unitCost: string; costSnapshotId: string }[];
}

export class FulfillmentApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string>,
  ) {
    super(message);
    this.name = "FulfillmentApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof FulfillmentApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<FulfillmentApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new FulfillmentApiError(res.status, code, message, fields);
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
    throw new FulfillmentApiError(0, "COM-0001", "The response has no items", {});
  }
  return body.items;
}

function query(params: Record<string, string | number | undefined>): string {
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

function command<T>(path: string, body: unknown, key: string, version?: number): Promise<T> {
  const headers: Record<string, string> = { "Idempotency-Key": key };
  if (version !== undefined) headers["If-Match"] = `"${version}"`;
  return send<T>(path, { method: "POST", headers, body: body === undefined ? undefined : JSON.stringify(body) });
}

const shipmentPath = (id: string) => `/shipments/${encodeURIComponent(id)}`;
const slipPath = (id: string) => `/packing-slips/${encodeURIComponent(id)}`;

/** Runs the release rules over the given lines (FR-28.1). Reuse one key per form submit. */
export function runRelease(input: ReleaseInput, key: string): Promise<ReleaseResult> {
  return command<ReleaseResult>("/releases:run", input, key);
}

export function listShipments(
  filter: { status?: ShipmentStatus | ""; warehouseId?: string; orderId?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<ShipmentSummary[]> {
  return items<ShipmentSummary>(`/shipments${query(filter)}`, signal);
}

export function getShipment(id: string, signal?: AbortSignal): Promise<Shipment> {
  return send<Shipment>(shipmentPath(id), {}, signal);
}

export function confirmPick(id: string, version: number, picks: { shipmentLineId: string; picked: string }[], key: string): Promise<Shipment> {
  return command<Shipment>(`${shipmentPath(id)}:confirm-pick`, { picks }, key, version);
}

export function packContainer(
  id: string,
  version: number,
  input: { containerType: string; lines: { shipmentLineId: string; qty: string }[] },
  key: string,
): Promise<{ container: Container; shipment: Shipment }> {
  return command(`${shipmentPath(id)}/containers`, input, key, version);
}

export function postPackingSlip(id: string, version: number, input: PostInput, key: string): Promise<PackingSlip> {
  return command<PackingSlip>(`${shipmentPath(id)}:post-packing-slip`, input, key, version);
}

export function listPackingSlips(
  filter: { shipmentId?: string; orderId?: string; status?: PackingSlipStatus | ""; limit?: number } = {},
  signal?: AbortSignal,
): Promise<PackingSlipSummary[]> {
  return items<PackingSlipSummary>(`/packing-slips${query(filter)}`, signal);
}

export function getPackingSlip(id: string, signal?: AbortSignal): Promise<PackingSlip> {
  return send<PackingSlip>(slipPath(id), {}, signal);
}

/** Reverses a posted, uninvoiced slip; the response is the reversing slip (FR-28.5). */
export function cancelPackingSlip(id: string, key: string): Promise<PackingSlip> {
  return command<PackingSlip>(`${slipPath(id)}:cancel`, undefined, key);
}
