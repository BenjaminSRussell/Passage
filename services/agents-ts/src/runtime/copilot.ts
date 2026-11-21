import { randomUUID } from "node:crypto";
import { ApiError, asObject, type Principal } from "../api/principal.js";
import { AGENT_INVOKE_DUTY, INJECTION_GUARD } from "../agents/catalog.js";
import { buildActionRecord, type ActionRecord, type Decision } from "../domain/audit.js";
import { assertPromptWithinCap, estimateTokens, outputBudgetForCall } from "../domain/budget.js";
import {
  CopilotAnswerCache,
  COPILOT_MODES,
  gateCopilotTools,
  missingDutyMessage,
  type CopilotMode,
} from "../domain/copilot.js";
import { gateCitations, type Citation } from "../domain/knowledge.js";
import { exclusionNotice, NO_ROUTE_MESSAGE, permittedOutput, recordsOf } from "../domain/modelRoute.js";
import { AMOUNT_WITHHELD, AMOUNT_WITHHELD_NOTICE, applyFinancialPolicy } from "../domain/financialFields.js";
import { taskInstruction } from "../domain/routeCatalog.js";
import { classifyByRules, decideRoute, type RouteResult } from "../domain/promptClass.js";
import { assertNoPii, Pseudonymizer } from "../domain/redaction.js";
import { AgentError } from "../errors.js";
import { ProviderError, proposeTool } from "../providers/wire.js";
import type { ModelEndpoint } from "../cloudprofile/index.js";
import type { AuditPort, BudgetPort, KillSwitchPort, ModelUsage, RunRequest, RunResult } from "./engine.js";
import type { AiAccessAudit, AiAccessRow } from "./aiAccessAudit.js";
import type { AiPlan, AiRouter } from "./aiRouter.js";
import { EGRESS_GUARD_TOOL, GUARD_POLICY_VERSION, GUARD_REFUSAL_MESSAGE, highestClassOf, type DataClass, type EgressGuard, type GuardContext, type GuardResult, type OutputGuardReason } from "./egressGuard.js";
import { createChunkReleaser, createOutputGuard, OUTPUT_GUARD_CODE, OUTPUT_GUARD_MESSAGE, OUTPUT_GUARD_TOOL, presentForCaller, stripUnreadableCitations, type OutputGuard } from "./outputGuard.js";
import { CUI_VIEW_DUTY, type NeedToKnowClient } from "./needToKnow.js";
import type { McpConnector, McpSession } from "./mcp.js";
import type { LogFn } from "../logging/pseudonymize.js";
import type { KnowledgeService } from "./knowledge.js";

/** The duty to use the copilot (seeded in policies/authz/data.json). */
export const COPILOT_DUTY = "mcp.copilot.use";
export const COPILOT_AGENT_ID = "copilot";
export const MAX_MESSAGE_CHARS = 4000;
export const TOOL_LIMIT_MESSAGE = "I could not complete the answer within the tool-call limit.";
export const KNOWLEDGE_SEARCH_TOOL = "depot.knowledge.search";
/** Searching the knowledge index is part of using the copilot: the route is seeded under the same duty (policies/authz/data.json). */
export const KNOWLEDGE_READ_DUTY = COPILOT_DUTY;
export const SEARCH_TOOLS_TOOL = "depot.meta.search_tools";
export const HELP_SEARCH_TOOL = "depot.help.search";
const TASK_TOOLS: readonly string[] = [SEARCH_TOOLS_TOOL, HELP_SEARCH_TOOL];
export const TASK_CONNECT_TIMEOUT_MS = 2000;
export const TASK_MAX_TURNS = 2;
export const CONNECT_TIMEOUT_CODE = "GATEWAY_CONNECT_TIMEOUT";

/** Request and response of POST /api/v1/copilot/chat; the shape apps/web/src/modules/copilot/api/client.ts validates. */
export interface ChatRequest {
  mode: CopilotMode;
  message: string;
  recordRef?: string;
}

export interface ChatCitation {
  sourceType: "RECORD" | "METRIC" | "ARTICLE" | "DOC" | "POLICY";
  sourceRef: string;
  label: string;
}

export interface ChatToolCall {
  tool: string;
  status: "OK" | "DENIED" | "ERROR";
  code?: string;
}

export interface ChatResponse {
  answer: string;
  citations: ChatCitation[];
  toolCalls: ChatToolCall[];
  refusal: { code: string; message: string } | null;
  excluded?: { count: number; kinds: string[]; reason: "NO_APPROVED_ROUTE" };
  dataClass?: DataClass;
  route?: RouteResult;
  /** Always true: generated text is labelled as such in the UI (FR-47.12). */
  generated: true;
}

