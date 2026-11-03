// Client for the inventory service (services/inventory/api/openapi.yaml).
// Browser calls use /api/v1/inventory/..., which the web BFF rewrites to the service.

export const base = "/api/v1/inventory";

export const JOURNAL_TYPES = ["MOVEMENT", "ADJUSTMENT", "OPENING_BALANCE", "COUNT", "OWNERSHIP_CHANGE", "TRANSFER"] as const;
export type JournalType = (typeof JOURNAL_TYPES)[number];

/**
 * Types a user creates directly; COUNT journals come from posted count sheets.
 * OWNERSHIP_CHANGE also needs inventory.consignment.write (FR-16.17). TRANSFER
 * moves stock between two dimension sets of the legal entity.
 */
export const KEYED_JOURNAL_TYPES = ["MOVEMENT", "ADJUSTMENT", "OPENING_BALANCE", "OWNERSHIP_CHANGE", "TRANSFER"] as const;
export type KeyedJournalType = (typeof KEYED_JOURNAL_TYPES)[number];

export const JOURNAL_STATUSES = ["DRAFT", "SUBMITTED", "APPROVED", "POSTING", "POSTED", "REJECTED", "CANCELLED"] as const;
export type JournalStatus = (typeof JOURNAL_STATUSES)[number];

/**
 * Dimensions a journal line can carry (license_plate is reserved). owner (a
 * vendor id) is set only on OWNERSHIP_CHANGE lines; segment is
 * SALES_ORDER:<ref> or PROJECT:<ref> (FR-16.20).
 */
export const LINE_DIMENSIONS = ["site", "warehouse", "location", "batch", "serial", "inventory_status", "owner", "segment"] as const;
export type LineDimension = (typeof LINE_DIMENSIONS)[number];

export const ON_HAND_GROUPS = ["product", "legal_entity", "site", "warehouse", "location", "batch", "serial", "inventory_status", "owner", "segment"] as const;
export type OnHandGroup = (typeof ON_HAND_GROUPS)[number];

export interface JournalSummary {
  id: string;
  legalEntityId: string;
  journalType: JournalType;
  status: JournalStatus;
  postingDate: string;
  offsetAccountId?: string;
  currency: string;
  /** Journal name the journal was created under (PRD 3.16.13). */
  journalNameId?: string;
  version: number;
  lineCount: number;
  createdAt: string;
  postedAt?: string;
  /** Set on a reversal journal (BR-16.12). */
  reversesJournalId?: string;
  /** Set on a journal that has been reversed. */
  reversedByJournalId?: string;
}

export interface JournalLine {
  id: string;
  lineNo: number;
  clientKey?: string;
  /** On a reversal journal, the line this one negates. */
  reversesLineId?: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  /** Destination dimension set of a TRANSFER journal line. */
  toDimensions?: Partial<Record<string, string>>;
  qty: string;
  enteredQty: string;
  enteredUnit: string;
  costPrice: string;
}

export interface GLLine {
  account: string;
  debit: string;
  credit: string;
}

export interface Journal extends JournalSummary {
  lines: JournalLine[];
  glEntry?: { currency: string; lines: GLLine[]; ledgerEntryId: string | null };
}

export interface JournalPage {
  items: JournalSummary[];
  nextCursor?: string;
}

export interface CreateJournalInput {
  journalType: KeyedJournalType;
  legalEntityId: string;
  postingDate: string;
  currency: string;
  offsetAccountId?: string;
  journalNameId?: string;
}

export interface LineInput {
  productId: string;
  dimensions: Partial<Record<LineDimension, string>>;
  /** Destination of a TRANSFER line. */
  toDimensions?: Partial<Record<LineDimension, string>>;
  qty: string;
  enteredUnit: string;
  costPrice?: string;
}

export interface OnHandRow {
  group: Partial<Record<string, string>>;
  physicalInvent: string;
  /** Absent in an as-of replay: availability is not kept per date. */
  availPhysical?: string;
  /** Absent in an as-of replay. */
  availOrdered?: string;
  postedValue: string;
  /** Absent in an as-of replay. */
  reservedPhysical?: string;
  /** Reserved against expected receipts (RESERVED_ORDERED). Absent in an as-of replay. */
  reservedOrdered?: string;
  /** Physical quantity held as BLOCKED or QUARANTINE; not in the available columns. */
  blocked: string;
  /** Quantity under an active hold; left out of the available columns. Absent in an as-of replay. */
  heldPhysical?: string;
  currency: string;
  negative: boolean;
}

export interface OnHandResult {
  items: OnHandRow[];
  truncated: boolean;
  /** Replay date (YYYY-MM-DD) when the rows are the stock on that date; absent for the live projection. */
  asOf?: string;
}

export interface OnHandQuery {
  groupBy: readonly OnHandGroup[];
  /** Replay the ledger up to this date (YYYY-MM-DD). */
  asOf?: string;
  product?: string;
  site?: string;
  warehouse?: string;
  batch?: string;
  /** Vendor id of vendor-owned stock. */
  owner?: string;
  /** SALES_ORDER:<ref> or PROJECT:<ref>. */
  segment?: string;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class InventoryApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "InventoryApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof InventoryApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<InventoryApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new InventoryApiError(res.status, code, message, fields);
}

interface Sent<T> {
  body: T;
  etag: string | null;
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Sent<T>> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  if (res.status === 204) return { body: undefined as T, etag: null };
  return { body: (await res.json()) as T, etag: res.headers.get("ETag") };
}

function newKey(): string {
  return crypto.randomUUID();
}

export function onHandUrl(q: OnHandQuery): string {
  const params = new URLSearchParams();
  if (q.groupBy.length > 0) params.set("groupBy", q.groupBy.join(","));
  for (const key of ["asOf", "product", "site", "warehouse", "batch", "owner", "segment"] as const) {
    const value = q[key]?.trim();
    if (value) params.set(key, value);
  }
  const qs = params.toString();
  return `/onhand${qs ? `?${qs}` : ""}`;
}

export async function fetchOnHand(q: OnHandQuery, signal?: AbortSignal): Promise<OnHandResult> {
  const { body } = await send<Partial<OnHandResult>>(onHandUrl(q), {}, signal);
  if (!Array.isArray(body.items)) {
    throw new InventoryApiError(0, "COM-0001", "The on-hand response has no items");
  }
  return { items: body.items, truncated: body.truncated === true, ...(body.asOf ? { asOf: body.asOf } : {}) };
}

export interface JournalQuery {
  status?: JournalStatus | "";
  type?: JournalType | "";
  cursor?: string;
  limit?: number;
}

export async function listJournals(q: JournalQuery = {}, signal?: AbortSignal): Promise<JournalPage> {
  const params = new URLSearchParams();
  if (q.status) params.set("status", q.status);
  if (q.type) params.set("type", q.type);
  if (q.cursor) params.set("cursor", q.cursor);
  if (q.limit) params.set("limit", String(q.limit));
  const qs = params.toString();
  const { body } = await send<JournalPage>(`/journals${qs ? `?${qs}` : ""}`, {}, signal);
  return { items: body.items ?? [], nextCursor: body.nextCursor };
}

export async function getJournal(id: string, signal?: AbortSignal): Promise<Journal> {
  return (await send<Journal>(`/journals/${encodeURIComponent(id)}`, {}, signal)).body;
}

/** Creates a DRAFT journal; a fresh Idempotency-Key makes a retried click safe on the server. */
export async function createJournal(input: CreateJournalInput, key: string = newKey()): Promise<Journal> {
  const body: CreateJournalInput = { ...input };
  if (!body.offsetAccountId) delete body.offsetAccountId;
  if (!body.journalNameId) delete body.journalNameId;
  return (await send<Journal>("/journals", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) })).body;
}

