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
