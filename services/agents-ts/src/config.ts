import { createAnthropicProvider } from "./providers/anthropic.js";
import { createBedrockProvider } from "./providers/bedrock.js";
import { createOpenAICompatibleProvider } from "./providers/openai.js";
import type { ModelProvider } from "./runtime/engine.js";

/** The Claude models Passage defaults to; every one is overridable by configuration. */
export const MODEL_OPUS = "claude-opus-5-5";
export const MODEL_SONNET = "claude-sonnet-5-5";
export const MODEL_HAIKU = "claude-haiku-5-5";

export type ProviderId = "anthropic" | "openai" | "bedrock";

export interface Config {
  host: string;
  port: number;
  adminHost: string;
  adminPort: number;
  build: string;
  mcpUrl: string;
  provider: ProviderId;
  /** Model id agents use unless `agentModels` names one. */
  model: string;
  /** Per agent id overrides, for example ap_coding on the larger model and anomaly on the smaller. */
  agentModels: Record<string, string>;
  copilotModel: string;
  runTokenCap: number;
  copilotTokenCap: number;
  copilotMaxTurns: number;
  selfApprovalLimit: { amount: string; currency: string };
  approvalExpiryHours: number | undefined;
  /** `agent@version` pairs whose evaluation passed (the eval pipeline sets this; FR-47.10). */
  evalPassed: ReadonlySet<string>;
  /** Provider credentials and endpoints, read from the environment only. */
  anthropic: { apiKey: string; baseUrl: string | undefined };
  openai: { apiKey: string; baseUrl: string; auth: "bearer" | "api-key"; apiVersion: string | undefined };
  bedrock: { region: string; accessKeyId: string; secretAccessKey: string; sessionToken: string | undefined };
}

export function parseAddr(value: string, name: string): { host: string; port: number } {
  const idx = value.lastIndexOf(":");
  if (idx === -1) throw new Error(`${name} must be host:port`);
  const host = value.slice(0, idx) || "0.0.0.0";
  const port = Number(value.slice(idx + 1));
  if (!Number.isInteger(port) || port <= 0 || port > 65535) throw new Error(`${name} must end in a valid port`);
  return { host, port };
}

function positiveInt(env: NodeJS.ProcessEnv, key: string, fallback: number): number {
  const raw = env[key];
  if (raw === undefined || raw === "") return fallback;
  const n = Number(raw);
  if (!Number.isSafeInteger(n) || n < 1) throw new Error(`${key} must be a positive integer`);
  return n;
}

