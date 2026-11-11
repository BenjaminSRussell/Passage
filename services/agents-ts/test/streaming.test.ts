import { afterEach, describe, expect, it } from "vitest";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { createAnthropicProvider } from "../src/providers/anthropic.js";
import { createFailoverProvider } from "../src/providers/failover.js";
import { sseEvents, StreamRestorer, TextFieldExtractor } from "../src/providers/stream.js";
import { providerUnavailable } from "../src/providers/wire.js";
import { createCopilotService } from "../src/runtime/copilot.js";
import type { ModelProvider, ModelRequest, ModelResponse } from "../src/runtime/engine.js";
import { REAL_IBAN } from "./fakes.js";
import { FakeMcp, headers, MemoryStore, serve, tool } from "./surface-fakes.js";

const GET = "passage.ap.invoice.get";
const DUTIES = "mcp.copilot.use,ap.invoice.read";

const bodyOf = (chunks: string[]): ReadableStream<Uint8Array> => {
  const enc = new TextEncoder();
  return new ReadableStream({
    start(c) {
      for (const chunk of chunks) c.enqueue(enc.encode(chunk));
      c.close();
    },
  });
};

const sse = (event: string, data: unknown): string => `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;

/** An Anthropic Messages stream whose proposal input arrives in the given pieces. */
function messageStream(pieces: string[], opts: { stop?: boolean; tokensIn?: number; tokensOut?: number } = {}): string[] {
  const out = [
    sse("message_start", { type: "message_start", message: { usage: { input_tokens: opts.tokensIn ?? 120, output_tokens: 1 } } }),
    sse("content_block_start", { type: "content_block_start", index: 0, content_block: { type: "tool_use", id: "t1", name: "propose_action", input: {} } }),
  ];
  for (const p of pieces) out.push(sse("content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "input_json_delta", partial_json: p } }));
  out.push(sse("content_block_stop", { type: "content_block_stop", index: 0 }));
  out.push(sse("message_delta", { type: "message_delta", delta: { stop_reason: "tool_use" }, usage: { output_tokens: opts.tokensOut ?? 33 } }));
  if (opts.stop !== false) out.push(sse("message_stop", { type: "message_stop" }));
  return out;
}

describe("sseEvents", () => {
  it("splits frames across chunk boundaries and skips frames without data", async () => {
    const events: Array<{ event: string; data: string }> = [];
    for await (const e of sseEvents(bodyOf([": ping\n\nevent: a\nda", "ta: 1\n\nevent: b\r\ndata: 2\r\ndata: 3\r\n\r\nevent: c\n"]))) events.push(e);
    // the unterminated tail "event: c" is not an event
    expect(events).toEqual([
      { event: "a", data: "1" },
      { event: "b", data: "2\n3" },
    ]);
  });
});

describe("TextFieldExtractor", () => {
  const run = (pieces: string[]): string => {
    const x = new TextFieldExtractor();
    return pieces.map((p) => x.push(p)).join("");
  };

  it("yields the text value as it is written, whatever the piece boundaries", () => {
    const json = JSON.stringify({ text: 'He said "hi"\nthen left é 😀', tool: "x", args: { text: "NOT THIS" }, confidence: 1 });
    expect(run([json])).toBe('He said "hi"\nthen left é 😀');
    for (let size = 1; size <= 7; size += 1) {
      const pieces: string[] = [];
      for (let i = 0; i < json.length; i += size) pieces.push(json.slice(i, i + size));
      expect(run(pieces)).toBe('He said "hi"\nthen left é 😀');
    }
  });

  it("finds text after other keys and ignores keys that only look like text", () => {
    expect(run(['{"args":{"text":"inner"},"te', 'xt":"outer"}'])).toBe("outer");
    expect(run(['{"note":"text","text":"yes"}'])).toBe("yes");
    expect(run(['{"tool":"a"}'])).toBe("");
  });
});

describe("StreamRestorer", () => {
  it("holds back an unfinished pseudonym until it is whole", () => {
    const r = new StreamRestorer((t) => t.replaceAll("[IBAN-1]", REAL_IBAN));
    expect([r.push("Pay to [IB"), r.push("AN-1] now"), r.push(" [open"), r.flush()]).toEqual(["Pay to ", `${REAL_IBAN} now`, " ", "[open"]);
  });
});

describe("Anthropic streaming adapter", () => {
  const provider = (chunks: string[], status = 200, seen: Array<Record<string, unknown>> = []): ModelProvider =>
    createAnthropicProvider({
      apiKey: "test-key-not-real",
      model: "claude-sonnet-5-5",
      fetchFn: (async (_url: unknown, init?: RequestInit) => {
        seen.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return new Response(status === 200 ? bodyOf(chunks) : "", { status });
      }) as typeof fetch,
    });
  const input = JSON.stringify({ text: "Invoice INV-7 is due.", tool: GET, args: { invoiceId: "INV-7" }, confidence: 0.9, amount: { amount: "0", currency: "USD" } });

  it("sends stream:true, calls onText with the answer before the stream ends and returns the parsed proposal with its usage", async () => {
    const seen: Array<Record<string, unknown>> = [];
    const texts: string[] = [];
    const res = await provider(messageStream([input.slice(0, 20), input.slice(20, 41), input.slice(41)]), 200, seen).stream!(
      { system: "s", user: "u", allowedTools: [GET] },
      (t) => texts.push(t),
    );
    expect(seen[0]).toMatchObject({ stream: true, model: "claude-sonnet-5-5", tool_choice: { type: "tool", name: "propose_action" } });
    expect(texts.join("")).toBe("Invoice INV-7 is due.");
    expect(texts.length).toBeGreaterThan(1);
    expect(res).toMatchObject({ text: "Invoice INV-7 is due.", proposal: { tool: GET, args: { invoiceId: "INV-7" } }, usage: { tokensIn: 120, tokensOut: 33 } });
  });

  it("treats a stream cut off before message_stop as an outage that keeps the budget hold, and an HTTP error as unbilled", async () => {
    await expect(provider(messageStream([input], { stop: false })).stream!({ system: "s", user: "u", allowedTools: [GET] }, () => undefined)).rejects.toMatchObject({
      code: "AGT-0206",
      outage: true,
      unbilled: false,
    });
    await expect(provider([], 503).stream!({ system: "s", user: "u", allowedTools: [GET] }, () => undefined)).rejects.toMatchObject({ outage: true, unbilled: true });
    await expect(provider([], 400).stream!({ system: "s", user: "u", allowedTools: [GET] }, () => undefined)).rejects.toMatchObject({ outage: false, unbilled: true });
  });

  it("does not stream a request that carries raw PII", async () => {
    await expect(provider(messageStream([input])).stream!({ system: "s", user: `pay ${REAL_IBAN}`, allowedTools: [GET] }, () => undefined)).rejects.toMatchObject({ code: "AGT-0210" });
  });
});

describe("failover with streaming", () => {
  const ok = (text: string): ModelResponse => ({ text, usage: { tokensIn: 1, tokensOut: 1 } });
  it("fails over a stream that broke before any text, but not one that already showed text", async () => {
    let now = 0;
    const secondary: ModelProvider = { complete: async () => ok("secondary") };
    const broken = (emit: boolean): ModelProvider => ({
      complete: async () => ok("x"),
      stream: async (_req, onText) => {
        if (emit) onText("partial");
        throw providerUnavailable("down");
      },
    });
    const make = (emit: boolean) =>
      createFailoverProvider({ primary: { name: "p", region: "r", provider: broken(emit) }, secondary: { name: "s", region: "r", provider: secondary }, residencyRegion: null, residencyAllows: () => true, outageWindowMs: 0, nowMs: () => now });
    const req: ModelRequest = { system: "s", user: "u", allowedTools: [GET] };
    const quiet = make(false);
    expect((await quiet.stream!(req, () => undefined)).text).toBe("secondary");
    now += 1;
    await expect(make(true).stream!(req, () => undefined)).rejects.toMatchObject({ code: "AGT-0206" });
  });
});

/** A fake streaming provider: the answer text goes out in pieces, the first before the call can finish. */
class StreamingProvider implements ModelProvider {
  readonly streamed: string[] = [];
  completeCalls = 0;
  release!: () => void;
  firstPieceSent!: () => void;
  readonly firstPiece = new Promise<void>((r) => (this.firstPieceSent = r));
  private readonly gate = new Promise<void>((r) => (this.release = r));
  constructor(
    private readonly pieces: string[],
    private readonly fail = false,
  ) {}
  async complete(): Promise<ModelResponse> {
    this.completeCalls += 1;
    return { text: this.pieces.join(""), usage: { tokensIn: 10, tokensOut: 5 } };
  }
  async stream(_req: ModelRequest, onText: (d: string) => void): Promise<ModelResponse> {
    onText(this.pieces[0] ?? "");
    this.streamed.push(this.pieces[0] ?? "");
    this.firstPieceSent();
    await this.gate;
    for (const p of this.pieces.slice(1)) {
      onText(p);
      this.streamed.push(p);
    }
    if (this.fail) throw providerUnavailable("connection dropped");
    return { text: this.pieces.join(""), usage: { tokensIn: 10, tokensOut: 5 } };
  }
}

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

async function setup(provider: ModelProvider) {
  const store = new MemoryStore();
  const copilot = createCopilotService({
    providerFor: () => provider,
    mcp: new FakeMcp([tool(GET, "ap.invoice.read")]),
    store,
    killSwitch: { isHalted: () => false },
    cache: new CopilotAnswerCache(),
    config: { tokenCap: 20_000, maxTurns: 3, pseudonymTtlMs: 60_000 },
    modelId: "claude-sonnet-5-5",
  });
  const srv = await serve({ approvals: store, copilot });
  close = srv.close;
  return srv;
}

const askStream = (base: string, message: string) =>
  fetch(`${base}/api/v1/copilot/chat`, { method: "POST", headers: { ...headers(DUTIES), accept: "text/event-stream" }, body: JSON.stringify({ mode: "ask", message }) });

const frames = (text: string): Array<{ event: string; data: { text?: string; answer?: string; code?: string } }> =>
  text
    .trim()
    .split("\n\n")
    .map((f) => {
      const [e, d] = f.split("\n");
      return { event: (e ?? "").slice(7), data: JSON.parse((d ?? "").slice(6)) as { text?: string } };
    });

describe("copilot token streaming (FR-47.3)", () => {
  it("sends the first token frame while the model is still writing, restores pseudonyms, and ends with the whole answer", async () => {
    const provider = new StreamingProvider(["Pay to [IB", "AN-1] ", "by Friday."]);
    const srv = await setup(provider);
    const res = await askStream(srv.base, `Who gets paid on ${REAL_IBAN}?`);
    expect(res.headers.get("content-type")).toContain("text/event-stream");
    const reader = res.body!.getReader();
    const dec = new TextDecoder();
    // The first piece is on the wire before the provider is allowed to finish: first-token latency is not the whole answer's.
    await provider.firstPiece;
    let got = "";
    while (!got.includes("event: token")) {
      const { done, value } = await reader.read();
      if (done) break;
      got += dec.decode(value);
    }
    expect(got).toContain('"text":"Pay to "');
    expect(got).not.toContain("event: done");
    provider.release();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      got += dec.decode(value);
    }
    const all = frames(got);
    const tokens = all.filter((f) => f.event === "token").map((f) => f.data.text);
    expect(tokens.join("")).toBe(`Pay to ${REAL_IBAN} by Friday.`);
    expect(tokens.length).toBeGreaterThan(1);
    expect(all.at(-1)).toMatchObject({ event: "done", data: { answer: `Pay to ${REAL_IBAN} by Friday.`, refusal: null, generated: true } });
    expect(provider.completeCalls).toBe(0);
  });

  it("answers a failure after the first token with an error frame, not a broken stream", async () => {
    const provider = new StreamingProvider(["Part", "ial"], true);
    const srv = await setup(provider);
    provider.release();
    const res = await askStream(srv.base, "hi");
    expect(res.status).toBe(200);
    const all = frames(await res.text());
    expect(all.at(-1)).toMatchObject({ event: "error", data: { code: "AGT-0206" } });
    expect(all.some((f) => f.event === "done")).toBe(false);
  });

  it("falls back to frames cut from the whole answer when the provider cannot stream", async () => {
    const plain: ModelProvider = { complete: async () => ({ text: "A whole answer that is cut into frames afterwards.", usage: { tokensIn: 5, tokensOut: 5 } }) };
    const srv = await setup(plain);
    const all = frames(await (await askStream(srv.base, "hi")).text());
    expect(all.filter((f) => f.event === "token").map((f) => f.data.text).join("")).toBe("A whole answer that is cut into frames afterwards.");
    expect(all.at(-1)?.event).toBe("done");
  });

  it("still answers a refusal before the model runs as an ordinary problem response", async () => {
    const srv = await setup(new StreamingProvider(["x"]));
    const res = await fetch(`${srv.base}/api/v1/copilot/chat`, { method: "POST", headers: { ...headers(DUTIES), accept: "text/event-stream" }, body: JSON.stringify({ mode: "nope", message: "hi" }) });
    expect(res.status).toBe(400);
    expect(res.headers.get("content-type")).not.toContain("text/event-stream");
  });
});
