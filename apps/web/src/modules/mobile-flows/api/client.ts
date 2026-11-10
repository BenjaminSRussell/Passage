// Flow designer API client. Paths follow the mobile service OpenAPI (wave 32, §6.32.5)
// as exposed to the browser through the BFF at /api/v1/mobile.
import type { FlowDraft } from "../domain/flowGraph";
import { apiFetch } from "../../../shell/api/http";

export const base = "/api/v1/mobile";

/** Menu a device loads when none is provisioned (FR-32.2, GET /mobile/v1/menus/{id}). */
export const DEFAULT_MENU_ID = "default";

/** SSE channel the device watches for publishes (dev.passage.mobile.flow.published.v1). */
export const PUBLISH_EVENTS_URL = `${base}/events`;

export interface SavedFlow extends FlowDraft {
  flowId: string;
  version: number;
  status: "DRAFT" | "PUBLISHED" | "RETIRED";
}

export class FlowApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string | undefined,
    message: string,
  ) {
    super(message);
    this.name = "FlowApiError";
  }
}

async function failure(res: Response): Promise<FlowApiError> {
  let code: string | undefined;
  let detail = `request failed with ${res.status}`;
  try {
    const body = (await res.json()) as { code?: string; detail?: string };
    code = body.code;
    detail = body.detail ?? detail;
  } catch {
    // Non-JSON error bodies keep the generic message.
  }
  return new FlowApiError(res.status, code, detail);
}

/** Creates or updates a DRAFT flow. Requires warehouse.mobile-flow.admin. */
export async function saveDraft(flow: FlowDraft, idempotencyKey: string): Promise<SavedFlow> {
  const res = await apiFetch(`${base}/flows`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(flow),
  });
  if (!res.ok) throw await failure(res);
  return (await res.json()) as SavedFlow;
}

/** A device confirmation that replay could not apply (BR-32.4, MOB-0107). */
export interface OfflineConflict {
  /** `device_id:seq`, the Idempotency-Key the device sent on replay. */
  idempotencyKey: string;
  deviceId: string;
  command: string;
  /** Device capture time corrected by the measured server skew (BR-32.4). */
  capturedAt: string;
  /** For example "line was CANCELLED by alice". */
  conflictReason: string;
  version: number;
}

export type ConflictResolution = "reapply" | "reject";

/**
 * Lists CONFLICT transactions for the supervisor queue (§6.32.14). The endpoint belongs to the
 * mobile service, which is a handoff (apps/warehouse-mobile/NOT-IMPLEMENTED.md); this is the
 * shape the browser expects.
 */
export async function listConflicts(signal?: AbortSignal): Promise<OfflineConflict[]> {
  const res = await apiFetch(`${base}/offline/conflicts`, { headers: { Accept: "application/json" }, credentials: "same-origin", signal });
  if (!res.ok) throw await failure(res);
  const body = (await res.json()) as { items?: OfflineConflict[] };
  if (!Array.isArray(body.items)) throw new FlowApiError(res.status, "COM-0001", "The response has no items");
  return body.items;
}

/**
 * Resolves one conflict (BR-32.4). "reapply" moves the inventory as scanned with the order link
 * dropped; "reject" discards the transaction. Requires warehouse.offline-conflict.resolve. If-Match carries the
 * conflict version so two supervisors cannot both resolve it.
 */
export async function resolveConflict(conflict: OfflineConflict, action: ConflictResolution, idempotencyKey: string): Promise<void> {
  const res = await apiFetch(`${base}/offline/conflicts/${encodeURIComponent(conflict.idempotencyKey)}:resolve`, {
    method: "POST",
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": idempotencyKey,
      "If-Match": `"${conflict.version}"`,
    },
    body: JSON.stringify({ action }),
  });
  if (!res.ok) throw await failure(res);
}

/** Error text for the conflict queue; a 404 without a code means the mobile service is not deployed. */
export function describeConflictError(error: unknown): { code: string; message: string; fields: Record<string, string> } {
  if (error instanceof FlowApiError) {
    if (error.status === 404 && !error.code) {
      return { code: "HTTP-404", message: "The mobile service is not available yet, so offline conflicts cannot be listed or resolved.", fields: {} };
    }
    if (error.status === 412) {
      return { code: error.code ?? "HTTP-412", message: "Another supervisor changed this conflict. Reload the queue and try again.", fields: {} };
    }
    return { code: error.code ?? `HTTP-${error.status}`, message: error.message, fields: {} };
  }
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

/** Validates on the server and publishes. Devices pick the version up on the next menu load. */
export async function publishFlow(flowId: string, idempotencyKey: string, ifMatch: string): Promise<SavedFlow> {
  const res = await apiFetch(`${base}/flows/${encodeURIComponent(flowId)}:publish`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": ifMatch },
  });
  if (!res.ok) throw await failure(res);
  return (await res.json()) as SavedFlow;
}

