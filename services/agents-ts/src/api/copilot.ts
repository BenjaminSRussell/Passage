import type { ServerResponse } from "node:http";
import { COPILOT_DUTY, parseChatRequest, type ChatResponse, type CopilotService } from "../runtime/copilot.js";
import { AgentError } from "../errors.js";
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
 * POST /api/v1/copilot/chat. A client that asks for `text/event-stream` gets `token` frames as the model writes the
 * answer, then one `done` frame (the panel's contract); any other client gets the JSON body when the answer is whole.
 * Everything that can refuse before the model runs (the duty, the body, the kill switch, an empty tool view) is an
 * ordinary problem response because no byte has been sent yet. A failure after the first token is an `error` frame.
 * An answer that was not streamed (served from the cache, or a provider that cannot stream) is cut into token frames
 * once it is whole, so the stream always carries the answer before `done`.
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
  const request = parseChatRequest(input.body);
  const wantsStream = input.accept.toLowerCase().includes("text/event-stream");
  const res = input.res;
  if (!wantsStream) {
    const reply: ChatResponse = await input.copilot.chat(input.principal, input.token, request);
    const payload = JSON.stringify(reply);
    res.writeHead(200, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store", "content-length": Buffer.byteLength(payload).toString() });
    res.end(payload);
    return;
  }
  // The model call stops when the client goes away: nobody is left to read the answer and the tokens would still be paid for.
  const gone = new AbortController();
  res.on("close", () => {
    if (!res.writableEnded) gone.abort();
  });
  let open = false;
  let streamed = false;
  const begin = (): void => {
    if (open) return;
    open = true;
    res.writeHead(200, { "content-type": "text/event-stream; charset=utf-8", "cache-control": "no-store", connection: "keep-alive", "x-accel-buffering": "no" });
  };
  try {
    const reply = await input.copilot.chat(input.principal, input.token, request, {
      signal: gone.signal,
      onToken: (text) => {
        begin();
        streamed = true;
        res.write(frame("token", { text }));
      },
    });
    begin();
    if (!streamed) for (const text of tokenFrames(reply.answer)) res.write(frame("token", { text }));
    res.end(frame("done", reply));
  } catch (err) {
    if (!open) throw err;
    const code = err instanceof AgentError ? err.code : "AGT-0206";
    // Only a message this service wrote is shown; anything else (a socket error, a bug) stays in the logs.
    res.end(frame("error", { code, message: err instanceof AgentError ? err.message : "The answer could not be completed." }));
  }
}
