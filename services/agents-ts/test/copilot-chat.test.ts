import { afterEach, describe, expect, it } from "vitest";
import { tokenFrames } from "../src/api/copilot.js";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { createCopilotService } from "../src/runtime/copilot.js";
import type { ModelResponse } from "../src/runtime/engine.js";
import { REAL_IBAN, ScriptedProvider } from "./fakes.js";
import { tokenFor } from "./jwt.js";
import { FakeMcp, headers, MemoryStore, serve, T1, tool, USER } from "./surface-fakes.js";

const GET = "passage.ap.invoice.get";
const LIST = "passage.ap.vendor.list";
const SEARCH = "passage.knowledge.search";
const DUTIES = "mcp.copilot.use,ap.invoice.read,ap.vendor.read";
const answer = (text: string, usage = { tokensIn: 100, tokensOut: 20 }): ModelResponse => ({ text, usage });
const call = (toolName: string, args: Record<string, unknown> = {}): ModelResponse => ({
  text: "",
  proposal: { tool: toolName, args, confidence: 1, amount: { amount: "0", currency: "USD" } },
  usage: { tokensIn: 200, tokensOut: 10 },
});

const setup = async (script: ModelResponse[], mcp = new FakeMcp([tool(GET, "ap.invoice.read"), tool(LIST, "ap.vendor.read")], { [GET]: { ok: true, output: { id: "INV-7", total: "1200.00" }, ref: "INV-7" } }), killed = false) => {
  const store = new MemoryStore();
  const provider = new ScriptedProvider(script);
  const cache = new CopilotAnswerCache();
  const copilot = createCopilotService({
    providerFor: () => provider,
    mcp,
    store,
    killSwitch: { isHalted: () => killed },
    cache,
    config: { tokenCap: 20_000, maxTurns: 3, pseudonymTtlMs: 60_000 },
    modelId: "claude-haiku-5-5",
  });
  const srv = await serve({ approvals: store, copilot });
  return { store, provider, mcp, srv };
};

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

const chat = (base: string, body: unknown, duties = DUTIES, accept = "application/json") =>
  fetch(`${base}/api/v1/copilot/chat`, { method: "POST", headers: { ...headers(duties), accept }, body: JSON.stringify(body) });

