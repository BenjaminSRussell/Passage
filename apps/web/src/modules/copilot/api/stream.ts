import { z } from "zod";
import { chatResponseSchema, type ChatRequest, type ChatResponse } from "./client";

/**
 * Streaming client for POST /api/v1/copilot/chat when the server answers with
 * text/event-stream (FR-47.3). Frames: `token` ({text}) while the answer is
 * generated, then exactly one terminal frame: `done` (the full ChatResponse,
 * validated like the JSON path) or `error` ({code, message}). Unknown event
 * names are ignored so the contract can grow additively.
 */

export interface SseFrame {
  event: string;
  data: string;
  id?: string;
}

/** Splits complete frames out of a buffer. An unterminated tail is returned as `rest`. */
export function parseSseFrames(buffer: string): { events: SseFrame[]; rest: string } {
  const parts = buffer.replace(/\r\n?/g, "\n").split("\n\n");
  const rest = parts.pop() ?? "";
  const events: SseFrame[] = [];
  for (const frame of parts) {
    let event = "message";
    let id: string | undefined;
    const data: string[] = [];
    for (const line of frame.split("\n")) {
      if (line === "" || line.startsWith(":")) continue;
      const colon = line.indexOf(":");
      const field = colon === -1 ? line : line.slice(0, colon);
      let value = colon === -1 ? "" : line.slice(colon + 1);
      if (value.startsWith(" ")) value = value.slice(1);
      if (field === "event") event = value;
      else if (field === "data") data.push(value);
      else if (field === "id") id = value;
    }
    // Per the SSE specification a frame without data is never dispatched.
    if (data.length === 0) continue;
    const frameOut: SseFrame = { event, data: data.join("\n") };
    if (id !== undefined) frameOut.id = id;
    events.push(frameOut);
  }
  return { events, rest };
}

export const tokenSchema = z.object({ text: z.string() });
export const errorFrameSchema = z.object({ code: z.string(), message: z.string() });

export type StreamEvent =
  | { type: "token"; text: string }
  | { type: "done"; response: ChatResponse }
  | { type: "error"; code: string; message: string };

/** Maps one frame to a validated event, or undefined for an event name this client does not know. */
export function toStreamEvent(frame: SseFrame): StreamEvent | undefined {
  let json: unknown;
  switch (frame.event) {
    case "token":
      json = JSON.parse(frame.data) as unknown;
      return { type: "token", text: tokenSchema.parse(json).text };
    case "done":
      json = JSON.parse(frame.data) as unknown;
      return { type: "done", response: chatResponseSchema.parse(json) };
    case "error": {
      json = JSON.parse(frame.data) as unknown;
      const e = errorFrameSchema.parse(json);
      return { type: "error", code: e.code, message: e.message };
    }
    default:
      return undefined;
  }
}

/**
 * Yields token events as they arrive and ends after the terminal frame. Throws
 * when the connection closes before a terminal frame, so a truncated answer is
 * never shown as complete.
 */
export async function* streamChat(req: ChatRequest, fetchFn: typeof fetch = fetch): AsyncGenerator<StreamEvent> {
  const res = await fetchFn("/api/v1/copilot/chat", {
    method: "POST",
    headers: { "content-type": "application/json", accept: "text/event-stream" },
    body: JSON.stringify(req),
  });
  if (!res.ok) throw new Error(`Copilot is unavailable (HTTP ${res.status}).`);
  yield* readChatStream(res);
}

/** Reads the frames of an event-stream response; the caller has checked the status. */
export async function* readChatStream(res: Response): AsyncGenerator<StreamEvent> {
  if (!res.body) throw new Error("Copilot returned no stream.");
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      buffer += decoder.decode(chunk.value, { stream: true });
      const parsed = parseSseFrames(buffer);
      buffer = parsed.rest;
      for (const frame of parsed.events) {
        const event = toStreamEvent(frame);
        if (event === undefined) continue;
        yield event;
        if (event.type === "done" || event.type === "error") return;
      }
    }
    // Flush a final frame that the server closed without a blank line.
    buffer += decoder.decode();
    for (const frame of parseSseFrames(`${buffer}\n\n`).events) {
      const event = toStreamEvent(frame);
      if (event === undefined) continue;
      yield event;
      if (event.type === "done" || event.type === "error") return;
    }
    throw new Error("Copilot stream ended before the answer was complete.");
  } finally {
    await reader.cancel().catch(() => undefined);
  }
}

function isEventStream(res: Response): boolean {
  return (res.headers.get("content-type") ?? "").toLowerCase().startsWith("text/event-stream");
}

/**
 * One copilot turn, as the panel uses it. Offers both response types: when the
 * server answers with an event stream, each token is passed to `onToken` as it
 * arrives and the validated `done` frame is returned; when it answers with JSON,
 * the body is validated and returned. A terminal `error` frame throws with its
 * code, so the panel shows it like any other failure.
 */
export async function askCopilot(
  req: ChatRequest,
  onToken?: (text: string) => void,
  fetchFn: typeof fetch = fetch,
): Promise<ChatResponse> {
  const res = await fetchFn("/api/v1/copilot/chat", {
    method: "POST",
    headers: { "content-type": "application/json", accept: "text/event-stream, application/json;q=0.9" },
    body: JSON.stringify(req),
  });
  if (!res.ok) throw new Error(`Copilot is unavailable (HTTP ${res.status}).`);
  if (!isEventStream(res)) return chatResponseSchema.parse(await res.json());
  for await (const event of readChatStream(res)) {
    if (event.type === "token") onToken?.(event.text);
    else if (event.type === "done") return event.response;
    else throw new Error(`${event.code}: ${event.message}`);
  }
  // readChatStream throws before it can end without a terminal frame.
  throw new Error("Copilot stream ended before the answer was complete.");
}