/** Adds lines; each line gets a client key so a retry never duplicates it. */
export async function addLines(journalId: string, lines: LineInput[]): Promise<Journal> {
  const payload = lines.map((line) => {
    const dimensions: Record<string, string> = {};
    for (const [name, value] of Object.entries(line.dimensions)) {
      if (value && value.trim() !== "") dimensions[name] = value.trim();
    }
    const out: Record<string, unknown> = { clientKey: newKey(), productId: line.productId, dimensions, qty: line.qty, enteredUnit: line.enteredUnit };
    if (line.costPrice && line.costPrice.trim() !== "") out.costPrice = line.costPrice.trim();
    if (line.toDimensions) {
      const to: Record<string, string> = {};
      for (const [name, value] of Object.entries(line.toDimensions)) {
        if (value && value.trim() !== "") to[name] = value.trim();
      }
      out.toDimensions = to;
    }
    return out;
  });
  return (
    await send<Journal>(`/journals/${encodeURIComponent(journalId)}/lines:bulk`, { method: "POST", body: JSON.stringify({ lines: payload }) })
  ).body;
}

/** Posts the journal at the version the screen shows (If-Match). */
export async function postJournal(journalId: string, version: number): Promise<Journal> {
  return (await send<Journal>(`/journals/${encodeURIComponent(journalId)}:post`, { method: "POST", headers: { "If-Match": `"${version}"` } })).body;
}

/** Approval workflow steps of BR-16.11, each guarded by the journal version (If-Match). */
export type JournalStep = "submit" | "approve" | "reject" | "reopen" | "cancel";

export async function moveJournal(journalId: string, version: number, step: JournalStep): Promise<Journal> {
  return (await send<Journal>(`/journals/${encodeURIComponent(journalId)}:${step}`, { method: "POST", headers: { "If-Match": `"${version}"` } })).body;
}

export interface Validation {
  valid: true;
  journalId: string;
  status: JournalStatus;
  version: number;
  lineCount: number;
}

/** Dry-runs the post (FR-16.6); a journal that would not post rejects with the post's problem. */
export async function validateJournal(journalId: string): Promise<Validation> {
  return (await send<Validation>(`/journals/${encodeURIComponent(journalId)}:validate`, { method: "POST" })).body;
}

/**
 * Creates the DRAFT reversal of a POSTED journal (BR-16.12). A journal has one
 * reversal, so repeating the call returns the same one.
 */
export async function reverseJournal(journalId: string, postingDate: string): Promise<Journal> {
  return (await send<Journal>(`/journals/${encodeURIComponent(journalId)}:reverse`, { method: "POST", body: JSON.stringify({ postingDate }) })).body;
}

// ---- Transaction inquiry (FR-16.13) ----

export const TRANSACTION_STATUSES = [
  "ON_ORDER",
  "RESERVED_ORDERED",
  "RESERVED_PHYSICAL",
  "PICKED",
  "DEDUCTED",
  "SOLD",
  "ORDERED",
  "ARRIVED",
  "REGISTERED",
  "RECEIVED",
  "PURCHASED",
] as const;
export type TransactionStatus = (typeof TRANSACTION_STATUSES)[number];

/**
 * Origin types: the service's own documents, then the external origins other
 * services register through transactions:apply (purchase, receipt, sales and
 * production lines).
 */
export const SOURCE_TYPES = [
  "INVENT_JOURNAL_LINE",
  "BLOCKING",
  "TRANSFER_LINE",
  "CONSIGNMENT_LINE",
  "OWNERSHIP_CHANGE",
  "TRANSFER_IN",
  "PURCH_LINE",
  "RECEIPT_LINE",
  "SALES_LINE",
  "PROD_LINE",
] as const;
export type SourceType = (typeof SOURCE_TYPES)[number];

export interface Transaction {
  id: string;
  legalEntityId: string;
  originId: string;
  sourceType: SourceType;
  sourceId: string;
  journalId?: string;
  reference?: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  qty: string;
  enteredQty: string;
  enteredUnit: string;
  issueStatus?: TransactionStatus;
  receiptStatus?: TransactionStatus;
  physicalDate: string;
  financialDate?: string;
  currency: string;
  costAmountPhysical: string;
  costAmountPosted?: string;
  createdAt: string;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export interface TransactionQuery {
  product?: string;
  status?: TransactionStatus | "";
  sourceType?: SourceType | "";
  from?: string;
  to?: string;
  cursor?: string;
}

function query(params: Record<string, string | undefined>): string {
  const out = new URLSearchParams();
  for (const [name, value] of Object.entries(params)) {
    const v = value?.trim();
    if (v) out.set(name, v);
  }
  const qs = out.toString();
  return qs ? `?${qs}` : "";
}

export async function listTransactions(q: TransactionQuery = {}, signal?: AbortSignal): Promise<Page<Transaction>> {
  const { body } = await send<Page<Transaction>>(`/transactions${query({ ...q })}`, {}, signal);
  return { items: body.items ?? [], nextCursor: body.nextCursor };
}

// ---- Transaction status changes (FR-16.2, BR-16.1) ----

/**
 * CREATE registers a transaction for an external origin and STEP moves it
 * forward (both service-to-service); UNPICK, UNREGISTER and
 * CANCEL_RESERVATION are the named backward moves a user runs.
 */
export const STATUS_COMMANDS = ["CREATE", "STEP", "UNPICK", "UNREGISTER", "CANCEL_RESERVATION"] as const;
export type StatusCommand = (typeof STATUS_COMMANDS)[number];

export type NamedCommand = Extract<StatusCommand, "UNPICK" | "UNREGISTER" | "CANCEL_RESERVATION">;

/** The named backward moves (BR-16.1): the statuses each applies to and where it moves them. */
export const NAMED_MOVES: Record<NamedCommand, { verb: string; from: Partial<Record<TransactionStatus, TransactionStatus>> }> = {
  UNPICK: { verb: "unpick", from: { PICKED: "RESERVED_PHYSICAL" } },
  UNREGISTER: { verb: "unregister", from: { REGISTERED: "ARRIVED" } },
  CANCEL_RESERVATION: { verb: "cancel-reservation", from: { RESERVED_PHYSICAL: "ON_ORDER", RESERVED_ORDERED: "ON_ORDER" } },
};

/** One status-change audit row; fromStatus is absent for CREATE. */
export interface StatusChange {
  id: string;
  legalEntityId: string;
  transactionId: string;
  command: StatusCommand;
  fromStatus?: TransactionStatus;
  toStatus: TransactionStatus;
  qty: string;
  date: string;
  createdBy?: string;
  occurredAt: string;
}

export interface TransactionDetail extends Transaction {
  /** Status history, oldest first. Journal postings have none. */
  statusChanges: StatusChange[];
}

export async function getTransaction(id: string, signal?: AbortSignal): Promise<TransactionDetail> {
  const { body } = await send<TransactionDetail>(`/transactions/${encodeURIComponent(id)}`, {}, signal);
  return { ...body, statusChanges: body.statusChanges ?? [] };
}

export interface StatusChangeQuery {
  transaction?: string;
  command?: StatusCommand | "";
  cursor?: string;
}

export async function listStatusChanges(q: StatusChangeQuery = {}, signal?: AbortSignal): Promise<Page<StatusChange>> {
  const { body } = await send<Page<StatusChange>>(`/status-changes${query({ ...q })}`, {}, signal);
  return { items: body.items ?? [], nextCursor: body.nextCursor };
}

/**
 * Runs a named backward move on a transaction. The caller keeps the key for a
 * retry of the same move, so a lost response replays instead of failing.
 */
export async function applyNamedMove(transactionId: string, command: NamedCommand, date: string, key: string = newKey()): Promise<StatusChange> {
  const init: RequestInit = { method: "POST", headers: { "Idempotency-Key": key } };
  if (date) init.body = JSON.stringify({ date });
  return (await send<StatusChange>(`/transactions/${encodeURIComponent(transactionId)}:${NAMED_MOVES[command].verb}`, init)).body;
}

export function newIdempotencyKey(): string {
  return newKey();
}

// ---- Reservations (FR-16.7) ----

export const RESERVATION_POLICIES = ["FIFO", "FEFO", "LIFO", "MANUAL"] as const;
export type ReservationPolicy = (typeof RESERVATION_POLICIES)[number];

export const DEMAND_TYPES = ["SALES_ORDER", "PRODUCTION_ORDER", "TRANSFER_ORDER", "MANUAL"] as const;
export type DemandType = (typeof DEMAND_TYPES)[number];

export const RESERVATION_STATUSES = ["ACTIVE", "RELEASED", "CONSUMED"] as const;
export type ReservationStatus = (typeof RESERVATION_STATUSES)[number];

export const RESERVATION_KINDS = ["PHYSICAL", "ORDERED"] as const;
export type ReservationKind = (typeof RESERVATION_KINDS)[number];

export interface Reservation {
  id: string;
  legalEntityId: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  qty: string;
  demandType: DemandType;
  demandId: string;
  /** Segment of the demand (FR-16.20); absent when unsegmented. */
  demandSegment?: string;
  policy: ReservationPolicy;
  status: ReservationStatus;
  /** PHYSICAL holds stock on hand; ORDERED holds part of the expected receipt of the dimension set. */
  kind?: ReservationKind;
  allocationNo: number;
  createdAt: string;
  releasedAt?: string;
  /** When an ACTIVE reservation lapses; absent when it holds until released. */
  expiresAt?: string;
  /** Set when a reservation against an expected receipt became physical because the receipt arrived. */
  convertedAt?: string;
}

export interface ReserveInput {
  legalEntityId: string;
  productId: string;
  qty: string;
  demandType: DemandType;
  demandId: string;
  policy: ReservationPolicy;
  requiredDate?: string;
  shelfLifeMarginDays?: number;
  allowPartial?: boolean;
  dimensions: Partial<Record<string, string>>;
  /** Segment of the demand: a policy reservation takes this segment's stock first (BR-16.17). */
  segmentType?: SegmentType;
  segmentRef?: string;
  /** Let a segmented demand take unsegmented stock (default true). */
  segmentFallback?: boolean;
  /** Hours after which the reservation lapses; 0 or absent means no expiry (it holds until released), never "expire now". */
  ttlHours?: number;
  /** ORDERED reserves part of the expected receipt of the one dimension set named (policy MANUAL); absent or PHYSICAL reserves stock on hand. */
  against?: ReservationKind;
}

export const SEGMENT_TYPES = ["NONE", "SALES_ORDER", "PROJECT"] as const;
export type SegmentType = (typeof SEGMENT_TYPES)[number];

export interface ReserveResult {
  items: Reservation[];
  shortfall: string;
}

export interface ReservationQuery {
  product?: string;
  status?: ReservationStatus | "";
  demandId?: string;
  cursor?: string;
}

function cleanDimensions(dims: Partial<Record<string, string>>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(dims)) {
    if (value && value.trim() !== "") out[name] = value.trim();
  }
  return out;
}

