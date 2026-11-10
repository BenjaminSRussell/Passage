import { z } from "zod";

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export const defaultFetch: FetchLike = (input, init) => globalThis.fetch(input, init);

export const BASE = "/api/v1/receiving";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export async function readProblem(res: Response): Promise<ApiError> {
  let body: { code?: unknown; detail?: unknown; errors?: unknown } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    // Non-JSON error bodies fall through to the generic error below.
  }
  const code = typeof body.code === "string" ? body.code : "COM-0500";
  const detail = typeof body.detail === "string" ? body.detail : `request failed with status ${res.status}`;
  const fields = z.record(z.string(), z.string()).safeParse(body.errors ?? {});
  return new ApiError(res.status, code, detail, fields.success ? fields.data : {});
}

export function newKey(): string {
  return crypto.randomUUID();
}

export async function request<T>(method: string, path: string, schema: z.ZodType<T>, fetchImpl: FetchLike, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetchImpl(`${BASE}${path}`, {
    method,
    headers: { Accept: "application/json", ...(body === undefined ? {} : { "Content-Type": "application/json" }), ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await readProblem(res);
  return schema.parse(await res.json());
}

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DECIMAL = /^\d+(\.\d{1,6})?$/;
export const SIGNED_DECIMAL = /^-?\d+(\.\d{1,6})?$/;

export function fieldErrors(error: unknown): Record<string, string> {
  return error instanceof ApiError ? error.fields : {};
}
