/** Client for the production order API (services/production, wave 21). Paths follow api/openapi.yaml. */

export const base = "/api/v1/production/orders";

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

export interface OrderLine {
  lineNo: string;
  itemId: string;
  qtyPer: string;
  seriesSize: string;
  scrapPct: string;
  roundUp: boolean;
  qtyRequired: string;
  unitCost: string;
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
  quantity: string;
  qtyRaf: string;
  qtyError: string;
  status: ProductionStatus;
  deliveryDate?: string;
  originType: string;
  originId?: string;
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
  plannedCost: string;
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
  productId: string;
  siteId: string;
  warehouseId: string;
  currency: string;
  quantity: string;
  deliveryDate?: string;
  bom: BOMComponentInput[];
  operations: OperationInput[];
}

/** Planned cost read (FR-21.11): totals per group and overall to cents, and the 4-decimal rows. */
export interface CostView {
  orderId: string;
  currency: string;
  totals: Partial<Record<CostGroup, string>>;
  total: string;
  rows: CostRow[];
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

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(`${base}${path}`, init);
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
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
  cursor?: string;
  limit?: number;
  signal?: AbortSignal;
}

export function listOrders({ status, cursor, limit = 50, signal }: ListOptions = {}): Promise<OrderPage> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (status) {
    params.set("status", status);
  }
  if (cursor) {
    params.set("cursor", cursor);
  }
  return request<OrderPage>(`?${params}`, { signal });
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
  };
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
