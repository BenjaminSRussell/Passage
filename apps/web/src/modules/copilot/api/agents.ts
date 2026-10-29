import { z } from "zod";
import { readProblem, type FetchLike, defaultFetch } from "../../../shell/api/http";

/**
 * Client for the agent runtime's kill switch (services/agents-ts/api/openapi.yaml,
 * GET and POST /api/v1/agents/kill-switch). The edge sets the caller's tenant,
 * user and duties; the browser never sends them.
 */
export const KILL_SWITCH_PATH = "/api/v1/agents/kill-switch";

const stateSchema = z.object({
  active: z.boolean(),
  version: z.number().int().positive().nullable(),
  changedAt: z.string().nullable(),
});

export const killSwitchStatusSchema = z.object({
  tenantId: z.string(),
  halted: z.boolean(),
  stale: z.boolean(),
  tenant: stateSchema.extend({ byUser: z.string().nullable() }),
  global: stateSchema,
});

export type KillSwitchStatus = z.infer<typeof killSwitchStatusSchema>;

export const killSwitchResultSchema = z.object({
  scope: z.enum(["GLOBAL", "TENANT"]),
  tenantId: z.string().nullable(),
  active: z.boolean(),
  version: z.number().int().positive(),
  changedAt: z.string(),
  byUser: z.string(),
});

export type KillSwitchResult = z.infer<typeof killSwitchResultSchema>;

export type KillSwitchCommand = { scope: "TENANT"; tenantId: string; active: boolean } | { scope: "GLOBAL"; active: boolean };

async function parse<T>(res: Response, schema: z.ZodType<T>): Promise<T> {
  if (!res.ok) throw await readProblem(res);
  const parsed = schema.safeParse(await res.json());
  if (!parsed.success) throw new Error("The agent runtime sent a response we couldn't read.");
  return parsed.data;
}

/** The caller's tenant switch and the global switch. */
export async function getKillSwitch(signal?: AbortSignal, fetchFn: FetchLike = defaultFetch): Promise<KillSwitchStatus> {
  const res = await fetchFn(KILL_SWITCH_PATH, { headers: { accept: "application/json" }, ...(signal ? { signal } : {}) });
  return parse(res, killSwitchStatusSchema);
}

/** Halts or resumes agent runs. Idempotent by scope on the server. */
export async function setKillSwitch(cmd: KillSwitchCommand, fetchFn: FetchLike = defaultFetch): Promise<KillSwitchResult> {
  const res = await fetchFn(KILL_SWITCH_PATH, {
    method: "POST",
    headers: { "content-type": "application/json", accept: "application/json" },
    body: JSON.stringify(cmd),
  });
  return parse(res, killSwitchResultSchema);
}