/** Reserves stock; the key makes a retried submit replay instead of reserving twice. */
export async function createReservation(input: ReserveInput, key: string = newKey()): Promise<ReserveResult> {
  const body = { ...input, dimensions: cleanDimensions(input.dimensions) };
  if (!body.requiredDate) delete body.requiredDate;
  if (!body.ttlHours) delete body.ttlHours;
  if (body.against !== "ORDERED") delete body.against;
  if (!body.segmentType || body.segmentType === "NONE") {
    delete body.segmentType;
    delete body.segmentRef;
    delete body.segmentFallback;
  } else {
    body.segmentRef = body.segmentRef?.trim();
  }
  const { body: out } = await send<ReserveResult>("/reservations", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) });
  return { items: out.items ?? [], shortfall: out.shortfall ?? "0" };
}

export async function listReservations(q: ReservationQuery = {}, signal?: AbortSignal): Promise<Page<Reservation>> {
  const { body } = await send<Page<Reservation>>(`/reservations${query({ ...q })}`, {}, signal);
  return { items: body.items ?? [], nextCursor: body.nextCursor };
}

export type CandidateReason = "RESTRICTED" | "EXPIRES_BEFORE" | "OTHER_SEGMENT";

/** One dimension set a reservation could take, with the reason when a policy request would not (FR-16.7). */
export interface ReservationCandidate {
  dimensions: Partial<Record<string, string>>;
  batchId?: string;
  serial?: string;
  available: string;
  status: string;
  segment?: string;
  expiryDate?: string;
  eligible: boolean;
  reason?: CandidateReason;
}

export interface CandidateQuery {
  legalEntityId: string;
  productId: string;
  policy?: ReservationPolicy;
  requiredDate?: string;
  shelfLifeMarginDays?: number;
  segmentType?: SegmentType;
  segmentRef?: string;
  segmentFallback?: boolean;
  dimensions?: Partial<Record<string, string>>;
}

/** Lists the stock a reservation could take, eligible rows first in the pick order of the policy. */
export async function listReservationCandidates(q: CandidateQuery, signal?: AbortSignal): Promise<ReservationCandidate[]> {
  const segmented = q.segmentType !== undefined && q.segmentType !== "NONE";
  const params: Record<string, string | undefined> = {
    legalEntityId: q.legalEntityId,
    productId: q.productId,
    policy: q.policy,
    requiredDate: q.requiredDate,
    shelfLifeMarginDays: q.shelfLifeMarginDays === undefined ? undefined : String(q.shelfLifeMarginDays),
    segmentType: segmented ? q.segmentType : undefined,
    segmentRef: segmented ? q.segmentRef : undefined,
    segmentFallback: segmented && q.segmentFallback !== undefined ? String(q.segmentFallback) : undefined,
    ...cleanDimensions(q.dimensions ?? {}),
  };
  const { body } = await send<{ items?: ReservationCandidate[] }>(`/reservation-candidates${query(params)}`, {}, signal);
  return body.items ?? [];
}

export async function releaseReservation(id: string): Promise<Reservation> {
  return (await send<Reservation>(`/reservations/${encodeURIComponent(id)}`, { method: "DELETE" })).body;
}

export interface ExpireResult {
  released: number;
  more: boolean;
  items: Reservation[];
}

/** Releases the reservations whose time to live has passed (FR-16.7); repeating it releases only what is still due. */
export async function expireReservations(limit?: number): Promise<ExpireResult> {
  const { body } = await send<ExpireResult>(`/reservations:expire${query({ limit: limit ? String(limit) : undefined })}`, { method: "POST" });
  return { released: body.released ?? 0, more: body.more ?? false, items: body.items ?? [] };
}

// ---- Holds (wave 33 AC2) ----

export const HOLD_STATES = ["ACTIVE", "RELEASED"] as const;
export type HoldState = (typeof HOLD_STATES)[number];

export interface Hold {
  id: string;
  legalEntityId: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  qty: string;
  releasedQty: string;
  /** qty minus releasedQty: what availability leaves out. */
  heldQty: string;
  wholeLot: boolean;
  reason: string;
  sourceType: string;
  sourceRef: string;
  note?: string;
  state: HoldState;
  version: number;
}

export interface HoldInput {
  legalEntityId: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  /** Omitted for a whole-lot hold. */
  qty?: string;
  wholeLot?: boolean;
  allowPartial?: boolean;
  reason: string;
  sourceType: string;
  sourceRef: string;
  note?: string;
}

