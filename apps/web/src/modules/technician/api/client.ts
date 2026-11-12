import { apiFetch } from "../../../shell/api/http";
import { fromBase64 } from "../domain/image";
import type { TruckStockItem } from "../domain/dayCache";
import { OfflineError, RefusedError, type SyncClient } from "../domain/sync";
import type { ActionBody, ActionOutcome, CaptureBody, TechnicianDay } from "../domain/types";

const BASE = "/api/v1/service";

async function problem(res: Response): Promise<RefusedError> {
  let code: string | undefined;
  let detail = `The server refused the request (${res.status}).`;
  try {
    const body = (await res.json()) as { code?: string; detail?: string };
    code = body.code;
    if (body.detail) detail = body.detail;
  } catch {
    // the body was not a problem document
  }
  return new RefusedError(detail, res.status, code);
}

/**
 * Runs a request; a lost connection, a passing server fault or an ended session is an OfflineError, any other 4xx a
 * RefusedError. A 401 must not be a refusal: the sync drops refused entries, and a session that expired during an
 * offline day would otherwise throw away the day's queued work instead of keeping it until the technician signs in.
 */
async function call(url: string, init: RequestInit): Promise<Response> {
  let res: Response;
  try {
    res = await apiFetch(url, init);
  } catch {
    throw new OfflineError("offline");
  }
  if (res.ok) return res;
  if (res.status === 401) throw new OfflineError("signed out");
  if (res.status >= 500 || res.status === 408 || res.status === 429) throw new OfflineError(`server ${res.status}`);
  throw await problem(res);
}

/** The signed-in technician's route for a local date (default today), `me` being the technician linked to the user. */
export async function fetchDay(date?: string): Promise<TechnicianDay> {
  const res = await call(`${BASE}/technicians/me/day${date ? `?date=${encodeURIComponent(date)}` : ""}`, { headers: { Accept: "application/json" } });
  return (await res.json()) as TechnicianDay;
}

export const ROUTE_EVENTS_URL = `${BASE}/technicians/me/day/events`;

export interface RouteSubscription {
  /** True while the stream is open: the device then polls only as a slow safety net. */
  readonly open: boolean;
  close(): void;
}

export interface SseEvent {
  event: string;
  data: string;
}

/**
 * Parses a server-sent event stream a chunk at a time. Frames end with a blank line; `event:` and `data:` fields are read,
 * comment lines (":") and other fields (`retry:`, `id:`) are ignored. Chunks may split a line or a frame anywhere.
 */
export function createSseParser(): (chunk: string) => SseEvent[] {
  let buffer = "";
  let event = "";
  let data: string[] = [];
  return (chunk) => {
    buffer += chunk;
    const out: SseEvent[] = [];
    for (;;) {
      const at = buffer.search(/\r\n|\n|\r/);
      if (at < 0) break;
      const line = buffer.slice(0, at);
      const eol = buffer.startsWith("\r\n", at) ? 2 : 1;
      if (at + eol === buffer.length && buffer[at] === "\r") break; // a CR at the end may be half of a CRLF
      buffer = buffer.slice(at + eol);
      if (line === "") {
        if (event !== "" || data.length > 0) out.push({ event: event || "message", data: data.join("\n") });
        event = "";
        data = [];
      } else if (line.startsWith(":")) {
        continue;
      } else {
        const colon = line.indexOf(":");
        const field = colon < 0 ? line : line.slice(0, colon);
        let value = colon < 0 ? "" : line.slice(colon + 1);
        if (value.startsWith(" ")) value = value.slice(1);
        if (field === "event") event = value;
        else if (field === "data") data.push(value);
      }
    }
    return out;
  };
}

export interface RouteStreamDeps {
  /** The request function; defaults to apiFetch, which adds the bearer token the gateway reads. */
  fetchImpl?: (url: string, init: RequestInit) => Promise<Response>;
  /** Waits before a reconnect; tests replace it. */
  sleep?: (ms: number) => Promise<void>;
}

const RECONNECT_MIN_MS = 1000;
const RECONNECT_MAX_MS = 30_000;

/**
 * Listens for "route" events (#45): the service says the technician's route changed (an assignment, a published day, a new
 * checklist), and the device reads its day again at once instead of waiting for the next poll. The stream is read with fetch
 * through apiFetch, not EventSource: the gateway authenticates the Authorization bearer header, which EventSource cannot send.
 * A dropped stream reconnects with a backoff (1 s doubling to 30 s) and the device reads its day again on every reconnect.
 * Without fetch streaming, or when the service answers 503 (no feed), the stream is never open and the device keeps its poll.
 */
export function subscribeRoute(onRoute: () => void, deps: RouteStreamDeps = {}): RouteSubscription {
  const fetchImpl = deps.fetchImpl ?? ((url, init) => apiFetch(url, init));
  const sleep = deps.sleep ?? ((ms) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
  const controller = new AbortController();
  let open = false;
  let closed = false;
  let connected = false;
  void (async () => {
    let wait = RECONNECT_MIN_MS;
    while (!closed) {
      try {
        const res = await fetchImpl(ROUTE_EVENTS_URL, { headers: { Accept: "text/event-stream" }, signal: controller.signal });
        if (!res.ok || !res.body) throw new Error(`route stream answered ${res.status}`);
        open = true;
        wait = RECONNECT_MIN_MS;
        // The first open follows the day that was just read; a later one is a reconnect that may follow missed events.
        if (connected) onRoute();
        connected = true;
        const parse = createSseParser();
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          for (const ev of parse(decoder.decode(value, { stream: true }))) if (ev.event === "route") onRoute();
        }
      } catch {
        // dropped or refused: reconnect below unless closed
      }
      open = false;
      if (closed) return;
      await sleep(wait);
      wait = Math.min(wait * 2, RECONNECT_MAX_MS);
    }
  })();
  return {
    get open() {
      return open;
    },
    close: () => {
      closed = true;
      open = false;
      controller.abort();
    },
  };
}

/** The truck's stock levels with what it holds; an empty list when inventory has none or cannot be reached. */
export async function fetchTruckStock(legalEntityId: string, siteId: string, warehouseId: string): Promise<TruckStockItem[]> {
  const q = new URLSearchParams({ legalEntityId, siteId, warehouseId });
  try {
    const res = await call(`/api/v1/inventory/truck-stock-levels?${q}`, { headers: { Accept: "application/json" } });
    return ((await res.json()) as { items: TruckStockItem[] }).items;
  } catch {
    return [];
  }
}

export const serverClient: SyncClient = {
  async postActions(workOrderId: string, technicianId: string, actions: ActionBody[]): Promise<ActionOutcome[]> {
    const res = await call(`${BASE}/work-orders/${workOrderId}/actions`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify({ technicianId, actions }),
    });
    return ((await res.json()) as { results: ActionOutcome[] }).results;
  },
  async putCapture(workOrderId: string, technicianId: string, c: CaptureBody): Promise<void> {
    const q = new URLSearchParams({ technicianId, kind: c.kind, capturedAt: c.capturedAt });
    if (c.latitude !== undefined && c.longitude !== undefined) {
      q.set("lat", String(c.latitude));
      q.set("lon", String(c.longitude));
    }
    const bytes = fromBase64(c.data);
    await call(`${BASE}/work-orders/${workOrderId}/captures/${c.captureId}?${q}`, {
      method: "PUT",
      headers: { "Content-Type": c.contentType, Accept: "application/json" },
      body: new Blob([bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer], { type: c.contentType }),
    });
  },
};
