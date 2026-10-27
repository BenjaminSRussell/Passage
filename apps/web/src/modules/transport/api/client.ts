// Client for the transport service (services/transport/api/openapi.yaml).
// Browser calls use /api/v1/transport/..., which the web BFF rewrites to /v1/transport/....
// Amounts are decimal strings end to end; the client never turns them into numbers.

export const base = "/api/v1/transport";

export type Basis = "LANE" | "ZONE" | "DISTANCE" | "WEIGHT_BREAK" | "PER_PALLET" | "PER_CONTAINER" | "FLAT";
export const BASES: Basis[] = ["LANE", "ZONE", "DISTANCE", "WEIGHT_BREAK", "PER_PALLET", "PER_CONTAINER", "FLAT"];

export type RateUnit = "KG" | "PALLET" | "CONTAINER" | "SHIPMENT" | "KM" | "MILE";
export const UNITS: RateUnit[] = ["KG", "PALLET", "CONTAINER", "SHIPMENT", "KM", "MILE"];

export interface RateRow {
  origin: string;
  destination: string;
  breakFrom: string;
  breakTo?: string | null;
  rate: string;
  unit: RateUnit;
}

export interface Accessorial {
  code: string;
  flat: string;
  pct: string;
}

export interface NewRateCard {
  carrierId: string;
  serviceId: string;
  basis: Basis;
  currency: string;
  validFrom: string;
  validTo?: string | null;
  minimumCharge: string;
  dimDivisor?: number;
  fuelPct: string;
  rows: RateRow[];
  accessorials: Accessorial[];
}

export interface RateCard extends NewRateCard {
  id: string;
  validTo: string | null;
  dimDivisor: number;
}

export interface RateRequest {
  originZone: string;
  destinationZone: string;
  weightKg: string;
  volumeM3: string;
  pallets: number;
  distanceKm?: string;
  shipDate: string;
  accessorialCodes?: string[];
}

export interface Charge {
  code: string;
  amount: string;
}

export interface TraceStep {
  step: string;
  input: string;
  amount: string;
}

export type QuoteStatus = "OK" | "NO_VALID_RATE_CARD" | "NO_LANE_RATE";

export interface Quote {
  carrierId: string;
  serviceId: string;
  currency: string;
  status: QuoteStatus;
  total: string;
  charges: Charge[];
  trace: TraceStep[];
}

export interface RateResponse {
  shipmentId: string;
  options: Quote[];
  chosen?: Quote;
}

export interface Line {
  chargeCode: string;
  amount: string;
}

export type BillStatus = "UNMATCHED" | "MATCHED" | "VARIANCE" | "DISPUTED" | "APPROVED" | "SETTLED";
export const BILL_STATUSES: BillStatus[] = ["UNMATCHED", "MATCHED", "VARIANCE", "DISPUTED", "APPROVED", "SETTLED"];

export type InvoiceStatus = "MATCHED" | "VARIANCE" | "UNEXPECTED_INVOICE";
export const INVOICE_STATUSES: InvoiceStatus[] = ["MATCHED", "VARIANCE", "UNEXPECTED_INVOICE"];

export type InvoiceSource = "EDI210" | "MANUAL" | "API";

export interface NewFreightBill {
  loadId: string;
  carrierId: string;
  currency: string;
  expectedLines: Line[];
}

export interface FreightBill extends NewFreightBill {
  id: string;
  expectedTotal: string;
  matchStatus: BillStatus;
  carrierInvoiceId?: string;
  varianceTotal?: string;
  version: number;
  createdAt: string;
}

export interface NewCarrierInvoice {
  carrierId: string;
  invoiceNo: string;
  invoiceDate: string;
  source: InvoiceSource;
  freightBillId?: string;
  currency: string;
  lines: Line[];
}

export interface CarrierInvoice extends NewCarrierInvoice {
  id: string;
  total: string;
  matchStatus: InvoiceStatus;
  varianceTotal?: string;
  createdAt: string;
}

export interface MatchLine {
  chargeCode: string;
  expected: string;
  invoiced: string;
  variance: string;
  allowed: string;
  within: boolean;
}

export interface MatchResult {
  status: "MATCHED" | "VARIANCE";
  lines: MatchLine[];
  varianceTotal: string;
}

export interface FreightBillDetail extends FreightBill {
  carrierInvoice?: CarrierInvoice;
  match?: MatchResult;
}

export interface InvoiceResponse {
  invoice: CarrierInvoice;
  freightBill?: FreightBill;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class TransportApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "TransportApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof TransportApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<TransportApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new TransportApiError(res.status, code, message, fields);
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
    throw new TransportApiError(0, "COM-0001", "The response has no items");
  }
  return body.items;
}

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(key, value);
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

