import { describe, expect, it } from "vitest";
import { createBedrockProvider, signSigV4 } from "../src/providers/bedrock.js";
import { createAnthropicProvider } from "../src/providers/anthropic.js";
import { ProviderError } from "../src/providers/wire.js";
import { REAL_IBAN } from "./fakes.js";

const ALLOWED = ["passage.ap.invoice.post"];
const ARGS = { remitTo: "[IBAN-1]", invoiceId: "INV-7" };
const AMOUNT = { amount: "1200.00", currency: "USD" };
const FIXED = new Date("2026-03-04T05:06:07.000Z");
const CREDS = { accessKeyId: "AKIDTESTNOTREAL", secretAccessKey: "secret-test-not-real" };

interface Seen {
  url: string;
  init: RequestInit;
}

function fakeFetch(status: number, body: unknown, seen: Seen[]): typeof fetch {
  const fn = async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    seen.push({ url: String(input), init: init ?? {} });
    const text = typeof body === "string" ? body : JSON.stringify(body);
    return new Response(text, { status, headers: { "content-type": "application/json" } });
  };
  return fn as unknown as typeof fetch;
}

/** The same proposal as the Anthropic and OpenAI fixtures, in Converse wire format. */
const converseAnswer = {
  output: {
    message: {
      role: "assistant",
      content: [
        { text: "ignored preamble" },
        {
          toolUse: {
            toolUseId: "tooluse_1",
            name: "propose_action",
            input: { text: "Coded to the posting account.", tool: "passage.ap.invoice.post", args: ARGS, confidence: 0.8, amount: AMOUNT },
          },
        },
      ],
    },
  },
  stopReason: "tool_use",
  usage: { inputTokens: 120, outputTokens: 45, totalTokens: 165 },
};

const expected = {
  text: "Coded to the posting account.",
  proposal: { tool: "passage.ap.invoice.post", args: ARGS, confidence: 0.8, amount: AMOUNT },
  usage: { tokensIn: 120, tokensOut: 45 },
};

describe("Bedrock adapter (FR-47.13)", () => {
  it("returns the same ModelResponse as the other adapters for the same answer", async () => {
    const seen: Seen[] = [];
    const bedrock = createBedrockProvider({
      credentials: CREDS,
      region: "eu-central-1",
      modelId: "anthropic.claude-test-v1:0",
      fetchFn: fakeFetch(200, converseAnswer, seen),
      now: () => FIXED,
    });
    const res = await bedrock.complete({ system: "You code invoices.", user: "Code invoice.", allowedTools: ALLOWED });
    expect(res).toEqual(expected);

    const anthropic = createAnthropicProvider({
      apiKey: "k",
      model: "m",
      fetchFn: fakeFetch(
        200,
        {
          content: [{ type: "tool_use", name: "propose_action", input: { text: "Coded to the posting account.", tool: "passage.ap.invoice.post", args: ARGS, confidence: 0.8, amount: AMOUNT } }],
          usage: { input_tokens: 120, output_tokens: 45 },
        },
        [],
      ),
    });
    expect(await anthropic.complete({ system: "s", user: "u", allowedTools: ALLOWED })).toEqual(res);
  });

  it("forces the proposal tool and sends the allow-list enum in Converse toolConfig", async () => {
    const seen: Seen[] = [];
    const bedrock = createBedrockProvider({ credentials: CREDS, region: "eu-central-1", modelId: "m", fetchFn: fakeFetch(200, converseAnswer, seen), now: () => FIXED });
    await bedrock.complete({ system: "s", user: "u", allowedTools: ALLOWED });
    const body = JSON.parse(String(seen[0]?.init.body)) as {
      toolConfig: { toolChoice: unknown; tools: Array<{ toolSpec: { inputSchema: { json: { properties: { tool: { enum: string[] } } } } } }> };
    };
    expect(body.toolConfig.toolChoice).toEqual({ tool: { name: "propose_action" } });
    expect(body.toolConfig.tools[0]?.toolSpec.inputSchema.json.properties.tool.enum).toEqual(ALLOWED);
  });

  it("maps a provider outage to AGT-0206 and never leaks the secret", async () => {
    const bedrock = createBedrockProvider({
      credentials: CREDS,
      region: "eu-central-1",
      modelId: "m",
      fetchFn: fakeFetch(503, {}, []),
      now: () => FIXED,
    });
    const err = await bedrock.complete({ system: "s", user: "u", allowedTools: ALLOWED }).catch((e: unknown) => e);
    expect(err).toMatchObject({ code: "AGT-0206" });
    expect(String((err as Error).message)).not.toContain(CREDS.secretAccessKey);
  });

  it("treats an answer without the proposal tool as a rejection, which never starts the failover clock", async () => {
    const bedrock = createBedrockProvider({
      credentials: CREDS,
      region: "eu-central-1",
      modelId: "m",
      fetchFn: fakeFetch(200, { output: { message: { role: "assistant", content: [{ text: "no tool" }] } } }, []),
      now: () => FIXED,
    });
    const err = await bedrock.complete({ system: "s", user: "u", allowedTools: ALLOWED }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ProviderError);
    expect(err).toMatchObject({ code: "AGT-0206", outage: false });
  });

  it("refuses a prompt that still carries an IBAN before any request leaves", async () => {
    const seen: Seen[] = [];
    const bedrock = createBedrockProvider({ credentials: CREDS, region: "eu-central-1", modelId: "m", fetchFn: fakeFetch(200, converseAnswer, seen) });
    await expect(bedrock.complete({ system: "s", user: `pay ${REAL_IBAN}`, allowedTools: ALLOWED })).rejects.toThrow();
    expect(seen).toHaveLength(0);
  });
});

