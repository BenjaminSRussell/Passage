import { apiFetch } from "../../../shell/api/http";
// Client for the edi service (services/edi/api/openapi.yaml). Browser calls use
// /api/v1/edi/..., which the web BFF rewrites to /edi/v1/... on the service.

export const base = "/api/v1/edi";

export type Environment = "TEST" | "PROD";
export const ENVIRONMENTS: readonly Environment[] = ["TEST", "PROD"];

export type AckStatus = "PENDING" | "ACCEPTED" | "ACCEPTED_WITH_ERRORS" | "REJECTED" | "TIMEOUT";
export const ACK_STATUSES: readonly AckStatus[] = ["PENDING", "ACCEPTED", "ACCEPTED_WITH_ERRORS", "REJECTED", "TIMEOUT"];

export type MessageStatus = "RECEIVED" | "MAPPED" | "POSTED" | "FAILED" | "SENT" | "ACKED";
export const MESSAGE_STATUSES: readonly MessageStatus[] = ["RECEIVED", "MAPPED", "POSTED", "FAILED", "SENT", "ACKED"];

export type Direction = "IN" | "OUT";

/** Transaction sets the service generates outbound (POST /partners/{id}/outbound). */
export type OutboundSet = "856" | "810" | "855";
export const OUTBOUND_SETS: readonly OutboundSet[] = ["856", "810", "855"];

/** Bounds the service enforces on the partner's 997 expectation (BR-35.5). */
export const ACK_SLA = { min: 5, max: 1440, default: 60 } as const;

export interface Partner {
  id: string;
  name: string;
  isaQualifier: string;
  isaId: string;
  environment: Environment;
  ackSlaMinutes: number;
  status: "active" | "inactive";
  version: number;
  createdAt: string;
}

export interface NewPartner {
  name: string;
  isaQualifier: string;
  isaId: string;
  environment: Environment;
  /** Omitted means the service default (60 minutes). */
  ackSlaMinutes?: number;
}

export interface Issue {
  code: string;
  detail: string;
  /** True when the issue rejects the transaction (997 AK5 R). */
  reject: boolean;
}

export interface OrderLine {
  lineNo: string;
  /** Decimal string. */
  quantity: string;
  uom: string;
  /** Decimal string. */
  unitPrice: string;
  priceBasis?: string;
  itemRef?: { qualifier: string; value: string };
  description?: string;
}

export interface Charge {
  indicator: "C" | "A";
  code: string;
  /** Decimal string with two decimals. */
  amount: string;
  description?: string;
}

/** Canonical order mapped from an inbound 850 (BR-35.1). */
export interface CanonicalOrder {
  purpose: string;
  orderType: string;
  poNumber: string;
  poDate: string;
  currency?: string;
  department?: string;
  vendorId?: string;
  requestedDelivery?: string;
  requestedShip?: string;
  shipToName?: string;
  shipToGln?: string;
  billToName?: string;
  billToGln?: string;
  charges?: Charge[];
  lines: OrderLine[];
}

export interface MapTransaction {
  transactionSet: string;
  stControl: string;
  status: "MAPPED" | "FAILED";
  order?: CanonicalOrder;
  issues: Issue[];
}

export interface MapTestResponse {
  transactions: MapTransaction[];
}

export interface InterchangeSummary {
  id: string;
  partnerId: string;
  partnerName: string;
  direction: "IN" | "OUT";
  standard: "X12" | "EDIFACT" | "UBL" | "CXML";
  version: string;
  controlNumber: string;
  ackStatus: AckStatus;
  receivedAt: string;
  messageCount: number;
  failedCount: number;
}

export interface ItemRefDoc {
  qualifier: "BP" | "VP" | "UP" | "EN";
  value: string;
}

export interface LocationDoc {
  name: string;
  /** 13-digit GS1 GLN. */
  gln: string;
}

