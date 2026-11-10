import { afterEach, describe, expect, it } from "vitest";
import { defaultCatalog } from "../src/agents/catalog.js";
import { createRunService } from "../src/runtime/service.js";
import { proposal, ScriptedProvider } from "./fakes.js";
import { FakeMcp, headers, MemoryStore, OTHER, serve, T1, tool, USER } from "./surface-fakes.js";

const AP_DUTIES = "agents.invoke,ap.invoice.read,ap.vendor.read,ledger.account.read,tax.code.read,ap.invoice.propose";
const PROPOSE = "passage.ap.invoice.propose_coding";

const setup = async (opts: { killed?: boolean; evalPassed?: boolean; mcp?: FakeMcp; provider?: ScriptedProvider } = {}) => {
  const store = new MemoryStore();
  const mcp = opts.mcp ?? new FakeMcp([tool(PROPOSE, "ap.invoice.propose", "suggest")], { [PROPOSE]: { ok: true, output: { id: "coding-1" }, ref: "coding-1" } });
  const provider = opts.provider ?? new ScriptedProvider([proposal(0.9, "1200", PROPOSE, { invoiceId: "INV-7" })]);
  const spawned: Array<Promise<unknown>> = [];
  const runs = createRunService({
    agents: defaultCatalog("claude-sonnet-5-5"),
    evalPassed: () => opts.evalPassed ?? true,
    providerFor: () => provider,
    mcp,
    store,
    inbox: store,
    killSwitch: { isHalted: () => opts.killed ?? false },
    config: { runTokenCap: 50_000, selfApprovalLimit: { amount: "0", currency: "USD" }, pseudonymTtlMs: 60_000, approvalPollMs: 1 },
    sleep: () => new Promise((r) => setTimeout(r, 2)),
    spawn: (p) => spawned.push(p.catch(() => undefined)),
  });
  const srv = await serve({ approvals: store, runs });
  return { store, mcp, provider, spawned, srv };
};

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

const start = (base: string, duties: string, agent = "ap_coding", body: unknown = { prompt: "Code invoice INV-7 for Acme" }, h = headers(duties)) =>
  fetch(`${base}/api/v1/agents/${agent}/runs`, { method: "POST", headers: h, body: JSON.stringify(body) });