export interface HoldResult {
  items: Hold[];
  shortfall: string;
}

export interface HoldQuery {
  state?: HoldState | "";
  product?: string;
  sourceType?: string;
  sourceRef?: string;
  /** 1 to 200 (service default 100); the service has no cursor. */
  limit?: number;
}

/** Places a hold; the key makes a retried submit replay instead of holding twice. */
export async function placeHold(input: HoldInput, key: string = newKey()): Promise<HoldResult> {
  const body = { ...input, dimensions: cleanDimensions(input.dimensions) };
  if (input.wholeLot || !body.qty) delete body.qty;
  if (!body.wholeLot) delete body.wholeLot;
  if (!body.allowPartial) delete body.allowPartial;
  if (!body.note) delete body.note;
  const { body: out } = await send<HoldResult>("/holds", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) });
  return { items: out.items ?? [], shortfall: out.shortfall ?? "0" };
}

export async function listHolds(q: HoldQuery = {}, signal?: AbortSignal): Promise<Hold[]> {
  const { body } = await send<{ items?: Hold[] }>(`/holds${query({ ...q, limit: q.limit ? String(q.limit) : undefined })}`, {}, signal);
  return body.items ?? [];
}

/** Releases a hold (all of it, or qty) at the version the screen shows (If-Match). */
export async function releaseHold(id: string, version: number, qty?: string): Promise<Hold> {
  return (
    await send<Hold>(`/holds/${encodeURIComponent(id)}:release`, {
      method: "POST",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify(qty ? { qty } : {}),
    })
  ).body;
}

// ---- Blocking and quarantine (FR-16.8) ----

export const BLOCK_TARGETS = ["BLOCKED", "QUARANTINE"] as const;
export type BlockTarget = (typeof BLOCK_TARGETS)[number];

export const BLOCK_REASONS = ["QUALITY", "DAMAGE", "RECALL", "LEGAL", "OTHER"] as const;
export type BlockReason = (typeof BLOCK_REASONS)[number];

export type BlockingState = "ACTIVE" | "RELEASED";

export interface Blocking {
  id: string;
  legalEntityId: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  qty: string;
  unit: string;
  targetStatus: BlockTarget;
  reason: BlockReason;
  note?: string;
  state: BlockingState;
  disposition?: BlockDisposition;
  physicalDate: string;
  /** When the hold is expected to end; information only. */
  expectedReleaseDate?: string;
  /** The posted ADJUSTMENT journal that wrote a scrapped blocking off. */
  scrapJournalId?: string;
  version: number;
}

/** Dispositions the screen offers; RETURN needs the vendor return and is refused by the service. */
export type BlockDisposition = "RELEASE" | "SCRAP" | "RETURN";

export interface BlockInput {
  legalEntityId: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  qty: string;
  unit: string;
  targetStatus: BlockTarget;
  reason: BlockReason;
  note?: string;
  physicalDate?: string;
  expectedReleaseDate?: string;
}

export async function createBlocking(input: BlockInput, key: string = newKey()): Promise<Blocking> {
  const body = { ...input, dimensions: cleanDimensions(input.dimensions) };
  if (!body.note) delete body.note;
  if (!body.physicalDate) delete body.physicalDate;
  if (!body.expectedReleaseDate) delete body.expectedReleaseDate;
  return (await send<Blocking>("/blockings", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) })).body;
}

export async function listBlockings(state: BlockingState | "" = "", signal?: AbortSignal): Promise<Blocking[]> {
  const { body } = await send<{ items?: Blocking[] }>(`/blockings${query({ state })}`, {}, signal);
  return body.items ?? [];
}

/**
 * Ends a blocking at the version the screen shows (If-Match): RELEASE returns the stock to available,
 * SCRAP writes it off through a posted adjustment journal (needs inventory.blocking.scrap).
 */
export async function releaseBlocking(id: string, version: number, disposition: BlockDisposition = "RELEASE"): Promise<Blocking> {
  return (
    await send<Blocking>(`/blockings/${encodeURIComponent(id)}:release`, {
      method: "POST",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify({ disposition }),
    })
  ).body;
}

/** Moves an active quarantine to blocked (QUARANTINE to BLOCKED) at the version the screen shows. */
export async function escalateBlocking(id: string, version: number): Promise<Blocking> {
  return (await send<Blocking>(`/blockings/${encodeURIComponent(id)}:block`, { method: "POST", headers: { "If-Match": `"${version}"` } })).body;
}

// ---- Cycle counting (FR-16.9) ----

export type CountSheetStatus = "OPEN" | "COUNTED" | "APPROVED" | "POSTED" | "CANCELLED";
export type CountAbcClass = "A" | "B" | "C";

export interface CountPlan {
  id: string;
  legalEntityId: string;
  name: string;
  warehouse?: string;
  currency: string;
  unit: string;
  tolerancePct: string;
  toleranceValue: string;
  blind: boolean;
  freezeStock: boolean;
  fourEyes: boolean;
  locationFrom?: string;
  locationTo?: string;
  abcClass?: CountAbcClass;
  samplePct?: string;
  minDaysSinceCount?: number;
  createdAt: string;
}

export interface CountPlanInput {
  legalEntityId: string;
  name: string;
  warehouse?: string;
  currency: string;
  unit: string;
  tolerancePct?: string;
  toleranceValue?: string;
  blind: boolean;
  freezeStock?: boolean;
  fourEyes?: boolean;
  locationFrom?: string;
  locationTo?: string;
  abcClass?: CountAbcClass | "";
  samplePct?: string;
  /** Select only rows not counted for this many days (whole days, 1 to 3650). */
  minDaysSinceCount?: number;
}

export interface CountSheetSummary {
  id: string;
  legalEntityId: string;
  planId: string;
  planName: string;
  number: string;
  status: CountSheetStatus;
  blind: boolean;
  approvedBy?: string;
  submittedBy?: string;
  fourEyes: boolean;
  freezeStock: boolean;
  /** Lines a supervisor asked to be counted again and that are not yet re-entered. */
  recountLines: number;
  journalId?: string;
  version: number;
  lineCount: number;
  countedCount: number;
  createdAt: string;
  postedAt?: string;
}

export interface CountLine {
  id: string;
  lineNo: number;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  /** Null while a blind sheet is open and the reader cannot approve. */
  expectedQty: string | null;
  countedQty: string | null;
  countedBy?: string;
  countedAt?: string;
  postedVariance: string | null;
  recountRequested: boolean;
  recountReason?: string;
  recountRounds: number;
  /** Who flagged the recount: a supervisor, or stock that moved after the line was counted. */
  recountSource?: "SUPERVISOR" | "MOVEMENT";
}

export interface CountSheet extends CountSheetSummary {
  lines: CountLine[];
}

export async function createCountPlan(input: CountPlanInput, key: string = newKey()): Promise<CountPlan> {
  const body: Record<string, unknown> = { ...input };
  for (const name of ["warehouse", "tolerancePct", "toleranceValue", "locationFrom", "locationTo", "abcClass", "samplePct"] as const) {
    if (!input[name] || input[name]?.trim() === "") delete body[name];
  }
  return (await send<CountPlan>("/count-plans", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) })).body;
}

export async function listCountPlans(signal?: AbortSignal): Promise<CountPlan[]> {
  const { body } = await send<{ items?: CountPlan[] }>("/count-plans", {}, signal);
  return body.items ?? [];
}

export async function generateCountSheet(planId: string, key: string = newKey()): Promise<CountSheet> {
  return (await send<CountSheet>(`/count-plans/${encodeURIComponent(planId)}:generate`, { method: "POST", headers: { "Idempotency-Key": key } })).body;
}

