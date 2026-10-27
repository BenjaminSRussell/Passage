/** Client for the production order API (services/production, wave 21). Paths follow api/openapi.yaml. */

export const root = "/api/v1/production";
export const base = `${root}/orders`;

export type ProductionStatus =
  | "Created"
  | "Estimated"
  | "Scheduled"
  | "Released"
  | "Started"
  | "ReportedAsFinished"
  | "Ended"
  | "Cancelled";

export const STATUSES: ProductionStatus[] = [
  "Created",
  "Estimated",
  "Scheduled",
  "Released",
  "Started",
  "ReportedAsFinished",
  "Ended",
  "Cancelled",
];

export type ProductionCommand =
  | "estimate"
  | "schedule"
  | "release"
  | "start"
  | "report-finished"
  | "end"
  | "cancel"
  | "reset-status";

export type CostGroup = "material" | "labor" | "overhead" | "subcontract";

/** When a BOM line's material is consumed (FR-21.5). */
export type Flushing = "manual" | "start" | "finish" | "available-at-location";

export type JournalType = "picking-list" | "route-card" | "report-as-finished";

export type Proposal = "remaining" | "proportional" | "none";

/** The four variance types of BR-21.6, and rework for a rework order (FR-21.20). */
export type VarianceType = "lot-size" | "production-quantity" | "production-price" | "production-substitution" | "rework";

/** What a rework order reports against (FR-21.20): an order by id, a batch or a serial by number. */
export type ReworkRefType = "order" | "batch" | "serial";

export interface ReworkReference {
  type: ReworkRefType;
  id: string;
}

/** The origin type of a rework order. */
export const REWORK = "Rework";

/** How an as-built component is identified (FR-21.19): a serial in quantity 1, or a batch. */
export type Tracking = "serial" | "batch";

export interface OrderLine {
  lineNo: string;
  itemId: string;
  qtyPer: string;
  seriesSize: string;
  scrapPct: string;
  roundUp: boolean;
  qtyRequired: string;
  unitCost: string;
  flushing?: Flushing;
  qtyConsumed?: string;
}

export interface Operation {
  operNumber: number;
  resourceId: string;
  setupTimeH: string;
  runTimeHPer: string;
  scrapPct: string;
  laborRate: string;
  overheadRate: string;
  subcontract: boolean;
  subcontractPrice: string;
  processQty: string;
  hoursReported?: string;
  qtyGood?: string;
  qtyError?: string;
  status?: "open" | "started" | "finished";
}

export interface CostRow {
  group: CostGroup;
  key: string;
  qty: string;
  amount: string;
  fixed: string;
}

export interface ProductionOrder {
  id: string;
  legalEntityId: string;
  orderNumber: string;
  productId: string;
  siteId: string;
  warehouseId: string;
  currency: string;
  itemGroup?: string;
  quantity: string;
  qtyRaf: string;
  qtyError: string;
  status: ProductionStatus;
  deliveryDate?: string;
  originType: string;
  originId?: string;
  reworkReference?: ReworkReference;
  version: number;
  lines: OrderLine[];
  operations: Operation[];
  costs: CostRow[];
  createdAt: string;
  updatedAt: string;
}

export interface OrderSummary {
  id: string;
  legalEntityId: string;
  orderNumber: string;
  productId: string;
  siteId: string;
  warehouseId: string;
  currency: string;
  quantity: string;
  qtyRaf: string;
  qtyError: string;
  remaining: string;
  status: ProductionStatus;
  deliveryDate?: string;
  originType?: string;
  reworkReference?: ReworkReference;
  plannedCost: string;
  actualCost?: string;
  version: number;
  createdAt: string;
}

export interface OrderPage {
  items: OrderSummary[];
  nextCursor?: string;
}

export interface StatusLogEntry {
  command: ProductionCommand;
  from: ProductionStatus;
  to: ProductionStatus;
  actor: string;
  traceId: string;
  occurredAt: string;
}

export interface BOMComponentInput {
  lineNo: string;
  itemId: string;
  qtyPer: string;
  scrapPct?: string;
  seriesSize?: string;
  unitCost?: string;
  roundUp?: boolean;
  flushing?: Flushing;
}

export interface OperationInput {
  operNumber: number;
  resourceId: string;
  setupTimeH?: string;
  runTimeHPer?: string;
  laborRate?: string;
  overheadRate?: string;
  scrapPct?: string;
}

