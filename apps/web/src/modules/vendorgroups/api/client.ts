import { z } from "zod";
import { apiFetch } from "../../../shell/api/http";

export const base = "/api/v1/party";

export const vendorGroupSchema = z.object({
  id: z.string(),
  code: z.string(),
  name: z.string(),
  version: z.number().int(),
});
export type VendorGroup = z.infer<typeof vendorGroupSchema>;

const vendorGroupsSchema = z.object({ vendorGroups: z.array(vendorGroupSchema) });

export interface NewVendorGroup {
  /** Registers the code for a group id vendors already carry; omitted, the service assigns a new id. */
  id?: string;
  code: string;
  name: string;
}

export class PartyApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "PartyApiError";
  }
}

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

const defaultFetch: FetchLike = (input, init) => apiFetch(input, init);

async function problem(res: Response): Promise<PartyApiError> {
  let body: { code?: unknown; detail?: unknown; errors?: unknown } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    // A non-JSON error body falls through to the generic message.
  }
  const fields = z.record(z.string(), z.string()).safeParse(body.errors ?? {});
  return new PartyApiError(
    res.status,
    typeof body.code === "string" ? body.code : "COM-0500",
    typeof body.detail === "string" ? body.detail : `request failed with status ${res.status}`,
    fields.success ? fields.data : {},
  );
}

/** GET /vendor-groups: the tenant's vendor groups ordered by code. */
export async function listVendorGroups(fetchImpl: FetchLike = defaultFetch): Promise<VendorGroup[]> {
  const res = await fetchImpl(`${base}/vendor-groups`, { headers: { Accept: "application/json" } });
  if (!res.ok) throw await problem(res);
  return vendorGroupsSchema.parse(await res.json()).vendorGroups;
}

/** POST /vendor-groups. One Idempotency-Key per intended group so a retry replays the original. */
export async function createVendorGroup(input: NewVendorGroup, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<VendorGroup> {
  const res = await fetchImpl(`${base}/vendor-groups`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw await problem(res);
  return vendorGroupSchema.parse(await res.json());
}
