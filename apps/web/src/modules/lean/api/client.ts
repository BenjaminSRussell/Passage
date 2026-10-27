// Client for the lean service (services/lean/api/openapi.yaml). Browser calls
// use /api/v1/lean/..., which the web BFF routes to the service. Quantities
// travel as decimal strings.

export const base = "/api/v1/lean";

export const KANBAN_STATES = ["not-planned", "planned", "prepared", "in-progress", "completed", "emptied"] as const;
export type KanbanState = (typeof KANBAN_STATES)[number];

export const ACTIVITY_KINDS = ["process", "transfer", "service"] as const;
export type ActivityKind = (typeof ACTIVITY_KINDS)[number];

export type FlowStatus = "draft" | "active" | "expired";
export type RuleType = "manufacturing" | "withdrawal";
export type Replenishment = "fixed" | "scheduled" | "event";
export const EVENT_SOURCES = ["sales", "bom", "kanban", "min-stock"] as const;
export type EventSource = (typeof EVENT_SOURCES)[number];

export interface ActivityInput {
  sequence: number;
  name: string;
  kind: ActivityKind;
  cycleTimeS: number;
  workCellResourceId?: string | null;
  inputLocationId?: string | null;
  outputLocationId?: string | null;
  predecessors: number[];
  pickingRequired: boolean;
}

export interface Activity extends ActivityInput {
  id: string;
}

export interface Flow {
  id: string;
  valueStreamId: string;
  siteId: string;
  name: string;
  taktTimeS: number | null;
  status: FlowStatus;
  activeFrom: string | null;
  activeTo: string | null;
  version: number;
  activityCount: number;
  activities?: Activity[];
  createdAt: string;
  updatedAt: string;
}

export interface NewFlow {
  valueStreamId: string;
  siteId: string;
  name: string;
  taktTimeS?: number | null;
  activities: ActivityInput[];
}

export interface Rule {
  id: string;
  code: string;
  itemId: string;
  siteId: string;
  flowId: string | null;
  ruleType: RuleType;
  replenishment: Replenishment;
  fixedQty: string;
  numberOfKanbans: number | null;
  leadTimeH: number;
  eventSource: EventSource | null;
  exactQty: boolean;
  effectiveFrom: string;
  effectiveTo: string | null;
  version: number;
  activeKanbans: number;
  createdAt: string;
}

export interface NewRule {
  code: string;
  itemId: string;
  siteId: string;
  flowId?: string | null;
  ruleType: RuleType;
  replenishment: Replenishment;
  fixedQty: string;
  numberOfKanbans?: number | null;
  leadTimeH: number;
  eventSource?: EventSource | null;
  exactQty: boolean;
  effectiveFrom?: string;
}

export interface Kanban {
  id: string;
  ruleId: string;
  ruleCode: string;
  itemId: string;
  ruleType: RuleType;
  replenishment: Replenishment;
  cardNumber: string;
  qty: string;
  state: KanbanState;
  due: string | null;
  eventRefType: string | null;
  eventRefId: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface GenerateInput {
  ruleId: string;
  count?: number;
  demandQty?: string;
  eventRefType?: string;
  eventRefId?: string;
}

export interface BoardColumn {
  state: KanbanState;
  kanbans: Kanban[];
}

export interface Board {
  columns: BoardColumn[];
  truncated: boolean;
}

export interface FixedCountRequest {
  dailyDemand: string;
  leadTimeDays: string;
  safetyPct: string;
  fixedQty: string;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class LeanApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "LeanApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof LeanApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<LeanApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new LeanApiError(res.status, code, message, fields);
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
    throw new LeanApiError(0, "COM-0001", "The response has no items");
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

function post<T>(path: string, body: unknown, key?: string): Promise<T> {
  const headers: Record<string, string> = {};
  if (key) headers["Idempotency-Key"] = key;
  return send<T>(path, { method: "POST", headers, body: JSON.stringify(body) });
}

export function listFlows(status: FlowStatus | "" = "", signal?: AbortSignal): Promise<Flow[]> {
  return items<Flow>(`/flows${query({ status, limit: 200 })}`, signal);
}

export function getFlow(id: string, signal?: AbortSignal): Promise<Flow> {
  return send<Flow>(`/flows/${encodeURIComponent(id)}`, {}, signal);
}

/** Creates a draft flow; reuse one key per form so a retried submit cannot create two. */
export function createFlow(input: NewFlow, key: string = newKey()): Promise<Flow> {
  return post<Flow>("/flows", input, key);
}

/** Validates connectivity and activates a draft flow (FR-24.8). */
export function activateFlow(id: string, input: { version: number; activeFrom?: string }, key: string = newKey()): Promise<Flow> {
  return post<Flow>(`/flows/${encodeURIComponent(id)}:activate`, input, key);
}

export function listRules(flowId = "", signal?: AbortSignal): Promise<Rule[]> {
  return items<Rule>(`/kanban-rules${query({ flowId, limit: 200 })}`, signal);
}

export function getRule(id: string, signal?: AbortSignal): Promise<Rule> {
  return send<Rule>(`/kanban-rules/${encodeURIComponent(id)}`, {}, signal);
}

export function createRule(input: NewRule, key: string = newKey()): Promise<Rule> {
  return post<Rule>("/kanban-rules", input, key);
}

/** BR-24.7 suggestion for a fixed rule's number of kanbans; writes nothing. */
export function suggestFixedCount(req: FixedCountRequest): Promise<{ numberOfKanbans: number }> {
  return post("/kanban-rules:fixed-count", req);
}

/** Generates cards for a fixed rule (count) or an event rule (demandQty and its reference). */
export async function generateKanbans(input: GenerateInput, key: string = newKey()): Promise<Kanban[]> {
  const body = await post<{ items?: Kanban[] }>("/kanbans:generate", input, key);
  return body.items ?? [];
}

/** Moves a card one step (BR-24.6); version is the one the board read. */
export function moveKanban(id: string, input: { to: KanbanState; version: number }, key: string = newKey()): Promise<Kanban> {
  return post<Kanban>(`/kanbans/${encodeURIComponent(id)}:move`, input, key);
}

export function fetchBoard(filter: { siteId?: string; flowId?: string; ruleId?: string } = {}, signal?: AbortSignal): Promise<Board> {
  return send<Board>(`/board${query(filter)}`, {}, signal);
}