export async function listCountSheets(q: { planId?: string; status?: CountSheetStatus | "" } = {}, signal?: AbortSignal): Promise<CountSheetSummary[]> {
  const { body } = await send<{ items?: CountSheetSummary[] }>(`/count-sheets${query({ ...q })}`, {}, signal);
  return body.items ?? [];
}

export async function getCountSheet(id: string, signal?: AbortSignal): Promise<CountSheet> {
  return (await send<CountSheet>(`/count-sheets/${encodeURIComponent(id)}`, {}, signal)).body;
}

/** Records one counted quantity at the sheet version the screen shows (If-Match). */
export async function enterCount(sheetId: string, lineId: string, version: number, countedQty: string): Promise<CountSheet> {
  return (
    await send<CountSheet>(`/count-sheets/${encodeURIComponent(sheetId)}/lines/${encodeURIComponent(lineId)}`, {
      method: "PUT",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify({ countedQty }),
    })
  ).body;
}

export async function postCountSheet(sheetId: string, version: number): Promise<CountSheet> {
  return (await send<CountSheet>(`/count-sheets/${encodeURIComponent(sheetId)}:post`, { method: "POST", headers: { "If-Match": `"${version}"` } })).body;
}

async function sheetAction(sheetId: string, action: string, version: number): Promise<CountSheet> {
  return (await send<CountSheet>(`/count-sheets/${encodeURIComponent(sheetId)}:${action}`, { method: "POST", headers: { "If-Match": `"${version}"` } })).body;
}

/** Closes counting: the sheet becomes COUNTED. */
export const submitCountSheet = (sheetId: string, version: number) => sheetAction(sheetId, "submit", version);
/** A supervisor's approval of a COUNTED sheet (a four-eyes plan needs it before posting). */
export const approveCountSheet = (sheetId: string, version: number) => sheetAction(sheetId, "approve", version);
/** Abandons an unposted sheet and lifts its stock freeze. */
export const cancelCountSheet = (sheetId: string, version: number) => sheetAction(sheetId, "cancel", version);

/** Flags a counted line for recount; the sheet returns to OPEN. */
export async function requestCountRecount(sheetId: string, lineId: string, version: number, reason: string): Promise<CountSheet> {
  return (
    await send<CountSheet>(`/count-sheets/${encodeURIComponent(sheetId)}/lines/${encodeURIComponent(lineId)}:recount`, {
      method: "POST",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify({ reason }),
    })
  ).body;
}

// ---- Tag counting (FR-16.18) ----

export const TAG_STATUSES = ["OPEN", "USED", "VOID", "MISSING"] as const;
export type TagStatus = (typeof TAG_STATUSES)[number];

/** Most tags one generate call creates. */
export const MAX_TAGS = 1000;

export interface CountTag {
  id: string;
  tagNumber: string;
  planId: string;
  status: TagStatus;
  productId?: string;
  dimensions: Partial<Record<string, string>>;
  /** Counted quantity of a used tag (zero is a count); null while open. */
  countedQty: string | null;
  countedBy?: string;
  /** Count sheet the tag was transferred to; the tag is then final. */
  sheetId?: string;
  version: number;
}

export interface TagCountInput {
  productId: string;
  dimensions: Partial<Record<string, string>>;
  countedQty: string;
}

export async function generateTags(planId: string, count: number, key: string = newKey()): Promise<CountTag[]> {
  const { body } = await send<{ items?: CountTag[] }>(`/count-plans/${encodeURIComponent(planId)}:generate-tags`, {
    method: "POST",
    headers: { "Idempotency-Key": key },
    body: JSON.stringify({ count }),
  });
  return body.items ?? [];
}

export async function listTags(planId: string, signal?: AbortSignal): Promise<CountTag[]> {
  const { body } = await send<{ items?: CountTag[] }>(`/count-plans/${encodeURIComponent(planId)}/tags`, {}, signal);
  return body.items ?? [];
}

/** Enters the count of one open tag at the version the screen shows (If-Match). */
export async function enterTagCount(tagId: string, version: number, input: TagCountInput): Promise<CountTag> {
  const dimensions: Record<string, string> = {};
  for (const [name, value] of Object.entries(input.dimensions)) {
    if (value && value.trim() !== "") dimensions[name] = value.trim();
  }
  return (
    await send<CountTag>(`/count-tags/${encodeURIComponent(tagId)}:use`, {
      method: "POST",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify({ productId: input.productId, dimensions, countedQty: input.countedQty }),
    })
  ).body;
}

export async function voidTag(tagId: string, version: number): Promise<CountTag> {
  return (await send<CountTag>(`/count-tags/${encodeURIComponent(tagId)}:void`, { method: "POST", headers: { "If-Match": `"${version}"` } })).body;
}

/**
 * Moves the plan's used tags to a new count sheet. markOpenMissing (supervisor
 * only) marks the tags still open as MISSING instead of blocking the transfer.
 */
export async function transferTags(planId: string, markOpenMissing: boolean, key: string = newKey()): Promise<CountSheet> {
  return (
    await send<CountSheet>(`/count-plans/${encodeURIComponent(planId)}:transfer-tags`, {
      method: "POST",
      headers: { "Idempotency-Key": key },
      body: JSON.stringify({ markOpenMissing }),
    })
  ).body;
}

// ---- Transfer orders and stock in transit (FR-16.15, FR-16.16) ----

export const TRANSFER_STATUSES = ["DRAFT", "CREATED", "SHIPPED", "PARTIALLY_RECEIVED", "RECEIVED", "CANCELLED"] as const;
export type TransferStatus = (typeof TRANSFER_STATUSES)[number];

export interface TransferLine {
  id: string;
  lineNo: number;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  unit: string;
  qty: string;
  shippedQty: string;
  receivedQty: string;
  inTransit: string;
  underdeliveryPct: string;
  overdeliveryPct: string;
  /** Ordered quantity not shipped yet (partial shipments); 0 once shipped or closed. */
  unshippedQty?: string;
  closed: boolean;
}

export interface TransferOrder {
  id: string;
  legalEntityId: string;
  number: string;
  status: TransferStatus;
  fromWarehouseId: string;
  toWarehouseId: string;
  toSiteId?: string;
  transitWarehouseId?: string;
  useTransit: boolean;
  shipDate: string;
  /** Planned receipt date when one was given; an open shipment is late once it has passed. */
  plannedReceiptDate?: string;
  /** Delivery terms code (for example an incoterm) when one was given. */
  deliveryTerms?: string;
  /** Mode of delivery code (for example ROAD) when one was given. */
  modeOfDelivery?: string;
  receiptDate?: string;
  version: number;
  createdAt: string;
  lines: TransferLine[];
}

export interface TransferLineInput {
  productId: string;
  dimensions: Partial<Record<string, string>>;
  unit: string;
  qty: string;
  underdeliveryPct?: string;
  overdeliveryPct?: string;
}

export interface TransferInput {
  legalEntityId: string;
  fromWarehouseId: string;
  toWarehouseId: string;
  toSiteId?: string;
  transitWarehouseId?: string;
  useTransit: boolean;
  shipDate: string;
  plannedReceiptDate?: string;
  deliveryTerms?: string;
  modeOfDelivery?: string;
  /** Create a DRAFT working copy instead of a CREATED order. */
  draft?: boolean;
  lines: TransferLineInput[];
}

export interface ReceiptInput {
  lineId: string;
  qty: string;
  closeShort: boolean;
}

export interface InTransitItem {
  orderId: string;
  number: string;
  lineId: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  unit: string;
  fromWarehouseId: string;
  toWarehouseId: string;
  transitWarehouseId: string;
  shipDate: string;
  daysInTransit: number;
  inTransitQty: string;
  closedShort: boolean;
}