export interface CreateOrderInput {
  itemGroup?: string;
  /** Rework makes a rework order; it needs the Create rework orders duty. */
  originType?: string;
  reworkReference?: ReworkReference;
  productId: string;
  siteId: string;
  warehouseId: string;
  currency: string;
  quantity: string;
  deliveryDate?: string;
  bom: BOMComponentInput[];
  operations: OperationInput[];
}

export interface Variance {
  type: VarianceType;
  costGroup: CostGroup;
  amount: string;
}

/**
 * Cost read (FR-21.11): planned and actual totals per group and overall in the currency's minor
 * unit, the 4-decimal rows, the WIP balance and the variances posted at End.
 */
export interface CostView {
  orderId: string;
  currency: string;
  totals: Partial<Record<CostGroup, string>>;
  total: string;
  rows: CostRow[];
  actualTotals: Partial<Record<CostGroup, string>>;
  actualTotal: string;
  actualRows: CostRow[];
  wipBalance: string;
  variances: Variance[];
  varianceTotal: string;
}

export interface JournalLine {
  kind: "issue" | "hours" | "receipt";
  lineNo?: string;
  operNumber?: number;
  itemId?: string;
  qty: string;
  qtyGood: string;
  qtyError: string;
  finished: boolean;
  unitCost: string;
  amount: string;
  overheadAmount: string;
}

export interface Posting {
  postingType: string;
  account: string;
  dr: string;
  cr: string;
  wip: boolean;
}

export interface Journal {
  id: string;
  orderId: string;
  type: JournalType;
  journalNumber: string;
  postingDate: string;
  posted: boolean;
  postedAt?: string;
  postedBy?: string;
  qtyGood: string;
  qtyError: string;
  reversalOf?: string;
  reversedBy?: string;
  journalEntryId?: string;
  createdBy: string;
  createdAt: string;
  lines: JournalLine[];
  postings: Posting[];
}

export interface IssueInput {
  lineNo: string;
  quantity: string;
  unitCost?: string;
}

export interface HoursInput {
  operNumber: number;
  hours: string;
  qtyGood?: string;
  qtyError?: string;
  finished?: boolean;
}

/** CreateJournal: without issues or hours the service proposes the lines. */
export interface CreateJournalInput {
  type: JournalType;
  postingDate?: string;
  proposal?: Proposal;
  quantity?: string;
  errorQuantity?: string;
  issues?: IssueInput[];
  hours?: HoursInput[];
}

export interface PostingProfileAccounts {
  wipAccount: string;
  inventoryAccount: string;
  finishedGoodsAccount: string;
  laborAbsorptionAccount: string;
  overheadAbsorptionAccount: string;
  lotSizeVarianceAccount: string;
  quantityVarianceAccount: string;
  priceVarianceAccount: string;
  substitutionVarianceAccount: string;
  /** Optional until the legal entity runs rework orders; ending one with rework cost needs it. */
  reworkVarianceAccount?: string;
}

export interface PostingProfileInput extends PostingProfileAccounts {
  legalEntityId?: string;
  itemGroup: string;
  siteId: string;
}

export interface PostingProfile extends PostingProfileInput {
  legalEntityId: string;
  version: number;
  updatedBy: string;
  updatedAt: string;
}

/** One component serial or batch consumed into a finished serial or batch (FR-21.19). */
export interface AsBuiltRecord {
  id: string;
  orderId: string;
  orderNumber: string;
  finishedItemId: string;
  finishedSerialOrBatch: string;
  componentItemId: string;
  tracking: Tracking;
  componentSerialOrBatch: string;
  qty: string;
  journalId?: string;
  rafJournalId?: string;
  assignedBy: string;
  assignedAt: string;
  detachedBy?: string;
  detachedAt?: string;
}

export interface AssignAsBuiltInput {
  finishedSerialOrBatch: string;
  componentItemId: string;
  tracking: Tracking;
  componentSerialOrBatch: string;
  /** Defaults to 1; a serial is always 1. */
  qty?: string;
}

/** The as-built inquiry: as-built of a finished serial, or where-used of a component serial. */
export interface AsBuiltQuery {
  finishedSerialOrBatch?: string;
  componentSerialOrBatch?: string;
  orderId?: string;
}

export interface CommandBody {
  quantity?: string;
  errorQuantity?: string;
  status?: ProductionStatus;
}

