import { z } from "zod";

// Client for the analytics service (services/analytics/api/openapi.yaml).
// Responses are validated with zod before the pages use them.

export const base = "/api/v1/analytics";

export const GRAINS = ["journal_line", "sales_line", "resource_shift"] as const;
export const RECON_STATUSES = ["OK", "FAILED", "BLOCKED"] as const;

/** Query limits from the service (PRD §9.46.5). */
export const MAX_MEASURES = 10;
export const MAX_DIMENSIONS = 8;
export const MAX_SPAN_YEARS = 5;

const metricSchema = z.object({
  metricId: z.string(),
  version: z.number().int(),
  name: z.string(),
  formula: z.string(),
  grain: z.enum(GRAINS),
  allowedDimensions: z.array(z.string()),
  reconciliationSource: z.string().optional(),
  nonFinancial: z.boolean(),
  status: z.enum(["DRAFT", "PUBLISHED", "DEPRECATED"]),
});

const metricsResponseSchema = z.object({ items: z.array(metricSchema) });

const filterSchema = z.object({ member: z.string(), operator: z.string(), values: z.array(z.string()) });

const planSchema = z.object({
  model: z.string(),
  grain: z.string(),
  measures: z.array(z.string()),
  dimensions: z.array(z.string()),
  from: z.string(),
  to: z.string(),
  filters: z.array(filterSchema),
  reason: z.literal("NO_SCOPE").optional(),
});

const queryResponseSchema = z.object({ plan: planSchema });

/** Amounts are decimal strings, never floats. */
const decimalString = z.string().regex(/^-?\d+(\.\d+)?$/);

const reconciliationSchema = z.object({
  metricId: z.string(),
  legalEntityId: z.string(),
  date: z.string(),
  currency: z.string(),
  semanticValue: decimalString,
  sourceValue: decimalString,
  delta: decimalString,
  tolerance: decimalString,
  status: z.enum(RECON_STATUSES),
  consecutiveOkDays: z.number().int(),
  trusted: z.boolean(),
});

const reconciliationResponseSchema = z.object({
  date: z.string(),
  failedCount: z.number().int(),
  items: z.array(reconciliationSchema),
});

export type Metric = z.infer<typeof metricSchema>;
export type Filter = z.infer<typeof filterSchema>;
export type Plan = z.infer<typeof planSchema>;
export type Reconciliation = z.infer<typeof reconciliationSchema>;
export type ReconciliationDay = z.infer<typeof reconciliationResponseSchema>;

export interface QueryInput {
  measures: string[];
  dimensions: string[];
  from: string;
  to: string;
}

/** A failed call. `code` is the stable Passage problem code (e.g. AUTH-0003). */
export class AnalyticsApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "AnalyticsApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export interface Problem {
  status: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof AnalyticsApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  if (error instanceof z.ZodError) {
    return { status: 0, code: "CONTRACT", message: "The analytics service sent a response this page does not understand.", fields: {} };
  }
  return { status: 0, code: "NETWORK", message: error instanceof Error ? error.message : "Request failed.", fields: {} };
}

async function toError(res: Response): Promise<AnalyticsApiError> {
  let code = `HTTP-${res.status}`;
  let detail = `Request failed with status ${res.status}.`;
  let fields: Record<string, string> = {};
  try {
    const body = (await res.json()) as { code?: unknown; detail?: unknown; errors?: unknown };
    if (typeof body.code === "string") code = body.code;
    if (typeof body.detail === "string") detail = body.detail;
    if (body.errors && typeof body.errors === "object" && !Array.isArray(body.errors)) {
      fields = Object.fromEntries(
        Object.entries(body.errors as Record<string, unknown>).filter((e): e is [string, string] => typeof e[1] === "string"),
      );
    }
  } catch {
    // Non-JSON error bodies keep the generic message.
  }
  return new AnalyticsApiError(res.status, code, detail, fields);
}

type Fetch = typeof fetch;
const defaultFetch: Fetch = (input, init) => fetch(input, init);

async function call(fetchImpl: Fetch, path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<unknown> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetchImpl(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return res.json();
}

/** Lists the published governed metrics. */
export async function listMetrics(fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<Metric[]> {
  return metricsResponseSchema.parse(await call(fetchImpl, "/metrics", {}, signal)).items;
}

/** Validates a query and returns the Cube plan with the caller's scope filters. */
export async function planQuery(input: QueryInput, fetchImpl: Fetch = defaultFetch, signal?: AbortSignal): Promise<Plan> {
  const body = JSON.stringify(input);
  return queryResponseSchema.parse(await call(fetchImpl, "/query", { method: "POST", body }, signal)).plan;
}

/** Reconciliation results for one day, optionally for one metric. */
export async function getReconciliation(
  date: string,
  metric = "",
  fetchImpl: Fetch = defaultFetch,
  signal?: AbortSignal,
): Promise<ReconciliationDay> {
  const q = new URLSearchParams({ date });
  if (metric !== "") q.set("metric", metric);
  return reconciliationResponseSchema.parse(await call(fetchImpl, `/reconciliation?${q.toString()}`, {}, signal));
}