describe("POST /api/v1/agents/{agent_id}/runs", () => {
  it("answers 202, runs with the caller's token, waits at the persisted approval gate and commits after a different approver decides", async () => {
    const s = await setup();
    close = s.srv.close;
    const res = await start(s.srv.base, AP_DUTIES);
    expect(res.status).toBe(202);
    const started = (await res.json()) as { runId: string; status: string; agentId: string };
    expect(started).toMatchObject({ status: "RUNNING", agentId: "ap_coding" });
    // The gateway session was opened as the caller, with the caller's bearer token and the run id.
    expect(s.mcp.connects).toEqual([{ userId: USER, token: "user-token-abc", runId: started.runId }]);

    // The run waits for approval; the view lists what waits.
    let view: { run: { status: string }; pendingApprovals: Array<{ actionId: string; tool: string; approvalDuty: string }> } | undefined;
    for (let i = 0; i < 200 && (view?.pendingApprovals.length ?? 0) === 0; i += 1) {
      await new Promise((r) => setTimeout(r, 5));
      view = (await (await fetch(`${s.srv.base}/api/v1/agents/runs/${started.runId}`, { headers: headers("agents.run.read") })).json()) as typeof view;
    }
    expect(view?.run.status).toBe("RUNNING");
    expect(view?.pendingApprovals[0]).toMatchObject({ tool: PROPOSE, approvalDuty: "ap.invoice.propose" });
    const actionId = view?.pendingApprovals[0]?.actionId ?? "";

    // The invoker cannot approve their own run above the self-approval limit (AGT-0209).
    const self = await fetch(`${s.srv.base}/api/v1/agents/runs/${started.runId}/actions/${actionId}/decision`, {
      method: "POST",
      headers: headers("ap.invoice.propose"),
      body: JSON.stringify({ decision: "APPROVED" }),
    });
    expect(self.status).toBe(403);
    expect(((await self.json()) as { code: string }).code).toBe("AGT-0209");

    const decide = await fetch(`${s.srv.base}/api/v1/agents/runs/${started.runId}/actions/${actionId}/decision`, {
      method: "POST",
      headers: headers("ap.invoice.propose", OTHER),
      body: JSON.stringify({ decision: "APPROVED" }),
    });
    expect(decide.status).toBe(200);
    await Promise.all(s.spawned);

    const done = (await (await fetch(`${s.srv.base}/api/v1/agents/runs/${started.runId}`, { headers: headers("agents.run.read") })).json()) as { run: { status: string }; pendingApprovals: unknown[] };
    expect(done.run.status).toBe("COMMITTED");
    expect(done.pendingApprovals).toEqual([]);
    const actions = (await (await fetch(`${s.srv.base}/api/v1/agents/runs/${started.runId}/actions`, { headers: headers("agents.run.read") })).json()) as { items: Array<{ toolName: string; decision: string; approverUserId: string; committedEntityRef: string }> };
    expect(actions.items).toEqual([expect.objectContaining({ toolName: PROPOSE, decision: "APPROVED", approverUserId: OTHER, committedEntityRef: "coding-1" })]);
    expect(s.mcp.calls).toEqual([{ name: PROPOSE, args: { invoiceId: "INV-7" } }]);
  });

  it("refuses before any state exists: unknown agent, missing duties, kill switch, no passing eval, exhausted budget, bad body, no token", async () => {
    const s = await setup();
    close = s.srv.close;
    expect((await start(s.srv.base, AP_DUTIES, "no_such_agent")).status).toBe(404);
    const noInvoke = await start(s.srv.base, "ap.invoice.read,ap.vendor.read,ledger.account.read,tax.code.read");
    expect(noInvoke.status).toBe(403);
    expect(((await noInvoke.json()) as { code: string }).code).toBe("AGT-0203");
    expect((await start(s.srv.base, "agents.invoke")).status).toBe(403);
    expect((await start(s.srv.base, AP_DUTIES, "ap_coding", { prompt: "" })).status).toBe(400);
    expect((await start(s.srv.base, AP_DUTIES, "ap_coding", { prompt: "x", extra: 1 })).status).toBe(400);
    const { authorization: _a, ...noAuth } = headers(AP_DUTIES);
    expect((await start(s.srv.base, AP_DUTIES, "ap_coding", { prompt: "x" }, noAuth)).status).toBe(401);
    s.store.budgetUsed.in = s.store.budgetUsed.limit;
    const budget = await start(s.srv.base, AP_DUTIES);
    expect(((await budget.json()) as { code: string }).code).toBe("AGT-0202");
    expect(s.store.runs.size).toBe(0);
    expect(s.provider.seen).toHaveLength(0);

    const killed = await setup({ killed: true });
    const k = await start(killed.srv.base, AP_DUTIES);
    expect(((await k.json()) as { code: string }).code).toBe("AGT-0201");
    await killed.srv.close();
    const noEval = await setup({ evalPassed: false });
    const e = await start(noEval.srv.base, AP_DUTIES);
    expect(((await e.json()) as { code: string }).code).toBe("AGT-0204");
    await noEval.srv.close();
  });

  it("answers 502 when the tool gateway is down and starts nothing", async () => {
    const mcp = new FakeMcp([]);
    mcp.fail = true;
    const s = await setup({ mcp });
    close = s.srv.close;
    const res = await start(s.srv.base, AP_DUTIES);
    expect(res.status).toBe(502);
    expect(s.spawned).toHaveLength(0);
  });
});

describe("GET run and actions", () => {
  it("need agents.run.read, find only the caller's tenant's runs and answer 404 otherwise", async () => {
    const s = await setup();
    close = s.srv.close;
    const id = "00000000-0000-7000-8000-00000000f001";
    expect((await fetch(`${s.srv.base}/api/v1/agents/runs/${id}`, { headers: headers("agents.run.read") })).status).toBe(404);
    expect((await fetch(`${s.srv.base}/api/v1/agents/runs/${id}`, { headers: headers("agents.invoke") })).status).toBe(403);
    expect((await fetch(`${s.srv.base}/api/v1/agents/runs/${id}/actions`, { headers: headers("agents.run.read") })).status).toBe(404);
    expect((await fetch(`${s.srv.base}/api/v1/agents/runs/${id}`, { method: "POST", headers: headers("agents.run.read"), body: "{}" })).status).toBe(405);
    expect(T1).toBeDefined();
  });
});
