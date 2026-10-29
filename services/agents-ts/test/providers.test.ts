import { describe, expect, it } from "vitest";
import { createAnthropicProvider } from "../src/providers/anthropic.js";
import { createOpenAICompatibleProvider } from "../src/providers/openai.js";
import type { ModelProvider } from "../src/runtime/engine.js";
import { runAgent } from "../src/runtime/engine.js";
import { APPROVER, FakeApprovals, FakeTools, makeDeps, makeRequest, postDescriptor, REAL_IBAN, TENANT } from "./fakes.js";

const ALLOWED = ["passage.ap.invoice.post"];
const ARGS = { remitTo: "[IBAN-1]", invoiceId: "INV-7" };
const AMOUNT = { amount: "1200.00", currency: "USD" };

interface Seen {
  url: string;
  init: RequestInit;
}

/** A fetch stand-in that records the request and answers with a fixed status and body. */
function fakeFetch(status: number, body: unknown, seen: Seen[]): typeof fetch {
  const fn = async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    seen.push({ url: String(input), init: init ?? {} });
    const text = typeof body === "string" ? body : JSON.stringify(body);
    return new Response(text, { status, headers: { "content-type": "application/json" } });
  };
  return fn as unknown as typeof fetch;
}

/** The same model answer, expressed in each provider's wire format. */
const anthropicAnswer = {
  content: [
    { type: "text", text: "ignored preamble" },
    {
      type: "tool_use",
      id: "toolu_1",
      name: "propose_action",
      input: { text: "Coded to the posting account.", tool: "passage.ap.invoice.post", args: ARGS, confidence: 0.8, amount: AMOUNT },
    },
  ],
  usage: { input_tokens: 120, output_tokens: 45 },
};

const openAiAnswer = {
  choices: [
    {
      message: {
        content: null,
        tool_calls: [
          {
            id: "call_1",
            type: "function",
            function: {
              name: "propose_action",
              arguments: JSON.stringify({ text: "Coded to the posting account.", tool: "passage.ap.invoice.post", args: ARGS, confidence: 0.8, amount: AMOUNT }),
            },
          },
        ],
      },
    },
  ],
  usage: { prompt_tokens: 120, completion_tokens: 45 },
};

const expected = {
  text: "Coded to the posting account.",
  proposal: { tool: "passage.ap.invoice.post", args: ARGS, confidence: 0.8, amount: AMOUNT },
  usage: { tokensIn: 120, tokensOut: 45 },
};

function providers(seenA: Seen[], seenO: Seen[]): Array<{ name: string; provider: ModelProvider; seen: Seen[] }> {
  return [
    {
      name: "anthropic",
      seen: seenA,
      provider: createAnthropicProvider({ apiKey: "test-key-not-real", model: "claude-test", fetchFn: fakeFetch(200, anthropicAnswer, seenA) }),
    },
    {
      name: "openai-compatible",
      seen: seenO,
      provider: createOpenAICompatibleProvider({
        baseUrl: "https://llm.example.test/v1",
        model: "open-weights-test",
        apiKey: "test-key-not-real",
        fetchFn: fakeFetch(200, openAiAnswer, seenO),
      }),
    },
  ];
}