/** Canonical advance ship notice for an outbound 856 (BR-35.2). Decimals are strings. */
export interface AsnDocument {
  shipmentId: string;
  shipDate: string;
  shipTime: string;
  bol?: string;
  proNumber?: string;
  scac: string;
  grossWeight: string;
  weightUnit: "LB" | "KG";
  shipFrom: LocationDoc;
  shipTo: LocationDoc;
  orders: {
    poNumber: string;
    poDate: string;
    packs: { sscc: string; items: { lineNo: string; quantity: string; uom: string; itemRefs: ItemRefDoc[] }[] }[];
  }[];
}

/** Canonical customer invoice for an outbound 810 (BR-35.3). Decimals are strings. */
export interface InvoiceDocument {
  number: string;
  date: string;
  poNumber: string;
  poDate: string;
  creditNote?: boolean;
  shipDate?: string;
  bol?: string;
  terms: { code?: string; discountPct: string; discountDays?: string; netDays?: string };
  remitTo: LocationDoc;
  billTo: LocationDoc;
  shipTo: LocationDoc;
  lines: { lineNo: string; quantity: string; uom: string; unitPrice: string; itemRefs: ItemRefDoc[] }[];
  taxes?: { type: "ST" | "VA" | "GS"; amount: string; percent: string }[];
  charges?: { indicator: "C" | "A"; code: string; amount: string }[];
  total: string;
}

/** Canonical purchase order acknowledgment for an outbound 855. */
export interface AckDocument {
  poNumber: string;
  poDate: string;
  ackDate: string;
  ackType: "AD" | "AC" | "RD" | "AK" | "RJ";
  lines?: { lineNo: string; orderedQty: string; uom: string; status: "IA" | "IQ" | "IR"; ackQty: string }[];
}

/** A partner's 997 as read by the service. Codes are A, E, P (group only) or R. */
export interface FunctionalAck {
  functionalId: string;
  groupControl: string;
  code: string;
  sets: { setId: string; control: string; code: string }[];
}

export type CanonicalDocument = CanonicalOrder | FunctionalAck | AsnDocument | InvoiceDocument | AckDocument;

export interface InterchangeMessage {
  id: string;
  transactionSet: string;
  stControl: string;
  status: MessageStatus;
  /** Shape follows transactionSet: 850 order, 997 acknowledgment, 856/810/855 outbound document. */
  canonical?: CanonicalDocument;
  error?: string;
}

export interface InterchangeDetail extends InterchangeSummary {
  ack997?: string;
  messages: InterchangeMessage[];
}

export interface ReceiveResult {
  interchangeId: string;
  ackStatus: AckStatus;
  messages: { messageId: string; transactionSet: string; stControl: string; status: MessageStatus; error?: string }[];
  /** Absent when only 997s were received: a 997 is never acknowledged. */
  ack997?: string;
}

export interface OutboundResult {
  interchangeId: string;
  messageId: string;
  transactionSet: OutboundSet;
  /** ISA13, nine digits. */
  controlNumber: string;
  /** The serialized interchange. */
  x12: string;
}

/** One row of the message monitor (GET /messages): a transaction with its interchange envelope. */
export interface MonitorMessage {
  id: string;
  interchangeId: string;
  partnerId: string;
  partnerName: string;
  direction: Direction;
  standard: "X12" | "EDIFACT" | "UBL" | "CXML";
  /** ISA13 of the interchange. */
  controlNumber: string;
  ackStatus: AckStatus;
  transactionSet: string;
  stControl: string;
  status: MessageStatus;
  error?: string;
  createdAt: string;
}

/** One message with its canonical document (GET /messages/{id}). The raw payload is never returned. */
export interface MonitorMessageDetail extends MonitorMessage {
  canonical?: CanonicalDocument;
  /** True when party names in the document are "***" because the user lacks edi.pii.view (BR-35.12). */
  piiMasked: boolean;
}

/** The value the service puts in place of personal data the user may not see (BR-35.12). */
export const MASKED = "***";

/** One data element of the raw viewer. */
export interface SegmentElement {
  /** X12 reference: segment id plus position, for example N102. */
  ref: string;
  /** X12 dictionary name, shown on hover; absent when the element is not in the dictionary. */
  name?: string;
  value: string;
  /** True when the value was replaced by "***" (BR-35.12). */
  masked?: boolean;
}