/** What a caller of `chat` can listen for while the answer is written. */
export interface ChatHooks {
  /**
   * The answer as the model writes it, with pseudonyms already restored (FR-47.3). Called only when the provider
   * can stream and no answer is served from the cache; the finished ChatResponse stays the authoritative answer.
   * Nothing is sent for a chat whose knowledge results fail the citation gate (AGT-0212): that answer is refused.
   */
  onToken?: (text: string) => void;
  /** Called once when the output guard withdraws the answer; no token follows it. */
  onWithdraw?: () => void;
  /** Aborts the model call when the caller is gone. */
  signal?: AbortSignal;
  act?: { tool: string; inputSchema: Record<string, unknown> | undefined; maxTurns: number };
}

export interface CopilotStore extends AuditPort, BudgetPort {}

export interface CopilotConfig {
  /** Token cap of one chat turn, input and output (FR-47.11). */
  tokenCap: number;
  /** Most model turns of one chat (read-tool calls feed further turns). */
  maxTurns: number;
  pseudonymTtlMs: number;
  /** How long task mode waits for the gateway session before answering without tools; defaults to TASK_CONNECT_TIMEOUT_MS. */
  taskConnectTimeoutMs?: number;
}

export interface CopilotDeps {
  router: AiRouter;
  mcp: McpConnector;
  store: CopilotStore;
  killSwitch: KillSwitchPort;
  cache: CopilotAnswerCache;
  config: CopilotConfig;
  modelId: string;
  /** The knowledge index. When set, `depot.knowledge.search` is a read tool of the copilot to everyone who may use the copilot. */
  knowledge?: KnowledgeService;
  guard: EgressGuard;
  outputGuard?: OutputGuard;
  needToKnow?: NeedToKnowClient;
  /** Every tool result is written to the AI data-access audit before the model sees it; a failed write refuses the call. */
  aiAccess: AiAccessAudit;
  log?: LogFn;
  /** Task mode answered without a tool: `gateway_unavailable` (the session failed to open) or `no_task_tools` (the caller may call neither). */
  onTaskFallback?: (reason: TaskFallbackReason) => void;
  now?: () => Date;
}

export type TaskFallbackReason = "gateway_unavailable" | "no_task_tools";

const MODE_PROMPTS: Record<CopilotMode, string> = {
  ask: "Answer the user's question about their Depot data. Use the read tools to fetch what you need and state only what the tool results show.",
  explain:
    "Explain the record the user names: what it is, how it got into its state and what usually happens next. Read depot.platform.audit_trail.get, depot.platform.document_flow.get and depot.workflow.history.get first; state only what the tools show. Say which part you could not read instead of guessing it. Count related records the user cannot open and never name them. List changes oldest first with who, when and what, and say plainly when there is no change history. Suggest next steps only as links to a screen or a proposal for a person to approve, never as something you did.",
  draft: "Draft the document text the user asks for. The draft is for a person to review and is never sent or posted by you.",
  summarize: "Summarize the workflow or case history the user names. Use the read tools; state only what they show.",
  route:
    'Classify the user\'s message as exactly one of ask (a question about data), navigate (open, list, find or create a screen or record) or act (change a record or amount). Call no tool. Answer with only this JSON: {"kind":"ask|navigate|act","confidence":0 to 1,"runnerUp":{"kind":"ask|navigate|act","confidence":0 to 1}}.',
  task: taskInstruction(),
  act: "Extract the parameters of one action from the user's request. Use the read tools to look up what the request names; never invent an id. You do not run the action: a person approves it later.",
};

const READ_TOOL = /\.(get|list|search)$/;

/** Validates the POST body: a known mode, a message of 1 to 4000 characters and an optional record reference. */
export function parseChatRequest(raw: unknown): ChatRequest {
  const body = asObject(raw, ["mode", "message", "recordRef"]);
  const { mode, message, recordRef } = body;
  if (typeof mode !== "string" || !(COPILOT_MODES as readonly string[]).includes(mode)) {
    throw new ApiError(400, "AGT-0400", `mode must be one of ${COPILOT_MODES.join(", ")}`);
  }
  if (typeof message !== "string" || message.trim() === "" || message.length > MAX_MESSAGE_CHARS) {
    throw new ApiError(400, "AGT-0400", `message is required, at most ${MAX_MESSAGE_CHARS} characters`);
  }
  if (recordRef !== undefined && (typeof recordRef !== "string" || recordRef === "" || recordRef.length > 200)) {
    throw new ApiError(400, "AGT-0400", "recordRef must be a string of 1 to 200 characters");
  }
  return { mode: mode as CopilotMode, message, ...(recordRef !== undefined ? { recordRef } : {}) };
}

