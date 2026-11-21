import { afterEach, describe, expect, it, vi } from "vitest";
import { defaultCatalog } from "../src/agents/catalog.js";
import { createAgentDefinitionService, type AgentDefinitionStore, type DisabledRow } from "../src/api/agentDefinitions.js";
import type { BriefingContent, BriefingFacts, BriefingStore, StoredBriefing } from "../src/domain/briefing.js";
import { createBriefingService } from "../src/runtime/briefing.js";
import { createRunService } from "../src/runtime/service.js";
import { proposal, ScriptedProvider } from "./fakes.js";
import { FakeMcp, headers, MemoryStore, serve, tool } from "./surface-fakes.js";

const AP_DUTIES = "agents.invoke,ap.invoice.read,ap.vendor.read,ledger.account.read,tax.code.read,ap.invoice.propose";
const ANOMALY_DUTIES = "agents.invoke,ledger.journal.read,ledger.account.read";
const ADMIN = "agents.admin";
const PROPOSE = "depot.ap.invoice.propose_coding";
const DEFINITIONS = "/api/v1/agents/definitions";
// Relative to the real clock: the briefing service reads Date.now(), and a fixed date eventually falls
// inside the 24 h expiry horizon, which lists the pending item as a risk too and breaks the counts below.
const FACT_TIME = new Date(Date.now() - 3_600_000).toISOString();

class MemoryDefinitions implements AgentDefinitionStore {
  readonly rows = new Map<string, DisabledRow>();
  readonly changes: Array<{ agentId: string; disabled: boolean; by: string; reason: string | null }> = [];
  failReads = false;

  async disabledAgents(): Promise<ReadonlyMap<string, DisabledRow>> {
    if (this.failReads) throw new Error("db password is hunter2");
    return this.rows;
  }
  async setDisabled(_t: string, agentId: string, disabled: boolean, by: string, reason: string | null): Promise<void> {
    if (disabled === this.rows.has(agentId)) return;
    if (disabled) this.rows.set(agentId, { disabledBy: by, disabledAt: FACT_TIME, reason });
    else this.rows.delete(agentId);
    this.changes.push({ agentId, disabled, by, reason });
  }
}

class MemoryBriefings implements BriefingStore {
  readonly rows = new Map<string, StoredBriefing>();
  async create(): Promise<string> {
    const id = `00000000-0000-7000-8000-0000000000f${this.rows.size}`;
    this.rows.set(id, { id, status: "RUNNING", narrative: false, createdAt: FACT_TIME, completedAt: null, readAt: null, content: null });
    return id;
  }
  async finish(_t: string, id: string, content: BriefingContent): Promise<void> {
    this.rows.set(id, { ...this.rows.get(id)!, status: "READY", content });
  }
  async fail(_t: string, id: string): Promise<void> {
    this.rows.set(id, { ...this.rows.get(id)!, status: "FAILED" });
  }
  async get(_t: string, _u: string, id: string) {
    return this.rows.get(id);
  }
  async list() {
    return { items: [...this.rows.values()], nextCursor: null };
  }
  async markRead() {
    return undefined;
  }
}

const facts = (): BriefingFacts => ({
  pending: ["ap_coding", "cash_app"].map((agentId, i) => ({
    agentId: agentId as "ap_coding" | "cash_app",
    actionId: `00000000-0000-7000-8000-00000000a00${i}`,
    tool: PROPOSE,
    amount: "100.00",
    currency: "USD",
    selfApprovalLimit: "500.00",
    selfApprovalCurrency: "USD",
    requestedAt: FACT_TIME,
    expiryHours: 72,
  })),
  decided: [],
  runs: [{ agentId: "ap_coding", runId: "run-1", status: "FAILED", createdAt: FACT_TIME }],
});

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

const setup = async (opts: { wired?: boolean; facts?: () => Promise<BriefingFacts> } = {}) => {
  const definitions = new MemoryDefinitions();
  const store = new MemoryStore();
  const mcp = new FakeMcp([tool(PROPOSE, "ap.invoice.propose", "suggest")]);
  const provider = new ScriptedProvider([proposal(0.9, "1200", PROPOSE, { invoiceId: "INV-7" })]);
  const agents = defaultCatalog("claude-sonnet-5-5");
  const disabledAgents = async () => new Set((await definitions.disabledAgents()).keys());
  const runs = createRunService({
    agents,
    evalPassed: async () => true,
    providerFor: () => provider,
    mcp,
    store,
    inbox: store,
    killSwitch: { isHalted: () => false },
    disabledAgents,
    config: { runTokenCap: 50_000, selfApprovalLimit: { amount: "0", currency: "USD" }, pseudonymTtlMs: 60_000, approvalPollMs: 1 },
    sleep: () => new Promise((r) => setTimeout(r, 2)),
    spawn: (p) => void p.catch(() => undefined),
  });
  const briefingStore = new MemoryBriefings();
  const logs: string[] = [];
  const briefings = createBriefingService({ store: briefingStore, facts: { read: opts.facts ?? (async () => facts()) }, disabledAgents, log: (_l, m) => void logs.push(m) });
  const srv = await serve({ approvals: store, runs, briefings, ...(opts.wired === false ? {} : { definitions: createAgentDefinitionService({ agents, store: definitions }) }) });
  close = srv.close;
  const call = (method: string, path: string, duties: string, body?: unknown) =>
    fetch(`${srv.base}${path}`, { method, headers: headers(duties), ...(body === undefined ? {} : { body: typeof body === "string" ? body : JSON.stringify(body) }) });
  return { base: srv.base, definitions, store, mcp, provider, briefingStore, logs, call };
};

