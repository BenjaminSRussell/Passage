// Flow designer API client. Paths follow the mobile service OpenAPI (wave 32, §6.32.5)
// as exposed to the browser through the BFF at /api/v1/mobile.
import type { FlowDraft } from "../domain/flowGraph";

const base = "/api/v1/mobile";

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

/** Creates or updates a DRAFT flow. Requires WMS_MOBILE_ADMIN. */
export async function saveDraft(flow: FlowDraft, idempotencyKey: string): Promise<SavedFlow> {
  const res = await fetch(`${base}/flows`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(flow),
  });
  if (!res.ok) throw await failure(res);
  return (await res.json()) as SavedFlow;
}

/** Validates on the server and publishes. Devices pick the version up on the next menu load. */
export async function publishFlow(flowId: string, idempotencyKey: string, ifMatch: string): Promise<SavedFlow> {
  const res = await fetch(`${base}/flows/${encodeURIComponent(flowId)}:publish`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey, "If-Match": ifMatch },
  });
  if (!res.ok) throw await failure(res);
  return (await res.json()) as SavedFlow;
}

/** Text for a failed flow call; the mobile service may not be deployed yet (404). */
export function describeFlowError(error: unknown): string {
  if (error instanceof FlowApiError) {
    if (error.status === 404 && !error.code) return "The mobile flow service is not available yet. The draft was not saved.";
    return error.code ? `${error.message} (${error.code})` : error.message;
  }
  return error instanceof Error ? error.message : "Request failed";
}