/** Creates a CREATED transfer order; the key makes a retried submit replay instead of creating twice. */
export async function createTransfer(input: TransferInput, key: string = newKey()): Promise<TransferOrder> {
  const body: Record<string, unknown> = {
    legalEntityId: input.legalEntityId,
    fromWarehouseId: input.fromWarehouseId,
    toWarehouseId: input.toWarehouseId,
    useTransit: input.useTransit,
    shipDate: input.shipDate,
    lines: input.lines.map((line) => {
      const out: Record<string, unknown> = { productId: line.productId, dimensions: cleanDimensions(line.dimensions), unit: line.unit, qty: line.qty };
      if (line.underdeliveryPct?.trim()) out.underdeliveryPct = line.underdeliveryPct.trim();
      if (line.overdeliveryPct?.trim()) out.overdeliveryPct = line.overdeliveryPct.trim();
      return out;
    }),
  };
  if (input.plannedReceiptDate?.trim()) body.plannedReceiptDate = input.plannedReceiptDate.trim();
  if (input.deliveryTerms?.trim()) body.deliveryTerms = input.deliveryTerms.trim();
  if (input.modeOfDelivery?.trim()) body.modeOfDelivery = input.modeOfDelivery.trim();
  if (input.draft) body.draft = true;
  if (input.toSiteId?.trim()) body.toSiteId = input.toSiteId.trim();
  if (input.useTransit && input.transitWarehouseId?.trim()) body.transitWarehouseId = input.transitWarehouseId.trim();
  return (await send<TransferOrder>("/transfer-orders", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) })).body;
}

export async function listTransfers(status: TransferStatus | "" = "", signal?: AbortSignal): Promise<TransferOrder[]> {
  const { body } = await send<{ items?: TransferOrder[] }>(`/transfer-orders${query({ status })}`, {}, signal);
  return body.items ?? [];
}

export async function getTransfer(id: string, signal?: AbortSignal): Promise<TransferOrder> {
  return (await send<TransferOrder>(`/transfer-orders/${encodeURIComponent(id)}`, {}, signal)).body;
}

export interface ShipLineInput {
  lineId: string;
  qty: string;
}

/**
 * Ships on shipDate at the version the screen shows (If-Match): the quantities in `lines` (a
 * partial shipment), or, without lines, what is left on every open line.
 */
export async function shipTransfer(id: string, version: number, shipDate: string, lines?: ShipLineInput[]): Promise<TransferOrder> {
  const body: Record<string, unknown> = { shipDate };
  if (lines && lines.length > 0) body.lines = lines;
  return (
    await send<TransferOrder>(`/transfer-orders/${encodeURIComponent(id)}:ship`, {
      method: "POST",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify(body),
    })
  ).body;
}

export async function receiveTransfer(id: string, version: number, receiptDate: string, lines: ReceiptInput[]): Promise<TransferOrder> {
  return (
    await send<TransferOrder>(`/transfer-orders/${encodeURIComponent(id)}:receive`, {
      method: "POST",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify({ receiptDate, lines }),
    })
  ).body;
}

/** One atomic edit of a DRAFT or CREATED transfer order; an empty date, site or code clears it. */
export interface TransferEditInput {
  shipDate?: string;
  plannedReceiptDate?: string;
  toSiteId?: string;
  deliveryTerms?: string;
  modeOfDelivery?: string;
  addLines?: TransferLineInput[];
  updateLines?: { lineId: string; qty?: string; underdeliveryPct?: string; overdeliveryPct?: string }[];
  removeLineIds?: string[];
}

export async function editTransfer(id: string, version: number, edit: TransferEditInput): Promise<TransferOrder> {
  return (
    await send<TransferOrder>(`/transfer-orders/${encodeURIComponent(id)}`, {
      method: "PATCH",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify(edit),
    })
  ).body;
}

export async function cancelTransfer(id: string, version: number): Promise<TransferOrder> {
  return (await send<TransferOrder>(`/transfer-orders/${encodeURIComponent(id)}:cancel`, { method: "POST", headers: { "If-Match": `"${version}"` } })).body;
}

/** Confirms a DRAFT order at the version the screen shows; it becomes CREATED. */
export async function confirmTransfer(id: string, version: number): Promise<TransferOrder> {
  return (await send<TransferOrder>(`/transfer-orders/${encodeURIComponent(id)}:confirm`, { method: "POST", headers: { "If-Match": `"${version}"` } })).body;
}

/** Deletes a DRAFT or CREATED order that shipped nothing, at the version the screen shows. */
export async function deleteTransfer(id: string, version: number): Promise<void> {
  await send<void>(`/transfer-orders/${encodeURIComponent(id)}`, { method: "DELETE", headers: { "If-Match": `"${version}"` } });
}

export async function listInTransit(signal?: AbortSignal): Promise<InTransitItem[]> {
  const { body } = await send<{ items?: InTransitItem[] }>("/transfers/in-transit", {}, signal);
  return body.items ?? [];
}

export type TransferLateness = "LATE_SHIPMENT" | "LATE_RECEIPT";

/** One transfer order line with its order header; `late` is absent when the line is on time. */
export interface TransferLineItem {
  orderId: string;
  legalEntityId: string;
  number: string;
  orderStatus: TransferStatus;
  fromWarehouseId: string;
  toWarehouseId: string;
  /** Planned until the order ships, then the actual ship date. */
  shipDate: string;
  plannedReceiptDate?: string;
  lineId: string;
  lineNo: number;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  unit: string;
  qty: string;
  shippedQty: string;
  receivedQty: string;
  remainingQty: string;
  inTransit: string;
  daysInTransit: number;
  closed: boolean;
  late?: TransferLateness;
  /** Days past the due date (planned ship date, or ship date plus the receipt allowance); 0 when on time. */
  daysLate: number;
}

export interface TransferLineQuery {
  legalEntityId?: string;
  status?: TransferStatus | "";
  productId?: string;
  fromWarehouseId?: string;
  toWarehouseId?: string;
  /** Ship date range, inclusive (YYYY-MM-DD). */
  from?: string;
  to?: string;
  lateOnly?: boolean;
}

export interface TransferLinePage {
  items: TransferLineItem[];
  /** More than the service's 1,000-line cap matched. */
  truncated: boolean;
}

/** Transfer order line inquiry across all orders, oldest ship date first (FR-16.15). */
export async function listTransferLines(query: TransferLineQuery = {}, signal?: AbortSignal): Promise<TransferLinePage> {
  const params = new URLSearchParams();
  for (const key of ["legalEntityId", "status", "productId", "fromWarehouseId", "toWarehouseId", "from", "to"] as const) {
    const value = query[key]?.trim();
    if (value) params.set(key, value);
  }
  if (query.lateOnly) params.set("late", "true");
  const qs = params.toString();
  const { body } = await send<{ items?: TransferLineItem[]; truncated?: boolean }>(`/transfer-order-lines${qs ? `?${qs}` : ""}`, {}, signal);
  return { items: body.items ?? [], truncated: body.truncated ?? false };
}

// ---- Inquiries and reports (PRD 3.16.14) ----

function reportQuery(params: Record<string, string | undefined>): string {
  const out = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    const v = value?.trim();
    if (v) out.set(key, v);
  }
  const qs = out.toString();
  return qs ? `?${qs}` : "";
}

export type ValueBasis = "JOURNAL" | "ESTIMATE";

export interface CountVarianceLine {
  sheetId: string;
  sheetNumber: string;
  sheetStatus: string;
  planId: string;
  planName: string;
  warehouseId?: string;
  legalEntityId: string;
  lineId: string;
  lineNo: number;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  unit: string;
  currency: string;
  expectedQty: string;
  countedQty: string;
  varianceQty: string;
  unitCost: string;
  varianceValue: string;
  /** JOURNAL is the cost of the posted COUNT journal line; ESTIMATE is today's running average. */
  valueBasis: ValueBasis;
  countedBy?: string;
  countedAt: string;
}