describe("GET /api/v1/agents/definitions", () => {
  it("lists every agent enabled, then shows a disabled one with who, when and why", async () => {
    const s = await setup();
    const first = (await (await s.call("GET", DEFINITIONS, ADMIN)).json()) as { items: Array<{ agentId: string; enabled: boolean }> };
    expect(first.items.map((i) => i.agentId)).toEqual(["ap_coding", "cash_app", "mrp_triage", "collections", "anomaly"]);
    expect(first.items.every((i) => i.enabled)).toBe(true);
    await s.call("POST", `${DEFINITIONS}/cash_app:disable`, ADMIN, { reason: "audit hold" });
    const second = (await (await s.call("GET", DEFINITIONS, ADMIN)).json()) as { items: unknown[] };
    expect(second.items).toContainEqual({ agentId: "cash_app", version: 1, enabled: false, disabledBy: expect.any(String), disabledAt: FACT_TIME, reason: "audit hold" });
  });

  it("answers 401 without a token, 403 AGT-0203 without agents.admin and 404 when no service is wired", async () => {
    const s = await setup();
    const { authorization: _a, ...anonymous } = headers(ADMIN);
    expect((await fetch(`${s.base}${DEFINITIONS}`, { headers: anonymous })).status).toBe(401);
    expect((await fetch(`${s.base}${DEFINITIONS}/ap_coding:disable`, { method: "POST", headers: anonymous })).status).toBe(401);
    for (const duties of ["agentbudget.write", "agents.run.read", ""]) {
      const res = await s.call("GET", DEFINITIONS, duties);
      expect(res.status).toBe(403);
      expect(await res.json()).toMatchObject({ code: "AGT-0203" });
    }
    const bare = await setup({ wired: false });
    expect((await bare.call("GET", DEFINITIONS, ADMIN)).status).toBe(404);
  });

  it("answers 500 AGT-0500 without echoing the cause when the store read fails", async () => {
    const s = await setup();
    s.definitions.failReads = true;
    const res = await s.call("GET", DEFINITIONS, ADMIN);
    expect(res.status).toBe(500);
    const text = await res.text();
    expect(text).toContain("AGT-0500");
    expect(text).not.toContain("hunter2");
  });
});

describe("POST definitions disable and enable", () => {
  it("refuses a caller without agents.admin and writes nothing", async () => {
    const s = await setup();
    const res = await s.call("POST", `${DEFINITIONS}/ap_coding:disable`, "agentbudget.write,agents.run.read");
    expect(res.status).toBe(403);
    expect(await res.json()).toMatchObject({ code: "AGT-0203" });
    expect((await s.call("POST", `${DEFINITIONS}/ap_coding:enable`, "agentbudget.write")).status).toBe(403);
    expect(s.definitions.changes).toEqual([]);
  });

  it("records the actor on a change and none on a repeat, and enable reverses it", async () => {
    const s = await setup();
    expect((await s.call("POST", `${DEFINITIONS}/ap_coding:disable`, ADMIN, { reason: "" })).status).toBe(200);
    expect((await s.call("POST", `${DEFINITIONS}/ap_coding:disable`, ADMIN)).status).toBe(200);
    expect(s.definitions.changes).toHaveLength(1);
    expect(s.definitions.changes[0]).toMatchObject({ agentId: "ap_coding", disabled: true, reason: null });
    const enabled = await s.call("POST", `${DEFINITIONS}/ap_coding:enable`, ADMIN);
    expect(await enabled.json()).toMatchObject({ agentId: "ap_coding", enabled: true, disabledBy: null });
    expect((await s.call("POST", `${DEFINITIONS}/ap_coding:enable`, ADMIN)).status).toBe(200);
    expect(s.definitions.changes.map((c) => c.disabled)).toEqual([true, false]);
  });

  it("answers 404 for an unknown or malformed agent id and 405 with Allow for the wrong method", async () => {
    const s = await setup();
    for (const id of ["no_such_agent", "AP", "a.b"]) {
      const res = await s.call("POST", `${DEFINITIONS}/${id}:disable`, ADMIN);
      expect(res.status, id).toBe(404);
      expect(await res.json()).toMatchObject({ code: "AGT-0404" });
    }
    for (const [method, path] of [["GET", `${DEFINITIONS}/ap_coding:disable`], ["DELETE", `${DEFINITIONS}/ap_coding:enable`], ["POST", DEFINITIONS]] as const) {
      const res = await s.call(method, path, ADMIN);
      expect([res.status, res.headers.get("allow")]).toEqual([405, method === "POST" ? "GET" : "POST"]);
    }
    expect(s.definitions.changes).toEqual([]);
  });

  it.each([
    ["not json", "{nope", 400, "AGT-0400"],
    ["an unknown key", { force: true }, 400, "AGT-0400"],
    ["a null reason", { reason: null }, 422, "AGT-0422"],
    ["a numeric reason", { reason: 5 }, 422, "AGT-0422"],
    ["an object reason", { reason: {} }, 422, "AGT-0422"],
    ["a reason over 500 characters", { reason: "x".repeat(501) }, 422, "AGT-0422"],
  ])("refuses a disable body with %s", async (_name, body, status, code) => {
    const s = await setup();
    const res = await s.call("POST", `${DEFINITIONS}/ap_coding:disable`, ADMIN, body);
    expect([res.status, ((await res.json()) as { code: string }).code]).toEqual([status, code]);
    expect(s.definitions.changes).toEqual([]);
  });

  it("refuses an enable body that carries a key", async () => {
    const s = await setup();
    expect((await s.call("POST", `${DEFINITIONS}/ap_coding:enable`, ADMIN, { reason: "x" })).status).toBe(400);
  });
});

