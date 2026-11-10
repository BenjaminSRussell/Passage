import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import type { ApprovalRequest, RecordedDecision } from "../src/api/decision.js";
import { createAgentsHandler, type AgentsApiDeps } from "../src/api/router.js";
import type { ActionRecord } from "../src/domain/audit.js";
import { KillSwitchRegistry } from "../src/domain/killswitch.js";
import type { NewApproval, PendingApproval } from "../src/store/approvals.js";
import type { StoredRun } from "../src/store/pg.js";
import type { McpConnector, McpSession, McpToolInfo } from "../src/runtime/mcp.js";
import type { RunRequest, RunResult, ToolResult } from "../src/runtime/engine.js";
import type { RunStore } from "../src/runtime/service.js";
import { testBudgets } from "./fakes.js";

export const T1 = "00000000-0000-7000-8000-000000000001";
export const USER = "00000000-0000-7000-8000-0000000000c1";
export const OTHER = "00000000-0000-7000-8000-0000000000c2";

/** A tool as the gateway lists it. */
export const tool = (name: string, requiredDuty: string, approvalMode: McpToolInfo["approvalMode"] = "none"): McpToolInfo => ({
  name,
  description: name,
  requiredDuty,
  approvalMode,
  readOnly: approvalMode === "none",
});

/** A gateway fake: lists what it is given and answers calls from `answers` (or the default). */
export class FakeMcp implements McpConnector {
  readonly connects: Array<{ userId: string; token: string; runId: string | undefined }> = [];
  readonly calls: Array<{ name: string; args: Record<string, unknown> }> = [];
  fail = false;

  constructor(
    private readonly tools: McpToolInfo[],
    private readonly answers: Record<string, ToolResult> = {},
  ) {}

  async connect(caller: { userId: string }, token: string, runId?: string): Promise<McpSession> {
    if (this.fail) throw new Error("gateway down");
    this.connects.push({ userId: caller.userId, token, runId });
    const byName = new Map(this.tools.map((t) => [t.name, t]));
    return {
      tools: this.tools,
      describe: (n) => {
        const t = byName.get(n);
        return t === undefined ? undefined : { requiredDuty: t.requiredDuty, approvalMode: t.approvalMode };
      },
      call: async (name, args) => {
        this.calls.push({ name, args });
        return this.answers[name] ?? { ok: true, output: { ok: true } };
      },
    };
  }
}

/** An in-memory RunStore, approval inbox and budget that records what the engine does. */
export class MemoryStore implements RunStore {
  readonly runs = new Map<string, StoredRun>();
  readonly actionRows = new Map<string, ActionRecord[]>();
  readonly settled: Array<[number, number]> = [];
  readonly reserved: Array<{ agentId: string; tokensIn: number }> = [];
  budgetUsed = { in: 0, out: 0, limit: 1_000_000 };
  readonly approvals = new Map<string, ApprovalRequest & NewApproval & { actionId: string }>();
  private n = 0;

  async startRun(req: RunRequest): Promise<void> {
    this.runs.set(req.runId, {
      runId: req.runId,
      agentId: req.agent.agentId,
      agentVersion: req.agent.version,
      invokerUserId: req.invokerUserId,
      status: "RUNNING",
      tokensIn: 0,
      tokensOut: 0,
      estimated: false,
      createdAt: "2026-10-21T00:00:00.000Z",
    });
  }
  async runStatus(req: RunRequest): Promise<string | undefined> {
    return this.runs.get(req.runId)?.status;
  }
  async finishRun(req: RunRequest, result: RunResult): Promise<void> {
    const r = this.runs.get(req.runId);
    if (r === undefined) throw new Error("no run");
    this.runs.set(req.runId, { ...r, status: result.status, ...(result.code !== undefined ? { code: result.code } : {}), tokensIn: result.tokensIn, tokensOut: result.tokensOut });
    this.actionRows.set(req.runId, result.actions);
  }
  async getRun(tenantId: string, runId: string): Promise<StoredRun | undefined> {
    return tenantId === T1 ? this.runs.get(runId) : undefined;
  }
  async loadActions(_t: string, runId: string): Promise<ActionRecord[]> {
    return this.actionRows.get(runId) ?? [];
  }
  async budgetSnapshot() {
    return { limitIn: this.budgetUsed.limit, limitOut: this.budgetUsed.limit, usedIn: this.budgetUsed.in, usedOut: this.budgetUsed.out, softPct: 80 };
  }
  async reserve(r: Parameters<typeof testBudgets.reserve>[0]) {
    this.reserved.push({ agentId: r.agentId, tokensIn: r.tokensIn });
    return testBudgets.reserve(r);
  }
  async settle(h: Parameters<typeof testBudgets.settle>[0], u: Parameters<typeof testBudgets.settle>[1]) {
    this.settled.push([u.tokensIn, u.tokensOut]);
    return testBudgets.settle(h, u);
  }

  // approval inbox
  async open(a: NewApproval): Promise<string> {
    const actionId = `00000000-0000-7000-8000-00000000a${String((this.n += 1)).padStart(3, "0")}`;
    this.approvals.set(actionId, {
      ...a,
      actionId,
      invokerUserId: a.invokerUserId,
      requestedAtMs: Date.now(),
      approvalDuty: a.approvalDuty,
      amount: a.amount,
      selfApprovalLimit: a.selfApprovalLimit,
    });
    return actionId;
  }
  async find(_t: string, _r: string, actionId: string): Promise<ApprovalRequest | undefined> {
    return this.approvals.get(actionId);
  }
  async record(_t: string, _r: string, actionId: string, d: RecordedDecision): Promise<RecordedDecision> {
    const a = this.approvals.get(actionId);
    if (a === undefined) throw new Error("no approval");
    a.decided ??= d;
    return a.decided;
  }
  async pendingForRun(_t: string, runId: string): Promise<PendingApproval[]> {
    return [...this.approvals.values()]
      .filter((a) => a.runId === runId && a.decided === undefined)
      .map((a) => ({ actionId: a.actionId, runId: a.runId, seq: a.seq, tool: a.tool, requestedAt: "2026-10-21T00:00:00.000Z", approvalDuty: a.approvalDuty }));
  }
}

export async function serve(deps: Partial<AgentsApiDeps> & Pick<AgentsApiDeps, "approvals">): Promise<{ base: string; close(): Promise<void>; registry: KillSwitchRegistry }> {
  const registry = deps.registry ?? new KillSwitchRegistry(() => Date.now());
  const handler = createAgentsHandler({ registry, versions: { next: () => 1 }, publish: async () => undefined, ...deps });
  const server: Server = createServer((req, res) => void handler(req, res));
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  return {
    base: `http://127.0.0.1:${(server.address() as AddressInfo).port}`,
    registry,
    close: () => new Promise<void>((r) => server.close(() => r())),
  };
}

export const headers = (duties: string, user = USER, tenant = T1): Record<string, string> => ({
  "content-type": "application/json",
  authorization: "Bearer user-token-abc",
  "x-passage-tenant": tenant,
  "x-passage-user": user,
  "x-passage-duties": duties,
});
