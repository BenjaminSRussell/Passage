import type { ServerResponse } from "node:http";
import { COPILOT_DUTY, parseChatRequest, type ChatResponse, type CopilotService } from "../runtime/copilot.js";
import { requireDuty, type Principal } from "./principal.js";

/** Splits an answer into frames of whole words, at most `size` characters each. */
export function tokenFrames(answer: string, size = 48): string[] {
  const out: string[] = [];
  let cur = "";
  for (const word of answer.split(/(?<=\s)/)) {
    if (cur !== "" && cur.length + word.length > size) {
      out.push(cur);
      cur = "";
    }
    cur += word;
  }
  if (cur !== "") out.push(cur);
  return out;
}

const frame = (event: string, data: unknown): string => `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;

/**
 * POST /api/v1/copilot/chat. The answer is complete before the first byte is sent, so every refusal
 * and failure is an ordinary problem response; a client that asks for `text/event-stream` then gets
 * `token` frames and one `done` frame (the panel's contract), and any other client the JSON body.
 * The provider layer does not stream yet, so frames are cut from the finished answer.
 */
export async function handleChat(input: {
  principal: Principal;
  token: string;
  body: unknown;
  accept: string;
  copilot: CopilotService;
  res: ServerResponse;
}): Promise<void> {
  requireDuty(input.principal, COPILOT_DUTY, "the copilot");
  const reply: ChatResponse = await input.copilot.chat(input.principal, input.token, parseChatRequest(input.body));
  const wantsStream = input.accept.toLowerCase().includes("text/event-stream");
  if (!wantsStream) {
    const payload = JSON.stringify(reply);
    input.res.writeHead(200, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store", "content-length": Buffer.byteLength(payload).toString() });
    input.res.end(payload);
    return;
  }
  input.res.writeHead(200, { "content-type": "text/event-stream; charset=utf-8", "cache-control": "no-store", connection: "keep-alive", "x-accel-buffering": "no" });
  for (const text of tokenFrames(reply.answer)) input.res.write(frame("token", { text }));
  input.res.end(frame("done", reply));
}