describe("provider parity (FR-47.13, AC-47.10)", () => {
  it("every adapter returns the same ModelResponse for the same answer", async () => {
    const seenA: Seen[] = [];
    const seenO: Seen[] = [];
    for (const { provider } of providers(seenA, seenO)) {
      const res = await provider.complete({ system: "You code invoices.", user: "Code invoice from [IBAN-1].", allowedTools: ALLOWED });
      expect(res).toEqual(expected);
    }
  });

  it("every adapter forces the same proposal tool and the same allow-list enum", async () => {
    const seenA: Seen[] = [];
    const seenO: Seen[] = [];
    for (const { provider } of providers(seenA, seenO)) {
      await provider.complete({ system: "s", user: "u", allowedTools: ALLOWED });
    }
    const a = JSON.parse(String(seenA[0]?.init.body)) as Record<string, unknown>;
    const o = JSON.parse(String(seenO[0]?.init.body)) as Record<string, unknown>;
    expect(a.tool_choice).toEqual({ type: "tool", name: "propose_action" });
    expect(o.tool_choice).toEqual({ type: "function", function: { name: "propose_action" } });
    const aSchema = (a.tools as Array<{ input_schema: { properties: { tool: { enum: string[] } } } }>)[0]?.input_schema;
    const oSchema = (o.tools as Array<{ function: { parameters: { properties: { tool: { enum: string[] } } } } }>)[0]?.function.parameters;
    expect(aSchema?.properties.tool.enum).toEqual(ALLOWED);
    expect(oSchema?.properties.tool.enum).toEqual(ALLOWED);
  });

  it("authenticates each adapter with its own header and sends no tenant id", async () => {
    const seenA: Seen[] = [];
    const seenO: Seen[] = [];
    for (const { provider } of providers(seenA, seenO)) {
      await provider.complete({ system: "s", user: "u", allowedTools: ALLOWED });
    }
    // The schema text tells the model never to send tenant_id; no tenant value or field may travel.
    const aHeaders = seenA[0]?.init.headers as Record<string, string>;
    expect(aHeaders["x-api-key"]).toBe("test-key-not-real");
    expect(aHeaders["anthropic-version"]).toBe("2023-06-01");
    const oHeaders = seenO[0]?.init.headers as Record<string, string>;
    expect(oHeaders.authorization).toBe("Bearer test-key-not-real");
    for (const body of [String(seenA[0]?.init.body), String(seenO[0]?.init.body)]) {
      expect(body).not.toContain(TENANT);
      expect(body).not.toMatch(/"tenant_?id"\s*:/i);
    }
  });

  it("Azure OpenAI is the same adapter with an api-key header and api-version query", async () => {
    const seen: Seen[] = [];
    const azure = createOpenAICompatibleProvider({
      baseUrl: "https://res.openai.azure.com/openai/deployments/code-dep",
      model: "gpt-test",
      apiKey: "azure-key-not-real",
      auth: "api-key",
      query: { "api-version": "2024-06-01" },
      fetchFn: fakeFetch(200, openAiAnswer, seen),
    });
    const res = await azure.complete({ system: "s", user: "u", allowedTools: ALLOWED });
    expect(res).toEqual(expected);
    expect(seen[0]?.url).toBe("https://res.openai.azure.com/openai/deployments/code-dep/chat/completions?api-version=2024-06-01");
    expect((seen[0]?.init.headers as Record<string, string>)["api-key"]).toBe("azure-key-not-real");
  });
});

