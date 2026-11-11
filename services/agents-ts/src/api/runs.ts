import type { IncomingHttpHeaders } from "node:http";
import type { RunService } from "../runtime/service.js";
import { ApiError, asObject, requireDuty, type Principal } from "./principal.js";

/** Duty to read runs and their audit rows (seeded in policies/authz/data.json). */
export const RUN_READ_DUTY = "agents.run.read";
export const MAX_PROMPT_CHARS = 8000;

/** The caller's bearer token, forwarded to the tool gateway so tools run as the user (AC-47.2). */
export function bearerToken(h: IncomingHttpHeaders): string {
  const raw = h["authorization"];
  const value = Array.isArray(raw) ? raw[0] : raw;
  const m = value === undefined ? null : /^Bearer ([A-Za-z0-9._~+/=-]+)$/.exec(value.trim());
  if (m === null || m[1] === undefined) throw new ApiError(401, "AGT-0401", "a bearer token is required");
  return m[1];
}

export function parseRunStart(raw: unknown): string {
  const prompt = asObject(raw, ["prompt"]).prompt;
  if (typeof prompt !== "string" || prompt.trim() === "" || prompt.length > MAX_PROMPT_CHARS) {
    throw new ApiError(400, "AGT-0400", `prompt is required, at most ${MAX_PROMPT_CHARS} characters`);
  }
  return prompt;
}

export async function handleStartRun(input: { principal: Principal; token: string; agentId: string; body: unknown; runs: RunService }) {
  return input.runs.start(input.principal, input.token, input.agentId, parseRunStart(input.body));
}

export async function handleGetRun(input: { principal: Principal; runId: string; runs: RunService }) {
  requireDuty(input.principal, RUN_READ_DUTY, "agent runs");
  return input.runs.view(input.principal, input.runId);
}

export async function handleListActions(input: { principal: Principal; runId: string; runs: RunService }) {
  requireDuty(input.principal, RUN_READ_DUTY, "agent runs");
  return { items: await input.runs.actions(input.principal, input.runId) };
}
