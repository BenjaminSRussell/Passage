import { z } from "zod";

/** Base path of the planning service through the web BFF (see services/planning/api/openapi.yaml). */
export const PLANNING_API = "/api/v1/planning";

export const OrderType = z.enum(["purchase", "production", "transfer"]);
export const OrderStatus = z.enum(["unprocessed", "approved", "firmed"]);
export const MessageAction = z.enum(["delay", "postpone", "advance", "shortage"]);

export const PlannedOrder = z.object({
  id: z.string(),
  versionId: z.string(),
  number: z.string(),
  itemId: z.string(),
  orderType: OrderType,
  qty: z.string(),
  orderDate: z.string(),
  deliveryDate: z.string(),
  futuresDate: z.string().nullish(),
  delayDays: z.number().int(),
  lotRule: z.string(),
  status: OrderStatus,
});
export type PlannedOrder = z.infer<typeof PlannedOrder>;

export const Message = z.object({
  action: MessageAction,
  itemId: z.string(),
  supplyRef: z.string().nullish(),
  demandRef: z.string().nullish(),
  suggestedDate: z.string().nullish(),
  delayDays: z.number().int(),
  qty: z.string(),
});
export type Message = z.infer<typeof Message>;

/** Response of GET /planned-orders/{id}/pegging: the explain tree of one order. */
export interface PegNode {
  ref: string;
  refType: string;
  qty: string;
  children: PegNode[];
}
export const PegNode: z.ZodType<PegNode> = z.lazy(() =>
  z.object({
    ref: z.string(),
    refType: z.string(),
    qty: z.string(),
    children: z.array(PegNode),
  }),
);

export const Pegging = z.object({
  orderId: z.string(),
  number: z.string(),
  qty: z.string(),
  tree: PegNode,
});
export type Pegging = z.infer<typeof Pegging>;

export const PlanVersion = z.object({
  id: z.string(),
  planCode: z.string(),
  number: z.number().int(),
  status: z.enum(["running", "complete", "failed", "current"]),
  asOf: z.string(),
  itemsPlanned: z.number().int(),
  plannedOrders: z.number().int(),
  messages: z.number().int(),
  durationMs: z.number(),
  planHash: z.string(),
});
export type PlanVersion = z.infer<typeof PlanVersion>;

/** The engine snapshot a plan run takes (RunRequest in the OpenAPI). */
export interface RunPlanRequest {
  asOf: string;
  siteId: string;
  items: unknown[];
}

/** Error raised for any non-2xx response; `code` is the stable problem code (e.g. PLAN-0001). */
export class PlanningApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "PlanningApiError";
  }
}

async function request<T>(path: string, schema: z.ZodType<T>, init?: RequestInit): Promise<T> {
  const res = await fetch(`${PLANNING_API}${path}`, {
    credentials: "same-origin",
    ...init,
    headers: { Accept: "application/json", ...(init?.headers ?? {}) },
  });
  if (!res.ok) {
    let code = "PLAN-UNKNOWN";
    let detail = res.statusText;
    try {
      const problem = (await res.json()) as { code?: string; detail?: string };
      code = problem.code ?? code;
      detail = problem.detail ?? detail;
    } catch {
      // non-JSON error body: keep the defaults
    }
    throw new PlanningApiError(res.status, code, detail);
  }
  return schema.parse(await res.json());
}

export interface OrderQuery {
  planCode: string;
  status?: z.infer<typeof OrderStatus>;
  itemId?: string;
  limit?: number;
}

export async function listPlannedOrders(q: OrderQuery, init?: RequestInit): Promise<PlannedOrder[]> {
  const params = new URLSearchParams({ planCode: q.planCode });
  if (q.status) params.set("status", q.status);
  if (q.itemId) params.set("itemId", q.itemId);
  if (q.limit) params.set("limit", String(q.limit));
  const body = await request(
    `/planned-orders?${params.toString()}`,
    z.object({ items: z.array(PlannedOrder) }),
    init,
  );
  return body.items;
}

export async function listMessages(
  planCode: string,
  action?: z.infer<typeof MessageAction>,
  init?: RequestInit,
): Promise<Message[]> {
  const params = new URLSearchParams({ planCode });
  if (action) params.set("action", action);
  const body = await request(`/messages?${params.toString()}`, z.object({ items: z.array(Message) }), init);
  return body.items;
}

export async function getPlannedOrder(id: string, init?: RequestInit): Promise<PlannedOrder> {
  return request(`/planned-orders/${encodeURIComponent(id)}`, PlannedOrder, init);
}

export async function getPegging(orderId: string, depth = 20, init?: RequestInit): Promise<Pegging> {
  return request(`/planned-orders/${encodeURIComponent(orderId)}/pegging?depth=${depth}`, Pegging, init);
}

export async function getVersion(planCode: string, number: number): Promise<PlanVersion> {
  return request(`/plans/${encodeURIComponent(planCode)}/versions/${number}`, PlanVersion);
}

/** Plan run history, newest version first. */
export async function listVersions(planCode: string, limit = 50, init?: RequestInit): Promise<PlanVersion[]> {
  const body = await request(
    `/plans/${encodeURIComponent(planCode)}/versions?limit=${limit}`,
    z.object({ items: z.array(PlanVersion) }),
    init,
  );
  return body.items;
}

/** Requests a static plan run. The Idempotency-Key makes retries safe (409 when a run is already in progress). */
export async function runPlan(planCode: string, idempotencyKey: string, body: RunPlanRequest): Promise<PlanVersion> {
  return request(`/plans/${encodeURIComponent(planCode)}:run`, PlanVersion, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}
