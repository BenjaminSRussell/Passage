// Device session calls to the mobile service (services/mobile/api/openapi.yaml, FR-32.1, FR-32.6).
// The browser reaches them through the BFF at /api/v1/mobile.
import { apiFetch } from "../../../shell/api/http";
import { base } from "./client";

export interface DeviceSession {
  sessionId: string;
  deviceId: string;
  userId: string;
  warehouseId: string;
  menuKey: string;
  status: "ACTIVE" | "STALE" | "ENDED";
  version: number;
}

export interface SessionStart {
  session: DeviceSession;
  /** The caller's own open session on this device was picked up again. */
  resumed: boolean;
  /** MOB-0117 when a flow run was in progress. */
  notice?: { code: string; message: string };
}

/** A failed session call: the HTTP status and the problem code, when the body had one. */
export class SessionApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string | undefined,
    message: string,
  ) {
    super(message);
    this.name = "SessionApiError";
  }
}

async function failure(res: Response): Promise<SessionApiError> {
  let code: string | undefined;
  let detail = `request failed with ${res.status}`;
  try {
    const body = (await res.json()) as { code?: string; detail?: string };
    code = body.code;
    detail = body.detail ?? detail;
  } catch {
    // Not a problem body: keep the generic text.
  }
  return new SessionApiError(res.status, code, detail);
}

async function post<T>(path: string, body?: unknown): Promise<T> {
  const res = await apiFetch(`${base}${path}`, {
    method: "POST",
    credentials: "same-origin",
    headers: { Accept: "application/json", ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await failure(res);
  return (await res.json()) as T;
}

/** Binds the device to the signed-in user, a warehouse and a menu; resumes the user's own open session. */
export function startSession(input: { deviceId: string; warehouseId: string; menuKey?: string }): Promise<SessionStart> {
  return post<SessionStart>("/sessions", input);
}

/** Records liveness and the device clock (the replay skew check, MOB-0114). */
export async function heartbeat(sessionId: string, deviceTime: Date): Promise<{ session: DeviceSession; relocked: boolean; reason?: string }> {
  return post(`/sessions/${encodeURIComponent(sessionId)}:heartbeat`, { deviceTime: deviceTime.toISOString() });
}

export async function endSession(sessionId: string): Promise<void> {
  await post(`/sessions/${encodeURIComponent(sessionId)}:end`);
}
