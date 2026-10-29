import { describe, expect, it } from "vitest";
import { askCopilot, parseSseFrames, streamChat, toStreamEvent, type StreamEvent } from "./stream";

const donePayload = {
  answer: "Revenue rose 4.2%.",
  citations: [{ sourceType: "METRIC", sourceRef: "revenue", label: "Revenue, Sep vs Aug" }],
  toolCalls: [{ tool: "passage.ledger.statement.get", status: "OK" }],
  refusal: null,
  generated: true,
};

function frames(...items: Array<[string, unknown]>): string {
  return items.map(([event, data]) => `event: ${event}\ndata: ${typeof data === "string" ? data : JSON.stringify(data)}\n\n`).join("");
}

/** A Response-like object whose body yields the given byte chunks in order. */
function streamingFetch(chunks: Uint8Array[], status = 200, contentType = "text/event-stream"): typeof fetch {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const c of chunks) controller.enqueue(c);
      controller.close();
    },
  });
  const fn = async (): Promise<Response> => new Response(body, { status, headers: { "content-type": contentType } });
  return fn as unknown as typeof fetch;
}

async function collect(gen: AsyncGenerator<StreamEvent>): Promise<StreamEvent[]> {
  const out: StreamEvent[] = [];
  for await (const e of gen) out.push(e);
  return out;
}

describe("parseSseFrames", () => {
  it("joins multi-line data, skips comments and frames without data, and keeps the unterminated tail", () => {
    const text = ": keep-alive\n\nevent: token\ndata: one\ndata: two\n\nid: 9\nevent: token\n\nevent: token\r\ndata: three\r\n\r\nevent: token\ndata: pa";
    const { events, rest } = parseSseFrames(text);
    expect(events).toEqual([
      { event: "token", data: "one\ntwo" },
      { event: "token", data: "three" },
    ]);
    expect(rest).toBe("event: token\ndata: pa");
  });
});

describe("toStreamEvent", () => {
  it("validates done frames with the same schema as the JSON path", () => {
    const ev = toStreamEvent({ event: "done", data: JSON.stringify(donePayload) });
    expect(ev).toMatchObject({ type: "done", response: { answer: "Revenue rose 4.2%.", generated: true } });
  });

  it("rejects a done frame without the generated label (AI labelling, FR-47.12)", () => {
    expect(() => toStreamEvent({ event: "done", data: JSON.stringify({ ...donePayload, generated: false }) })).toThrow();
  });

  it("ignores event names it does not know, so the contract can grow", () => {
    expect(toStreamEvent({ event: "progress", data: "{}" })).toBeUndefined();
  });
});

describe("streamChat", () => {
  it("yields tokens then the validated final answer, even when bytes split inside frames", async () => {
    const text = frames(["token", { text: "Revenue " }], ["token", { text: "rose." }], ["done", donePayload]);
    const bytes = new TextEncoder().encode(text);
    // One byte per chunk: every frame boundary and multi-byte split is exercised.
    const chunks = Array.from(bytes, (b) => new Uint8Array([b]));
    const events = await collect(streamChat({ mode: "ask", message: "revenue?" }, streamingFetch(chunks)));
    expect(events.map((e) => e.type)).toEqual(["token", "token", "done"]);
    expect(events[0]).toEqual({ type: "token", text: "Revenue " });
  });

  it("yields a terminal error frame and stops", async () => {
    const text = frames(["token", { text: "partial" }], ["error", { code: "AGT-0206", message: "Provider unavailable." }], ["token", { text: "never" }]);
    const events = await collect(streamChat({ mode: "explain", message: "why", recordRef: "INV-1" }, streamingFetch([new TextEncoder().encode(text)])));
    expect(events.at(-1)).toEqual({ type: "error", code: "AGT-0206", message: "Provider unavailable." });
    expect(events).toHaveLength(2);
  });

  it("throws when the stream closes before a terminal frame, so a truncated answer is never complete", async () => {
    const text = frames(["token", { text: "half an answer" }]);
    await expect(collect(streamChat({ mode: "summarize", message: "summarize" }, streamingFetch([new TextEncoder().encode(text)])))).rejects.toThrow(
      "ended before the answer was complete",
    );
  });

  it("reports a non-OK status without reading a body", async () => {
    await expect(collect(streamChat({ mode: "ask", message: "x" }, streamingFetch([], 503)))).rejects.toThrow("HTTP 503");
  });
});

describe("askCopilot (the panel's client)", () => {
  const encode = (text: string) => [new TextEncoder().encode(text)];

  it("streams tokens to the callback and returns the validated final answer", async () => {
    const tokens: string[] = [];
    const text = frames(["token", { text: "Revenue " }], ["token", { text: "rose." }], ["done", donePayload]);
    const res = await askCopilot({ mode: "ask", message: "revenue?" }, (t) => tokens.push(t), streamingFetch(encode(text), 200, "text/event-stream; charset=utf-8"));
    expect(tokens).toEqual(["Revenue ", "rose."]);
    expect(res.answer).toBe("Revenue rose 4.2%.");
  });

  it("reads a JSON answer when the server does not stream", async () => {
    const onToken = (): void => {
      throw new Error("no tokens on the JSON path");
    };
    const res = await askCopilot({ mode: "ask", message: "x" }, onToken, streamingFetch(encode(JSON.stringify(donePayload)), 200, "application/json"));
    expect(res).toMatchObject({ answer: "Revenue rose 4.2%.", generated: true });
  });

  it("asks for either response type", async () => {
    let accept = "";
    const fetchFn = (async (_url: string, init?: RequestInit) => {
      accept = String((init?.headers as Record<string, string>).accept);
      return new Response(JSON.stringify(donePayload), { status: 200, headers: { "content-type": "application/json" } });
    }) as unknown as typeof fetch;
    await askCopilot({ mode: "ask", message: "x" }, undefined, fetchFn);
    expect(accept).toContain("text/event-stream");
    expect(accept).toContain("application/json");
  });

  it("throws the code and message of a terminal error frame", async () => {
    const text = frames(["token", { text: "partial" }], ["error", { code: "AGT-0202", message: "Monthly token budget exhausted." }]);
    await expect(askCopilot({ mode: "ask", message: "x" }, undefined, streamingFetch(encode(text)))).rejects.toThrow(
      "AGT-0202: Monthly token budget exhausted.",
    );
  });

  it("rejects a JSON answer without the generated label", async () => {
    const body = JSON.stringify({ ...donePayload, generated: false });
    await expect(askCopilot({ mode: "ask", message: "x" }, undefined, streamingFetch(encode(body), 200, "application/json"))).rejects.toThrow();
  });
});