export interface Problem {
  status: number;
  code: string;
  message: string;
  errors?: Record<string, string>;
}

export class ProductionApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly errors?: Record<string, string>,
  ) {
    super(message);
    this.name = "ProductionApiError";
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ProductionApiError) {
    return { status: error.status, code: error.code, message: error.message, errors: error.errors };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed" };
}

async function toError(res: Response): Promise<ProductionApiError> {
  const body = (await res.json().catch(() => null)) as {
    code?: unknown;
    detail?: unknown;
    title?: unknown;
    errors?: unknown;
  } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : "COM-0001";
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (${res.status})`;
  const errors =
    body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : undefined;
  return new ProductionApiError(res.status, code, message, errors);
}

async function requestAt<T>(url: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(url, init);
  if (!res.ok) {
    throw await toError(res);
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return (await res.json()) as T;
}

function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  return requestAt<T>(`${base}${path}`, init);
}

/** A fresh Idempotency-Key. Keep one per user intent so a retry replays instead of duplicating. */
export function newIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `key-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export interface ListOptions {
  status?: ProductionStatus | "";
  /** Only orders of this origin, for example Rework. */
  originType?: string;
  /** Only the rework orders that reference this order id (FR-21.20). */
  reworkOf?: string;
  cursor?: string;
  limit?: number;
  signal?: AbortSignal;
}

export async function listOrders({ status, originType, reworkOf, cursor, limit = 50, signal }: ListOptions = {}): Promise<OrderPage> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (status) {
    params.set("status", status);
  }
  if (originType) {
    params.set("originType", originType);
  }
  if (reworkOf) {
    params.set("reworkOf", reworkOf);
  }
  if (cursor) {
    params.set("cursor", cursor);
  }
  const page = await request<Partial<OrderPage>>(`?${params}`, { signal });
  return { items: Array.isArray(page.items) ? page.items : [], nextCursor: page.nextCursor };
}

export function getOrder(id: string, signal?: AbortSignal): Promise<ProductionOrder> {
  return request<ProductionOrder>(`/${encodeURIComponent(id)}`, { signal });
}