describe("POST /api/v1/copilot/chat", () => {
  it("calls a read tool as the user, feeds the result back, cites the record, audits one run and settles the tokens", async () => {
    const s = await setup([call(GET, { invoiceId: "INV-7" }), answer("Invoice INV-7 totals 1200.00.")]);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "explain", message: "Explain INV-7", recordRef: "ap/invoice/INV-7" });
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({
      answer: "Invoice INV-7 totals 1200.00.",
      citations: [{ sourceType: "RECORD", sourceRef: "INV-7", label: GET }],
      toolCalls: [{ tool: GET, status: "OK" }],
      refusal: null,
      generated: true,
    });
    expect(s.mcp.connects[0]).toMatchObject({ userId: USER, token: tokenFor(T1, USER, DUTIES) });
    expect(s.mcp.calls).toEqual([{ name: GET, args: { invoiceId: "INV-7" } }]);
    // The second prompt carries the tool result; only read tools were offered.
    expect(s.provider.seen[1]?.user).toContain("Result of passage.ap.invoice.get");
    expect(s.provider.seen[0]?.allowedTools).toEqual([GET, LIST]);
    const [run] = [...s.store.runs.values()];
    expect(run).toMatchObject({ agentId: "copilot", status: "COMMITTED", tokensIn: 300, tokensOut: 30 });
    expect(s.store.actionRows.get(run?.runId ?? "")).toEqual([expect.objectContaining({ toolName: GET, decision: "AUTO", reason: "read" })]);
    expect(s.store.reserved).toEqual([expect.objectContaining({ agentId: "copilot", tokensIn: 20_000 })]);
    expect(s.store.settled).toEqual([[300, 30]]);
  });

  it("sends the provider and the audit row only redacted text and restores real values for the user", async () => {
    const s = await setup([call(GET, { invoiceId: "INV-7", iban: REAL_IBAN }), answer("Pay to [IBAN-1].")]);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: `What is open for IBAN ${REAL_IBAN}?` });
    const body = (await res.json()) as { answer: string };
    expect(body.answer).toBe(`Pay to ${REAL_IBAN}.`);
    for (const seen of s.provider.seen) expect(seen.user).not.toContain(REAL_IBAN);
    expect(JSON.stringify([...s.store.actionRows.values()])).not.toContain(REAL_IBAN);
    // The tool itself is called with the real value (the user's own request).
    expect(s.mcp.calls[0]?.args).toEqual({ invoiceId: "INV-7", iban: REAL_IBAN });
  });

  it("refuses a tool outside the caller's allowed set with AGT-0203 and never calls it", async () => {
    const s = await setup([call("passage.ap.invoice.post", { invoiceId: "INV-7" })]);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: "post it" });
    const body = (await res.json()) as { refusal: { code: string; message: string }; toolCalls: unknown[] };
    expect(body.refusal.code).toBe("AGT-0203");
    expect(body.toolCalls).toEqual([{ tool: "passage.ap.invoice.post", status: "DENIED", code: "AGT-0203" }]);
    expect(s.mcp.calls).toHaveLength(0);
    const [run] = [...s.store.runs.values()];
    expect(s.store.actionRows.get(run?.runId ?? "")?.[0]).toMatchObject({ decision: "DENIED" });
  });

  it("offers a user only the tools whose duty they hold and refuses before any provider call when none remain", async () => {
    const s = await setup([answer("unused")], new FakeMcp([tool(GET, "ap.invoice.read")]));
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: "hi" }, "mcp.copilot.use");
    const body = (await res.json()) as { refusal: { code: string; message: string } };
    expect(body.refusal.code).toBe("AGT-0203");
    expect(body.refusal.message).toBe("I cannot do that: you do not hold duty ap.invoice.read required by passage.ap.invoice.get.");
    expect(s.provider.seen).toHaveLength(0);
    expect(s.store.runs.size).toBe(0);
  });

  it("answers a repeated question from the cache for the same user only", async () => {
    const s = await setup([answer("Once."), answer("Twice.")]);
    close = s.srv.close;
    const ask = () => chat(s.srv.base, { mode: "ask", message: "same question" }).then((r) => r.json() as Promise<{ answer: string }>);
    expect((await ask()).answer).toBe("Once.");
    expect((await ask()).answer).toBe("Once.");
    expect(s.provider.seen).toHaveLength(1);
    const other = await fetch(`${s.srv.base}/api/v1/copilot/chat`, {
      method: "POST",
      headers: headers(DUTIES, "00000000-0000-7000-8000-0000000000c9"),
      body: JSON.stringify({ mode: "ask", message: "same question" }),
    });
    expect(((await other.json()) as { answer: string }).answer).toBe("Twice.");
  });

  it("applies the citation gate to knowledge answers (AGT-0212) and cites grounded chunks", async () => {
    const chunk = (similarity: number) => ({ chunkId: "c1", sourceType: "POLICY", sourceRef: "policy/travel", section: "Meals", fusedScore: 0.5, similarity });
    const mcp = (similarity: number) => new FakeMcp([tool(SEARCH, "knowledge.read")], { [SEARCH]: { ok: true, output: { chunks: [chunk(similarity)] } } });
    const weak = await setup([call(SEARCH, { q: "meals" }), answer("Meals are covered.")], mcp(0.4));
    const refused = (await (await chat(weak.srv.base, { mode: "ask", message: "meal policy?" }, "mcp.copilot.use,knowledge.read")).json()) as { refusal: { code: string } | null; citations: unknown[] };
    await weak.srv.close();
    expect(refused.refusal?.code).toBe("AGT-0212");
    expect(refused.citations).toEqual([]);

    const strong = await setup([call(SEARCH, { q: "meals" }), answer("Meals are covered.")], mcp(0.9));
    close = strong.srv.close;
    const ok = (await (await chat(strong.srv.base, { mode: "ask", message: "meal policy?" }, "mcp.copilot.use,knowledge.read")).json()) as { refusal: unknown; citations: unknown[] };
    expect(ok.refusal).toBeNull();
    expect(ok.citations).toEqual([{ sourceType: "POLICY", sourceRef: "policy/travel", label: "Meals" }]);
  });

  it("streams token frames then one done frame for an event-stream client, and the same body as JSON otherwise", async () => {
    const text = "Invoice INV-7 totals 1200.00 and is due on 30 November, so it is not yet late.";
    const s = await setup([answer(text)]);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: "stream" }, DUTIES, "text/event-stream, application/json;q=0.9");
    expect(res.headers.get("content-type")).toContain("text/event-stream");
    const raw = await res.text();
    const frames = raw.split("\n\n").filter((f) => f !== "");
    expect(frames.at(-1)).toMatch(/^event: done\ndata: /);
    const tokens = frames.filter((f) => f.startsWith("event: token")).map((f) => (JSON.parse(f.split("data: ")[1] ?? "{}") as { text: string }).text);
    expect(tokens.length).toBeGreaterThan(1);
    expect(tokens.join("")).toBe(text);
    const done = JSON.parse((frames.at(-1) ?? "").split("data: ")[1] ?? "{}") as { answer: string; generated: boolean };
    expect(done).toMatchObject({ answer: text, generated: true });
  });

  it("rejects a bad body, a missing duty, and a kill switch before any provider call", async () => {
    const s = await setup([answer("x")]);
    close = s.srv.close;
    expect((await chat(s.srv.base, { mode: "nope", message: "x" })).status).toBe(400);
    expect((await chat(s.srv.base, { mode: "ask", message: "" })).status).toBe(400);
    expect((await chat(s.srv.base, { mode: "ask", message: "x", extra: 1 })).status).toBe(400);
    const noDuty = await chat(s.srv.base, { mode: "ask", message: "x" }, "ap.invoice.read");
    expect(noDuty.status).toBe(403);
    expect(s.provider.seen).toHaveLength(0);
    const killed = await setup([answer("x")], undefined, true);
    expect(((await (await chat(killed.srv.base, { mode: "ask", message: "x" })).json()) as { code: string }).code).toBe("AGT-0201");
    await killed.srv.close();
  });

  it("marks the run FAILED when the provider fails and answers a 500 that shows nothing", async () => {
    const s = await setup([]);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: "x" });
    expect(res.status).toBe(500);
    const [run] = [...s.store.runs.values()];
    expect(run?.status).toBe("FAILED");
  });
});

describe("tokenFrames", () => {
  it("cuts on word boundaries and loses nothing", () => {
    const text = "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron";
    const frames = tokenFrames(text, 20);
    expect(frames.join("")).toBe(text);
    expect(frames.every((f) => f.length <= 20)).toBe(true);
    expect(tokenFrames("")).toEqual([]);
  });
});
