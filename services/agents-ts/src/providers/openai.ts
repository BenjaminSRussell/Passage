import { assertProviderEndpoint, type EgressOptions } from "../cloudprofile/index.js";
import { assertNoPii } from "../domain/redaction.js";
import type { ModelProvider, ModelResponse, ModelUsage } from "../runtime/engine.js";
import { postJson } from "./http.js";
import { isRecord, PROPOSE_TOOL_NAME, providerRejected, parseJsonBody, proposeTool, toModelResponse, outputLimit } from "./wire.js";

export interface OpenAICompatibleConfig {
  /** Base URL up to (not including) /chat/completions, e.g. https://host/v1 or an Azure deployment URL. */
  baseUrl: string;
  model: string;
  apiKey: string;
  /** "bearer" sends Authorization: Bearer; "api-key" sends the Azure OpenAI header. */
  auth?: "bearer" | "api-key";
  /** Extra query parameters, e.g. Azure's api-version. */
  query?: Record<string, string>;
  maxTokens?: number;
  fetchFn?: typeof fetch;
  timeoutMs?: number;
  /** Cloud profile egress check (#599); defaults to PASSAGE_CLOUD_PROFILE. `region` is where the endpoint is hosted. */
  egress?: EgressOptions;
}

/**
 * OpenAI chat-completions adapter. Serves OpenAI-compatible open-weights
 * endpoints (vLLM, TGI) and Azure OpenAI deployments (FR-47.13); Azure and
 * vLLM differ only by configuration. Bedrock uses its own wire format and
 * signing and is not covered here.
 */
export function createOpenAICompatibleProvider(cfg: OpenAICompatibleConfig): ModelProvider {
  const url = new URL(`${cfg.baseUrl.replace(/\/$/, "")}/chat/completions`);
  assertProviderEndpoint(url.toString(), cfg.egress);
  for (const [k, v] of Object.entries(cfg.query ?? {})) url.searchParams.set(k, v);
  const headers: Record<string, string> =
    (cfg.auth ?? "bearer") === "api-key" ? { "api-key": cfg.apiKey } : { authorization: `Bearer ${cfg.apiKey}` };
  return {
    async complete(req): Promise<ModelResponse> {
      assertNoPii(req.user);
      const tool = proposeTool(req.allowedTools);
      const json = await postJson(
        url.toString(),
        headers,
        {
          model: cfg.model,
          max_tokens: outputLimit(cfg.maxTokens, req.maxOutputTokens),
          messages: [
            { role: "system", content: req.system },
            { role: "user", content: req.user },
          ],
          tools: [{ type: "function", function: { name: tool.name, description: tool.description, parameters: tool.inputSchema } }],
          tool_choice: { type: "function", function: { name: tool.name } },
        },
        { fetchFn: cfg.fetchFn ?? fetch, timeoutMs: cfg.timeoutMs ?? 30_000 },
      );
      return toModelResponse(openAiArguments(json), openAiUsage(json), req.allowedTools);
    },
  };
}

function openAiArguments(json: unknown): unknown {
  if (!isRecord(json) || !Array.isArray(json.choices) || !isRecord(json.choices[0])) {
    throw providerRejected("openai-compatible response has no choices");
  }
  const message = json.choices[0].message;
  if (!isRecord(message) || !Array.isArray(message.tool_calls)) {
    throw providerRejected("openai-compatible response did not call the proposal tool");
  }
  for (const call of message.tool_calls) {
    if (!isRecord(call) || !isRecord(call.function)) continue;
    if (call.function.name === PROPOSE_TOOL_NAME && typeof call.function.arguments === "string") {
      return parseJsonBody(call.function.arguments);
    }
  }
  throw providerRejected("openai-compatible response did not call the proposal tool");
}

function openAiUsage(json: unknown): ModelUsage | undefined {
  if (!isRecord(json) || !isRecord(json.usage)) return undefined;
  const tokensIn = json.usage.prompt_tokens;
  const tokensOut = json.usage.completion_tokens;
  if (typeof tokensIn !== "number" || typeof tokensOut !== "number") return undefined;
  return { tokensIn, tokensOut };
}