export function listRateCards(filter: { carrierId?: string; serviceId?: string } = {}, signal?: AbortSignal): Promise<RateCard[]> {
  return items<RateCard>(`/rate-cards${query(filter)}`, signal);
}

/** Creates a rate card; reuse one key per form so a retried submit cannot create two. */
export function createRateCard(input: NewRateCard, key: string = newKey()): Promise<RateCard> {
  return post<RateCard>("/rate-cards", input, key);
}

export function rateShipment(shipmentId: string, body: RateRequest): Promise<RateResponse> {
  return post<RateResponse>(`/shipments/${encodeURIComponent(shipmentId)}:rate`, body);
}

export function listFreightBills(filter: { matchStatus?: BillStatus | ""; carrierId?: string } = {}, signal?: AbortSignal): Promise<FreightBill[]> {
  return items<FreightBill>(`/freight-bills${query(filter)}`, signal);
}

export function getFreightBill(id: string, signal?: AbortSignal): Promise<FreightBillDetail> {
  return send<FreightBillDetail>(`/freight-bills/${encodeURIComponent(id)}`, {}, signal);
}

export function createFreightBill(input: NewFreightBill, key: string = newKey()): Promise<FreightBill> {
  return post<FreightBill>("/freight-bills", input, key);
}

export function listCarrierInvoices(
  filter: { matchStatus?: InvoiceStatus | ""; carrierId?: string } = {},
  signal?: AbortSignal,
): Promise<CarrierInvoice[]> {
  return items<CarrierInvoice>(`/carrier-invoices${query(filter)}`, signal);
}

/** Registers a carrier invoice; the carrier and invoice number are its natural idempotency key (TMS-0108). */
export function registerCarrierInvoice(input: NewCarrierInvoice): Promise<InvoiceResponse> {
  return post<InvoiceResponse>("/carrier-invoices", input);
}

// ---- Tenders (FR-34.5, BR-34.3) ---------------------------------------------

export type TenderStatus = "PENDING" | "ACCEPTED" | "TENDER_FAILED";
export const TENDER_STATUSES: TenderStatus[] = ["PENDING", "ACCEPTED", "TENDER_FAILED"];

export type AttemptResponse = "PENDING" | "ACCEPTED" | "DECLINED" | "EXPIRED";

export interface TenderAttempt {
  rank: number;
  carrierId: string;
  sentAt: string;
  expiresAt: string;
  response: AttemptResponse;
  rateQuoted?: string;
}

export interface Tender {
  id: string;
  loadId: string;
  status: TenderStatus;
  carriers: string[];
  currency?: string;
  timeoutMin: number;
  attempts: TenderAttempt[];
  acceptedCarrierId?: string;
  plannerTask: boolean;
  pendingCarrierId?: string;
  pendingExpiresAt?: string;
  overdue: boolean;
  version: number;
  createdAt: string;
}

export interface TenderCarrier {
  carrierId: string;
  rateQuoted?: string;
}

export interface NewTender {
  carriers: TenderCarrier[];
  currency?: string;
  timeoutMin?: number;
}

/** Starts a tender cascade for a load; reuse one key per form so a retried submit cannot start two. */
export function startTender(loadId: string, input: NewTender, key: string = newKey()): Promise<Tender> {
  return post<Tender>(`/loads/${encodeURIComponent(loadId)}:tender`, input, key);
}

export function listTenders(filter: { status?: TenderStatus | ""; loadId?: string } = {}, signal?: AbortSignal): Promise<Tender[]> {
  return items<Tender>(`/tenders${query(filter)}`, signal);
}

export function getTender(id: string, signal?: AbortSignal): Promise<Tender> {
  return send<Tender>(`/tenders/${encodeURIComponent(id)}`, {}, signal);
}

/** Records a carrier's accept or decline; repeating the same response returns the tender unchanged. */
export function respondTender(id: string, body: { carrierId: string; accept: boolean }): Promise<Tender> {
  return post<Tender>(`/tenders/${encodeURIComponent(id)}:respond`, body);
}

/** Runs the expiry sweep and returns the tenders it cascaded. */
export async function expireTenders(): Promise<Tender[]> {
  const body = await post<{ items?: Tender[] }>("/tenders:expire", {});
  return Array.isArray(body.items) ? body.items : [];
}

// ---- Tracking (FR-34.6, BR-34.9) --------------------------------------------

export type TrackingCode = "X3" | "AF" | "X1" | "D1";
export const TRACKING_CODES: TrackingCode[] = ["X3", "AF", "X1", "D1"];

export type TrackingSource = "EDI214" | "API" | "TELEMATICS" | "DRIVER";
export const TRACKING_SOURCES: TrackingSource[] = ["EDI214", "API", "TELEMATICS", "DRIVER"];