describe("provider failure handling (BR-47.9)", () => {
  it.each([
    [500, true],
    [503, true],
    [429, false],
    [400, false],
    [401, false],
  ])("HTTP %i is AGT-0206 with outage=%s, and the key never appears in the message", async (status, outage) => {
    const seen: Seen[] = [];
    const p = createAnthropicProvider({ apiKey: "secret-key-value", model: "m", fetchFn: fakeFetch(status, { error: "x" }, seen) });
    const err = await p.complete({ system: "s", user: "u", allowedTools: ALLOWED }).catch((e: unknown) => e);
    expect(err).toMatchObject({ code: "AGT-0206", outage });
    expect(String((err as Error).message)).not.toContain("secret-key-value");
    expect(seen).toHaveLength(1);
  });

  it("a transport error or timeout is an outage", async () => {
    const failing = (async () => {
      throw new TypeError("fetch failed");
    }) as unknown as typeof fetch;
    const p = createOpenAICompatibleProvider({ baseUrl: "https://llm.example.test/v1", model: "m", apiKey: "k", fetchFn: failing });
    await expect(p.complete({ system: "s", user: "u", allowedTools: ALLOWED })).rejects.toMatchObject({ code: "AGT-0206", outage: true });
  });

  it("an explanation without a proposal is a valid answer (the model may refuse)", async () => {
    const body = { content: [{ type: "tool_use", name: "propose_action", input: { text: "I cannot post without approval." } }] };
    const p = createAnthropicProvider({ apiKey: "k", model: "m", fetchFn: fakeFetch(200, body, []) });
    await expect(p.complete({ system: "s", user: "u", allowedTools: ALLOWED })).resolves.toEqual({ text: "I cannot post without approval." });
  });

  it("rejects a proposal whose args are not an object", async () => {
    const body = {
      content: [{ type: "tool_use", name: "propose_action", input: { text: "t", tool: "passage.ap.invoice.post", args: "INV-7", confidence: 0.9, amount: AMOUNT } }],
    };
    const p = createAnthropicProvider({ apiKey: "k", model: "m", fetchFn: fakeFetch(200, body, []) });
    await expect(p.complete({ system: "s", user: "u", allowedTools: ALLOWED })).rejects.toMatchObject({ code: "AGT-0206", outage: false });
  });

  it("refuses a prompt that still carries an IBAN before any request leaves (FR-47.12)", async () => {
    const seen: Seen[] = [];
    const p = createAnthropicProvider({ apiKey: "k", model: "m", fetchFn: fakeFetch(200, anthropicAnswer, seen) });
    await expect(p.complete({ system: "s", user: `pay to ${REAL_IBAN}`, allowedTools: ALLOWED })).rejects.toThrow();
    expect(seen).toHaveLength(0);
  });

  it("rejects a proposal naming a tool outside the allow-list", async () => {
    const bad = {
      content: [{ type: "tool_use", name: "propose_action", input: { text: "t", tool: "passage.ledger.journal.reverse", args: {}, confidence: 0.9, amount: AMOUNT } }],
    };
    const p = createAnthropicProvider({ apiKey: "k", model: "m", fetchFn: fakeFetch(200, bad, []) });
    await expect(p.complete({ system: "s", user: "u", allowedTools: ALLOWED })).rejects.toMatchObject({ code: "AGT-0206" });
  });

  it("rejects a non-decimal amount or confidence outside 0..1", async () => {
    const badAmount = {
      content: [{ type: "tool_use", name: "propose_action", input: { text: "t", tool: "passage.ap.invoice.post", args: {}, confidence: 0.9, amount: { amount: "12,00", currency: "USD" } } }],
    };
    const badConf = {
      content: [{ type: "tool_use", name: "propose_action", input: { text: "t", tool: "passage.ap.invoice.post", args: {}, confidence: 1.5, amount: AMOUNT } }],
    };
    for (const body of [badAmount, badConf]) {
      const p = createAnthropicProvider({ apiKey: "k", model: "m", fetchFn: fakeFetch(200, body, []) });
      await expect(p.complete({ system: "s", user: "u", allowedTools: ALLOWED })).rejects.toMatchObject({ code: "AGT-0206" });
    }
  });

  it("a non-JSON body is AGT-0206 (not an outage), not a crash", async () => {
    const p = createOpenAICompatibleProvider({ baseUrl: "https://llm.example.test/v1", model: "m", apiKey: "k", fetchFn: fakeFetch(200, "<html>", []) });
    await expect(p.complete({ system: "s", user: "u", allowedTools: ALLOWED })).rejects.toMatchObject({ code: "AGT-0206" });
  });
});

describe("runAgent through each adapter", () => {
  it("both providers drive the same suggest-mode run to COMMITTED after approval", async () => {
    const seenA: Seen[] = [];
    const seenO: Seen[] = [];
    for (const { name, provider } of providers(seenA, seenO)) {
      const tools = new FakeTools({ "passage.ap.invoice.post": postDescriptor });
      const approvals = new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER });
      const result = await runAgent(makeDeps({ provider, tools, approvals }), makeRequest());
      expect(result.status, name).toBe("COMMITTED");
      expect(tools.calls, name).toHaveLength(1);
      expect(result.actions[0]?.decision, name).toBe("APPROVED");
    }
  });

  it("a provider outage ends the run FAILED with AGT-0206 and calls no tool", async () => {
    const tools = new FakeTools({ "passage.ap.invoice.post": postDescriptor });
    const provider = createAnthropicProvider({ apiKey: "k", model: "m", fetchFn: fakeFetch(503, {}, []) });
    const result = await runAgent(makeDeps({ provider, tools, approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }), makeRequest());
    expect(result).toMatchObject({ status: "FAILED", code: "AGT-0206" });
    expect(tools.calls).toHaveLength(0);
  });
});
