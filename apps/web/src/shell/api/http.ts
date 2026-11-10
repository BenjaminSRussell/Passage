import { z } from "zod";

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export const SESSION_EXPIRED_EVENT = "passage:session-expired";
export const REQUEST_ID_HEADER = "X-Request-Id";

function newRequestId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `req-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

/**
 * The one transport every module uses. It always calls globalThis.fetch at call time (the sign-in
 * layer and the demo backend both patch it), tags the request with an X-Request-Id, and makes
 * failures traceable: an error body gains `requestId` so the error message can show it to support.
 * A 401 tells the shell the session has ended.
 */
export async function apiFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const sent = tag(init);
  const res = await globalThis.fetch(input, sent.init);
  if (res.ok) return res;
  if (res.status === 401 && typeof window !== "undefined") window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
  return withRequestId(res, res.headers?.get?.(REQUEST_ID_HEADER) ?? sent.id);
}

// Plain-object headers stay plain objects so callers and tests that read them keep working.
function tag(init: RequestInit): { init: RequestInit; id: string } {
  const h = init.headers;
  if (h === undefined || (!(h instanceof Headers) && !Array.isArray(h))) {
    const existing = Object.entries(h ?? {}).find(([k]) => k.toLowerCase() === REQUEST_ID_HEADER.toLowerCase());
    const id = existing?.[1] ?? newRequestId();
    return { init: existing ? init : { ...init, headers: { ...h, [REQUEST_ID_HEADER]: id } }, id };
  }
  const headers = new Headers(h);
  if (!headers.has(REQUEST_ID_HEADER)) headers.set(REQUEST_ID_HEADER, newRequestId());
  return { init: { ...init, headers }, id: headers.get(REQUEST_ID_HEADER) ?? "" };
}

async function withRequestId(res: Response, requestId: string): Promise<Response> {
  if (typeof res.clone !== "function") return res;
  try {
    const text = await res.clone().text();
    const body: unknown = JSON.parse(text);
    if (typeof body !== "object" || body === null || Array.isArray(body) || "requestId" in body) return res;
    return new Response(JSON.stringify({ ...body, requestId }), { status: res.status, statusText: res.statusText, headers: res.headers });
  } catch {
    return res;
  }
}

export const defaultFetch: FetchLike = (input, init) => apiFetch(input, init);

export const BASE = "/api/v1/receiving";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;
  readonly requestId?: string;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}, requestId?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
    this.requestId = requestId;
  }
}

export async function readProblem(res: Response): Promise<ApiError> {
  let body: { code?: unknown; detail?: unknown; errors?: unknown; requestId?: unknown } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    // Non-JSON error bodies fall through to the generic error below.
  }
  const code = typeof body.code === "string" ? body.code : "COM-0500";
  const detail = typeof body.detail === "string" ? body.detail : `request failed with status ${res.status}`;
  const fields = z.record(z.string(), z.string()).safeParse(body.errors ?? {});
  return new ApiError(res.status, code, detail, fields.success ? fields.data : {}, typeof body.requestId === "string" ? body.requestId : res.headers.get(REQUEST_ID_HEADER) ?? undefined);
}

export function newKey(): string {
  return crypto.randomUUID();
}

export async function requestUrl<T>(method: string, url: string, schema: z.ZodType<T>, fetchImpl: FetchLike, headers: Record<string, string> = {}, body?: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetchImpl(url, {
    method,
    headers: { Accept: "application/json", ...(body === undefined ? {} : { "Content-Type": "application/json" }), ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  if (!res.ok) throw await readProblem(res);
  return schema.parse(await res.json());
}

export function request<T>(method: string, path: string, schema: z.ZodType<T>, fetchImpl: FetchLike, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  return requestUrl(method, `${BASE}${path}`, schema, fetchImpl, headers, body);
}

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DECIMAL = /^\d+(\.\d{1,6})?$/;
export const SIGNED_DECIMAL = /^-?\d+(\.\d{1,6})?$/;

export function fieldErrors(error: unknown): Record<string, string> {
  return error instanceof ApiError ? error.fields : {};
}