export interface CountVarianceTotals {
  counter: string;
  currency: string;
  lines: number;
  absVarianceQty: string;
  gainValue: string;
  lossValue: string;
  netValue: string;
}

export interface CountVarianceQuery {
  legalEntityId?: string;
  planId?: string;
  sheetId?: string;
  warehouseId?: string;
  counter?: string;
  from?: string;
  to?: string;
}

export interface CountVarianceReport {
  items: CountVarianceLine[];
  totalsByCounter: CountVarianceTotals[];
  truncated: boolean;
}

/** Count variance report: counted lines with variance quantity and value, and totals per counter. */
export async function getCountVariance(query: CountVarianceQuery = {}, signal?: AbortSignal): Promise<CountVarianceReport> {
  const { body } = await send<Partial<CountVarianceReport>>(`/reports/count-variance${reportQuery({ ...query })}`, {}, signal);
  return { items: body.items ?? [], totalsByCounter: body.totalsByCounter ?? [], truncated: body.truncated ?? false };
}

export interface NegativeOnHandLine {
  legalEntityId: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  physicalInvent: string;
  shortfallQty: string;
  postedValue: string;
  currency?: string;
  firstNegativeDate?: string;
}

export interface NegativeOnHandQuery {
  legalEntityId?: string;
  productId?: string;
  siteId?: string;
  warehouseId?: string;
}

export interface ReportPage<T> {
  items: T[];
  truncated: boolean;
}

export async function listNegativeOnHand(query: NegativeOnHandQuery = {}, signal?: AbortSignal): Promise<ReportPage<NegativeOnHandLine>> {
  const { body } = await send<{ items?: NegativeOnHandLine[]; truncated?: boolean }>(`/reports/negative-onhand${reportQuery({ ...query })}`, {}, signal);
  return { items: body.items ?? [], truncated: body.truncated ?? false };
}

export const TRACE_DIRECTIONS = ["BOTH", "FORWARD", "BACKWARD"] as const;
export type TraceDirection = (typeof TRACE_DIRECTIONS)[number];

export interface TraceLine extends Transaction {
  /** Traced physical balance of the product after this movement (BOTH direction, physical movements only). */
  running?: string;
}

export interface TraceQuery {
  batchId?: string;
  serialId?: string;
  productId?: string;
  legalEntityId?: string;
  direction?: TraceDirection;
}

export interface TraceResult extends ReportPage<TraceLine> {
  direction: TraceDirection;
}

/** Batch or serial trace: the transactions that carry the batch or serial, oldest first. */
export async function traceTracking(query: TraceQuery, signal?: AbortSignal): Promise<TraceResult> {
  const { body } = await send<{ items?: TraceLine[]; truncated?: boolean; direction?: TraceDirection }>(`/reports/tracking-trace${reportQuery({ ...query })}`, {}, signal);
  return { items: body.items ?? [], truncated: body.truncated ?? false, direction: body.direction ?? query.direction ?? "BOTH" };
}

export interface DimensionCombination {
  legalEntityId: string;
  productId: string;
  dimensionSetHash: string;
  dimensions: Partial<Record<string, string>>;
  activeDimensions: string[];
  firstUse: string;
  lastUse: string;
  transactionCount: number;
}

export async function listDimensionCombinations(query: { legalEntityId?: string; productId?: string } = {}, signal?: AbortSignal): Promise<ReportPage<DimensionCombination>> {
  const { body } = await send<{ items?: DimensionCombination[]; truncated?: boolean }>(`/reports/dimension-combinations${reportQuery({ ...query })}`, {}, signal);
  return { items: body.items ?? [], truncated: body.truncated ?? false };
}

// ---- ABC classification (FR-16.19) ----

export const ABC_CLASSES = ["A", "B", "C", "N"] as const;
export type AbcClass = (typeof ABC_CLASSES)[number];

export interface AbcItem {
  productId: string;
  site: string;
  value: string;
  cumPct: string;
  class: AbcClass;
}

export interface AbcRun {
  id: string;
  legalEntityId: string;
  criterion: "VALUE";
  thresholds: { a: string; b: string; c: string };
  zeroClass: "C" | "N";
  createdAt: string;
  classCounts: Partial<Record<AbcClass, number>>;
  /** Present on a single run, absent in the list. */
  items?: AbcItem[];
}

export interface AbcRunInput {
  legalEntityId: string;
  zeroClass: "C" | "N";
  thresholds: { a: string; b: string; c: string };
}

export async function runAbc(input: AbcRunInput, key: string = newKey()): Promise<AbcRun> {
  const body = { ...input, criterion: "VALUE" };
  return (await send<AbcRun>("/abc-runs", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) })).body;
}

export async function listAbcRuns(signal?: AbortSignal): Promise<AbcRun[]> {
  const { body } = await send<{ items?: AbcRun[] }>("/abc-runs", {}, signal);
  return body.items ?? [];
}

export async function getAbcRun(id: string, signal?: AbortSignal): Promise<AbcRun> {
  return (await send<AbcRun>(`/abc-runs/${encodeURIComponent(id)}`, {}, signal)).body;
}

// ---- Vendor consignment (FR-16.17) ----

export const CONSIGNMENT_STATUSES = ["CREATED", "PARTIALLY_RECEIVED", "RECEIVED", "CLOSED", "CANCELLED"] as const;
export type ConsignmentStatus = (typeof CONSIGNMENT_STATUSES)[number];

/** Dimensions a replenishment line may name; site, warehouse and owner come from the order. */
export const CONSIGNMENT_LINE_DIMENSIONS = ["location", "batch", "serial"] as const;
export type ConsignmentLineDimension = (typeof CONSIGNMENT_LINE_DIMENSIONS)[number];

export interface ConsignmentLine {
  id: string;
  lineNo: number;
  productId: string;
  /** The vendor-owned dimension set received into (site, warehouse and owner included). */
  dimensions: Partial<Record<string, string>>;
  unit: string;
  qty: string;
  receivedQty: string;
  /** Still expected; 0 once the order is closed or cancelled. */
  openQty: string;
  /** What a closed or cancelled order no longer expects; 0 otherwise. */
  givenUpQty: string;
  consignmentPrice: string;
}

export interface ConsignmentOrder {
  id: string;
  legalEntityId: string;
  number: string;
  vendorId: string;
  siteId: string;
  warehouseId: string;
  requestedDate: string;
  currency: string;
  status: ConsignmentStatus;
  version: number;
  createdAt: string;
  /** Set when the order was closed or cancelled. */
  closedAt: string | null;
  closedBy?: string;
  closeReason?: string;
  lines: ConsignmentLine[];
}

export interface ConsignmentLineInput {
  productId: string;
  unit: string;
  qty: string;
  consignmentPrice?: string;
  dimensions: Partial<Record<ConsignmentLineDimension, string>>;
}

export interface ConsignmentInput {
  legalEntityId: string;
  vendorId: string;
  siteId: string;
  warehouseId: string;
  requestedDate: string;
  currency: string;
  lines: ConsignmentLineInput[];
}

export interface ConsignmentReceiptInput {
  lineId: string;
  qty: string;
}

export interface ConsignmentStockItem {
  legalEntityId: string;
  vendorId: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
  qty: string;
  consignmentPrice: string | null;
  currency?: string;
  /** qty x consignment price: a statistic, never posted. */
  statisticalValue: string | null;
  oldestReceipt: string | null;
  ageDays: number | null;
}