function systemPrompt(policy: ModePolicy, mode: CopilotMode, tools: readonly string[], act?: ChatHooks["act"]): string {
  if (policy.toolless || (policy.task && tools.length === 0)) return `${MODE_PROMPTS[mode]} ${INJECTION_GUARD}`;
  if (policy.task) return `${MODE_PROMPTS[mode]} Tools you may call: ${tools.join(", ")}. To call one, propose it with its arguments; you will receive its result. ${INJECTION_GUARD}`;
  const actTail =
    act === undefined
      ? ""
      : ` The action is ${act.tool}; its input schema is ${JSON.stringify(act.inputSchema ?? {})}. When you have enough, answer with only this JSON: {"params":{...}} using the schema's field names, or {"clarifyingQuestion":"..."} when a required value or the record the request means is missing or ambiguous. Use an entity id only if it appears in the request or its record reference.`;
  return `${MODE_PROMPTS[mode]}${actTail} Tools you may call: ${tools.join(", ")}. To call one, propose it with its arguments; you will receive its result and may call another. When you have enough, answer in plain language without proposing a tool. ${INJECTION_GUARD}`;
}

export const TYPED_REPLACED_NOTICE = "I replaced sensitive numbers in your message with placeholders before sending it to the AI model.";

const clip = (s: string, n: number): string => (s.length > n ? `${s.slice(0, n)}...` : s);

const errorClassOf = (err: unknown): string => {
  if (!(err instanceof Error)) return typeof err;
  const code = (err as { code?: unknown }).code;
  return typeof code === "string" ? code.slice(0, 40) : err.name;
};

async function connectWithin(connect: Promise<McpSession>, ms: number): Promise<McpSession> {
  let timer: NodeJS.Timeout | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(Object.assign(new Error("gateway connect timed out"), { code: CONNECT_TIMEOUT_CODE })), ms);
  });
  try {
    return await Promise.race([connect, timeout]);
  } finally {
    clearTimeout(timer);
    connect.catch(() => undefined);
  }
}

type ModePolicy = ReturnType<typeof modePolicy>;

function modePolicy(mode: CopilotMode, hasAct: boolean) {
  const toolless = mode === "route";
  const task = mode === "task";
  return {
    toolless,
    task,
    cacheable: !toolless && !task && !hasAct,
    knowledgeTool: !toolless && !task,
    /** Answer-only turns offer the provider no tool at all, so a proposal can never come back. */
    answerOnly: (allowed: number): boolean => toolless || (task && allowed === 0),
    maxTurns: (configured: number): number => (task ? Math.min(TASK_MAX_TURNS, configured) : configured),
    offers: (tool: string): boolean => (task ? TASK_TOOLS.includes(tool) : READ_TOOL.test(tool)),
    skipsRecords: (tool: string): boolean => tool === KNOWLEDGE_SEARCH_TOOL || (task && TASK_TOOLS.includes(tool)),
    refusesUnofferedTool: !task,
  };
}

/**
 * The copilot chat backend (FR-47.3, BR-47.11). One chat turn: the caller's own tool view (the
 * gateway lists only tools whose duty the caller holds), the model asked in up to `maxTurns` rounds
 * with read tools only, every prompt redacted before the provider sees it, a budget hold settled with
 * the tokens used, one audited run (`agent_id` copilot) with an action row per tool call, and answers
 * cached per tenant, user, duty set and mode for 5 minutes. A tool the model names outside the
 * caller's allowed set is answered AGT-0203 and never called.
 */
