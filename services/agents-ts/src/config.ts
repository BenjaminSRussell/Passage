import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { createAnthropicProvider } from "./providers/anthropic.js";
import { createBedrockProvider } from "./providers/bedrock.js";
import { createOpenAICompatibleProvider } from "./providers/openai.js";
import { createPhiGuard, parsePhiFields, type PhiGuard } from "./domain/phi.js";
import { NO_ROUTE_MESSAGE, type Boundary } from "./domain/modelRoute.js";
import { AgentError } from "./errors.js";
import type { ModelProvider, ModelRequest } from "./runtime/engine.js";
import { GUARD_REFUSAL_MESSAGE, type EgressGuard } from "./runtime/egressGuard.js";
import { Pseudonymizer } from "./domain/redaction.js";

/** The Claude models Depot defaults to; every one is overridable by configuration. */
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
  taskConnectTimeoutMs: number;
  outputGuardTimeoutMs: number;
  needToKnowCacheMs: number;
  selfApprovalLimit: { amount: string; currency: string };
  approvalExpiryHours: number | undefined;
  /** How often each pod polls the shared kill switch (AC-47.6); below the 10 s fail-closed window. */
  killSwitchPollMs: number;
  /** Provider credentials and endpoints, read from the environment only. */
  anthropic: { apiKey: string; baseUrl: string | undefined };
  openai: {
    apiKey: string;
    baseUrl: string;
    auth: "bearer" | "api-key";
    apiVersion: string | undefined;
  };
  bedrock: {
    region: string;
    accessKeyId: string;
    secretAccessKey: string;
    sessionToken: string | undefined;
  };
  /**
   * Azure region the model endpoint is hosted in (AGENTS_PROVIDER_REGION). A cloud profile that pins regions
   * (gcc, gcc-high, dod, eu-data-boundary) refuses an endpoint without one, and every run records it (#605).
   */
  providerRegion: string | undefined;
  /** Entity metadata fields tagged `phi`: text carrying a value under one of them is never sent to a model (#606). */
  phiFields: readonly string[];
  /** Caller authentication: the realm that signs the bearer tokens callers send (auth.ts). */
  jwt: {
    issuer: string;
    audience: string;
    jwksUrl: string | undefined;
    jwksFile: string | undefined;
  };
  /**
   * How runs execute (AGENTS_RUN_ENGINE). `temporal`: each run is a workflow (workflow-ts agentRunWorkflow) whose steps call
   * this service, so a run resumes after an approval across pod restarts (FR-47.4). `inprocess` (the default, for dev):
   * the run lives in one promise of this pod and is lost with it.
   */
  runEngine: "inprocess" | "temporal";
  /** workflow-ts base URL (AGENTS_WORKFLOW_URL): where a run is started and an approval decision is signalled; required with `temporal`. */
  workflowUrl: string | undefined;
  /** Public keys of the services allowed to call the run-step routes (DEPOT_SERVICE_PUBLIC_KEYS, `name[:kid]=base64` comma separated); required with `temporal`. */
  servicePublicKeys: string;
  boundary: Boundary;
  aiEnabled: boolean;
  complianceUrl: string | undefined;
  auditUrl: string | undefined;
}

export function parseAddr(
  value: string,
  name: string,
): { host: string; port: number } {
  const idx = value.lastIndexOf(":");
  if (idx === -1) throw new Error(`${name} must be host:port`);
  const host = value.slice(0, idx) || "0.0.0.0";
  const port = Number(value.slice(idx + 1));
  if (!Number.isInteger(port) || port <= 0 || port > 65535)
    throw new Error(`${name} must end in a valid port`);
  return { host, port };
}

function positiveInt(
  env: NodeJS.ProcessEnv,
  key: string,
  fallback: number,
): number {
  const raw = env[key];
  if (raw === undefined || raw === "") return fallback;
  const n = Number(raw);
  if (!Number.isSafeInteger(n) || n < 1)
    throw new Error(`${key} must be a positive integer`);
  return n;
}

function phiFile(path: string | undefined): string | undefined {
  return path === undefined || path === ""
    ? undefined
    : readFileSync(path, "utf8");
}

/**
 * The model settings that pin each agent definition (FR-47.10): the default model and the per-agent overrides. The
 * service and the eval recorder (cli/record-eval.ts) read the same ones, so both compute the same prompt hashes.
 */
