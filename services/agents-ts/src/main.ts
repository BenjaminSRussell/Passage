import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import { pathToFileURL } from "node:url";
import { AGENT_IDS, defaultCatalog } from "./agents/catalog.js";
import { createAgentBudgetService } from "./api/agentBudgets.js";
import { createAgentDefinitionService, type DisabledAgents } from "./api/agentDefinitions.js";
import { createAuthenticator } from "./api/auth.js";
import { PgAgentBudgets } from "./store/agentBudgets.js";
import { PgAgentDefinitions } from "./store/agentDefinitions.js";
import type { AgentsApiDeps } from "./api/router.js";
import { createRunProviderFactory, loadConfig, type Config } from "./config.js";
import { CopilotAnswerCache } from "./domain/copilot.js";
import {
  KILL_PROPAGATION_MS,
  KillSwitchRegistry,
} from "./domain/killswitch.js";
import { createLogger } from "./logging/pseudonymize.js";
import { createAiPolicyGuard } from "./runtime/aiPolicyGuard.js";
import { PgAiDataPolicyStore } from "./store/aiDataPolicy.js";
import {
  assertBoundarySupported,
  createConfiguredRouter,
} from "./runtime/aiRouter.js";
import { createBriefingService } from "./runtime/briefing.js";
import { PgBriefingFacts, PgBriefingStore } from "./store/briefing.js";
import { createAiAccessAudit } from "./runtime/aiAccessAudit.js";
import { createActService } from "./runtime/act.js";
import { createCopilotService } from "./runtime/copilot.js";
import { createNeedToKnowClient } from "./runtime/needToKnow.js";
import { createEgressGuard } from "./runtime/egressGuard.js";
import { createOutputGuard } from "./runtime/outputGuard.js";
import { createKnowledgeService } from "./runtime/knowledge.js";
import { PgKeywordIndex } from "./store/knowledge.js";
import { createMcpConnector } from "./runtime/mcp.js";
import { createVerifier, parsePublicKeys } from "./api/serviceToken.js";
import { createRunSteps } from "./runtime/steps.js";
import { createWorkflowGateway } from "./runtime/workflowGateway.js";
import { serviceSignerFromEnv } from "./api/serviceSigner.js";
import { PgRunStateStore } from "./store/runstate.js";
import { createRunService } from "./runtime/service.js";
import {
  createAdminServer,
  createRequestHandler,
  listen,
  Metrics,
} from "./server.js";
import { PgApprovalStore } from "./store/approvals.js";
import { PgActionStore } from "./store/actions.js";
import { createActionService } from "./runtime/actions.js";
import { PgKillSwitchLog } from "./store/killswitch.js";
import { KillSwitchPoller } from "./runtime/killswitch-poller.js";
import { MIGRATE_COMMAND, migrateFromEnv, openAgentStore } from "./store/pg.js";
import { PgVersionStore } from "./store/registry.js";

export { loadConfig };

/** The version-1 catalog with each agent's model pinned: the override for its id, else the default model (FR-47.10). */
export function agentCatalog(cfg: Pick<Config, "model" | "agentModels">) {
  return defaultCatalog(cfg.model).map((a) => ({
    ...a,
    modelId: cfg.agentModels[a.agentId] ?? a.modelId,
  }));
}

// Under the eu-data-boundary overlay (or LOG_PSEUDONYMIZE=true) user and person values are pseudonymized before the line is written (#605).
const log = createLogger(process.env, "agents-ts");

