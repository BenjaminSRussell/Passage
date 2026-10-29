import { createHash, createHmac } from "node:crypto";
import { assertNoPii } from "../domain/redaction.js";
import type { ModelProvider, ModelResponse, ModelUsage } from "../runtime/engine.js";
import { postJson } from "./http.js";
import { isRecord, PROPOSE_TOOL_NAME, providerRejected, proposeTool, toModelResponse } from "./wire.js";

/** Static credentials are read from Vault-projected files by the caller (FR-47.12, 9.47.10). */
export interface AwsCredentials {
  accessKeyId: string;
  secretAccessKey: string;
  sessionToken?: string;
}

export interface BedrockConfig {
  credentials: AwsCredentials;
  /** AWS region of the endpoint. The residency port checks this value (BR-47.9). */
  region: string;
  /** Bedrock model id, for example `anthropic.claude-3-5-sonnet-20241022-v2:0`. */
  modelId: string;
  maxTokens?: number;
  /** Replaces `https://bedrock-runtime.<region>.amazonaws.com`, for VPC endpoints. */
  baseUrl?: string;
  fetchFn?: typeof fetch;
  timeoutMs?: number;
  /** Injected clock for signing tests. Defaults to the current time. */
  now?: () => Date;
}

/**
 * Amazon Bedrock adapter (FR-47.13). Uses the Converse API with a forced tool
 * call, so its responses map through the same `toModelResponse` as the other
 * adapters (provider parity, AC-47.10). Requests are signed with SigV4 in-tree;
 * no AWS SDK is pulled in.
 */
export function createBedrockProvider(cfg: BedrockConfig): ModelProvider {
  const base = (cfg.baseUrl ?? `https://bedrock-runtime.${cfg.region}.amazonaws.com`).replace(/\/$/, "");
  // The wire path encodes the model id once (`:` becomes %3A). SigV4 for
  // non-S3 services encodes each path segment again in the canonical request,
  // so the id is encoded twice there. The canonical path is taken from the
  // final URL so a VPC endpoint base URL with a path prefix is signed too.
  const url = new URL(`${base}/model/${uriEncode(cfg.modelId)}/converse`);
  const canonicalPath = url.pathname
    .split("/")
    .map((segment) => uriEncode(segment))
    .join("/");
  return {
    async complete(req): Promise<ModelResponse> {
      assertNoPii(req.user);
      const tool = proposeTool(req.allowedTools);
      const body = {
        system: [{ text: req.system }],
        messages: [{ role: "user", content: [{ text: req.user }] }],
        inferenceConfig: { maxTokens: cfg.maxTokens ?? 1024 },
        toolConfig: {
          tools: [{ toolSpec: { name: tool.name, description: tool.description, inputSchema: { json: tool.inputSchema } } }],
          toolChoice: { tool: { name: tool.name } },
        },
      };
      const signed = signSigV4({
        method: "POST",
        host: url.host,
        canonicalPath,
        body: JSON.stringify(body),
        region: cfg.region,
        service: "bedrock",
        credentials: cfg.credentials,
        date: (cfg.now ?? (() => new Date()))(),
      });
      const json = await postJson(url.toString(), signed, body, {
        fetchFn: cfg.fetchFn ?? fetch,
        timeoutMs: cfg.timeoutMs ?? 30_000,
      });
      return toModelResponse(bedrockArguments(json), bedrockUsage(json), req.allowedTools);
    },
  };
}

/**
 * The Converse answer's forced tool call. A malformed answer is a rejection,
 * not an outage: the provider answered, so it must not start the BR-47.9
 * failover clock (the same rule as the Anthropic and OpenAI adapters).
 */
function bedrockArguments(json: unknown): unknown {
  if (!isRecord(json) || !isRecord(json.output) || !isRecord(json.output.message)) {
    throw providerRejected("bedrock response has no message");
  }
  const content = json.output.message.content;
  if (!Array.isArray(content)) throw providerRejected("bedrock response has no content blocks");
  for (const block of content) {
    if (isRecord(block) && isRecord(block.toolUse) && block.toolUse.name === PROPOSE_TOOL_NAME) {
      return block.toolUse.input;
    }
  }
  throw providerRejected("bedrock response did not call the proposal tool");
}

function bedrockUsage(json: unknown): ModelUsage | undefined {
  if (!isRecord(json) || !isRecord(json.usage)) return undefined;
  const tokensIn = json.usage.inputTokens;
  const tokensOut = json.usage.outputTokens;
  if (typeof tokensIn !== "number" || typeof tokensOut !== "number") return undefined;
  return { tokensIn, tokensOut };
}

export interface SigV4Input {
  method: string;
  host: string;
  /** The canonical URI: each wire path segment percent-encoded once more (non-S3 rule). */
  canonicalPath: string;
  body: string;
  region: string;
  service: string;
  credentials: AwsCredentials;
  date: Date;
}

/**
 * AWS Signature Version 4 for a JSON POST with no query string. Returns the
 * headers to send. The signed header set is content-type, host and x-amz-date
 * (plus x-amz-security-token when a session token is present).
 */
export function signSigV4(input: SigV4Input): Record<string, string> {
  const amzDate = input.date.toISOString().replace(/[:-]|\.\d{3}/g, "");
  const dateStamp = amzDate.slice(0, 8);
  const scope = `${dateStamp}/${input.region}/${input.service}/aws4_request`;

  const headers: Record<string, string> = {
    "content-type": "application/json",
    host: input.host,
    "x-amz-date": amzDate,
  };
  if (input.credentials.sessionToken) headers["x-amz-security-token"] = input.credentials.sessionToken;
  const signedNames = Object.keys(headers).sort();
  const canonicalHeaders = signedNames.map((n) => `${n}:${(headers[n] ?? "").trim()}\n`).join("");
  const signedHeaders = signedNames.join(";");
  const payloadHash = sha256Hex(input.body);

  const canonicalRequest = [
    input.method,
    input.canonicalPath,
    "", // no query string
    canonicalHeaders,
    signedHeaders,
    payloadHash,
  ].join("\n");

  const stringToSign = ["AWS4-HMAC-SHA256", amzDate, scope, sha256Hex(canonicalRequest)].join("\n");
  const kDate = hmac(`AWS4${input.credentials.secretAccessKey}`, dateStamp);
  const kRegion = hmac(kDate, input.region);
  const kService = hmac(kRegion, input.service);
  const kSigning = hmac(kService, "aws4_request");
  const signature = createHmac("sha256", kSigning).update(stringToSign).digest("hex");

  const out: Record<string, string> = {
    "x-amz-date": amzDate,
    authorization: `AWS4-HMAC-SHA256 Credential=${input.credentials.accessKeyId}/${scope}, SignedHeaders=${signedHeaders}, Signature=${signature}`,
  };
  if (input.credentials.sessionToken) out["x-amz-security-token"] = input.credentials.sessionToken;
  return out;
}

/** RFC 3986 unreserved characters pass; everything else is percent-encoded. */
function uriEncode(s: string): string {
  return encodeURIComponent(s).replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
}

function sha256Hex(s: string): string {
  return createHash("sha256").update(s).digest("hex");
}

function hmac(key: string | Buffer, data: string): Buffer {
  return createHmac("sha256", key).update(data).digest();
}
