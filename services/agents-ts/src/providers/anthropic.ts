import { assertNoPii } from "../domain/redaction.js";
import type { ModelProvider, ModelResponse, ModelUsage } from "../runtime/engine.js";
import { postJson } from "./http.js";
import { isRecord, PROPOSE_TOOL_NAME, providerRejected, proposeTool, toModelResponse, outputLimit } from "./wire.js";

export interface AnthropicConfig {
  apiKey: string;
  model: string;
  maxTokens?: number;
  /** Defaults to the public API; a residency-compliant gateway URL may replace it (BR-48.7). */
  baseUrl?: string;
  fetchFn?: typeof fetch;
  timeoutMs?: number;
}

/** Anthropic Messages API adapter (default provider, PRD 9.47.10). */
export function createAnthropicProvider(cfg: AnthropicConfig): ModelProvider {
  const base = (cfg.baseUrl ?? "https://api.anthropic.com").replace(/\/$/, "");
  return {
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
