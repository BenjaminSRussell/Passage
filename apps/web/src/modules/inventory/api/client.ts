// Client for the inventory service (services/inventory/api/openapi.yaml).
// Browser calls use /api/v1/inventory/..., which the web BFF rewrites to the service.

export const base = "/api/v1/inventory";

export const JOURNAL_TYPES = ["MOVEMENT", "ADJUSTMENT", "OPENING_BALANCE"] as const;
export type JournalType = (typeof JOURNAL_TYPES)[number];

export const JOURNAL_STATUSES = ["DRAFT", "SUBMITTED", "APPROVED", "POSTING", "POSTED", "REJECTED", "CANCELLED"] as const;
export type JournalStatus = (typeof JOURNAL_STATUSES)[number];

/** Dimensions a journal line can carry in this slice (owner and license_plate are reserved). */
export const LINE_DIMENSIONS = ["site", "warehouse", "location", "batch", "serial", "inventory_status"] as const;
export type LineDimension = (typeof LINE_DIMENSIONS)[number];

export const ON_HAND_GROUPS = ["product", "legal_entity", "site", "warehouse", "location", "batch", "serial", "inventory_status"] as const;
export type OnHandGroup = (typeof ON_HAND_GROUPS)[number];

export interface JournalSummary {
  id: string;
  legalEntityId: string;
  journalType: JournalType;
  status: JournalStatus;
  postingDate: string;
  offsetAccountId?: string;
  currency: string;
  version: number;
  lineCount: number;
  createdAt: string;
  postedAt?: string;
}

export interface JournalLine {
  id: string;
  lineNo: number;
  clientKey?: string;
  productId: string;
  dimensions: Partial<Record<string, string>>;
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
  journalType: JournalType;
  legalEntityId: string;
  postingDate: string;
  currency: string;
  offsetAccountId?: string;
}

export interface LineInput {
  productId: string;
  dimensions: Partial<Record<LineDimension, string>>;
  qty: string;
  enteredUnit: string;
  costPrice?: string;
}

export interface OnHandRow {
  group: Partial<Record<string, string>>;
  physicalInvent: string;
  availPhysical: string;
  availOrdered: string;
  postedValue: string;
  reservedPhysical: string;
  currency: string;
  negative: boolean;
}

export interface OnHandResult {
  items: OnHandRow[];
  truncated: boolean;
}

export interface OnHandQuery {
  groupBy: readonly OnHandGroup[];
  product?: string;
  site?: string;
  warehouse?: string;
  batch?: string;
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
  return { body: (await res.json()) as T, etag: res.headers.get("ETag") };
}

function newKey(): string {
  return crypto.randomUUID();
}

export function onHandUrl(q: OnHandQuery): string {
  const params = new URLSearchParams();
  if (q.groupBy.length > 0) params.set("groupBy", q.groupBy.join(","));
  for (const key of ["product", "site", "warehouse", "batch"] as const) {
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
  return { items: body.items, truncated: body.truncated === true };
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
