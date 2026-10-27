/** Browser client for the warehouse service, through the web BFF at /api/v1/warehouse (service routes /v1/warehouse/...). */
export const base = "/api/v1/warehouse";

export type Direction = "PUT" | "PICK";

/** The work line to place (BR-31.1). Quantities are decimal strings, never floats. */
export interface SimulateLine {
  itemId: string;
  workType: string;
  direction: Direction;
  warehouseId: string;
  qty: string;
  weight?: string;
  volume?: string;
  lps?: number;
  attributes?: Record<string, string>;
}

export type HazmatRel = "allowed" | "separate" | "prohibited";

/** Hazmat reference data for storage compatibility (FR-31.19, BR-31.15). Limits are decimal strings in kg. */
export interface Compliance {
  hazmatProfiles?: { itemId: string; storageClass: string; unNumber?: string; shippingName?: string; class?: string }[];
  segregation?: { a: string; b: string; rel: HazmatRel }[];
  sectionLimitsKg?: Record<string, string>;
}

/** Directives and locations travel as the service's JSON shapes (see services/warehouse/api/openapi.yaml). */
export interface SimulateRequest {
  line: SimulateLine;
  directives: unknown[];
  locations: unknown[];
  compliance?: Compliance;
}

export interface TraceStep {
  directive: string;
  band?: number;
  action?: number;
  outcome: "hit" | "miss" | "skip";
  reason: string;
}

/** Result of a directive simulation. A blocked line is an answer, not an error (WMS-0101). */
export interface SimulateResponse {
  blocked: boolean;
  code?: string;
  locationId?: string;
  location?: string;
  directive?: string;
  trace: TraceStep[];
}

/** Problem details (RFC 9457) with the stable service code and field errors. */
export class WarehouseApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "WarehouseApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof WarehouseApiError) {
    return { code: error.code, message: error.message, fields: error.fields };
  }
  return { code: "NETWORK", message: error instanceof Error ? error.message : "The request failed.", fields: {} };
}

async function toError(res: Response): Promise<WarehouseApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new WarehouseApiError(res.status, code, message, fields);
}

/** POST /v1/warehouse/location-directives:simulate (duty warehouse.setup.view). Nothing is saved. */
export async function simulateDirectives(request: SimulateRequest, signal?: AbortSignal): Promise<SimulateResponse> {
  const res = await fetch(`${base}/location-directives:simulate`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(request),
    signal,
  });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as SimulateResponse;
}
