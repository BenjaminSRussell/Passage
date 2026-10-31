import { z } from "zod";

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

const defaultFetch: FetchLike = (input, init) => globalThis.fetch(input, init);

export const CURRENCY = /^[A-Z]{3}$/;
export const DECIMAL = /^-?\d+(\.\d+)?$/;
export const DATE = /^\d{4}-\d{2}-\d{2}$/;

export const rateTypeSchema = z.object({
  id: z.string(),
  code: z.string(),
  name: z.string(),
  allowInverse: z.boolean(),
  provider: z.string().optional(),
});
export type RateType = z.infer<typeof rateTypeSchema>;

export const historyEntrySchema = z.object({
  id: z.string(),
  version: z.number().int(),
  validFrom: z.string(),
  rate: z.string(),
  source: z.string(),
  providerTimestamp: z.string().optional(),
  payloadHash: z.string().optional(),
  supersededBy: z.string().optional(),
  createdAt: z.string(),
  createdBy: z.string().optional(),
});
export type HistoryEntry = z.infer<typeof historyEntrySchema>;

const hopSchema = z.object({ from: z.string(), to: z.string(), rate: z.string(), validFrom: z.string() });

export const resolutionSchema = z.object({
  rateType: z.string(),
  from: z.string(),
  to: z.string(),
  date: z.string(),
  path: z.enum(["direct", "inverse", "triangulated"]),
  rate: z.string(),
  hops: z.array(hopSchema),
});
export type Resolution = z.infer<typeof resolutionSchema>;

const amountSchema = z.object({ value: z.string(), currency: z.string() });

export const convertResponseSchema = z.object({
  rateType: z.string(),
  date: z.string(),
  to: z.string(),
  results: z.array(z.object({ amount: amountSchema, converted: amountSchema, path: z.string(), rate: z.string() })),
});
export type ConvertResponse = z.infer<typeof convertResponseSchema>;

export class FxApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;
  readonly retryAfter?: number;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}, retryAfter?: number) {
    super(message);
    this.name = "FxApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
    this.retryAfter = retryAfter;
  }
}

async function readProblem(res: Response): Promise<FxApiError> {
  let body: { code?: unknown; detail?: unknown; errors?: unknown } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    // A non-JSON error body falls through to the generic message below.
  }
  const code = typeof body.code === "string" ? body.code : "COM-0500";
  const detail = typeof body.detail === "string" ? body.detail : `request failed with status ${res.status}`;
  const fields = z.record(z.string(), z.string()).safeParse(body.errors ?? {});
  const retryAfter = Number.parseInt(res.headers.get("Retry-After") ?? "", 10);
  return new FxApiError(res.status, code, detail, fields.success ? fields.data : {}, Number.isFinite(retryAfter) ? retryAfter : undefined);
}

async function send<T>(url: string, init: RequestInit, schema: z.ZodType<T>, fetchImpl: FetchLike): Promise<T> {
  const res = await fetchImpl(url, init);
  if (!res.ok) throw await readProblem(res);
  return schema.parse(await res.json());
}

const GET = { headers: { Accept: "application/json" } };

export interface PairQuery {
  rateType: string;
  from: string;
  to: string;
}

export interface LookupQuery extends PairQuery {
  date?: string;
  via?: string;
}

export function fetchRateTypes(fetchImpl: FetchLike = defaultFetch): Promise<RateType[]> {
  return send("/api/v1/fx/rate-types", GET, z.array(rateTypeSchema), fetchImpl);
}

const historySchema = z.object({ entries: z.array(historyEntrySchema) });

export async function fetchHistory(q: PairQuery, fetchImpl: FetchLike = defaultFetch): Promise<HistoryEntry[]> {
  const params = new URLSearchParams({ type: q.rateType, from: q.from, to: q.to });
  const body = await send(`/api/v1/fx/rates/history?${params.toString()}`, GET, historySchema, fetchImpl);
  return body.entries;
}

export function lookupRate(q: LookupQuery, fetchImpl: FetchLike = defaultFetch): Promise<Resolution> {
  const params = new URLSearchParams({ type: q.rateType, from: q.from, to: q.to });
  if (q.date) params.set("date", q.date);
  if (q.via) params.set("via", q.via);
  return send(`/api/v1/fx/rates?${params.toString()}`, GET, resolutionSchema, fetchImpl);
}

export interface RateTypeInput {
  name: string;
  allowInverse: boolean;
  provider?: string;
}

export function createRateType(code: string, input: RateTypeInput, fetchImpl: FetchLike = defaultFetch): Promise<RateType> {
  return send(
    "/api/v1/fx/rate-types",
    { method: "POST", headers: { Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ code, ...input }) },
    rateTypeSchema,
    fetchImpl,
  );
}

export function putRateType(code: string, input: RateTypeInput, fetchImpl: FetchLike = defaultFetch): Promise<RateType> {
  return send(
    `/api/v1/fx/rate-types/${encodeURIComponent(code)}`,
    { method: "PUT", headers: { Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify(input) },
    rateTypeSchema,
    fetchImpl,
  );
}

export interface RateEntryInput extends PairQuery {
  validFrom: string;
  rate: string;
}

const publishResponseSchema = z.object({
  rates: z.array(z.object({ id: z.string(), version: z.number().int(), superseded: z.boolean(), unchanged: z.boolean() })),
});
export type PublishResponse = z.infer<typeof publishResponseSchema>;

export function publishRate(input: RateEntryInput, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<PublishResponse> {
  return send(
    "/api/v1/fx/rates",
    {
      method: "POST",
      headers: { Accept: "application/json", "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
      body: JSON.stringify({ rates: [input] }),
    },
    publishResponseSchema,
    fetchImpl,
  );
}

export interface ConvertInput {
  rateType: string;
  to: string;
  date?: string;
  via?: string;
  amounts: { value: string; currency: string }[];
}

export function convertAmounts(input: ConvertInput, fetchImpl: FetchLike = defaultFetch): Promise<ConvertResponse> {
  const body: Record<string, unknown> = { rateType: input.rateType, to: input.to, amounts: input.amounts };
  if (input.date) body.date = input.date;
  if (input.via) body.via = input.via;
  return send(
    "/api/v1/fx/convert",
    { method: "POST", headers: { Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify(body) },
    convertResponseSchema,
    fetchImpl,
  );
}

const importRunSchema = z.object({
  runId: z.string(),
  provider: z.string(),
  rateType: z.string(),
  date: z.string(),
  attempts: z.number().int(),
  imported: z.number().int(),
  unchanged: z.number().int(),
  alreadyDone: z.boolean(),
  degraded: z.boolean(),
  pending: z.boolean().optional(),
  lastSuccessAt: z.string().optional(),
});
export const IMPORT_PROVIDERS: readonly string[] = ["ecb", "openexchangerates"];
export type ImportRun = z.infer<typeof importRunSchema>;

export function importNow(provider: string, rateType: string, fetchImpl: FetchLike = defaultFetch): Promise<ImportRun> {
  return send(
    `/api/v1/fx/providers/${encodeURIComponent(provider)}:import`,
    { method: "POST", headers: { Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ rateType }) },
    importRunSchema,
    fetchImpl,
  );
}