export function createCopilotService(deps: CopilotDeps) {
  const now = deps.now ?? (() => new Date());
  const guard = deps.guard;
  const outputGuard = deps.outputGuard ?? createOutputGuard();
  const needToKnow: NeedToKnowClient = deps.needToKnow ?? { enforced: async (c) => !c.duties.has(CUI_VIEW_DUTY) };
  return {
    async chat(caller: Principal, token: string, req: ChatRequest, hooks: ChatHooks = {}): Promise<ChatResponse> {
      if (deps.killSwitch.isHalted(caller.tenantId)) throw new AgentError("AGT-0201", "agent runs are halted by the kill switch");
      const prompt = req.recordRef === undefined ? req.message : `${req.message}\n[record: ${req.recordRef}]`;
      const asker = { tenantId: caller.tenantId, userId: caller.userId, duties: caller.duties };
      const rules = req.mode === "route" ? classifyByRules(req.message) : undefined;
      const policy = modePolicy(req.mode, hooks.act !== undefined);
      const cacheable = policy.cacheable;
      const cached = cacheable ? deps.cache.get(asker, req.mode, prompt) : undefined;
      if (cached !== undefined) return JSON.parse(cached) as ChatResponse;

      const runId = randomUUID();
      let session: McpSession | undefined;
      let gatewayError: string | undefined;
      if (!policy.toolless) {
        try {
          const opening = deps.mcp.connect(caller, token, runId);
          session = policy.task ? await connectWithin(opening, deps.config.taskConnectTimeoutMs ?? TASK_CONNECT_TIMEOUT_MS) : await opening;
        } catch (err) {
          if (!policy.task) throw err;
          gatewayError = errorClassOf(err);
        }
      }
      const candidates = session === undefined ? [] : session.tools.filter((t) => t.approvalMode === "none" && policy.offers(t.name));
      if (policy.knowledgeTool && session !== undefined && deps.knowledge !== undefined && !candidates.some((t) => t.name === KNOWLEDGE_SEARCH_TOOL)) {
        candidates.push({ name: KNOWLEDGE_SEARCH_TOOL, requiredDuty: KNOWLEDGE_READ_DUTY, approvalMode: "none" } as (typeof candidates)[number]);
      }
      const gate = gateCopilotTools(caller.duties, candidates.map((t) => ({ name: t.name, requiredDuty: t.requiredDuty })));
      const answerOnly = policy.answerOnly(gate.allowed.length);
      if (policy.task && answerOnly) {
        const reason: TaskFallbackReason = gatewayError !== undefined ? "gateway_unavailable" : "no_task_tools";
        deps.log?.("warn", "copilot task mode fell back to an answer-only turn", { tenantId: caller.tenantId, runId, reason, ...(gatewayError !== undefined ? { errorClass: gatewayError } : {}) });
        deps.onTaskFallback?.(reason);
      }
      if (session !== undefined && !policy.task && gate.allowed.length === 0) {
        const first = gate.denied[0];
        return refusal(first !== undefined ? "AGT-0203" : "AGT-0212", first?.message ?? "No tool is available to answer this.");
      }

      // TODO: DEPOT-306 createAiPolicyGuard.check is still unwired
      let plan: AiPlan;
      try {
        plan = await deps.router.plan({ caller, modelId: deps.modelId, refs: [] });
      } catch (err) {
        if (err instanceof AgentError && err.code === "AGT-0218") {
          await recordBoundaryBlock(deps.store, auditRequest(runId, caller, deps.modelId, deps.config.tokenCap), now());
          return refusal("AGT-0218", NO_ROUTE_MESSAGE);
        }
        throw err;
      }

      const month = now().toISOString().slice(0, 7);
      const hold = await deps.store.reserve({
        tenantId: caller.tenantId,
        agentId: COPILOT_AGENT_ID,
        runId,
        month,
        tokensIn: deps.config.tokenCap,
        tokensOut: deps.config.tokenCap,
        ttlMs: 10 * 60_000,
      });
      const runReq = auditRequest(runId, caller, deps.modelId, deps.config.tokenCap);
      try {
        await deps.store.startRun(runReq);
      } catch (err) {
        await deps.store.settle(hold, { tokensIn: 0, tokensOut: 0 }).catch(() => undefined);
        throw err;
      }

      const pseudo = new Pseudonymizer(caller.tenantId, deps.config.pseudonymTtlMs, () => now().getTime());
      const actions: ActionRecord[] = [];
      const toolCalls: ChatToolCall[] = [];
      const citations: ChatCitation[] = [];
      const knowledge: Citation[] = [];
      const usage: ModelUsage = { tokensIn: 0, tokensOut: 0 };
      let answer = "";
      let refused: ChatResponse["refusal"] = null;
      let settleSpend = true;
      let provider = plan.provider;
      let routeId = plan.routeId;
      let dataClass: DataClass = "P0";
      let typedReplaced = 0;
      let withheldCount = 0;
      let amountsWithheld = 0;
      let needToKnowHidden = 0;
      let cuiDerived = false;
      let policyDropped = 0;
      let declared: DataClass = "P0";
      let policyCell: "tenant" | "default" | undefined;
      let policyVersion: number | undefined;
      const withheldKinds = new Set<string>();
      const system = systemPrompt(policy, req.mode, gate.allowed, hooks.act);
      const results: string[] = [];
      const pending: Pick<AiAccessRow, "tool" | "entityType" | "recordIds" | "recordClasses">[] = [];
      const sink = hooks.onToken;
      const recordIds = new Set<string>();
      let guardContext: GuardContext = { inputTokens: new Set(), inputRecordIds: new Set(), highestClass: "P0" };
      const outcome: { failure?: OutputGuardReason } = {};
      const verify = async (text: string, partial: boolean): Promise<string | undefined> => {
        const verdict = await outputGuard.check({ tenantId: caller.tenantId, text, context: guardContext, pseudo, duties: caller.duties, declaredClass: dataClass, partial });
        dataClass = verdict.dataClass;
        if (verdict.ok) return verdict.text;
        if (outcome.failure === undefined) {
          outcome.failure = verdict.reason;
          hooks.onWithdraw?.();
        }
        return undefined;
      };
      const present = (text: string): string => stripUnreadableCitations(presentForCaller(outputGuard, pseudo, text, caller.duties), guardContext.inputRecordIds);
      // Text is shown a sentence at a time, after the output guard passed it, unless a knowledge result already makes the answer unanswerable.
      const streamable = (): boolean => knowledge.length === 0 || gateCitations(knowledge).answerable;
      const releaser =
        sink === undefined
          ? undefined
          : createChunkReleaser(async (chunk) => {
              if (!streamable()) return true;
              const clean = await verify(chunk, true);
              if (clean === undefined) return false;
              sink(present(clean));
              return true;
            });
      const row = (tool: string, input: unknown, output: unknown, decision: Decision, why?: string): void => {
        actions.push(
          buildActionRecord({
            runId,
            seq: actions.length + 1,
            toolName: tool,
            inputRedacted: pseudo.redact(JSON.stringify(input) ?? "null"),
            outputRedacted: pseudo.redact(JSON.stringify(output) ?? "null"),
            approverUserId: null,
            decision,
            decidedAt: now().toISOString(),
            ...(why !== undefined ? { reason: why } : {}),
          }),
        );
      };

      const guardRow = (r: GuardResult, toolName = EGRESS_GUARD_TOOL): void => {
        actions.push(
          buildActionRecord({
            runId,
            seq: actions.length + 1,
            toolName,
            inputRedacted: "{}",
            outputRedacted: JSON.stringify({ decision: r.decision, highestClass: r.highestClass }),
            approverUserId: null,
            decision: r.decision === "BLOCKED" || r.decision === "OUTPUT_BLOCKED" ? "DENIED" : "AUTO",
            decidedAt: now().toISOString(),
            guardResult: r,
            ...(r.errorCode !== undefined ? { reason: r.errorCode } : {}),
          }),
        );
      };

      try {
        const turns = rules === undefined ? (answerOnly ? 1 : (hooks.act?.maxTurns ?? policy.maxTurns(deps.config.maxTurns))) : rules.status === "decided" ? 0 : 1;
        for (let turn = 0; turn < turns; turn += 1) {
          const verdict = await guard.inspect({ tenantId: caller.tenantId, callId: randomUUID(), routeId, pseudo, system, typed: prompt, results, recordIds, excluded: Object.fromEntries(Object.entries({ [AMOUNT_WITHHELD]: amountsWithheld, NO_APPROVED_ROUTE: withheldCount, NEED_TO_KNOW: needToKnowHidden, TENANT_POLICY: policyDropped }).filter(([, n]) => n > 0)), declared, ...(policyCell !== undefined ? { policyCell } : {}), ...(policyVersion !== undefined ? { policyVersion } : {}) });
          guardRow(verdict.result);
          dataClass = highestClassOf([dataClass, verdict.result.highestClass]);
          if (pending.length > 0) {
            const g = verdict.result;
            const occurredAt = now().toISOString();
            try {
              await deps.aiAccess.record(
                caller,
                pending.map((r) => ({
                  ...r,
                  agentId: COPILOT_AGENT_ID,
                  runId,
                  highestClass: g.highestClass,
                  routeId: g.routeId,
                  redactionCounts: g.counts,
                  excludedCounts: g.excluded,
                  policyVersion: g.policyVersion,
                  decision: g.decision,
                  traceId: runId,
                  occurredAt,
                })),
              );
            } catch {
              refused = { code: "AGT-0232", message: GUARD_REFUSAL_MESSAGE };
              answer = refused.message;
              break;
            }
            pending.length = 0;
          }
          if (verdict.result.decision === "BLOCKED") {
            refused = { code: "AGT-0219", message: GUARD_REFUSAL_MESSAGE };
            answer = refused.message;
            break;
          }
          typedReplaced = Math.max(typedReplaced, verdict.typedReplaced);
          guardContext = verdict.context;
          const context = verdict.user;
          assertNoPii(context);
          const noTools = answerOnly || (policy.task && turn === turns - 1);
          const toolsText = JSON.stringify(proposeTool(gate.allowed, noTools));
          assertPromptWithinCap([system, context, toolsText], deps.config.tokenCap - usage.tokensIn);
          const maxOutputTokens = outputBudgetForCall(
            deps.config.tokenCap,
            usage.tokensIn + usage.tokensOut,
            estimateTokens(system) + estimateTokens(context) + estimateTokens(toolsText),
          );
          let response;
          try {
            const call = { system, user: context, allowedTools: gate.allowed, maxOutputTokens, ...(noTools ? { answerOnly: true } : {}), ...(hooks.signal !== undefined ? { signal: hooks.signal } : {}) };
            response = releaser !== undefined && provider.stream !== undefined ? await provider.stream(call, (delta) => releaser.push(delta)) : await provider.complete(call);
            await releaser?.end();
          } catch (err) {
            if (err instanceof ProviderError && err.unbilled) settleSpend = false;
            throw err;
          }
          const used = response.usage ?? { tokensIn: estimateTokens(system) + estimateTokens(context), tokensOut: estimateTokens(response.text) };
          usage.tokensIn += used.tokensIn;
          usage.tokensOut += used.tokensOut;
          answer = response.text;
          const p = response.proposal;
          if (outcome.failure !== undefined || p === undefined || session === undefined || noTools) break;
          if (!gate.allowed.includes(p.tool) && !policy.refusesUnofferedTool) {
            toolCalls.push({ tool: p.tool, status: "DENIED", code: "AGT-0203" });
            row(p.tool, p.args, { error: "AGT-0203" }, "DENIED");
            answer = "";
            break;
          }
          if (!gate.allowed.includes(p.tool)) {
            // BR-47.11: a tool outside the caller's allowed set is refused with AGT-0203, never called.
            const duty = session.describe(p.tool)?.requiredDuty ?? AGENT_INVOKE_DUTY;
            toolCalls.push({ tool: p.tool, status: "DENIED", code: "AGT-0203" });
            row(p.tool, p.args, { error: "AGT-0203" }, "DENIED");
            refused = { code: "AGT-0203", message: missingDutyMessage(p.tool, duty) };
            answer = refused.message;
            break;
          }
          const args = JSON.parse(pseudo.restore(JSON.stringify(p.args))) as Record<string, unknown>;
          const out =
            p.tool === KNOWLEDGE_SEARCH_TOOL && deps.knowledge !== undefined
              ? await searchKnowledge(deps.knowledge, caller.tenantId, args)
              : await session.call(p.tool, args);
          if (!out.ok) {
            const denied = out.code === "AGT-0203";
            toolCalls.push({ tool: p.tool, status: denied ? "DENIED" : "ERROR", code: out.code });
            row(p.tool, p.args, { error: out.code }, denied ? "DENIED" : "AUTO");
            results.push(`Tool ${p.tool} failed with ${out.code}.`);
            if (denied) {
              refused = { code: "AGT-0203", message: out.message };
              answer = out.message;
              break;
            }
            continue;
          }
          toolCalls.push({ tool: p.tool, status: "OK" });
          const found = policy.skipsRecords(p.tool) ? [] : recordsOf(p.tool, out.output);
          const next = found.length === 0 ? undefined : await deps.router.plan({ caller, modelId: deps.modelId, refs: found });
          answer = "";
          const excludedHere = next?.excluded ?? [];
          const hiddenHere = excludedHere.length === 0 && (next?.restricted.length ?? 0) > 0 && (await needToKnow.enforced(caller)) ? (next?.restricted ?? []) : [];
          needToKnowHidden += hiddenHere.length;
          if ((next?.restricted.length ?? 0) > 0) cuiDerived = true;
          const dropped = [...excludedHere, ...hiddenHere];
          const shown = dropped.length === 0 ? out.output : permittedOutput(out.output, dropped);
          if (excludedHere.length > 0) {
            withheldCount += excludedHere.length;
            for (const r of excludedHere) withheldKinds.add(r.entityType === "" ? "unidentified" : r.entityType);
          }
          if (shown === undefined) {
            row(p.tool, p.args, excludedHere.length > 0 ? { withheld: "NO_APPROVED_ROUTE" } : { records: 0 }, "AUTO", "read");
            results.push(excludedHere.length > 0 ? `Result of ${p.tool} was withheld: its data has no approved AI route.` : `Tool ${p.tool} returned no records.`);
            continue;
          }
          const governed = applyFinancialPolicy(shown, pseudo);
          amountsWithheld += governed.withheld;
          const admission = await guard.admit({ tenantId: caller.tenantId, tool: p.tool, text: JSON.stringify(governed.value) ?? "null" });
          policyCell = admission.policyCell;
          policyVersion = admission.policyVersion;
          if (admission.failure !== undefined) {
            guardRow({ callId: randomUUID(), highestClass: admission.class, routeId, policyVersion: admission.policyVersion, policyCell: admission.policyCell, counts: {}, excluded: {}, decision: "BLOCKED", errorCode: admission.failure });
            refused = { code: "AGT-0219", message: GUARD_REFUSAL_MESSAGE };
            answer = refused.message;
            break;
          }
          if (!admission.pass) {
            policyDropped += 1;
            row(p.tool, p.args, { records: 0 }, "AUTO", "read");
            results.push(`Result of ${p.tool} was left out: the tenant AI data policy does not allow it.`);
            continue;
          }
          declared = highestClassOf([declared, admission.class]);
          row(p.tool, p.args, governed.value, "AUTO", "read");
          const shownRefs = found.flatMap((ref, i) => (dropped.includes(ref) || ref.recordId === "" ? [] : [{ id: ref.recordId, class: next?.classes[i] ?? "UNKNOWN" }]));
          pending.push({
            tool: p.tool,
            entityType: (p.tool === KNOWLEDGE_SEARCH_TOOL ? "knowledge" : found[0]?.entityType) || "unidentified",
            recordIds: shownRefs.map((r) => auditableId(r.id)),
            recordClasses: shownRefs.map((r) => r.class),
          });
          if (next !== undefined) {
            provider = next.provider;
            routeId = next.routeId;
          }
          if (out.ref !== undefined) recordIds.add(out.ref);
          for (const r of found) if (r.recordId !== "" && !dropped.some((e) => e.recordId === r.recordId)) recordIds.add(r.recordId);
          if (p.tool === KNOWLEDGE_SEARCH_TOOL) knowledge.push(...knowledgeCitations(out.output));
          else citations.push({ sourceType: "RECORD", sourceRef: out.ref ?? p.tool, label: p.tool });
          const typed = req.mode === "explain" ? typedRecordCitation(req.recordRef, p.tool, args, out.output) : undefined;
          if (typed !== undefined && !citations.some((c) => c.sourceType === "RECORD" && c.sourceRef.endsWith(typed.sourceRef.slice(typed.sourceRef.indexOf("/"))))) citations.push(typed);
          results.push(`Result of ${p.tool}: ${clip(JSON.stringify(governed.value) ?? "null", 4000)}`);
        }
        if (refused === null && knowledge.length > 0) {
          const g = gateCitations(knowledge);
          if (!g.answerable) {
            refused = { code: g.code, message: g.message };
            answer = g.message;
          } else {
            for (const c of g.citations) citations.push({ sourceType: c.sourceType, sourceRef: c.sourceRef, label: c.section });
          }
        }
        if (answer === "" && refused === null && !answerOnly) answer = TOOL_LIMIT_MESSAGE;
        if (refused === null && outcome.failure === undefined && answer !== "") answer = (await verify(answer, false)) ?? answer;
        if (outcome.failure !== undefined) {
          refused = { code: OUTPUT_GUARD_CODE, message: OUTPUT_GUARD_MESSAGE };
          answer = refused.message;
          citations.length = 0;
          guardRow(
            { callId: randomUUID(), highestClass: dataClass, routeId, policyVersion: GUARD_POLICY_VERSION, counts: {}, excluded: {}, decision: "OUTPUT_BLOCKED", errorCode: outcome.failure },
            OUTPUT_GUARD_TOOL,
          );
        }
        const noticed = hooks.act === undefined && !policy.task;
        const notice = noticed && withheldCount > 0 ? `\n\n${exclusionNotice([...withheldKinds], withheldCount)}` : "";
        const replacedNotice = noticed && typedReplaced > 0 ? `\n\n${TYPED_REPLACED_NOTICE}` : "";
        const policyNotice = noticed && policyDropped > 0 ? `\n\n${policyDropped} tool result(s) were left out because your AI data policy does not allow that data.` : "";
        const amountNotice = noticed && amountsWithheld > 0 ? `\n\n${AMOUNT_WITHHELD_NOTICE}` : "";
        const display = outcome.failure !== undefined ? answer : (refused !== null ? answer : present(answer)) + notice + replacedNotice + amountNotice + policyNotice;
        const result = finished(runId, "COMMITTED", actions, usage, display, provider.endpoint);
        await deps.store.finishRun(runReq, result);
        const out: ChatResponse = {
          answer: display,
          citations,
          toolCalls,
          refusal: refused,
          dataClass,
          ...(rules !== undefined && refused === null ? { route: decideRoute(rules, answer) } : {}),
          ...(withheldCount > 0 ? { excluded: { count: withheldCount, kinds: [...withheldKinds], reason: "NO_APPROVED_ROUTE" as const } } : {}),
          generated: true,
        };
        if (refused === null && withheldCount === 0 && policyDropped === 0 && needToKnowHidden === 0 && !cuiDerived && cacheable) deps.cache.put(asker, req.mode, prompt, JSON.stringify(out));
        return out;
      } catch (err) {
        await deps.store
          .finishRun(runReq, { ...finished(runId, "FAILED", actions, usage, "", provider.endpoint), ...(err instanceof AgentError ? { code: err.code } : {}), reason: err instanceof Error ? err.message : String(err) })
          .catch(() => undefined);
        throw err;
      } finally {
        // A call that may have been billed with no usage known stays held for the sweep (as in the run engine).
        if (settleSpend) await deps.store.settle(hold, usage).catch(() => undefined);
        else await deps.store.settle(hold, { tokensIn: 0, tokensOut: 0 }).catch(() => undefined);
      }
    },
  };
}