/** One node of the segment tree: interchange (ISA), group (GS), transaction (ST) or segment. */
export interface SegmentNode {
  kind: "interchange" | "group" | "transaction" | "segment";
  name: string;
  elements?: SegmentElement[];
  children?: SegmentNode[];
}

/** GET /interchanges/{id}/payload: the raw payload as a segment tree. The raw text is never returned. */
export interface PayloadTree {
  interchangeId: string;
  direction: Direction;
  piiMasked: boolean;
  tree: SegmentNode;
}

/** The service reprocesses at most this many interchanges in one bulk request. */
export const MAX_BULK_REPROCESS = 200;

export type BulkOutcome = "REPROCESSED" | "NOT_FOUND" | "NOT_INBOUND" | "UNREADABLE";

export interface BulkReprocessItem {
  interchangeId: string;
  outcome: BulkOutcome;
  error?: string;
  changed: number;
  /** Empty unless the outcome is REPROCESSED. */
  messages: ReprocessedMessage[];
}

/** POST /interchanges:reprocess: one item per requested interchange, in request order. */
export interface BulkReprocessResult {
  dryRun: boolean;
  requested: number;
  changed: number;
  items: BulkReprocessItem[];
}

export interface MessagePage {
  items: MonitorMessage[];
  /** Absent on the last page. */
  nextCursor?: string;
}

export interface MessageFilter {
  partnerId?: string;
  direction?: Direction | "";
  status?: MessageStatus | "";
  /** Three digits, for example 850. */
  transactionSet?: string;
  /** ISA13, up to nine digits; the service zero-pads it. */
  controlNumber?: string;
  limit?: number;
  cursor?: string;
}

/** Outcome of POST /interchanges/{id}:reprocess for one transaction that was FAILED. */
export interface ReprocessedMessage {
  messageId: string;
  transactionSet: string;
  stControl: string;
  previousError?: string;
  status: "MAPPED" | "FAILED";
  error?: string;
  /** False when the outcome is the same as before: nothing was (or would be) written. */
  changed: boolean;
}

export interface ReprocessResult {
  interchangeId: string;
  dryRun: boolean;
  changed: number;
  messages: ReprocessedMessage[];
}

/** One partner on the SLA dashboard (GET /partners/sla). */
export interface PartnerSla {
  partnerId: string;
  partnerName: string;
  environment: Environment;
  ackSlaMinutes: number;
  /** Transactions in both directions in the window. */
  messages: number;
  failed: number;
  /** failed / messages × 100, two decimals. */
  failureRatePct: number;
  /** Outbound interchanges recorded in the window, and how many have the partner's 997. */
  outbound: number;
  acked: number;
  /** 95th percentile 997 latency in minutes; absent when nothing was acked. */
  p95AckMinutes?: number;
  /** Acked after ackSlaMinutes. */
  lateAcks: number;
  /** Still waiting past ackSlaMinutes. */
  overdueAcks: number;
}

export interface PartnerSlaReport {
  days: number;
  items: PartnerSla[];
}

/** Window choices on the SLA dashboard; the service accepts 1 to 365 days. */
export const SLA_WINDOWS: readonly number[] = [7, 30, 90, 365];

export class EdiApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "EdiApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof EdiApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<EdiApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new EdiApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  const res = await apiFetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new EdiApiError(0, "COM-0001", "The response has no items");
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

export function listPartners(filter: { environment?: Environment | "" } = {}, signal?: AbortSignal): Promise<Partner[]> {
  return items<Partner>(`/partners${query({ environment: filter.environment })}`, signal);
}

export function getPartner(id: string, signal?: AbortSignal): Promise<Partner> {
  return send<Partner>(`/partners/${encodeURIComponent(id)}`, {}, signal);
}

/** Creates a partner; reuse one key per form so a retried submit cannot create two. */
export function createPartner(input: NewPartner, key: string = newKey()): Promise<Partner> {
  return send<Partner>("/partners", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": key },
    body: JSON.stringify(input),
  });
}

