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

export type DecisionAction = "APPROVE" | "DISPUTE";

/** One approval or dispute of a freight bill, the audit trail of BR-34.4. */
export interface Decision {
  id: string;
  freightBillId: string;
  action: DecisionAction;
  fromStatus: BillStatus;
  toStatus: BillStatus;
  reason?: string;
  decidedBy: string;
  decidedAt: string;
}

export interface FreightBillDetail extends FreightBill {
  carrierInvoice?: CarrierInvoice;
  match?: MatchResult;
  decisions?: Decision[];
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

function post<T>(path: string, body: unknown, key?: string, extra: Record<string, string> = {}): Promise<T> {
  const headers: Record<string, string> = { ...extra };
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

/**
 * Approves (MATCHED, VARIANCE or DISPUTED) or disputes (VARIANCE) a freight bill
 * (BR-34.4). `version` is the bill version the user saw, sent as If-Match; a
 * stale one is 412 conflict_version. A reason is required except to approve a
 * MATCHED bill.
 */
export function decideFreightBill(id: string, action: DecisionAction, version: number, reason: string, key: string = newKey()): Promise<FreightBill> {
  const verb = action === "APPROVE" ? "approve" : "dispute";
  return post<FreightBill>(`/freight-bills/${encodeURIComponent(id)}:${verb}`, reason ? { reason } : {}, key, { "If-Match": `"${version}"` });
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

// ---- Loads, DG checks and screening (FR-34.1, FR-34.15, FR-34.19) ------------

export type LoadMode = "TL" | "LTL" | "PARCEL" | "INTERMODAL";
export const LOAD_MODES: LoadMode[] = ["TL", "LTL", "PARCEL", "INTERMODAL"];

export type LoadStatus = "PLANNED" | "RATED" | "TENDERED" | "ACCEPTED" | "DISPATCHED" | "IN_TRANSIT" | "DELIVERED" | "CLOSED" | "CANCELLED";
export const LOAD_STATUSES: LoadStatus[] = ["PLANNED", "RATED", "TENDERED", "ACCEPTED", "DISPATCHED", "IN_TRANSIT", "DELIVERED", "CLOSED", "CANCELLED"];

export interface Capacity {
  maxWeightKg: string;
  maxVolumeM3: string;
  maxPallets: number;
}

export interface LoadShipment {
  id: string;
  shipFromSiteId: string;
  shipToRegion: string;
  mode: LoadMode;
  pickupDate: string;
  weightKg: string;
  volumeM3: string;
  pallets: number;
}

export interface LoadBuildRequest {
  capacity: Capacity;
  shipments: LoadShipment[];
}

export interface Load {
  id: string;
  mode: LoadMode;
  status: LoadStatus;
  shipFromSiteId: string;
  shipToRegion: string;
  pickupDate: string;
  totalWeightKg: string;
  totalVolumeM3: string;
  pallets: number;
  capacity: Capacity;
  shipmentIds: string[];
  version: number;
  createdAt: string;
}

export type Regulation = "ADR" | "49CFR";
export const REGULATIONS: Regulation[] = ["ADR", "49CFR"];

export type DGResult = "PASS" | "WARN" | "FAIL";

export interface DGLine {
  unNumber: string;
  properShippingName: string;
  class: string;
  packingGroup?: "" | "I" | "II" | "III";
  quantity: string;
  transportCategory: number;
  limitedQuantity?: boolean;
}

export interface DGRequest {
  regulation: Regulation;
  emergencyResponsePhone?: string;
  lines: DGLine[];
}

export interface DGFinding {
  ruleId: string;
  severity: "WARN" | "FAIL";
  code?: string;
  detail: string;
}

export interface PaperLine {
  unNumber: string;
  properShippingName: string;
  class: string;
  packingGroup?: string;
  quantity: string;
  limitedQuantity: boolean;
  emergencyResponsePhone?: string;
}

export interface DGCheck {
  id: string;
  loadId: string;
  regulation: Regulation;
  result: DGResult;
  adrPoints: string;
  findings: DGFinding[];
  shippingPapers: PaperLine[];
  checkedBy: string;
  checkedAt: string;
}

export interface LoadDetail extends Load {
  dgChecks: DGCheck[];
}

export type PartyRole = "SOLD_TO" | "SHIP_TO" | "CONSIGNEE" | "FORWARDER" | "CARRIER";
export const PARTY_ROLES: PartyRole[] = ["SOLD_TO", "SHIP_TO", "CONSIGNEE", "FORWARDER", "CARRIER"];

export interface ScreeningParty {
  role: PartyRole;
  name: string;
  country?: string;
}

export type ScreenStatus = "CLEAR" | "POTENTIAL_MATCH" | "BLOCKED" | "SCREENING_PENDING" | "NOT_SCREENED";

export interface ScreeningDecision {
  loadId: string;
  crossBorder: boolean;
  decision: "CLEAR" | "WARN" | "HOLD";
  parties: PartyScreening[];
  /** The screening hold a HOLD decision opened or refreshed. */
  holdId?: string;
}

export interface PartyScreening {
  role: PartyRole;
  name: string;
  status: ScreenStatus;
  code?: string;
}

/** Builds loads by rule; reuse one key per form so a retried submit cannot plan the shipments twice. */
export async function buildLoads(input: LoadBuildRequest, key: string = newKey()): Promise<Load[]> {
  const body = await post<{ items?: Load[] }>("/loads:build", input, key);
  return Array.isArray(body.items) ? body.items : [];
}

export function listLoads(filter: { status?: LoadStatus | "" } = {}, signal?: AbortSignal): Promise<Load[]> {
  return items<Load>(`/loads${query(filter)}`, signal);
}

export function getLoad(id: string, signal?: AbortSignal): Promise<LoadDetail> {
  return send<LoadDetail>(`/loads/${encodeURIComponent(id)}`, {}, signal);
}

/** Runs and stores the DG check of a load; a FAIL blocks tendering (AC-34.13). */
export function checkLoadDG(id: string, input: DGRequest): Promise<DGCheck> {
  return post<DGCheck>(`/loads/${encodeURIComponent(id)}:dg-check`, input);
}

/** Screens the parties of a load through the screening provider hook (BR-34.17). */
export function screenLoad(id: string, input: { crossBorder?: boolean; parties: ScreeningParty[] }): Promise<ScreeningDecision> {
  return post<ScreeningDecision>(`/loads/${encodeURIComponent(id)}:screen`, input);
}

// ---- Gate check-in and check-out (FR-34.14, BR-34.13, BR-34.14) --------------

export type VisitDirection = "INBOUND" | "OUTBOUND";
export const VISIT_DIRECTIONS: VisitDirection[] = ["INBOUND", "OUTBOUND"];

export type VisitStatus = "IN_YARD" | "CHECKED_OUT";
export const VISIT_STATUSES: VisitStatus[] = ["IN_YARD", "CHECKED_OUT"];

export type ArrivalStatus = "ON_TIME" | "EARLY" | "LATE";

export type GateLogKind = "CHECK_IN" | "SEALED" | "CHECK_OUT" | "CHECK_OUT_BLOCKED";

export interface NewCheckIn {
  siteId: string;
  direction: VisitDirection;
  loadId?: string;
  carrierScac: string;
  trailerNo: string;
  sealNo?: string;
  driverName: string;
  driverLicenseId: string;
  appointmentId?: string;
  scheduledAt?: string;
  arrivedAt?: string;
  freeTimeMin?: number;
  toleranceMin?: number;
}

export interface Arrival {
  status: ArrivalStatus;
  deltaMin: number;
  code?: string;
}

export interface Detention {
  dwellMin: number;
  detentionMin: number;
  hours: number;
  charge: string;
  code?: string;
}

export interface GateVisit {
  id: string;
  siteId: string;
  direction: VisitDirection;
  loadId?: string;
  carrierScac: string;
  trailerNo: string;
  sealNo?: string;
  driverName: string;
  driverLicenseId: string;
  appointmentId?: string;
  scheduledAt?: string;
  walkIn: boolean;
  arrival?: Arrival;
  checkedInAt: string;
  checkedOutAt?: string;
  freeTimeMin: number;
  status: VisitStatus;
  detention?: Detention;
  version: number;
}

export interface GateLogEntry {
  id: string;
  visitId: string;
  kind: GateLogKind;
  occurredAt: string;
  recordedBy: string;
  detail: Record<string, string>;
}

export interface GateVisitDetail extends GateVisit {
  log: GateLogEntry[];
}

export interface CheckOut {
  visitId: string;
  sealNo: string;
  checkOutAt?: string;
  hourlyRate?: string;
}

export interface GateVisitFilter {
  siteId?: string;
  status?: VisitStatus | "";
  carrierScac?: string;
  date?: string;
}

/** Checks a driver and trailer in; reuse one key per form so a retried submit cannot open two visits. */
export function checkIn(input: NewCheckIn, key: string = newKey()): Promise<GateVisit> {
  return post<GateVisit>("/gate/check-in", input, key);
}

/** Checks a visit out; a seal that does not match is TMS-0129 and stays in the gate log (AC-34.12). */
export function checkOut(input: CheckOut, key: string = newKey()): Promise<GateVisit> {
  return post<GateVisit>("/gate/check-out", input, key);
}

/** Records the seal applied at load confirmation. */
export function recordSeal(id: string, sealNo: string): Promise<GateVisit> {
  return post<GateVisit>(`/gate/visits/${encodeURIComponent(id)}:seal`, { sealNo });
}

export function listGateVisits(filter: GateVisitFilter = {}, signal?: AbortSignal): Promise<GateVisit[]> {
  return items<GateVisit>(`/gate/visits${query({ ...filter })}`, signal);
}

export function getGateVisit(id: string, signal?: AbortSignal): Promise<GateVisitDetail> {
  return send<GateVisitDetail>(`/gate/visits/${encodeURIComponent(id)}`, {}, signal);
}

// ---- Goods in transit (FR-34.12, BR-34.11) -----------------------------------

export type GITStatus = "PENDING" | "IN_TRANSIT";
export const GIT_STATUSES: GITStatus[] = ["PENDING", "IN_TRANSIT"];

/** Incoterms 2020 rules that move ownership at origin and so open a goods-in-transit order (BR-34.11). */
export const ORIGIN_INCOTERMS = ["EXW", "FCA", "FAS", "FOB", "CFR", "CIF", "CPT", "CIP"] as const;

export interface NewGoodsInTransit {
  voyageId: string;
  incoterm: string;
  siteId: string;
  receiptLines?: number[];
}

export interface GITLine {
  line: number;
  value: string;
  quantity: string;
  costs: { costType: string; amount: string }[];
  estimated: string;
}

export interface GoodsInTransit {
  id: string;
  voyageId: string;
  voyageReference: string;
  siteId: string;
  incoterm: string;
  transferEvent: string;
  inTransitWarehouse: string;
  currency: string;
  status: GITStatus;
  lines: GITLine[];
  goodsValue: string;
  estimatedTotal: string;
  version: number;
  createdAt: string;
  shippedAt?: string;
  code?: string;
}

/** Opens a goods-in-transit order; reuse one key per form so a retried submit cannot open two. */
export function createGoodsInTransit(input: NewGoodsInTransit, key: string = newKey()): Promise<GoodsInTransit> {
  return post<GoodsInTransit>("/goods-in-transit", input, key);
}

export function listGoodsInTransit(filter: { voyageId?: string; status?: GITStatus | "" } = {}, signal?: AbortSignal): Promise<GoodsInTransit[]> {
  return items<GoodsInTransit>(`/goods-in-transit${query(filter)}`, signal);
}

export function getGoodsInTransit(id: string, signal?: AbortSignal): Promise<GoodsInTransit> {
  return send<GoodsInTransit>(`/goods-in-transit/${encodeURIComponent(id)}`, {}, signal);
}

/** Ships the order at the transfer event (TMS-0121); a shipped order comes back unchanged. */
export function shipGoodsInTransit(id: string, shippedAt?: string): Promise<GoodsInTransit> {
  return post<GoodsInTransit>(`/goods-in-transit/${encodeURIComponent(id)}:ship`, shippedAt ? { shippedAt } : {});
}

// ---- Screening holds (FR-34.19, BR-34.17) -------------------------------------

export type HoldStatus = "OPEN" | "RELEASED";
export const HOLD_STATUSES: HoldStatus[] = ["OPEN", "RELEASED"];

export interface ScreeningHold {
  id: string;
  loadId: string;
  status: HoldStatus;
  parties: PartyScreening[];
  createdBy: string;
  createdAt: string;
  releasedBy?: string;
  releaseReason?: string;
  releasedAt?: string;
  version: number;
}

export function listScreeningHolds(filter: { status?: HoldStatus | ""; loadId?: string } = {}, signal?: AbortSignal): Promise<ScreeningHold[]> {
  return items<ScreeningHold>(`/screening-holds${query(filter)}`, signal);
}

export function getScreeningHold(id: string, signal?: AbortSignal): Promise<ScreeningHold> {
  return send<ScreeningHold>(`/screening-holds/${encodeURIComponent(id)}`, {}, signal);
}

/** Releases a POTENTIAL_MATCH or SCREENING_PENDING hold with a reason (TRADE_COMPLIANCE_RELEASE); a BLOCKED party is refused (TMS-0133). */
export function releaseScreeningHold(id: string, reason: string): Promise<ScreeningHold> {
  return post<ScreeningHold>(`/screening-holds/${encodeURIComponent(id)}:release`, { reason });
}

// ---- Route optimization (FR-34.2, BR-34.1) -----------------------------------

export interface RoutePoint {
  x: number;
  y: number;
}

export interface RouteStop {
  id: string;
  point: RoutePoint;
  earliest: number;
  latest: number;
  serviceMin: number;
  weightKg: number;
  volumeL: number;
  pallets: number;
}

export interface RouteVehicle {
  id: string;
  capacityKg: number;
  capacityL: number;
  pallets: number;
  fixedCost?: number;
  maxRouteMin?: number;
}

export interface RouteRequest {
  depot: RoutePoint;
  stops: RouteStop[];
  vehicles: RouteVehicle[];
  speedKmh?: number;
  latenessPerMin?: number;
  timeLimitS?: number;
}

export interface RouteSolutionRoute {
  vehicleId: string;
  stops: { stopId: string; arrivalMin: number; lateMin: number }[];
  distanceKm: number;
  endMin: number;
}

export interface RoutePlanSummary {
  id: string;
  requestHash: string;
  stopCount: number;
  vehicleCount: number;
  routeCount: number;
  droppedCount: number;
  totalDistanceKm: number;
  timeLimitS: number;
  createdBy: string;
  createdAt: string;
}

export interface RoutePlan extends RoutePlanSummary {
  request: RouteRequest;
  solution: { routes: RouteSolutionRoute[]; dropped: string[] };
  replayed: boolean;
  warnings: { code: string; severity: string; detail: string }[];
}

/** Solves and stores a route plan; the same request returns the stored plan (the request hash is the key). */
export function optimizeRoutes(input: RouteRequest): Promise<RoutePlan> {
  return post<RoutePlan>("/routes:optimize", input);
}

export function listRoutePlans(signal?: AbortSignal): Promise<RoutePlanSummary[]> {
  return items<RoutePlanSummary>("/routes", signal);
}

export function getRoutePlan(id: string, signal?: AbortSignal): Promise<RoutePlan> {
  return send<RoutePlan>(`/routes/${encodeURIComponent(id)}`, {}, signal);
}

// ---- Load plan stacking (FR-34.18) -------------------------------------------

export interface Trailer {
  lengthMm: number;
  widthMm: number;
  maxHeightMm: number;
  maxWeightKg: number;
  kingpinMm?: number;
  axleMm?: number;
}

export interface PlanPallet {
  id: string;
  lengthMm: number;
  widthMm: number;
  heightMm: number;
  weightKg: number;
  stackable: boolean;
  stopSeq: number;
  maxStackKg?: number;
}

export interface Placement {
  palletIds: string[];
  xMm: number;
  yMm: number;
  lengthMm: number;
  widthMm: number;
  heightMm: number;
  weightKg: number;
}

export interface LoadPlan {
  placements: Placement[];
  unplaced: string[];
  floorPositionsUsed: number;
  lengthUsedMm: number;
  layers: number;
  totalWeightKg: number;
  loadSequence: string[];
  axle: { kingpinKg: number; axleKg: number };
}

/** Computes a layer-based load plan; nothing is stored. */
export function stackLoadPlan(input: { trailer: Trailer; pallets: PlanPallet[] }): Promise<LoadPlan> {
  return post<LoadPlan>("/load-plans:stack", input);
}