/** The copilot's own knowledge search: the caller's tenant, a query of at most 1000 characters, ten chunks at most. */
async function searchKnowledge(knowledge: KnowledgeService, tenantId: string, args: Record<string, unknown>): Promise<{ ok: true; output: unknown; ref: string } | { ok: false; code: string; message: string }> {
  const query = typeof args["query"] === "string" ? args["query"] : typeof args["q"] === "string" ? args["q"] : "";
  if (query.trim() === "" || query.length > 1000) return { ok: false, code: "AGT-0214", message: "the knowledge search needs a query of 1 to 1000 characters" };
  try {
    return { ok: true, output: await knowledge.search(tenantId, query, 10), ref: KNOWLEDGE_SEARCH_TOOL };
  } catch {
    return { ok: false, code: "AGT-0214", message: "the knowledge search failed" };
  }
}

async function recordBoundaryBlock(store: CopilotStore, runReq: RunRequest, at: Date): Promise<void> {
  const result: GuardResult = {
    callId: randomUUID(),
    highestClass: "P0",
    routeId: "none",
    policyVersion: GUARD_POLICY_VERSION,
    counts: {},
    excluded: {},
    decision: "BLOCKED_BOUNDARY",
    errorCode: "NO_APPROVED_ROUTE",
  };
  const action = buildActionRecord({
    runId: runReq.runId,
    seq: 1,
    toolName: EGRESS_GUARD_TOOL,
    inputRedacted: "{}",
    outputRedacted: JSON.stringify({ decision: result.decision, highestClass: result.highestClass }),
    approverUserId: null,
    decision: "DENIED",
    decidedAt: at.toISOString(),
    guardResult: result,
    reason: "NO_APPROVED_ROUTE",
  });
  await store.startRun(runReq);
  await store.finishRun(runReq, finished(runReq.runId, "FAILED", [action], { tokensIn: 0, tokensOut: 0 }, NO_ROUTE_MESSAGE));
}