export function listInterchanges(
  filter: { partnerId?: string; ackStatus?: AckStatus | ""; direction?: Direction | ""; limit?: number } = {},
  signal?: AbortSignal,
): Promise<InterchangeSummary[]> {
  return items<InterchangeSummary>(`/interchanges${query(filter)}`, signal);
}

export function getInterchange(id: string, signal?: AbortSignal): Promise<InterchangeDetail> {
  return send<InterchangeDetail>(`/interchanges/${encodeURIComponent(id)}`, {}, signal);
}

/** Records one raw X12 interchange as if the partner had sent it, and returns the 997. */
export function receiveX12(raw: string, environment: Environment): Promise<ReceiveResult> {
  return send<ReceiveResult>(`/inbound/x12${query({ environment })}`, {
    method: "POST",
    headers: { "Content-Type": "text/plain" },
    body: raw,
  });
}

/** Runs the mapping over a sample interchange without persisting anything. */
export function testMaps(raw: string): Promise<MapTestResponse> {
  return send<MapTestResponse>("/maps:test", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ raw }),
  });
}

/**
 * Generates an outbound 855, 856 or 810 for the partner from its canonical
 * document. Reuse one key per attempt so a retried submit cannot send twice.
 */
export function createOutbound(partnerId: string, transactionSet: OutboundSet, document: unknown, key: string = newKey()): Promise<OutboundResult> {
  return send<OutboundResult>(`/partners/${encodeURIComponent(partnerId)}/outbound`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": key },
    body: JSON.stringify({ transactionSet, document }),
  });
}