async function main(): Promise<void> {
  const cfg = loadConfig(process.env);
  assertBoundarySupported(cfg);
  const { pool, store } = await openAgentStore(process.env);
  const inbox = new PgApprovalStore(store);
  // Every pod polls kill_switch_change (AC-47.6); a pod that cannot read it for 10 s fails closed, and a pod that has
  // not read it yet is closed from the start (and not ready).
  const registry = new KillSwitchRegistry(
    () => Date.now(),
    KILL_PROPAGATION_MS,
    { startSynced: false },
  );
  const killLog = new PgKillSwitchLog(store, pool);
  const killPoller = new KillSwitchPoller(killLog, registry, (err) =>
    log("warn", "kill switch poll failed", { error: String(err) }),
  );
  await killPoller.start(cfg.killSwitchPollMs);
  const policyStore = new PgAiDataPolicyStore(store);
  const aiPolicyGuard = createAiPolicyGuard({
    read: (tenantId) => policyStore.read(tenantId),
    onError: (err) =>
      log("warn", "AI data policy read failed", { error: String(err) }),
  });
  const aiPolicy = {
    read: (tenantId: string) => policyStore.read(tenantId),
    write: async (input: Parameters<typeof policyStore.write>[0]) => {
      const policy = await policyStore.write(input);
      aiPolicyGuard.invalidate(input.tenantId);
      return policy;
    },
  };
  const mcp = createMcpConnector({ baseUrl: cfg.mcpUrl });
  const providerFor = createRunProviderFactory(cfg, undefined, createEgressGuard({ log, policy: aiPolicyGuard }));
  const agents = agentCatalog(cfg);
  const definitionStore = new PgAgentDefinitions(store);
  const disabledAgents: DisabledAgents = async (tenantId) => new Set((await definitionStore.disabledAgents(tenantId)).keys());
  // Every definition this pod runs is in agent_version; a run starts only when its newest eval_run passed (AGT-0204).
  const versions = new PgVersionStore(pool);
  await versions.register(agents);
  // agent_action is partitioned by month (migration 0006): keep this month and the next two in place, checked daily.
  const ensurePartitions = (): Promise<void> =>
    store.ensureActionPartitions(2).then(
      (made) =>
        made > 0
          ? log("info", "agent_action partitions created", { made })
          : undefined,
      (err: unknown) =>
        log("warn", "agent_action partitions not created", {
          error: String(err),
        }),
    );
  await ensurePartitions();
  const partitionTimer = setInterval(
    () => void ensurePartitions(),
    24 * 3_600_000,
  );
  partitionTimer.unref();
  // AGENTS_RUN_ENGINE=temporal: runs are workflows (workflow-ts) that call these steps; the in-process engine stays the dev default.
  const approvalHours = cfg.approvalExpiryHours ?? 72;
  const serviceSigner = serviceSignerFromEnv(process.env, (path) => readFileSync(path, "utf8"));
  if (cfg.runEngine === "temporal" && serviceSigner === undefined)
    throw new Error(
      "AGENTS_RUN_ENGINE=temporal needs DEPOT_SERVICE_SIGNING_KEY or DEPOT_SERVICE_SIGNING_KEY_FILE: agents signs its calls to workflow-ts",
    );
  const metrics = new Metrics();
  const outputGuard = createOutputGuard({ log, observe: (seconds, decision) => metrics.observeGuard(seconds, decision), deadlineMs: cfg.outputGuardTimeoutMs });
  const stepsService =
    cfg.runEngine === "temporal"
      ? createRunSteps({
          agents,
          providerFor,
          mcp,
          audit: store,
          state: new PgRunStateStore(store),
          inbox,
          killSwitch: registry,
          outputGuard,
          // The pseudonym map must outlive the longest approval wait, or a resumed run could not restore real values.
          pseudonymTtlMs: Math.max(24, approvalHours + 24) * 3_600_000,
        })
      : undefined;
  const runs = createRunService({
    ...(stepsService !== undefined && cfg.workflowUrl !== undefined
      ? {
          workflow: {
            steps: stepsService,
            state: new PgRunStateStore(store),
            gateway: createWorkflowGateway({
              baseUrl: cfg.workflowUrl,
              signer: serviceSigner!,
            }),
          },
        }
      : {}),
    outputGuard,
    agents,
    evalPassed: (agent) => versions.evalPassed(agent),
    providerFor,
    mcp,
    store,
    inbox,
    killSwitch: registry,
    disabledAgents,
    config: {
      runTokenCap: cfg.runTokenCap,
      selfApprovalLimit: cfg.selfApprovalLimit,
      ...(cfg.approvalExpiryHours !== undefined
        ? { approvalExpiryHours: cfg.approvalExpiryHours }
        : {}),
      pseudonymTtlMs: 24 * 3_600_000,
    },
  });
  // Keyword half of the hybrid search only until an embedding port and a vector index are configured (NOT-IMPLEMENTED.md).
  const knowledge = createKnowledgeService({
    keywords: new PgKeywordIndex(store),
  });
  if (cfg.complianceUrl === undefined)
    log(
      "warn",
      "AGENTS_COMPLIANCE_URL is not set: every record is UNKNOWN and is withheld from AI calls",
      { boundary: cfg.boundary },
    );
  else if (serviceSigner === undefined)
    log(
      "warn",
      "DEPOT_SERVICE_SIGNING_KEY is not set: agents cannot sign its call to compliance, so every record is UNKNOWN and is withheld from AI calls",
      { boundary: cfg.boundary },
    );
  if (cfg.auditUrl === undefined || serviceSigner === undefined)
    log(
      "warn",
      "AGENTS_AUDIT_URL or DEPOT_SERVICE_SIGNING_KEY is not set: AI data access cannot be audited, so every copilot read is refused",
      { boundary: cfg.boundary },
    );
  const copilot = createCopilotService({
    aiAccess: createAiAccessAudit({ baseUrl: cfg.auditUrl, signer: serviceSigner, log }),
    log,
    onTaskFallback: (reason) => metrics.countTaskFallback(reason),
    guard: createEgressGuard({ log, policy: aiPolicyGuard, observe: (seconds, decision) => metrics.observeGuard(seconds, decision) }),
    outputGuard,
    knowledge,
    router: createConfiguredRouter(cfg, { log, ...(serviceSigner !== undefined ? { signer: serviceSigner } : {}) }),
    needToKnow: createNeedToKnowClient({
      baseUrl: cfg.complianceUrl,
      authHeaders: (caller) => {
        if (serviceSigner === undefined) throw new Error("no service signing key is configured");
        if (caller.legalEntityId === undefined) throw new Error("the caller's token names no legal entity");
        return { "x-depot-service-token": serviceSigner.mintForCompliance({ tenantId: caller.tenantId, legalEntityId: caller.legalEntityId, actor: caller.userId }) };
      },
      cacheMs: cfg.needToKnowCacheMs,
      log,
    }),
    mcp,
    store,
    killSwitch: registry,
    cache: new CopilotAnswerCache(),
    config: {
      tokenCap: cfg.copilotTokenCap,
      maxTurns: cfg.copilotMaxTurns,
      taskConnectTimeoutMs: cfg.taskConnectTimeoutMs,
      pseudonymTtlMs: 24 * 3_600_000,
    },
    modelId: cfg.copilotModel,
  });
  const act = createActService({ copilot, runs, mcp, store, outputGuard, modelId: cfg.copilotModel, tokenCap: cfg.copilotTokenCap, pseudonymTtlMs: 24 * 3_600_000, log });
  const api: AgentsApiDeps = {
    authenticate: createAuthenticator({
      issuer: cfg.jwt.issuer,
      audience: cfg.jwt.audience,
      jwksUrl: cfg.jwt.jwksUrl,
      jwks:
        cfg.jwt.jwksFile !== undefined
          ? (JSON.parse(readFileSync(cfg.jwt.jwksFile, "utf8")) as { keys: [] })
          : undefined,
    }),
    registry,
    aiEnabled: cfg.aiEnabled,
    // The toggle, its version and its killswitch.changed outbox row are stored in one transaction.
    log: killLog,
    approvals: inbox,
    approvalReader: inbox,
    disabledAgents,
    aiPolicy,
    runs,
    actions: createActionService({
      runs: store,
      actions: new PgActionStore(store),
    }),
    ...(stepsService !== undefined
      ? {
          steps: {
            service: stepsService,
            verifier: createVerifier({
              keys: parsePublicKeys(cfg.servicePublicKeys),
            }),
          },
        }
      : {}),
    copilot,
    act,
    knowledge,
    budgets: createAgentBudgetService({ store: new PgAgentBudgets(store) }),
    definitions: createAgentDefinitionService({ agents, store: definitionStore }),
    briefings: createBriefingService({ store: new PgBriefingStore(store), facts: new PgBriefingFacts(store), disabledAgents, log }),
  };
  const handler = createRequestHandler({
    api,
    metrics,
    ready: async () => {
      await pool.query("select 1");
      return !registry.isStale();
    },
  });
  const admin = await listen(
    createAdminServer(metrics),
    cfg.adminHost,
    cfg.adminPort,
  );
  const server = await listen(createServer(handler), cfg.host, cfg.port);
  log("info", "agents listening", {
    provider: cfg.provider,
    model: cfg.model,
    agents: AGENT_IDS.length,
  });
  process.once("SIGTERM", () => {
    killPoller.stop();
    clearInterval(partitionTimer);
    admin.close();
    server.close(() => void pool.end().finally(() => process.exit(0)));
  });
}

// Only the entrypoint starts the server; importing this module (tests) has no side effects.
// `node dist/main.js migrate` applies the migrations as the owner role and exits: the chart's PreSync migration Job (D28).
const isEntrypoint =
  process.argv[1] !== undefined &&
  import.meta.url === pathToFileURL(process.argv[1]).href;
if (isEntrypoint && process.argv[2] === MIGRATE_COMMAND) {
  migrateFromEnv(process.env).then(
    () => {
      process.stdout.write(
        `${JSON.stringify({ level: "info", message: "migrations applied", service: "agents-ts" })}\n`,
      );
      process.exit(0);
    },
    (err: unknown) => {
      process.stderr.write(
        `${JSON.stringify({ level: "error", message: "migrations failed", service: "agents-ts", error: String(err) })}\n`,
      );
      process.exit(1);
    },
  );
} else if (isEntrypoint) {
  main().catch((err: unknown) => {
    process.stderr.write(
      `${JSON.stringify({ level: "error", message: "agents failed to start", service: "agents-ts", error: String(err) })}\n`,
    );
    process.exit(1);
  });
}