const REQUEST_RECORD_REF = /^record:([A-Za-z0-9_-]{1,64})\/([A-Za-z0-9_-]{1,120})$/;

const RECORD_HISTORY_TOOLS = ["depot.platform.audit_trail.get", "depot.platform.document_flow.get", "depot.workflow.history.get"];

function hasContent(output: unknown): boolean {
  if (output === null || output === undefined || output === "") return false;
  if (Array.isArray(output)) return output.length > 0;
  if (typeof output !== "object") return true;
  const values = Object.values(output);
  return values.length > 0 && values.some(hasContent);
}

function typedRecordCitation(recordRef: string | undefined, tool: string, args: Record<string, unknown>, output: unknown): ChatCitation | undefined {
  const match = recordRef === undefined ? null : REQUEST_RECORD_REF.exec(recordRef);
  const domain = tool.split(".")[1];
  if (match === null || match[1] === undefined || match[2] === undefined || domain === undefined || !RECORD_HISTORY_TOOLS.includes(tool)) return undefined;
  if (!Object.values(args).includes(match[2]) || !hasContent(output)) return undefined;
  return { sourceType: "RECORD", sourceRef: `record:${domain}/${match[1]}/${match[2]}`, label: `${match[1].replaceAll("-", " ")} ${match[2]}` };
}