describe("Bedrock request signing (SigV4)", () => {
  it("posts to the regional runtime host with the model id encoded once on the wire", async () => {
    const seen: Seen[] = [];
    const bedrock = createBedrockProvider({
      credentials: CREDS,
      region: "eu-central-1",
      modelId: "anthropic.claude-test-v1:0",
      fetchFn: fakeFetch(200, converseAnswer, seen),
      now: () => FIXED,
    });
    await bedrock.complete({ system: "s", user: "u", allowedTools: ALLOWED });
    expect(seen[0]?.url).toBe("https://bedrock-runtime.eu-central-1.amazonaws.com/model/anthropic.claude-test-v1%3A0/converse");
  });

  it("sends an authorization header scoped to bedrock in the region, with a fixed date", async () => {
    const seen: Seen[] = [];
    const bedrock = createBedrockProvider({ credentials: CREDS, region: "eu-central-1", modelId: "m", fetchFn: fakeFetch(200, converseAnswer, seen), now: () => FIXED });
    await bedrock.complete({ system: "s", user: "u", allowedTools: ALLOWED });
    const headers = seen[0]?.init.headers as Record<string, string>;
    expect(headers["x-amz-date"]).toBe("20260304T050607Z");
    expect(headers.authorization).toMatch(
      /^AWS4-HMAC-SHA256 Credential=AKIDTESTNOTREAL\/20260304\/eu-central-1\/bedrock\/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=[0-9a-f]{64}$/,
    );
    expect(JSON.stringify(headers)).not.toContain(CREDS.secretAccessKey);
  });

  // Known-answer vectors computed with botocore's SigV4Auth (the AWS SDK
  // reference signer) for the same request, credentials and date.
  const VECTOR = {
    method: "POST",
    host: "bedrock-runtime.eu-central-1.amazonaws.com",
    canonicalPath: "/model/anthropic.claude-test-v1%253A0/converse",
    body: '{"a":1}',
    region: "eu-central-1",
    service: "bedrock",
    date: FIXED,
  };

  it("matches the AWS SDK reference signature", () => {
    expect(signSigV4({ ...VECTOR, credentials: CREDS }).authorization).toBe(
      "AWS4-HMAC-SHA256 Credential=AKIDTESTNOTREAL/20260304/eu-central-1/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=c4b50a48ee373a3d4122d150653a23ed016e06b5dfdaca0572ae7981ce76e36b",
    );
  });

  it("matches the AWS SDK reference signature with a session token", () => {
    const out = signSigV4({ ...VECTOR, credentials: { ...CREDS, sessionToken: "session-test-not-real" } });
    expect(out.authorization).toMatch(/Signature=ed96d191ebe1931f39b3d414bc481a935d0498f1c7b1c2e002894944503f30d2$/);
  });

  it("signs the double-encoded model id the adapter sends", async () => {
    const seen: Seen[] = [];
    const bedrock = createBedrockProvider({
      credentials: CREDS,
      region: "eu-central-1",
      modelId: "anthropic.claude-test-v1:0",
      fetchFn: fakeFetch(200, converseAnswer, seen),
      now: () => FIXED,
    });
    await bedrock.complete({ system: "s", user: "u", allowedTools: ALLOWED });
    const sent = seen[0]?.init.headers as Record<string, string>;
    const expectedHeaders = signSigV4({ ...VECTOR, body: String(seen[0]?.init.body), credentials: CREDS });
    expect(sent.authorization).toBe(expectedHeaders.authorization);
  });

  it("signs a VPC endpoint base URL's path prefix", async () => {
    const seen: Seen[] = [];
    const bedrock = createBedrockProvider({
      credentials: CREDS,
      region: "eu-central-1",
      modelId: "m",
      baseUrl: "https://vpce.example.internal/bedrock/",
      fetchFn: fakeFetch(200, converseAnswer, seen),
      now: () => FIXED,
    });
    await bedrock.complete({ system: "s", user: "u", allowedTools: ALLOWED });
    expect(seen[0]?.url).toBe("https://vpce.example.internal/bedrock/model/m/converse");
    const sent = seen[0]?.init.headers as Record<string, string>;
    const expectedHeaders = signSigV4({
      ...VECTOR,
      host: "vpce.example.internal",
      canonicalPath: "/bedrock/model/m/converse",
      body: String(seen[0]?.init.body),
      credentials: CREDS,
    });
    expect(sent.authorization).toBe(expectedHeaders.authorization);
  });

  it("is deterministic for the same input and changes when the body changes", () => {
    const base = {
      method: "POST",
      host: "bedrock-runtime.eu-central-1.amazonaws.com",
      canonicalPath: "/model/m/converse",
      region: "eu-central-1",
      service: "bedrock",
      credentials: CREDS,
      date: FIXED,
    };
    const a = signSigV4({ ...base, body: "{\"a\":1}" });
    const again = signSigV4({ ...base, body: "{\"a\":1}" });
    const other = signSigV4({ ...base, body: "{\"a\":2}" });
    expect(again.authorization).toBe(a.authorization);
    expect(other.authorization).not.toBe(a.authorization);
  });

  it("adds the security token header when a session token is configured", () => {
    const out = signSigV4({
      method: "POST",
      host: "h",
      canonicalPath: "/p",
      body: "{}",
      region: "us-east-1",
      service: "bedrock",
      credentials: { ...CREDS, sessionToken: "session-test-not-real" },
      date: FIXED,
    });
    expect(out["x-amz-security-token"]).toBe("session-test-not-real");
    expect(out.authorization).toContain("SignedHeaders=content-type;host;x-amz-date;x-amz-security-token");
  });
});