/** Published flows a device menu offers (GET /mobile/v1/menus/{id}, FR-32.2). */
export interface MobileMenu {
  id: string;
  flows: SavedFlow[];
}

function asSavedFlow(value: unknown): SavedFlow | null {
  if (typeof value !== "object" || value === null) return null;
  const f = value as Partial<SavedFlow>;
  if (typeof f.code !== "string" || typeof f.entryStepId !== "string" || !Array.isArray(f.steps)) return null;
  if (typeof f.flowId !== "string" || typeof f.version !== "number") return null;
  if (f.status !== "DRAFT" && f.status !== "PUBLISHED" && f.status !== "RETIRED") return null;
  return f as SavedFlow;
}

function asMenu(value: unknown): MobileMenu | null {
  if (typeof value !== "object" || value === null) return null;
  const body = value as { id?: unknown; flows?: unknown };
  if (typeof body.id !== "string" || !Array.isArray(body.flows)) return null;
  const flows: SavedFlow[] = [];
  for (const item of body.flows) {
    const flow = asSavedFlow(item);
    if (!flow) return null;
    flows.push(flow);
  }
  return { id: body.id, flows };
}

/** Loads a device menu. A 404 without a catalog code means the mobile service is not deployed. */
export async function fetchMenu(menuId: string = DEFAULT_MENU_ID, signal?: AbortSignal): Promise<MobileMenu> {
  const res = await apiFetch(`${base}/menus/${encodeURIComponent(menuId)}`, {
    headers: { Accept: "application/json" },
    credentials: "same-origin",
    signal,
  });
  if (!res.ok) throw await failure(res);
  const menu = asMenu(await res.json());
  if (!menu) throw new FlowApiError(res.status, "COM-0001", "The response has no menu flows");
  return menu;
}

/** A flow.published push (snake_case on the wire, camelCase in the client). */
export interface FlowPublished {
  flowId: string;
  code: string;
  version: number;
}

/** Parses one SSE data payload. Unknown or malformed frames are ignored. */
export function parseFlowPublished(data: string): FlowPublished | null {
  try {
    const body = JSON.parse(data) as { flow_id?: unknown; flowId?: unknown; code?: unknown; version?: unknown };
    const flowId = typeof body.flowId === "string" ? body.flowId : body.flow_id;
    if (typeof flowId !== "string" || typeof body.code !== "string" || typeof body.version !== "number") return null;
    return { flowId, code: body.code, version: body.version };
  } catch {
    return null;
  }
}

export interface SubscribePublishesDeps {
  EventSource?: typeof EventSource;
  url?: string;
}

/**
 * Listens for published flow versions (AC-32.4). Returns an unsubscribe that closes the
 * EventSource. The device then reloads the menu; it does not need a new app binary.
 */
export function subscribePublishes(onEvent: (event: FlowPublished) => void, deps: SubscribePublishesDeps = {}): () => void {
  const Ctor = deps.EventSource ?? (typeof EventSource === "undefined" ? undefined : EventSource);
  if (!Ctor) return () => undefined;
  let source: EventSource;
  try {
    source = new Ctor(deps.url ?? PUBLISH_EVENTS_URL);
  } catch {
    return () => undefined;
  }
  const listener = (ev: Event) => {
    const parsed = parseFlowPublished((ev as MessageEvent).data as string);
    if (parsed) onEvent(parsed);
  };
  source.addEventListener("flow.published", listener);
  return () => source.close();
}

/** Text for a failed flow call; the mobile service may not be deployed yet (404). */
export function describeFlowError(error: unknown): string {
  if (error instanceof FlowApiError) {
    if (error.status === 404 && !error.code) return "The mobile flow service is not available yet.";
    return error.code ? `${error.message} (${error.code})` : error.message;
  }
  return error instanceof Error ? error.message : "Request failed";
}