function auditableId(id: string): string {
  return id.replace(/[^\w.:/#@+=~%-]/g, "_").replace(/^[^A-Za-z0-9]/, "n").slice(0, 128);
}

function refusal(code: string, message: string): ChatResponse {
  return { answer: message, citations: [], toolCalls: [], refusal: { code, message }, generated: true };
}

function knowledgeCitations(output: unknown): Citation[] {
  const chunks = typeof output === "object" && output !== null && Array.isArray((output as { chunks?: unknown }).chunks) ? (output as { chunks: unknown[] }).chunks : [];
  const out: Citation[] = [];
  for (const c of chunks) {
    if (typeof c !== "object" || c === null) continue;
    const r = c as Record<string, unknown>;
    if (typeof r.chunkId !== "string" || typeof r.sourceRef !== "string" || typeof r.section !== "string") continue;
    if (r.sourceType !== "ARTICLE" && r.sourceType !== "DOC" && r.sourceType !== "POLICY") continue;
    out.push({
      chunkId: r.chunkId,
      sourceType: r.sourceType,
      sourceRef: r.sourceRef,
      section: r.section,
      fusedScore: typeof r.fusedScore === "number" ? r.fusedScore : 0,
      ...(typeof r.similarity === "number" ? { similarity: r.similarity } : {}),
    });
  }
  return out;
}

export function auditRequest(runId: string, caller: Principal, modelId: string, cap: number): RunRequest {
  return {
    runId,
    tenantId: caller.tenantId,
    invokerUserId: caller.userId,
    userDuties: caller.duties,
    agent: { agentId: COPILOT_AGENT_ID, version: 1, modelId, systemPrompt: "", allowedTools: [], requiredDuties: [COPILOT_DUTY] },
    evalPassed: true,
    policy: { mode: "SUGGEST", autoThreshold: 1, autoAmountLimit: { amount: "0", currency: "USD" } },
    selfApprovalLimit: { amount: "0", currency: "USD" },
    runTokenCap: cap,
    // The prompt is not stored; the audit row keeps only the run and its tool calls.
    prompt: "",
  };
}

export function finished(runId: string, status: "COMMITTED" | "FAILED", actions: ActionRecord[], usage: ModelUsage, displayText: string, endpoint?: ModelEndpoint): RunResult {
  return { runId, status, actions, tokensIn: usage.tokensIn, tokensOut: usage.tokensOut, estimated: false, budgetState: "OK", displayText, ...(endpoint !== undefined ? { modelEndpoint: endpoint } : {}) };
}

export type CopilotService = ReturnType<typeof createCopilotService>;
