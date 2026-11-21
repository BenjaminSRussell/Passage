import { z } from "zod";
import type { CopilotMode } from "../domain";
import { apiFetch } from "../../../shell/api/http";
import { AI_REQUEST_TIMEOUT_MS, BUDGET_REFUSAL_CODE, withDeadline } from "../../../shell/assistant/availability";

export const COPILOT_TIMEOUT_MESSAGE = "Copilot did not answer in time.";

export class CopilotError extends Error {
  readonly code: string | undefined;
  readonly status: number | undefined;
  constructor(message: string, code?: string, status?: number) {
    super(message);
    this.name = "CopilotError";
    this.code = code;
    this.status = status;
  }
}

const AI_UNAVAILABLE_STATUS = 503;
const AI_UNAVAILABLE_CODES: readonly string[] = ["AGT-0201", BUDGET_REFUSAL_CODE];

export function isAiUnavailable(failure: unknown): boolean {
  if (failure instanceof CopilotError) {
    return failure.message === COPILOT_TIMEOUT_MESSAGE || failure.status === AI_UNAVAILABLE_STATUS || (failure.code !== undefined && AI_UNAVAILABLE_CODES.includes(failure.code));
  }
  return failure instanceof TypeError;
}

const problemSchema = z.object({ code: z.string() });

export async function failedCopilotCall(res: Response): Promise<CopilotError> {
  let code: string | undefined;
  try {
    code = problemSchema.parse(await res.json()).code;
  } catch {
    code = undefined;
  }
  return new CopilotError(`Copilot is unavailable (HTTP ${res.status}).`, code, res.status);
}

/** Response of POST /api/v1/copilot/chat. Validated before any field is rendered. */
export const citationSchema = z.object({
  sourceType: z.enum(["RECORD", "METRIC", "ARTICLE", "DOC", "POLICY"]),
  sourceRef: z.string(),
  label: z.string(),
});

export const toolCallSchema = z.object({
  tool: z.string(),
  status: z.enum(["OK", "DENIED", "ERROR"]),
  code: z.string().optional(),
});

const excludedSchema = z.object({ reason: z.string(), count: z.number() });

export const chatResponseSchema = z.object({
  answer: z.string(),
  citations: z.array(citationSchema),
  toolCalls: z.array(toolCallSchema),
  refusal: z.object({ code: z.string(), message: z.string() }).nullable().optional(),
  dataClass: z.string().optional(),
  excluded: z.union([z.array(excludedSchema), excludedSchema]).optional(),
  generated: z.literal(true),
});

export type ChatResponse = z.infer<typeof chatResponseSchema>;

export interface ChatRequest {
  mode: CopilotMode;
  message: string;
  recordRef?: string;
}

const routeKindSchema = z.enum(["ask", "navigate", "act"]);

export const routeResponseSchema = z.union([
  z.object({
    kind: z.enum(["ask", "navigate", "act", "clarify"]),
    confidence: z.number().min(0).max(1),
    candidates: z.tuple([routeKindSchema, routeKindSchema]).optional(),
  }),
  z.object({ refusal: z.object({ code: z.string(), message: z.string() }) }),
]);

export type RouteResponse = z.infer<typeof routeResponseSchema>;

export async function postRoute(message: string, fetchFn: typeof fetch = apiFetch): Promise<RouteResponse> {
  const deadline = withDeadline(AI_REQUEST_TIMEOUT_MS);
  try {
    const res = await fetchFn("/api/v1/copilot/chat", {
      method: "POST",
      cache: "no-store",
      headers: { "content-type": "application/json", accept: "application/json" },
      body: JSON.stringify({ mode: "route", message }),
      signal: deadline.signal,
    });
    if (!res.ok) throw await failedCopilotCall(res);
    return routeResponseSchema.parse(await res.json());
  } catch (err) {
    throw deadline.expired() ? new CopilotError(COPILOT_TIMEOUT_MESSAGE) : err;
  } finally {
    deadline.clear();
  }
}

const taskValues = z.record(z.string(), z.union([z.boolean(), z.number(), z.string()]));

export const taskResponseSchema = z.union([
  z.strictObject({
    navigationIntent: z.strictObject({
      routeKey: z.string().min(1),
      path: z.string().regex(/^\/(?!\/)[^?#:\\]*$/),
      params: taskValues,
      draft: taskValues,
      summary: z.string(),
    }),
    generated: z.literal(true),
  }),
  z.strictObject({ clarifyingQuestion: z.string().min(1), candidates: z.array(z.string()).optional(), generated: z.literal(true) }),
  z.strictObject({ refusal: z.object({ code: z.string(), message: z.string() }), generated: z.literal(true) }),
]);

export type TaskResponse = z.infer<typeof taskResponseSchema>;

export async function postTask(message: string, fetchFn: typeof fetch = apiFetch): Promise<TaskResponse> {
  const deadline = withDeadline(AI_REQUEST_TIMEOUT_MS);
  try {
    const res = await fetchFn("/api/v1/copilot/chat", {
      method: "POST",
      cache: "no-store",
      headers: { "content-type": "application/json", accept: "application/json" },
      body: JSON.stringify({ mode: "task", message }),
      signal: deadline.signal,
    });
    if (!res.ok) throw await failedCopilotCall(res);
    return taskResponseSchema.parse(await res.json());
  } catch (err) {
    throw deadline.expired() ? new CopilotError(COPILOT_TIMEOUT_MESSAGE) : err;
  } finally {
    deadline.clear();
  }
}

const CONVERSATIONS_PATH = "/api/v1/agents/copilot/conversations";

export const conversationSchema = z.object({ conversationId: z.string().uuid() });

export async function createConversation(fetchFn: typeof fetch = apiFetch): Promise<string> {
  const deadline = withDeadline(AI_REQUEST_TIMEOUT_MS);
  try {
    const res = await fetchFn(CONVERSATIONS_PATH, {
      method: "POST",
      cache: "no-store",
      headers: { accept: "application/json" },
      signal: deadline.signal,
    });
    if (!res.ok) throw await failedCopilotCall(res);
    return conversationSchema.parse(await res.json()).conversationId;
  } catch (err) {
    throw deadline.expired() ? new CopilotError(COPILOT_TIMEOUT_MESSAGE) : err;
  } finally {
    deadline.clear();
  }
}

export function messagesPath(conversationId: string): string {
  return `${CONVERSATIONS_PATH}/${conversationSchema.shape.conversationId.parse(conversationId)}/messages`;
}

export async function postChat(req: ChatRequest, fetchFn: typeof fetch = apiFetch, conversationId?: string): Promise<ChatResponse> {
  const path = messagesPath(conversationId ?? (await createConversation(fetchFn)));
  const deadline = withDeadline(AI_REQUEST_TIMEOUT_MS);
  try {
    const res = await fetchFn(path, {
      method: "POST",
      cache: "no-store",
      headers: { "content-type": "application/json", accept: "application/json" },
      body: JSON.stringify(req),
      signal: deadline.signal,
    });
    if (!res.ok) throw await failedCopilotCall(res);
    return chatResponseSchema.parse(await res.json());
  } catch (err) {
    throw deadline.expired() ? new CopilotError(COPILOT_TIMEOUT_MESSAGE) : err;
  } finally {
    deadline.clear();
  }
}
