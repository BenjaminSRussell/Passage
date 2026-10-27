// Client for the edi service (services/edi/api/openapi.yaml). Browser calls use
// /api/v1/edi/..., which the web BFF rewrites to /edi/v1/... on the service.

export const base = "/api/v1/edi";

export type Environment = "TEST" | "PROD";
export const ENVIRONMENTS: readonly Environment[] = ["TEST", "PROD"];

export type AckStatus = "PENDING" | "ACCEPTED" | "ACCEPTED_WITH_ERRORS" | "REJECTED" | "TIMEOUT";
export const ACK_STATUSES: readonly AckStatus[] = ["PENDING", "ACCEPTED", "ACCEPTED_WITH_ERRORS", "REJECTED", "TIMEOUT"];

export type MessageStatus = "RECEIVED" | "MAPPED" | "POSTED" | "FAILED" | "SENT" | "ACKED";

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
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
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
