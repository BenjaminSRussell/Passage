import { assertProviderEndpoint, type EgressOptions } from "../cloudprofile/index.js";
import { assertNoPii } from "../domain/redaction.js";
import type { ModelProvider, ModelRequest, ModelResponse, ModelUsage } from "../runtime/engine.js";
import { postJson, postStream } from "./http.js";
import { sseEvents, TextFieldExtractor } from "./stream.js";
import { isRecord, PROPOSE_TOOL_NAME, ProviderError, providerRejected, providerUnavailable, proposeTool, toModelResponse, outputLimit } from "./wire.js";

export interface AnthropicConfig {
  apiKey: string;
  model: string;
  maxTokens?: number;
  /** Defaults to the public API; a residency-compliant gateway URL may replace it (BR-48.7). */
  baseUrl?: string;
  fetchFn?: typeof fetch;
  timeoutMs?: number;
  /** Bound on a whole streamed answer; defaults to 120 s. */
  streamTimeoutMs?: number;
  /** Cloud profile egress check (#599); defaults to PASSAGE_CLOUD_PROFILE. `region` is where the endpoint is hosted. */
  egress?: EgressOptions;
}

/** Anthropic Messages API adapter (default provider, PRD 9.47.10). */
export function createAnthropicProvider(cfg: AnthropicConfig): ModelProvider {
  const base = (cfg.baseUrl ?? "https://api.anthropic.com").replace(/\/$/, "");
  assertProviderEndpoint(base, cfg.egress);
  const body = (req: ModelRequest, stream: boolean): Record<string, unknown> => {
    const tool = proposeTool(req.allowedTools);
    return {
      model: cfg.model,
      max_tokens: outputLimit(cfg.maxTokens, req.maxOutputTokens),
      system: req.system,
      messages: [{ role: "user", content: req.user }],
      tools: [{ name: tool.name, description: tool.description, input_schema: tool.inputSchema }],
      tool_choice: { type: "tool", name: tool.name },
      ...(stream ? { stream: true } : {}),
    };
  };
  const headers = { "x-api-key": cfg.apiKey, "anthropic-version": "2023-06-01" };
  return {
    async stream(req, onText): Promise<ModelResponse> {
      assertNoPii(req.user);
      const events = await postStream(`${base}/v1/messages`, headers, body(req, true), { fetchFn: cfg.fetchFn ?? fetch, timeoutMs: cfg.streamTimeoutMs ?? 120_000 });
      return readMessageStream(events, req.allowedTools, onText);
    },
    async complete(req): Promise<ModelResponse> {
      // Defence in depth: the engine redacts first; the adapter refuses raw PII too.
      assertNoPii(req.user);
      const tool = proposeTool(req.allowedTools);
      const json = await postJson(
        `${base}/v1/messages`,
        { "x-api-key": cfg.apiKey, "anthropic-version": "2023-06-01" },
        {
          model: cfg.model,
          max_tokens: outputLimit(cfg.maxTokens, req.maxOutputTokens),
          system: req.system,
          messages: [{ role: "user", content: req.user }],
          tools: [{ name: tool.name, description: tool.description, input_schema: tool.inputSchema }],
          tool_choice: { type: "tool", name: tool.name },
        },
        { fetchFn: cfg.fetchFn ?? fetch, timeoutMs: cfg.timeoutMs ?? 30_000 },
      );
      return toModelResponse(anthropicArguments(json), anthropicUsage(json), req.allowedTools);
    },
  };
}

function anthropicArguments(json: unknown): unknown {
  if (!isRecord(json) || !Array.isArray(json.content)) {
    throw providerRejected("anthropic response has no content blocks");
  }
  for (const block of json.content) {
    if (isRecord(block) && block.type === "tool_use" && block.name === PROPOSE_TOOL_NAME) return block.input;
  }
  throw providerRejected("anthropic response did not call the proposal tool");
}

function anthropicUsage(json: unknown): ModelUsage | undefined {
  if (!isRecord(json) || !isRecord(json.usage)) return undefined;
  const tokensIn = json.usage.input_tokens;
  const tokensOut = json.usage.output_tokens;
  if (typeof tokensIn !== "number" || typeof tokensOut !== "number") return undefined;
  return { tokensIn, tokensOut };
}

/**
 * Reads an Anthropic Messages stream whose one tool call is the proposal: the pieces of its input JSON feed the text
 * extractor, so `onText` gets the answer as it is written, and the whole input is parsed at the end exactly like a
 * non-streamed answer. A stream that ends without `message_stop`, or an `error` event, is an outage for the run: the
 * model may have run, so the hold is kept (not unbilled).
 */
async function readMessageStream(body: ReadableStream<Uint8Array>, allowedTools: readonly string[], onText: (delta: string) => void): Promise<ModelResponse> {
  const extractor = new TextFieldExtractor();
  let input = "";
  let tokensIn: number | undefined;
  let tokensOut: number | undefined;
  let stopped = false;
  try {
    for await (const ev of sseEvents(body)) {
      let data: unknown;
      try {
        data = JSON.parse(ev.data) as unknown;
      } catch {
        throw providerRejected("anthropic stream carried an event that is not JSON");
      }
      if (!isRecord(data)) continue;
      switch (data.type) {
        case "message_start":
          if (isRecord(data.message) && isRecord(data.message.usage)) {
            if (typeof data.message.usage.input_tokens === "number") tokensIn = data.message.usage.input_tokens;
            if (typeof data.message.usage.output_tokens === "number") tokensOut = data.message.usage.output_tokens;
          }
          break;
        case "content_block_delta":
          if (isRecord(data.delta) && data.delta.type === "input_json_delta" && typeof data.delta.partial_json === "string") {
            input += data.delta.partial_json;
            const text = extractor.push(data.delta.partial_json);
            if (text !== "") onText(text);
          }
          break;
        case "message_delta":
          if (isRecord(data.usage) && typeof data.usage.output_tokens === "number") tokensOut = data.usage.output_tokens;
          break;
        case "message_stop":
          stopped = true;
          break;
        case "error":
          throw providerUnavailable("anthropic stream reported an error");
        default:
      }
    }
  } catch (err) {
    if (err instanceof ProviderError) throw err;
    throw providerUnavailable("provider connection dropped while streaming the response");
  }
  if (!stopped) throw providerUnavailable("anthropic stream ended before the message was complete");
  let args: unknown;
  try {
    args = JSON.parse(input) as unknown;
  } catch {
    throw providerRejected("anthropic response did not call the proposal tool");
  }
  const usage: ModelUsage | undefined = tokensIn !== undefined && tokensOut !== undefined ? { tokensIn, tokensOut } : undefined;
  return toModelResponse(args, usage, allowedTools);
}