/** 12-factor config. Invalid or missing required settings are an error at startup, never a silent default. */
export function loadConfig(env: NodeJS.ProcessEnv): Config {
  const mcpUrl = env["MCP_URL"] ?? "";
  if (!/^https?:\/\//.test(mcpUrl)) throw new Error("MCP_URL must be the MCP gateway's http(s) URL");
  const http = parseAddr(env["HTTP_ADDR"] ?? "0.0.0.0:8080", "HTTP_ADDR");
  const admin = parseAddr(env["ADMIN_ADDR"] ?? "0.0.0.0:9090", "ADMIN_ADDR");
  if (admin.port === http.port) throw new Error("ADMIN_ADDR must use a different port from HTTP_ADDR");
  const provider = (env["AGENTS_PROVIDER"] ?? "anthropic") as ProviderId;
  if (provider !== "anthropic" && provider !== "openai" && provider !== "bedrock") {
    throw new Error("AGENTS_PROVIDER must be anthropic, openai or bedrock");
  }
  let agentModels: Record<string, string> = {};
  const rawModels = env["AGENTS_MODEL_OVERRIDES"];
  if (rawModels !== undefined && rawModels !== "") {
    const parsed: unknown = JSON.parse(rawModels);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed) || Object.values(parsed).some((v) => typeof v !== "string" || v === "")) {
      throw new Error('AGENTS_MODEL_OVERRIDES must be a JSON object of agent id to model id, for example {"ap_coding":"claude-opus-5-5"}');
    }
    agentModels = parsed as Record<string, string>;
  }
  const limit = env["AGENTS_SELF_APPROVAL_LIMIT"] ?? "0";
  if (!/^[0-9]+(\.[0-9]{1,6})?$/.test(limit)) throw new Error("AGENTS_SELF_APPROVAL_LIMIT must be a decimal amount");
  const currency = env["AGENTS_SELF_APPROVAL_CURRENCY"] ?? "USD";
  if (!/^[A-Z]{3}$/.test(currency)) throw new Error("AGENTS_SELF_APPROVAL_CURRENCY must be an ISO 4217 code");
  const expiry = env["AGENTS_APPROVAL_EXPIRY_H"];
  const approvalExpiryHours = expiry === undefined || expiry === "" ? undefined : positiveInt(env, "AGENTS_APPROVAL_EXPIRY_H", 72);
  if (approvalExpiryHours !== undefined && approvalExpiryHours > 240) throw new Error("AGENTS_APPROVAL_EXPIRY_H must be 1 to 240");
  const model = env["AGENTS_MODEL"] ?? MODEL_SONNET;
  return {
    host: http.host,
    port: http.port,
    adminHost: admin.host,
    adminPort: admin.port,
    build: env["SERVICE_VERSION"] ?? "0.1.0",
    mcpUrl,
    provider,
    model,
    agentModels,
    copilotModel: env["AGENTS_COPILOT_MODEL"] ?? model,
    runTokenCap: positiveInt(env, "AGENTS_RUN_TOKEN_CAP", 20_000),
    copilotTokenCap: positiveInt(env, "AGENTS_COPILOT_TOKEN_CAP", 8_000),
    copilotMaxTurns: positiveInt(env, "AGENTS_COPILOT_MAX_TURNS", 4),
    selfApprovalLimit: { amount: limit, currency },
    approvalExpiryHours,
    evalPassed: new Set((env["AGENTS_EVAL_PASSED"] ?? "").split(",").map((s) => s.trim()).filter((s) => s !== "")),
    anthropic: { apiKey: env["ANTHROPIC_API_KEY"] ?? "", baseUrl: env["ANTHROPIC_BASE_URL"] },
    openai: {
      apiKey: env["OPENAI_API_KEY"] ?? "",
      baseUrl: env["AGENTS_OPENAI_BASE_URL"] ?? "",
      auth: env["AGENTS_OPENAI_AUTH"] === "api-key" ? "api-key" : "bearer",
      apiVersion: env["AGENTS_OPENAI_API_VERSION"],
    },
    bedrock: {
      region: env["AWS_REGION"] ?? "",
      accessKeyId: env["AWS_ACCESS_KEY_ID"] ?? "",
      secretAccessKey: env["AWS_SECRET_ACCESS_KEY"] ?? "",
      sessionToken: env["AWS_SESSION_TOKEN"],
    },
  };
}

/**
 * The provider factory: one adapter per model id, built on first use. A missing credential is an
 * error when the first run needs it, with the variable named and never its value. `fetchFn` lets a
 * test intercept every call, so no test reaches a real endpoint.
 */
export function createProviderFactory(cfg: Config, fetchFn?: typeof fetch): (modelId: string) => ModelProvider {
  const cache = new Map<string, ModelProvider>();
  const fetching = fetchFn !== undefined ? { fetchFn } : {};
  return (modelId) => {
    const hit = cache.get(modelId);
    if (hit !== undefined) return hit;
    let made: ModelProvider;
    switch (cfg.provider) {
      case "anthropic":
        if (cfg.anthropic.apiKey === "") throw new Error("ANTHROPIC_API_KEY is not set");
        made = createAnthropicProvider({ apiKey: cfg.anthropic.apiKey, model: modelId, ...(cfg.anthropic.baseUrl !== undefined ? { baseUrl: cfg.anthropic.baseUrl } : {}), ...fetching });
        break;
      case "openai":
        if (cfg.openai.apiKey === "" || cfg.openai.baseUrl === "") throw new Error("OPENAI_API_KEY and AGENTS_OPENAI_BASE_URL must be set");
        made = createOpenAICompatibleProvider({
          baseUrl: cfg.openai.baseUrl,
          model: modelId,
          apiKey: cfg.openai.apiKey,
          auth: cfg.openai.auth,
          ...(cfg.openai.apiVersion !== undefined ? { query: { "api-version": cfg.openai.apiVersion } } : {}),
          ...fetching,
        });
        break;
      case "bedrock":
        if (cfg.bedrock.region === "" || cfg.bedrock.accessKeyId === "" || cfg.bedrock.secretAccessKey === "") {
          throw new Error("AWS_REGION, AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set");
        }
        made = createBedrockProvider({
          region: cfg.bedrock.region,
          modelId,
          credentials: {
            accessKeyId: cfg.bedrock.accessKeyId,
            secretAccessKey: cfg.bedrock.secretAccessKey,
            ...(cfg.bedrock.sessionToken !== undefined ? { sessionToken: cfg.bedrock.sessionToken } : {}),
          },
          ...fetching,
        });
        break;
    }
    cache.set(modelId, made);
    return made;
  };
}