export async function getStatusLog(id: string, signal?: AbortSignal): Promise<StatusLogEntry[]> {
  const body = await request<{ items?: StatusLogEntry[] }>(`/${encodeURIComponent(id)}/status-log`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

export async function getCosts(id: string, signal?: AbortSignal): Promise<CostView> {
  const body = await request<Partial<CostView>>(`/${encodeURIComponent(id)}/costs`, { signal });
  return {
    orderId: body.orderId ?? id,
    currency: body.currency ?? "",
    totals: body.totals ?? {},
    total: body.total ?? "0",
    rows: Array.isArray(body.rows) ? body.rows : [],
    actualTotals: body.actualTotals ?? {},
    actualTotal: body.actualTotal ?? "0",
    actualRows: Array.isArray(body.actualRows) ? body.actualRows : [],
    wipBalance: body.wipBalance ?? "0",
    variances: Array.isArray(body.variances) ? body.variances : [],
    varianceTotal: body.varianceTotal ?? "0",
  };
}

export async function listJournals(orderId: string, signal?: AbortSignal): Promise<Journal[]> {
  const body = await request<{ items?: Journal[] }>(`/${encodeURIComponent(orderId)}/journals`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** Creates a draft journal on an order (FR-21.5 to FR-21.7). */
export function createJournal(orderId: string, input: CreateJournalInput, idempotencyKey: string): Promise<Journal> {
  return request<Journal>(`/${encodeURIComponent(orderId)}/journals`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** Posts a draft journal; a retry with the same key replays. */
export function postJournal(journalId: string, idempotencyKey: string): Promise<Journal> {
  return requestAt<Journal>(`${root}/journals/${encodeURIComponent(journalId)}:post`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
  });
}

export function deleteJournal(journalId: string): Promise<void> {
  return requestAt<void>(`${root}/journals/${encodeURIComponent(journalId)}`, { method: "DELETE" });
}

export async function listPostingProfiles(signal?: AbortSignal): Promise<PostingProfile[]> {
  const body = await requestAt<{ items?: PostingProfile[] }>(`${root}/posting-profiles`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** Creates a profile (no version) or replaces it under If-Match (FR-21.13). */
export function savePostingProfile(input: PostingProfileInput, version?: number): Promise<PostingProfile> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (version !== undefined) {
    headers["If-Match"] = `"${version}"`;
  }
  return requestAt<PostingProfile>(`${root}/posting-profiles`, { method: "PUT", headers, body: JSON.stringify(input) });
}

/** The attached as-built records of an order, oldest first (FR-21.19). */
export async function listOrderAsBuilt(orderId: string, signal?: AbortSignal): Promise<AsBuiltRecord[]> {
  const body = await request<{ items?: AsBuiltRecord[] }>(`/${encodeURIComponent(orderId)}/as-built`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** Assigns a component serial or batch to a finished serial or batch; a retry with the same key replays. */
export function assignAsBuilt(orderId: string, input: AssignAsBuiltInput, idempotencyKey: string): Promise<AsBuiltRecord> {
  return request<AsBuiltRecord>(`/${encodeURIComponent(orderId)}/as-built`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

/** Detaches an as-built record (a wrong assignment); detaching twice returns it unchanged. */
export function detachAsBuilt(recordId: string): Promise<AsBuiltRecord> {
  return requestAt<AsBuiltRecord>(`${root}/as-built/${encodeURIComponent(recordId)}:detach`, { method: "POST" });
}

/** The as-built of a finished serial or the where-used of a component serial (4.21.14). */
export async function queryAsBuilt(query: AsBuiltQuery, signal?: AbortSignal): Promise<AsBuiltRecord[]> {
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(query)) {
    if (value) {
      params.set(name, value);
    }
  }
  const body = await requestAt<{ items?: AsBuiltRecord[] }>(`${root}/as-built?${params}`, { signal });
  return Array.isArray(body.items) ? body.items : [];
}

/** Journal types an order in this status can take (FR-21.5 to FR-21.7; a route card starts a Released order). */
export function journalTypesFor(status: ProductionStatus): JournalType[] {
  switch (status) {
    case "Released":
      return ["route-card"];
    case "Started":
    case "ReportedAsFinished":
      return ["picking-list", "route-card", "report-as-finished"];
    default:
      return [];
  }
}

export function createOrder(input: CreateOrderInput, idempotencyKey: string): Promise<ProductionOrder> {
  return request<ProductionOrder>("", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
}

export function updateOrder(
  id: string,
  version: number,
  patch: { quantity?: string; deliveryDate?: string | null },
): Promise<ProductionOrder> {
  return request<ProductionOrder>(`/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json", "If-Match": `"${version}"` },
    body: JSON.stringify(patch),
  });
}

/** Applies a status command. version is the ETag of the last read (If-Match). */
export function runCommand(
  id: string,
  command: ProductionCommand,
  version: number,
  idempotencyKey: string,
  body: CommandBody = {},
): Promise<ProductionOrder> {
  return request<ProductionOrder>(`/${encodeURIComponent(id)}:${command}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey, "If-Match": `"${version}"` },
    body: JSON.stringify(body),
  });
}

/** The forward command for a status, or null at the end of the line (BR-21.1). */
export function nextCommand(status: ProductionStatus): ProductionCommand | null {
  switch (status) {
    case "Created":
      return "estimate";
    case "Estimated":
      return "schedule";
    case "Scheduled":
      return "release";
    case "Released":
      return "start";
    case "Started":
      return "report-finished";
    case "ReportedAsFinished":
      return "end";
    default:
      return null;
  }
}

/** Every command the state machine allows from a status, forward command first. */
export function allowedCommands(status: ProductionStatus): ProductionCommand[] {
  switch (status) {
    case "Created":
      return ["estimate", "cancel"];
    case "Estimated":
      return ["schedule", "estimate", "cancel", "reset-status"];
    case "Scheduled":
      return ["release", "estimate", "cancel", "reset-status"];
    case "Released":
      return ["start", "estimate", "cancel", "reset-status"];
    case "Started":
      return ["report-finished", "end", "reset-status"];
    case "ReportedAsFinished":
      return ["end", "report-finished", "reset-status"];
    default:
      return [];
  }
}

/** Statuses a reset may return to from status (FR-21.9), latest first. */
export function resetTargets(status: ProductionStatus): ProductionStatus[] {
  const order: ProductionStatus[] = ["Created", "Estimated", "Scheduled", "Released", "Started", "ReportedAsFinished"];
  const at = order.indexOf(status);
  return at <= 0 ? [] : order.slice(0, at).reverse();
}
