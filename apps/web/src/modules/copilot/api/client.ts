import { z } from "zod";
import type { CopilotMode } from "../domain";

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

export const chatResponseSchema = z.object({
  answer: z.string(),
  citations: z.array(citationSchema),
  toolCalls: z.array(toolCallSchema),
  refusal: z.object({ code: z.string(), message: z.string() }).nullable().optional(),
  generated: z.literal(true),
});

export type ChatResponse = z.infer<typeof chatResponseSchema>;

export interface ChatRequest {
  mode: CopilotMode;
  message: string;
  recordRef?: string;
}

export async function postChat(req: ChatRequest, fetchFn: typeof fetch = fetch): Promise<ChatResponse> {
  const res = await fetchFn("/api/v1/copilot/chat", {
    method: "POST",
    headers: { "content-type": "application/json", accept: "application/json" },
    body: JSON.stringify(req),
  });
  if (!res.ok) throw new Error(`Copilot is unavailable (HTTP ${res.status}).`);
  return chatResponseSchema.parse(await res.json());
}