export interface NewTrackingEvent {
  loadId: string;
  stopNo: number;
  code: TrackingCode;
  occurredAt: string;
  location?: string;
  source: TrackingSource;
  ref: string;
}

export interface TrackingEvent extends NewTrackingEvent {
  id: string;
  receivedAt: string;
}

export type StopStatus = "PENDING" | "ARRIVED" | "PICKED_UP" | "DELIVERED";

export interface StopState {
  stopNo: number;
  status: StopStatus;
  arrivedAt?: string;
  pickedUpAt?: string;
  deliveredAt?: string;
  pickupInferred: boolean;
}

export interface LoadTracking {
  loadId: string;
  events: TrackingEvent[];
  stops: StopState[];
}

/** Ingests one status report; source and ref are its idempotency key. */
export function recordTrackingEvent(input: NewTrackingEvent): Promise<TrackingEvent> {
  return post<TrackingEvent>("/tracking-events", input);
}

export function listTrackingEvents(filter: { loadId?: string } = {}, signal?: AbortSignal): Promise<TrackingEvent[]> {
  return items<TrackingEvent>(`/tracking-events${query(filter)}`, signal);
}

export function getLoadTracking(loadId: string, signal?: AbortSignal): Promise<LoadTracking> {
  return send<LoadTracking>(`/loads/${encodeURIComponent(loadId)}/tracking`, {}, signal);
}

// ---- Landed-cost voyages (FR-34.9, FR-34.12) ---------------------------------

export type VoyageStatus = "ESTIMATED" | "POSTED";
export const VOYAGE_STATUSES: VoyageStatus[] = ["ESTIMATED", "POSTED"];

export type LegMode = "OCEAN" | "AIR" | "ROAD" | "RAIL";
export const LEG_MODES: LegMode[] = ["OCEAN", "AIR", "ROAD", "RAIL"];

export type ContainerType = "20GP" | "40GP" | "40HC" | "45HC" | "REEFER";
export const CONTAINER_TYPES: ContainerType[] = ["20GP", "40GP", "40HC", "45HC", "REEFER"];

export type AllocationMethod = "WEIGHT" | "VOLUME" | "VALUE" | "QUANTITY" | "EQUAL";
export const ALLOCATION_METHODS: AllocationMethod[] = ["WEIGHT", "VOLUME", "VALUE", "QUANTITY", "EQUAL"];

export interface Leg {
  seq?: number;
  from: string;
  to: string;
  mode: LegMode;
}

export interface Container {
  number: string;
  type: ContainerType;
  seal?: string;
}

export interface Folio {
  folioNo: string;
  forwarder: string;
  containers: string[];
}

export interface VoyageCostLine {
  costType: string;
  amount: string;
  allocationMethod: AllocationMethod;
}

export interface VoyageReceiptLine {
  line: number;
  value: string;
  weight: string;
  volume: string;
  quantity: string;
}

export interface NewVoyage {
  reference: string;
  currency: string;
  legs: Leg[];
  containers: Container[];
  folios: Folio[];
  costLines: VoyageCostLine[];
  receiptLines: VoyageReceiptLine[];
}

export interface Allocation {
  line: number;
  amount: string;
}

export interface CostAllocation {
  costType: string;
  allocationMethod: AllocationMethod;
  amount: string;
  shares: Allocation[];
  residual: string;
  residualLine: number;
}

export interface Voyage extends NewVoyage {
  id: string;
  status: VoyageStatus;
  costAllocations: CostAllocation[];
  allocations: Allocation[];
  version: number;
  createdAt: string;
  postedAt?: string;
}

export interface VoyageSummary {
  id: string;
  reference: string;
  currency: string;
  status: VoyageStatus;
  costTotal: string;
  containers: number;
  receiptLines: number;
  createdAt: string;
  postedAt?: string;
}

export function createVoyage(input: NewVoyage, key: string = newKey()): Promise<Voyage> {
  return post<Voyage>("/voyages", input, key);
}

export function listVoyages(filter: { status?: VoyageStatus | "" } = {}, signal?: AbortSignal): Promise<VoyageSummary[]> {
  return items<VoyageSummary>(`/voyages${query(filter)}`, signal);
}

export function getVoyage(id: string, signal?: AbortSignal): Promise<Voyage> {
  return send<Voyage>(`/voyages/${encodeURIComponent(id)}`, {}, signal);
}

/** Allocates and posts the voyage's landed cost; a posted voyage comes back unchanged. */
export function allocateVoyage(id: string): Promise<Voyage> {
  return post<Voyage>(`/landed-cost/voyages/${encodeURIComponent(id)}:allocate`, {});
}