/** Creates a replenishment order; the key makes a retried submit replay instead of creating twice. */
export async function createConsignment(input: ConsignmentInput, key: string = newKey()): Promise<ConsignmentOrder> {
  const body = {
    ...input,
    lines: input.lines.map((line) => {
      const out: Record<string, unknown> = { productId: line.productId, unit: line.unit, qty: line.qty, dimensions: cleanDimensions(line.dimensions) };
      if (line.consignmentPrice?.trim()) out.consignmentPrice = line.consignmentPrice.trim();
      return out;
    }),
  };
  return (await send<ConsignmentOrder>("/consignment-orders", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) })).body;
}

export async function listConsignments(q: { status?: ConsignmentStatus | ""; vendor?: string } = {}, signal?: AbortSignal): Promise<ConsignmentOrder[]> {
  const { body } = await send<{ items?: ConsignmentOrder[] }>(`/consignment-orders${query({ ...q })}`, {}, signal);
  return body.items ?? [];
}

export async function getConsignment(id: string, signal?: AbortSignal): Promise<ConsignmentOrder> {
  return (await send<ConsignmentOrder>(`/consignment-orders/${encodeURIComponent(id)}`, {}, signal)).body;
}

/** Receives vendor-owned stock at the version the screen shows (If-Match). */
export async function receiveConsignment(id: string, version: number, receiptDate: string, lines: ConsignmentReceiptInput[]): Promise<ConsignmentOrder> {
  return (
    await send<ConsignmentOrder>(`/consignment-orders/${encodeURIComponent(id)}:receive`, {
      method: "POST",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify({ receiptDate, lines }),
    })
  ).body;
}

/**
 * Ends an order at the version the screen shows (If-Match): "close" gives up the open quantity of a
 * partly received order, "cancel" ends an order nothing was received against.
 */
export async function endConsignment(id: string, action: "close" | "cancel", version: number, reason: string): Promise<ConsignmentOrder> {
  const body = reason.trim() ? { reason: reason.trim() } : {};
  return (
    await send<ConsignmentOrder>(`/consignment-orders/${encodeURIComponent(id)}:${action}`, {
      method: "POST",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify(body),
    })
  ).body;
}

export async function listConsignmentStock(vendor = "", signal?: AbortSignal): Promise<ConsignmentStockItem[]> {
  const { body } = await send<{ items?: ConsignmentStockItem[] }>(`/consignment/stock${query({ vendor })}`, {}, signal);
  return body.items ?? [];
}

/** Inventory-to-ledger reconciliation (FR-16.12). */
export const RECONCILIATION_STATUSES = ["MATCHED", "DIFFERENCE"] as const;
export type ReconciliationStatus = (typeof RECONCILIATION_STATUSES)[number];

/**
 * One run per legal entity, inventory account, currency and run date; a
 * re-run on the same day overwrites it. diff is inventoryValue minus
 * ledgerBalance. GL_JOURNALS compares with the GL of the posted inventory
 * journals; LEDGER_OVERRIDE with a balance read from the ledger.
 */
export interface Reconciliation {
  id: string;
  legalEntityId: string;
  account: string;
  currency: string;
  runDate: string;
  inventoryValue: string;
  ledgerBalance: string;
  diff: string;
  status: ReconciliationStatus;
  ledgerSource: "GL_JOURNALS" | "LEDGER_OVERRIDE";
  negativeRows: number;
  createdAt: string;
}

export interface ReconcileInput {
  legalEntityId: string;
  currency: string;
  /** Inventory account balance from the ledger; omitted to compare with the posted journals. */
  ledgerBalance?: string;
}

/** Runs the reconciliation now (inventory.admin). Safe to repeat: the day's run is recomputed. */
export async function runReconciliation(input: ReconcileInput): Promise<Reconciliation> {
  const body: Record<string, string> = { legalEntityId: input.legalEntityId, currency: input.currency };
  if (input.ledgerBalance?.trim()) body.ledgerBalance = input.ledgerBalance.trim();
  return (await send<Reconciliation>("/reconciliations", { method: "POST", body: JSON.stringify(body) })).body;
}

export async function listReconciliations(
  q: { legalEntityId?: string; status?: ReconciliationStatus | "" } = {},
  signal?: AbortSignal,
): Promise<Reconciliation[]> {
  const { body } = await send<{ items?: Reconciliation[] }>(`/reconciliations${query({ ...q })}`, {}, signal);
  return body.items ?? [];
}

export async function getReconciliation(id: string, signal?: AbortSignal): Promise<Reconciliation> {
  return (await send<Reconciliation>(`/reconciliations/${encodeURIComponent(id)}`, {}, signal)).body;
}

// ---- Journal names (PRD 3.16.13) ----

export interface JournalName {
  id: string;
  legalEntityId: string;
  code: string;
  description: string;
  journalType: KeyedJournalType;
  requiresApproval: boolean;
  offsetAccountId?: string;
  active: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface JournalNameInput {
  legalEntityId: string;
  code: string;
  description?: string;
  journalType: KeyedJournalType;
  requiresApproval: boolean;
  offsetAccountId?: string;
}

export interface JournalNamePatch {
  description?: string;
  requiresApproval?: boolean;
  offsetAccountId?: string;
  active?: boolean;
}

export interface JournalNameQuery {
  legalEntityId?: string;
  journalType?: string;
  active?: "true" | "false" | "";
}

export async function listJournalNames(q: JournalNameQuery = {}, signal?: AbortSignal): Promise<JournalName[]> {
  const { body } = await send<{ items?: JournalName[] }>(`/journal-names${query({ legalEntityId: q.legalEntityId, journalType: q.journalType, active: q.active })}`, {}, signal);
  return body.items ?? [];
}

/** Creates a journal name; the key makes a retried click replay the same create. */
export async function createJournalName(input: JournalNameInput, key: string = newKey()): Promise<JournalName> {
  const body: JournalNameInput = { ...input };
  if (!body.description) delete body.description;
  if (!body.offsetAccountId) delete body.offsetAccountId;
  return (await send<JournalName>("/journal-names", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) })).body;
}

/** Changes a journal name at the version the screen shows (If-Match). */
export async function updateJournalName(id: string, version: number, patch: JournalNamePatch): Promise<JournalName> {
  return (
    await send<JournalName>(`/journal-names/${encodeURIComponent(id)}`, {
      method: "PATCH",
      headers: { "If-Match": `"${version}"` },
      body: JSON.stringify(patch),
    })
  ).body;
}

export interface WorkspaceSummary {
  journals: { draft: number; awaitingApproval: number };
  negativeOnHandRows: number;
  openCountSheets: number;
  countSheetsCounted: number;
  reconciliationDifferences: number;
  /** When the latest reconciliation run in scope was computed; undefined when none has run. */
  lastReconciliationAt?: string;
  activeReservations: number;
  transferOrdersByStatus: Partial<Record<TransferStatus, number>>;
}

/** Counts behind the inventory management workspace tiles, for the caller's legal entities or one of them. */
export async function getWorkspaceSummary(legalEntityId = "", signal?: AbortSignal): Promise<WorkspaceSummary> {
  const { body } = await send<Partial<Omit<WorkspaceSummary, "lastReconciliationAt">> & { lastReconciliationAt?: string | null }>(
    `/workspace/summary${reportQuery({ legalEntityId })}`,
    {},
    signal,
  );
  return {
    journals: { draft: body.journals?.draft ?? 0, awaitingApproval: body.journals?.awaitingApproval ?? 0 },
    negativeOnHandRows: body.negativeOnHandRows ?? 0,
    openCountSheets: body.openCountSheets ?? 0,
    countSheetsCounted: body.countSheetsCounted ?? 0,
    reconciliationDifferences: body.reconciliationDifferences ?? 0,
    lastReconciliationAt: body.lastReconciliationAt ?? undefined,
    activeReservations: body.activeReservations ?? 0,
    transferOrdersByStatus: body.transferOrdersByStatus ?? {},
  };
}
