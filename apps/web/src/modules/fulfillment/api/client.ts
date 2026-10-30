// Client for the fulfillment service (services/fulfillment/api/openapi.yaml).
// Browser calls use /api/v1/fulfillment/..., which the web BFF routes to the service.
// Commands send an Idempotency-Key; shipment commands also send If-Match with the
// shipment version from the last read.

export const base = "/api/v1/fulfillment";

export type ShipmentStatus = "OPEN" | "RELEASED" | "PICKING" | "PICKED" | "PACKED" | "PARTIALLY_SHIPPED" | "SHIPPED" | "DELIVERED" | "CANCELLED";
export type PackingSlipStatus = "PENDING" | "POSTED" | "CANCELLED" | "INVOICED";

export const SHIPMENT_STATUSES: ShipmentStatus[] = ["RELEASED", "PICKING", "PICKED", "PACKED", "PARTIALLY_SHIPPED", "SHIPPED", "DELIVERED", "CANCELLED"];

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
  /** Planned quantity given up when the remainder was closed (FR-28.8); absent or 0 when none. */
  qtyClosed?: string;
  pickConfirmed: boolean;
  /** Pick zone given at release; null when none. */
  zone: string | null;
}

export interface Container {
  id: string;
  containerType: string;
  sscc: string;
  status: "OPEN" | "CLOSED" | "SHIPPED";
  /** Decimal strings in kg. */
  tareKg: string;
  grossKg: string;
  returnable: boolean;
  packagingMaterial: string | null;
  /** The packing slip the container shipped with; null while CLOSED. */
  packingSlipId: string | null;
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
  carrierCode: string | null;
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

/** A statistical packaging fee line of a slip (BR-28.12); never a journal line. */
export interface PackagingFeeLine {
  countryCode: string;
  period: string;
  materialCode: string;
  weightKg: string;
  ratePerKg: string;
  feeAmount: string;
  currency: string;
}

export interface PackingSlip extends PackingSlipSummary {
  lines: { orderLineId: string; shipmentLineId: string; qty: string; costAmount: string; costSnapshotId: string }[];
  journal: JournalEntry[];
  packagingFees: PackagingFeeLine[];
}

export interface ContainerType {
  code: string;
  tareKg: string;
  returnable: boolean;
  packagingMaterial: string | null;
}

export interface PackingRule {
  id: string;
  productId: string;
  /** Null is the product's general rule. */
  customerId: string | null;
  containerType: string;
  qtyPerContainer: string;
  maxWeightKg: string;
}

export interface ProductPackaging {
  productId: string;
  materialCode: string;
  weightPerUnitKg: string;
}

export interface FeeRate {
  countryCode: string;
  materialCode: string;
  currency: string;
  ratePerKg: string;
  validFrom: string;
}

export interface PackagingReportRow {
  materialCode: string;
  weightKg: string;
  feeAmount: string;
  currency: string;
}

/** One packing slip in the delivered-not-invoiced report (FR-28.12). */
export interface DniLine {
  packingSlipId: string;
  packingSlipNumber: string;
  shipmentId: string;
  orderId: string;
  customerId: string;
  currency: string;
  postingDate: string;
  /** Open balance on account 1405, a decimal string. */
  amount: string;
  daysOutstanding: number;
}

export interface DniReport {
  legalEntityId: string;
  customerId: string | null;
  asOf: string;
  account: string;
  totals: { currency: string; count: number; amount: string }[];
  /** All open slips; `lines` holds at most the requested limit of them. */
  lineCount: number;
  lines: DniLine[];
}

export type ContainerStatus = "OPEN" | "CLOSED" | "SHIPPED";

/** One issued SSCC in the register (section 5.28.14, BR-28.5). */
export interface SsccRow {
  sscc: string;
  containerId: string;
  containerType: string;
  status: ContainerStatus;
  shipmentId: string;
  shipmentNumber: string;
  orderId: string;
  customerId: string;
  /** The slip the container shipped on; null until it ships. */
  packingSlipId: string | null;
  issuedAt: string;
  /** First date the SSCC could be issued again (YYYY-MM-DD). */
  noReuseUntil: string;
}

export interface SsccPage {
  items: SsccRow[];
  /** Pass back as `cursor` for the next page; absent on the last page. */
  nextCursor?: string;
}

export interface SsccFilter {
  companyPrefix?: string;
  customerId?: string;
  from?: string;
  to?: string;
  cursor?: string;
  limit?: number;
}

export interface ReturnableBalance {
  customerId: string;
  containerType: string;
  balance: string;
  lastShippedAt: string | null;
  lastReturnedAt: string | null;
}

/** One short-picked shipment line of the short picks and backorders report (FR-28.2). */
export interface BackorderRow {
  shipmentLineId: string;
  shipmentId: string;
  shipmentNumber: string;
  status: ShipmentStatus;
  warehouseId: string;
  orderId: string;
  customerId: string;
  orderLineId: string;
  productId: string;
  unit: string;
  qtyPlanned: string;
  qtyPicked: string;
  qtyShipped: string;
  /** Planned quantity given up when the remainder was closed (FR-28.8). */
  qtyClosed: string;
  /** Planned minus picked. */
  qtyRemainder: string;
  /** A remainder is left and the shipment is not cancelled. */
  open: boolean;
  /** The line's first short pick. */
  shortPickedAt: string;
}

export interface BackorderPage {
  items: BackorderRow[];
  nextCursor?: string;
}

export interface BackorderFilter {
  warehouseId?: string;
  status?: "open" | "all";
  from?: string;
  to?: string;
  cursor?: string;
  limit?: number;
}

/** A returnable balance aged first in, first out (FR-28.16). */
export interface AgedReturnableBalance extends ReturnableBalance {
  oldestOutstandingDate: string | null;
  daysOutstanding: number;
  chargeDue: boolean;
}

export interface ReturnableAging {
  asOf: string;
  chargeDays: number;
  count: number;
  chargeDueCount: number;
  items: AgedReturnableBalance[];
}

export interface AutoPackResult {
  shipment: Shipment;
  containers: Container[];
  unpackedLineIds: string[];
  warnings: { code: string; shipmentLineId: string; productId: string; message: string }[];
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
  /** The order's carrier; every line of one order names the same one. */
  carrierCode?: string;
  /** The line's pick zone. */
  zone?: string;
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
  /** Ship-to country whose packaging fee rates apply; omit for no packaging fee. */
  countryCode?: string;
  /** Publish the advance ship notice of this slip's containers (FR-28.9). */
  asnEnabled?: boolean;
  costs: { shipmentLineId: string; unitCost: string; costSnapshotId: string }[];
}

export type WaveGroupBy = "CUSTOMER" | "CARRIER" | "ZONE" | "PRODUCT";
export const WAVE_GROUP_KEYS: WaveGroupBy[] = ["CUSTOMER", "CARRIER", "ZONE", "PRODUCT"];

/** Picking workbench filter, group key and limits (FR-28.13). Empty filters match all. */
export interface WorkbenchInput {
  warehouseId: string;
  customerId?: string;
  carrier?: string;
  zone?: string;
  shipFrom?: string;
  shipTo?: string;
  groupBy: WaveGroupBy;
  maxLines?: number;
  maxOrders?: number;
}

export interface WaveShipment {
  id: string;
  shipmentNumber: string;
  orderId: string;
  customerId: string;
  status: ShipmentStatus;
  carrierCode: string | null;
  plannedShipDate: string;
  lineCount: number;
}

export interface WavePlan {
  groupKey: string;
  legalEntityId: string;
  lineCount: number;
  orderCount: number;
  shipments: WaveShipment[];
}

export interface WaveSummary {
  id: string;
  waveNumber: string;
  legalEntityId: string;
  warehouseId: string;
  groupBy: WaveGroupBy;
  groupKey: string;
  status: "OPEN";
  lineCount: number;
  orderCount: number;
  createdAt: string;
}

export interface Wave extends WaveSummary {
  shipments: WaveShipment[];
}

export interface WavesResult<T> {
  warehouseId: string;
  groupBy: WaveGroupBy;
  waveCount: number;
  lineCount: number;
  message: string;
  waves: T[];
}

export type TrackingStatus = "QUOTED" | "LABELLED" | "MANIFESTED" | "IN_TRANSIT" | "DELIVERED" | "EXCEPTION";
export const TRACKING_STATUSES: TrackingStatus[] = ["QUOTED", "LABELLED", "MANIFESTED", "IN_TRANSIT", "DELIVERED", "EXCEPTION"];

export interface TrackingEvent {
  provider: string;
  eventKey: string;
  trackingNumber: string;
  status: TrackingStatus;
  description: string;
  eventTime: string;
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

/** Cancels a shipment that shipped nothing and holds no container (BR-28.6). */
export function cancelShipment(id: string, version: number, key: string): Promise<Shipment> {
  return command<Shipment>(`${shipmentPath(id)}:cancel`, undefined, key, version);
}

/** Closes the open remainder of a partially shipped shipment at what shipped (FR-28.8, CANCEL_REMAINDER). */
export function closeShipmentRemainder(id: string, version: number, key: string): Promise<Shipment> {
  return command<Shipment>(`${shipmentPath(id)}:close-remainder`, undefined, key, version);
}

/** Read-only workbench preview: the waves the create would make now (FR-28.13). */
export function previewWaves(input: WorkbenchInput): Promise<WavesResult<WavePlan>> {
  return send<WavesResult<WavePlan>>("/waves:preview", { method: "POST", body: JSON.stringify(input) });
}

/** Creates the waves in one action; reuse one key per preview so a retry cannot create them twice. */
export function createWaves(input: WorkbenchInput, key: string): Promise<WavesResult<Wave>> {
  return command<WavesResult<Wave>>("/waves", input, key);
}

export function listWaves(filter: { warehouseId?: string; limit?: number } = {}, signal?: AbortSignal): Promise<WaveSummary[]> {
  return items<WaveSummary>(`/waves${query(filter)}`, signal);
}

export function getWave(id: string, signal?: AbortSignal): Promise<Wave> {
  return send<Wave>(`/waves/${encodeURIComponent(id)}`, {}, signal);
}

export function listTracking(id: string, signal?: AbortSignal): Promise<TrackingEvent[]> {
  return items<TrackingEvent>(`${shipmentPath(id)}/tracking`, signal);
}

/** Records a tracking event by hand (BR-28.9 manual tracking); the event key makes a resend harmless. */
export function recordTracking(id: string, event: Omit<TrackingEvent, "description"> & { description?: string }): Promise<TrackingEvent & { created: boolean }> {
  return send(`${shipmentPath(id)}/tracking`, { method: "POST", body: JSON.stringify(event) });
}

/** The container's GS1-128 SSCC label as a ZPL program (text). */
export async function containerLabel(shipmentId: string, containerId: string): Promise<string> {
  const res = await fetch(`${base}${shipmentPath(shipmentId)}/containers/${encodeURIComponent(containerId)}/label`, {
    headers: { Accept: "application/zpl" },
    credentials: "same-origin",
  });
  if (!res.ok) throw await toError(res);
  return res.text();
}

/** Auto-packs picked lines by packing rule (FR-28.15); unitWeights maps a shipment line id to kg per unit. */
export function autoPack(id: string, version: number, unitWeights: Record<string, string>, key: string): Promise<AutoPackResult> {
  return command<AutoPackResult>(`${shipmentPath(id)}:auto-pack`, { unitWeights }, key, version);
}

export function listContainerTypes(signal?: AbortSignal): Promise<ContainerType[]> {
  return items<ContainerType>("/container-types?limit=500", signal);
}

/** Creates or replaces a container type (code is the key). */
export function saveContainerType(input: ContainerType, key: string): Promise<ContainerType> {
  return command<ContainerType>("/container-types", input, key);
}

export function listPackingRules(filter: { productId?: string } = {}, signal?: AbortSignal): Promise<PackingRule[]> {
  return items<PackingRule>(`/packing-rules${query({ ...filter, limit: 500 })}`, signal);
}

/** Creates the rule of a product and customer scope, or replaces it. */
export function savePackingRule(input: Omit<PackingRule, "id">, key: string): Promise<PackingRule> {
  return command<PackingRule>("/packing-rules", input, key);
}

export function listProductPackaging(filter: { productId?: string } = {}, signal?: AbortSignal): Promise<ProductPackaging[]> {
  return items<ProductPackaging>(`/product-packaging${query({ ...filter, limit: 500 })}`, signal);
}

export function saveProductPackaging(input: ProductPackaging, key: string): Promise<ProductPackaging> {
  return command<ProductPackaging>("/product-packaging", input, key);
}

export function listFeeRates(filter: { countryCode?: string } = {}, signal?: AbortSignal): Promise<FeeRate[]> {
  return items<FeeRate>(`/packaging-fee-rates${query({ ...filter, limit: 500 })}`, signal);
}

export function saveFeeRate(input: FeeRate, key: string): Promise<FeeRate> {
  return command<FeeRate>("/packaging-fee-rates", input, key);
}

/** Packaging weight and fee per material for a country and month (YYYY-MM), net of reversals. */
export function packagingReport(filter: { countryCode: string; period: string; legalEntityId?: string }, signal?: AbortSignal): Promise<PackagingReportRow[]> {
  return items<PackagingReportRow>(`/packaging-report${query(filter)}`, signal);
}

export function listReturnableBalances(filter: { customerId?: string } = {}, signal?: AbortSignal): Promise<ReturnableBalance[]> {
  return items<ReturnableBalance>(`/returnable-packaging${query({ ...filter, limit: 500 })}`, signal);
}

/** Records returnable containers received back from a customer; more than the balance is FUL-0015. */
export function returnPackaging(input: { customerId: string; containerType: string; qty: string }, key: string): Promise<ReturnableBalance> {
  return command<ReturnableBalance>("/returnable-packaging:return", input, key);
}

/** Delivered-not-invoiced balance (GL 1405) of a legal entity as of a date, with its slips (FR-28.12). */
export async function deliveredNotInvoiced(
  filter: { legalEntityId: string; customerId?: string; asOf?: string; limit?: number },
  signal?: AbortSignal,
): Promise<DniReport> {
  const body = await send<DniReport>(`/delivered-not-invoiced${query(filter)}`, {}, signal);
  if (!Array.isArray(body.totals) || !Array.isArray(body.lines)) {
    throw new FulfillmentApiError(0, "COM-0001", "The response has no totals or lines", {});
  }
  return body;
}

/** One page of the SSCC register, in SSCC order (BR-28.5, section 5.28.14). */
export async function ssccRegister(filter: SsccFilter, signal?: AbortSignal): Promise<SsccPage> {
  const body = await send<SsccPage>(`/sscc-register${query({ ...filter })}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new FulfillmentApiError(0, "COM-0001", "The response has no items", {});
  }
  return body;
}

/** One page of the short picks and backorders report, newest short pick first (FR-28.2, section 5.28.14). */
export async function listBackorders(filter: BackorderFilter, signal?: AbortSignal): Promise<BackorderPage> {
  const body = await send<BackorderPage>(`/backorders${query({ ...filter })}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new FulfillmentApiError(0, "COM-0001", "The response has no items", {});
  }
  return body;
}

/** Outstanding returnable balances with days outstanding and the charge flag after chargeDays (FR-28.16). */
export async function returnableAging(
  filter: { customerId?: string; chargeDays?: number; chargeDue?: boolean; limit?: number },
  signal?: AbortSignal,
): Promise<ReturnableAging> {
  const { chargeDue, ...rest } = filter;
  const body = await send<ReturnableAging>(`/returnable-packaging/aging${query({ ...rest, chargeDue: chargeDue ? "true" : undefined })}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new FulfillmentApiError(0, "COM-0001", "The response has no items", {});
  }
  return body;
}