/** One page of the message monitor, newest first. Pass nextCursor back as cursor for the next page. */
export async function listMessages(filter: MessageFilter = {}, signal?: AbortSignal): Promise<MessagePage> {
  const body = await send<MessagePage>(`/messages${query({ ...filter })}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new EdiApiError(0, "COM-0001", "The response has no items");
  }
  return body;
}

export function getMessage(id: string, signal?: AbortSignal): Promise<MonitorMessageDetail> {
  return send<MonitorMessageDetail>(`/messages/${encodeURIComponent(id)}`, {}, signal);
}

/**
 * Maps the failed transactions of an inbound interchange again from its raw
 * payload. A dry run writes nothing and sends no key; otherwise reuse one key
 * per attempt so a retried click cannot run twice.
 */
export function reprocessInterchange(id: string, options: { dryRun: boolean; key?: string }): Promise<ReprocessResult> {
  const headers: Record<string, string> = {};
  if (!options.dryRun) headers["Idempotency-Key"] = options.key ?? newKey();
  return send<ReprocessResult>(`/interchanges/${encodeURIComponent(id)}:reprocess${query({ dryRun: options.dryRun ? "true" : undefined })}`, {
    method: "POST",
    headers,
  });
}

/** The raw payload of one interchange as a segment tree, masked unless the user holds edi.pii.view. */
export function getPayload(id: string, signal?: AbortSignal): Promise<PayloadTree> {
  return send<PayloadTree>(`/interchanges/${encodeURIComponent(id)}/payload`, {}, signal);
}

/**
 * Reprocesses up to 200 inbound interchanges in one request. A dry run writes
 * nothing and sends no key; otherwise reuse one key per attempt so a retried
 * click cannot run twice.
 */
export function bulkReprocess(interchangeIds: string[], options: { dryRun: boolean; key?: string }): Promise<BulkReprocessResult> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (!options.dryRun) headers["Idempotency-Key"] = options.key ?? newKey();
  return send<BulkReprocessResult>(`/interchanges:reprocess${query({ dryRun: options.dryRun ? "true" : undefined })}`, {
    method: "POST",
    headers,
    body: JSON.stringify({ interchangeIds }),
  });
}

export async function partnerSla(days: number, signal?: AbortSignal): Promise<PartnerSlaReport> {
  const body = await send<PartnerSlaReport>(`/partners/sla${query({ days })}`, {}, signal);
  if (!Array.isArray(body.items)) {
    throw new EdiApiError(0, "COM-0001", "The response has no items");
  }
  return body;
}

// Slice 5 dry runs: punchout cart check, self-billing match and Peppol UBL build.
// None of them stores anything, so they send no Idempotency-Key.

/** One requisition line a returned punchout cart would create (BR-35.13). */
export interface CartLine {
  line: number;
  supplierPartId: string;
  description: string;
  /** Decimal string. */
  quantity: string;
  /** UN/ECE Recommendation 20 code. */
  unitCode: string;
  /** Decimal string, price per unit. */
  unitPrice: string;
  currency: string;
  unspsc: string;
  leadTimeDays: number;
  /** Always true: the price is catalog-sourced for the wave-17 approval rules. */
  catalog: boolean;
  /** The price is above the reference price plus the supplier tolerance. */
  needsApproval: boolean;
}

export interface CartResult {
  lines: CartLine[];
  issues: Issue[];
  cartRejected: boolean;
  computedTotal: string;
  statedTotal?: string;
}

export type PunchoutFormat = "CXML" | "OCI";

export interface PunchoutSupplier {
  currencies: string[];
  /** Decimal 0..20. */
  tolerancePct?: string;
  /** Supplier unit to UN/ECE Recommendation 20 code. */
  uomMap?: Record<string, string>;
  /** Catalog reference price by supplier part id. */
  referencePrices?: Record<string, string>;
}

export interface PunchoutCheck {
  format: PunchoutFormat;
  /** cXML PunchOutOrderMessage, for CXML. */
  document?: string;
  /** NEW_ITEM-FIELD[n] pairs of the HOOK_URL form post, for OCI. */
  fields?: Record<string, string>;
  supplier: PunchoutSupplier;
}

export type SelfBillOutcome = "MATCHED" | "DIFFERENCE" | "UNMATCHED";

export interface SelfBillLineResult {
  line: string;
  outcome: SelfBillOutcome;
  shipmentId?: string;
  differences?: { field: "quantity" | "unitPrice" | "tax"; theirs: string; ours: string }[];
}

export interface SelfBillResult {
  lines: SelfBillLineResult[];
  /** Shipment ids not self-billed after the wait period (EDI-0138). */
  unbilled: string[];
  issues: Issue[];
}

export interface SelfBillShipment {
  id: string;
  deliveryNote: string;
  poLine: string;
  item: string;
  quantity: string;
  unitPrice: string;
  taxRatePct: string;
  /** YYYY-MM-DD. */
  shippedOn: string;
}

export interface SelfBillLine {
  line: string;
  deliveryNote: string;
  poLine: string;
  item: string;
  quantity: string;
  unitPrice: string;
  /** VAT the customer computed for the line. */
  tax: string;
}

export interface SelfBillMatch {
  /** YYYY-MM-DD; the service uses today when omitted. */
  asOf?: string;
  /** 1..120; the service default is 30. */
  waitDays?: number;
  tolerance?: { quantity?: string; price?: string };
  shipments: SelfBillShipment[];
  lines: SelfBillLine[];
}

export interface UblBuildResult {
  /** The UBL 2.1 XML; empty when valid is false. */
  xml: string;
  issues: Issue[];
  valid: boolean;
}

function postJson<T>(path: string, body: unknown): Promise<T> {
  return send<T>(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
}

/** Checks a returned punchout cart against BR-35.13; no requisition line is created. */
export function checkPunchout(input: PunchoutCheck): Promise<CartResult> {
  return postJson<CartResult>("/punchout/returns:parse", input);
}

/** Matches a self-billed document to the supplied shipments (BR-35.15); nothing is posted. */
export function matchSelfBilling(input: SelfBillMatch): Promise<SelfBillResult> {
  return postJson<SelfBillResult>("/self-billing:match", input);
}

/** Builds Peppol BIS Billing 3.0 UBL; blocking issues come back with valid false and no XML. */
export function buildPeppolInvoice(invoice: unknown): Promise<UblBuildResult> {
  return postJson<UblBuildResult>("/peppol/invoices:build", invoice);
}