describe("a disabled agent", () => {
  const start = (s: Awaited<ReturnType<typeof setup>>, agent: string, duties: string) =>
    s.call("POST", `/api/v1/agents/${agent}/runs`, duties, { prompt: "Code invoice INV-7 for Acme" });

  it("is refused with AGT-0235 before any run, provider call or gateway session, while another agent still starts", async () => {
    const s = await setup();
    await s.call("POST", `${DEFINITIONS}/ap_coding:disable`, ADMIN);
    const refused = await start(s, "ap_coding", AP_DUTIES);
    expect(refused.status).toBe(422);
    expect(await refused.json()).toMatchObject({ code: "AGT-0235" });
    expect(s.store.runs.size).toBe(0);
    expect(s.provider.seen).toHaveLength(0);
    expect(s.mcp.connects).toHaveLength(0);
    expect((await start(s, "anomaly", ANOMALY_DUTIES)).status).toBe(202);
    await s.call("POST", `${DEFINITIONS}/ap_coding:enable`, ADMIN);
    expect((await start(s, "ap_coding", AP_DUTIES)).status).toBe(202);
  });

  it("refuses the run with 500 AGT-0500 and starts nothing when the disabled flag cannot be read", async () => {
    const s = await setup();
    s.definitions.failReads = true;
    const res = await start(s, "ap_coding", AP_DUTIES);
    expect(res.status).toBe(500);
    expect(await res.json()).toMatchObject({ code: "AGT-0500" });
    expect(s.store.runs.size).toBe(0);
    expect(s.mcp.connects).toHaveLength(0);
  });

  it("is left out of a generated briefing, which states that it is disabled", async () => {
    const s = await setup();
    await s.call("POST", `${DEFINITIONS}/ap_coding:disable`, ADMIN);
    const { id } = (await (await s.call("POST", "/api/v1/agents/briefings:generate", "agents.invoke")).json()) as { id: string };
    await vi.waitFor(() => expect(s.briefingStore.rows.get(id)?.status).toBe("READY"));
    const body = (await (await s.call("GET", `/api/v1/agents/briefings/${id}`, "agents.run.read")).json()) as { changes: Array<{ agentId: string }>; risks: Array<{ agentId: string }>; recommendedActions: Array<{ agentId: string }>; notes: string[] };
    const agentsShown = [...body.changes, ...body.risks, ...body.recommendedActions].map((i) => i.agentId);
    expect(agentsShown).toEqual(["cash_app"]);
    expect(body.notes).toEqual(["ap_coding is disabled by your administrator"]);
    expect(Object.keys(s.briefingStore.rows.get(id)?.content?.counts ?? {})).not.toContain("ap_coding");
  });

  it("ends the briefing FAILED with nothing from any agent when the disabled flag cannot be read", async () => {
    const s = await setup();
    s.definitions.failReads = true;
    const { id } = (await (await s.call("POST", "/api/v1/agents/briefings:generate", "agents.invoke")).json()) as { id: string };
    await vi.waitFor(() => expect(s.briefingStore.rows.get(id)?.status).toBe("FAILED"));
    expect(s.briefingStore.rows.get(id)?.content).toBeNull();
    expect(s.logs).toEqual(["briefing build failed"]);
  });
});
