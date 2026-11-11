import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import { pathToFileURL } from "node:url";
import { AGENT_IDS, defaultCatalog } from "./agents/catalog.js";
import { createAuthenticator } from "./api/auth.js";
import type { AgentsApiDeps } from "./api/router.js";
import { createProviderFactory, loadConfig, type Config } from "./config.js";
import { CopilotAnswerCache } from "./domain/copilot.js";
import { KillSwitchRegistry } from "./domain/killswitch.js";
import { createCopilotService } from "./runtime/copilot.js";
import { createMcpConnector } from "./runtime/mcp.js";
import { createRunService } from "./runtime/service.js";
import { createAdminServer, createRequestHandler, listen, Metrics } from "./server.js";
import { PgApprovalStore } from "./store/approvals.js";
import { openAgentStore } from "./store/pg.js";

export { loadConfig };

/** The version-1 catalog with each agent's model pinned: the override for its id, else the default model (FR-47.10). */
export function agentCatalog(cfg: Config) {
  return defaultCatalog(cfg.model).map((a) => ({ ...a, modelId: cfg.agentModels[a.agentId] ?? a.modelId }));
}

const log = (level: string, message: string, extra: Record<string, unknown> = {}): void => {
  process.stdout.write(`${JSON.stringify({ level, message, service: "agents-ts", ...extra })}\n`);
};

async function main(): Promise<void> {
  const cfg = loadConfig(process.env);
  const { pool, store } = await openAgentStore(process.env);
  const inbox = new PgApprovalStore(store);
  const registry = new KillSwitchRegistry(() => Date.now());
  // The registry fails closed after 10 s without a heartbeat. The Kafka feed that drives heartbeats
  // across pods is not built (NOT-IMPLEMENTED.md), so each pod heartbeats itself and a toggle
  // reaches only the pod that took it.
  const heartbeat = setInterval(() => registry.heartbeat(), 2000);
  heartbeat.unref();
  let version = Date.now();
  const mcp = createMcpConnector({ baseUrl: cfg.mcpUrl });
  const providerFor = createProviderFactory(cfg);
  const agents = agentCatalog(cfg);
  const runs = createRunService({
    agents,
    evalPassed: (id, v) => cfg.evalPassed.has(`${id}@${v}`),
    providerFor,
    mcp,
    store,
    inbox,
    killSwitch: registry,
    config: {
      runTokenCap: cfg.runTokenCap,
      selfApprovalLimit: cfg.selfApprovalLimit,
      ...(cfg.approvalExpiryHours !== undefined ? { approvalExpiryHours: cfg.approvalExpiryHours } : {}),
      pseudonymTtlMs: 24 * 3_600_000,
    },
  });
  const copilot = createCopilotService({
    providerFor,
    mcp,
    store,
    killSwitch: registry,
    cache: new CopilotAnswerCache(),
    config: { tokenCap: cfg.copilotTokenCap, maxTurns: cfg.copilotMaxTurns, pseudonymTtlMs: 24 * 3_600_000 },
    modelId: cfg.copilotModel,
  });
  const api: AgentsApiDeps = {
    authenticate: createAuthenticator({
      issuer: cfg.jwt.issuer,
      audience: cfg.jwt.audience,
      jwksUrl: cfg.jwt.jwksUrl,
      jwks: cfg.jwt.jwksFile !== undefined ? (JSON.parse(readFileSync(cfg.jwt.jwksFile, "utf8")) as { keys: [] }) : undefined,
    }),
    registry,
    versions: { next: () => (version += 1) },
    approvals: inbox,
    // Writes the event to the tenant's outbox for the relay; the toggle answers only after it is stored.
    publish: async (event) => {
      await store.withTenant(event.tenantId, (c) =>
        c.query("insert into outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values ($1, $2, 'kill_switch', $3, $4, $5::jsonb)", [
          randomUUID(),
          event.tenantId,
          event.tenantId,
          event.type,
          JSON.stringify(event.data),
        ]),
      );
    },
    runs,
    copilot,
  };
  const metrics = new Metrics();
  const handler = createRequestHandler({
    api,
    metrics,
    ready: async () => {
      await pool.query("select 1");
      return !registry.isStale();
    },
  });
  const admin = await listen(createAdminServer(metrics), cfg.adminHost, cfg.adminPort);
  const server = await listen(createServer(handler), cfg.host, cfg.port);
  log("info", "agents listening", { provider: cfg.provider, model: cfg.model, agents: AGENT_IDS.length });
  process.once("SIGTERM", () => {
    clearInterval(heartbeat);
    admin.close();
    server.close(() => void pool.end().finally(() => process.exit(0)));
  });
}

// Only the entrypoint starts the server; importing this module (tests) has no side effects.
if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((err: unknown) => {
    process.stderr.write(`${JSON.stringify({ level: "error", message: "agents failed to start", service: "agents-ts", error: String(err) })}\n`);
    process.exit(1);
  });
}