export function loadModelSettings(env: NodeJS.ProcessEnv): {
  model: string;
  agentModels: Record<string, string>;
} {
  let agentModels: Record<string, string> = {};
  const rawModels = env["AGENTS_MODEL_OVERRIDES"];
  if (rawModels !== undefined && rawModels !== "") {
    const parsed: unknown = JSON.parse(rawModels);
    if (
      typeof parsed !== "object" ||
      parsed === null ||
      Array.isArray(parsed) ||
      Object.values(parsed).some((v) => typeof v !== "string" || v === "")
    ) {
      throw new Error(
        'AGENTS_MODEL_OVERRIDES must be a JSON object of agent id to model id, for example {"ap_coding":"claude-opus-5-5"}',
      );
    }
    agentModels = parsed as Record<string, string>;
  }
  return { model: env["AGENTS_MODEL"] ?? MODEL_SONNET, agentModels };
}

/** 12-factor config. Invalid or missing required settings are an error at startup, never a silent default. */
export function loadConfig(env: NodeJS.ProcessEnv): Config {
  const mcpUrl = env["MCP_URL"] ?? "";
  if (!/^https?:\/\//.test(mcpUrl))
    throw new Error("MCP_URL must be the MCP gateway's http(s) URL");
  const http = parseAddr(env["HTTP_ADDR"] ?? "0.0.0.0:8080", "HTTP_ADDR");
  const admin = parseAddr(env["ADMIN_ADDR"] ?? "0.0.0.0:9090", "ADMIN_ADDR");
  if (admin.port === http.port)
    throw new Error("ADMIN_ADDR must use a different port from HTTP_ADDR");
  const provider = (env["AGENTS_PROVIDER"] ?? "anthropic") as ProviderId;
  if (
    provider !== "anthropic" &&
    provider !== "openai" &&
    provider !== "bedrock"
  ) {
    throw new Error("AGENTS_PROVIDER must be anthropic, openai or bedrock");
  }
  const { model, agentModels } = loadModelSettings(env);
  if ((env["AGENTS_EVAL_PASSED"] ?? "") !== "") {
    throw new Error(
      "AGENTS_EVAL_PASSED is no longer read: record the eval pipeline's report with dist/cli/record-eval.js (eval_run)",
    );
  }
  const limit = env["AGENTS_SELF_APPROVAL_LIMIT"] ?? "0";
  if (!/^[0-9]+(\.[0-9]{1,6})?$/.test(limit))
    throw new Error("AGENTS_SELF_APPROVAL_LIMIT must be a decimal amount");
  const currency = env["AGENTS_SELF_APPROVAL_CURRENCY"] ?? "USD";
  if (!/^[A-Z]{3}$/.test(currency))
    throw new Error("AGENTS_SELF_APPROVAL_CURRENCY must be an ISO 4217 code");
  const expiry = env["AGENTS_APPROVAL_EXPIRY_H"];
  const approvalExpiryHours =
    expiry === undefined || expiry === ""
      ? undefined
      : positiveInt(env, "AGENTS_APPROVAL_EXPIRY_H", 72);
  if (approvalExpiryHours !== undefined && approvalExpiryHours > 240)
    throw new Error("AGENTS_APPROVAL_EXPIRY_H must be 1 to 240");
  const killSwitchPollMs = positiveInt(env, "AGENTS_KILLSWITCH_POLL_MS", 1000);
  if (killSwitchPollMs < 100 || killSwitchPollMs > 5000)
    throw new Error(
      "AGENTS_KILLSWITCH_POLL_MS must be 100 to 5000: a pod fails closed after 10 s without a poll",
    );
  const outputGuardTimeoutMs = positiveInt(env, "AGENTS_OUTPUT_GUARD_TIMEOUT_MS", 2000);
  if (outputGuardTimeoutMs < 100 || outputGuardTimeoutMs > 10_000)
    throw new Error("AGENTS_OUTPUT_GUARD_TIMEOUT_MS must be 100 to 10000");
  const needToKnowCacheMs = positiveInt(env, "AGENTS_NEED_TO_KNOW_CACHE_MS", 10_000);
  if (needToKnowCacheMs > 60_000) throw new Error("AGENTS_NEED_TO_KNOW_CACHE_MS must be 1 to 60000");
  const taskConnectTimeoutMs = positiveInt(env, "AGENTS_TASK_CONNECT_TIMEOUT_MS", 2000);
  if (taskConnectTimeoutMs < 100 || taskConnectTimeoutMs > 10_000)
    throw new Error("AGENTS_TASK_CONNECT_TIMEOUT_MS must be 100 to 10000");
  const issuer = env["AGENTS_JWT_ISSUER"] ?? "";
  if (!/^https?:\/\//.test(issuer))
    throw new Error(
      "AGENTS_JWT_ISSUER must be the issuer (realm URL) of the callers' bearer tokens",
    );
  const jwksUrl = env["AGENTS_JWKS_URL"] || undefined;
  const jwksFile = env["AGENTS_JWKS_FILE"] || undefined;
  if (jwksUrl === undefined && jwksFile === undefined)
    throw new Error(
      "AGENTS_JWKS_URL or AGENTS_JWKS_FILE (the realm's signing keys) is required",
    );
  if (jwksUrl !== undefined && !/^https?:\/\//.test(jwksUrl))
    throw new Error("AGENTS_JWKS_URL must be an http(s) URL");
  const runEngine = env["AGENTS_RUN_ENGINE"] ?? "inprocess";
  if (runEngine !== "inprocess" && runEngine !== "temporal")
    throw new Error("AGENTS_RUN_ENGINE must be inprocess or temporal");
  const workflowUrl = env["AGENTS_WORKFLOW_URL"] || undefined;
  const servicePublicKeys = env["DEPOT_SERVICE_PUBLIC_KEYS"] ?? "";
  if (runEngine === "temporal") {
    if (workflowUrl === undefined || !/^https?:\/\//.test(workflowUrl))
      throw new Error(
        "AGENTS_WORKFLOW_URL must be workflow-ts's http(s) URL when AGENTS_RUN_ENGINE=temporal",
      );
    if (servicePublicKeys.trim() === "")
      throw new Error(
        "DEPOT_SERVICE_PUBLIC_KEYS must hold workflow-ts's public key when AGENTS_RUN_ENGINE=temporal",
      );
  }
  const boundary = env["AGENTS_BOUNDARY"] || "standard";
  if (boundary !== "standard" && boundary !== "cui")
    throw new Error("AGENTS_BOUNDARY must be standard or cui");
  const aiEnabledRaw = env["AGENTS_AI_ENABLED"] || "false";
  if (aiEnabledRaw !== "true" && aiEnabledRaw !== "false")
    throw new Error("AGENTS_AI_ENABLED must be true or false");
  const complianceUrl = env["AGENTS_COMPLIANCE_URL"] || undefined;
  if (complianceUrl !== undefined && !/^https?:\/\//.test(complianceUrl))
    throw new Error("AGENTS_COMPLIANCE_URL must be an http(s) URL");
  const auditUrl = env["AGENTS_AUDIT_URL"] || undefined;
  if (auditUrl !== undefined && !/^https?:\/\//.test(auditUrl))
    throw new Error("AGENTS_AUDIT_URL must be an http(s) URL");
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
    copilotMaxTurns: positiveInt(env, "AGENTS_COPILOT_MAX_TURNS", 8),
    taskConnectTimeoutMs,
    outputGuardTimeoutMs,
    needToKnowCacheMs,
    selfApprovalLimit: { amount: limit, currency },
    approvalExpiryHours,
    killSwitchPollMs,
    anthropic: {
      apiKey: env["ANTHROPIC_API_KEY"] ?? "",
      baseUrl: env["ANTHROPIC_BASE_URL"],
    },
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
    providerRegion: env["AGENTS_PROVIDER_REGION"] || undefined,
    phiFields: parsePhiFields(
      env["AGENTS_PHI_FIELDS"],
      phiFile(env["AGENTS_PHI_FIELDS_FILE"]),
    ),
    jwt: {
      issuer,
      audience: env["AGENTS_JWT_AUDIENCE"] || "depot-api",
      jwksUrl,
      jwksFile,
    },
    runEngine,
    workflowUrl,
    servicePublicKeys,
    boundary,
    aiEnabled: aiEnabledRaw === "true",
    complianceUrl,
    auditUrl,
  };
}

export function createRunProviderFactory(
  cfg: Config,
  fetchFn: typeof fetch | undefined,
  guard: EgressGuard,
): (modelId: string) => ModelProvider {
  const direct = createProviderFactory(cfg, fetchFn);
  return (modelId) => {
    if (cfg.boundary === "cui")
      throw new AgentError("AGT-0218", NO_ROUTE_MESSAGE);
    return withEgressGuard(direct(modelId), guard);
  };
}

export function withEgressGuard(
  provider: ModelProvider,
  guard: EgressGuard,
): ModelProvider {
  const check = async (req: ModelRequest): Promise<void> => {
    const tenantId = req.guard?.tenantId ?? "run";
    const verdict = await guard.inspect({
      tenantId,
      callId: randomUUID(),
      routeId: provider.endpoint?.provider ?? "run",
      pseudo: new Pseudonymizer(tenantId, 1000, Date.now),
      system: req.system,
      typed: req.user,
      results: [],
    });
    req.guard?.report(verdict.result);
    if (verdict.result.decision !== "ALLOWED")
      throw new AgentError("AGT-0219", GUARD_REFUSAL_MESSAGE);
  };
  return {
    ...(provider.endpoint !== undefined ? { endpoint: provider.endpoint } : {}),
    complete: async (req) => {
      await check(req);
      return provider.complete(req);
    },
    ...(provider.stream !== undefined
      ? {
          stream: async (req, onText) => {
            await check(req);
            return provider.stream!(req, onText);
          },
        }
      : {}),
  };
}

/**
 * The provider factory: one adapter per model id, built on first use. A missing credential is an
 * error when the first run needs it, with the variable named and never its value. `fetchFn` lets a
 * test intercept every call, so no test reaches a real endpoint.
 */
export function createProviderFactory(
  cfg: Config,
  fetchFn?: typeof fetch,
): (modelId: string) => ModelProvider {
  const cache = new Map<string, ModelProvider>();
  const fetching = fetchFn !== undefined ? { fetchFn } : {};
  const egress =
    cfg.providerRegion !== undefined
      ? { egress: { region: cfg.providerRegion } }
      : {};
  const phi = createPhiGuard(cfg.phiFields);
  return (modelId) => {
    const hit = cache.get(modelId);
    if (hit !== undefined) return hit;
    let made: ModelProvider;
    switch (cfg.provider) {
      case "anthropic":
        if (cfg.anthropic.apiKey === "")
          throw new Error("ANTHROPIC_API_KEY is not set");
        made = createAnthropicProvider({
          apiKey: cfg.anthropic.apiKey,
          model: modelId,
          ...(cfg.anthropic.baseUrl !== undefined
            ? { baseUrl: cfg.anthropic.baseUrl }
            : {}),
          ...fetching,
          ...egress,
        });
        break;
      case "openai":
        if (cfg.openai.apiKey === "" || cfg.openai.baseUrl === "")
          throw new Error(
            "OPENAI_API_KEY and AGENTS_OPENAI_BASE_URL must be set",
          );
        made = createOpenAICompatibleProvider({
          baseUrl: cfg.openai.baseUrl,
          model: modelId,
          apiKey: cfg.openai.apiKey,
          auth: cfg.openai.auth,
          ...(cfg.openai.apiVersion !== undefined
            ? { query: { "api-version": cfg.openai.apiVersion } }
            : {}),
          ...fetching,
          ...egress,
        });
        break;
      case "bedrock":
        if (
          cfg.bedrock.region === "" ||
          cfg.bedrock.accessKeyId === "" ||
          cfg.bedrock.secretAccessKey === ""
        ) {
          throw new Error(
            "AWS_REGION, AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set",
          );
        }
        made = createBedrockProvider({
          region: cfg.bedrock.region,
          modelId,
          credentials: {
            accessKeyId: cfg.bedrock.accessKeyId,
            secretAccessKey: cfg.bedrock.secretAccessKey,
            ...(cfg.bedrock.sessionToken !== undefined
              ? { sessionToken: cfg.bedrock.sessionToken }
              : {}),
          },
          ...fetching,
          ...egress,
        });
        break;
    }
    made = withPhiGuard(made, phi);
    cache.set(modelId, made);
    return made;
  };
}

/**
 * Refuses text that carries a PHI-tagged field before the adapter sees it (#606). The check runs in the factory so
 * it covers every provider and every caller (agent runs and the copilot); the engine releases the budget hold for
 * this refusal because no model ran.
 */
export function withPhiGuard(
  provider: ModelProvider,
  guard: PhiGuard,
): ModelProvider {
  if (guard.fields.length === 0) return provider;
  return {
    ...(provider.endpoint !== undefined ? { endpoint: provider.endpoint } : {}),
    complete: async (req) => {
      guard.assert(req.system, req.user);
      return provider.complete(req);
    },
  };
}
