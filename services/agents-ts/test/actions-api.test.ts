import { readFileSync } from "node:fs";
import { afterEach, describe, expect, it } from "vitest";
import { createActionService } from "../src/runtime/actions.js";
import { createRunService } from "../src/runtime/service.js";
import { defaultCatalog } from "../src/agents/catalog.js";
import type { ActionDecline, ActionRecord } from "../src/domain/audit.js";
import { ScriptedProvider } from "./fakes.js";
import { FakeMcp, headers, MemoryActions, MemoryStore, OTHER, serve, T1, USER } from "./surface-fakes.js";

const RUN = "00000000-0000-7000-8000-0000000000f1";
const idOf = (n: number): string => `00000000-0000-7000-8000-00000000b00${n}`;
const READ = "agents.run.read";
const DECIDE = "agentapproval.decide";

const TOOLS = [
  "depot.ap.invoice.propose_coding",
  "depot.ar.settlement.propose",
  "depot.mrp.action_message.propose_triage",
  "depot.collections.letter.draft",
  "depot.ledger.journal.propose_flag",
];

const action = (n: number, toolName: string, decision: ActionRecord["decision"] = "AUTO"): ActionRecord => ({
  runId: RUN,
  actionId: idOf(n),
  seq: n,
  toolName,
  inputJson: "{}",
  outputHash: "0".repeat(64),
  outputJson: "{}",
  outputTruncated: false,
  approverUserId: null,
  decision,
  decidedAt: "2026-11-18T10:00:00.000Z",
  committedEntityRef: decision === "AUTO" ? "inv-1" : null,
  reason: null,
});

class ThrowingActions extends MemoryActions {
  override async decline(): Promise<ActionRecord | undefined> {
    throw new Error("connection refused: db.internal:5432");
  }
}

const open: Array<{ close(): Promise<void> }> = [];
afterEach(async () => {
  await Promise.all(open.splice(0).map((s) => s.close()));
});

async function setup(actionsFor: (store: MemoryStore) => MemoryActions = (s) => new MemoryActions(s)) {
  const store = new MemoryStore();
  const mcp = new FakeMcp([]);
  store.runs.set(RUN, { runId: RUN, agentId: "ap_coding", agentVersion: 1, invokerUserId: USER, status: "COMMITTED", tokensIn: 1, tokensOut: 1, estimated: false, createdAt: "2026-11-18T09:00:00.000Z" });
  store.actionRows.set(RUN, [...TOOLS.map((t, i) => action(i + 1, t)), action(6, "depot.ap.invoice.propose_coding", "DENIED")]);
  const actionStore = actionsFor(store);
  const runs = createRunService({
    agents: defaultCatalog("claude-sonnet-5-5"),
    evalPassed: async () => true,
    providerFor: () => new ScriptedProvider([]),
    mcp,
    store,
    inbox: store,
    killSwitch: { isHalted: () => false },
    disabledAgents: async () => new Set(),
    config: { runTokenCap: 50_000, selfApprovalLimit: { amount: "0", currency: "USD" }, pseudonymTtlMs: 60_000, approvalPollMs: 1 },
    spawn: () => undefined,
  });
  const srv = await serve({
    approvals: store,
    runs,
    actions: createActionService({ runs: store, actions: actionStore, now: () => new Date("2026-11-18T12:00:00.000Z") }),
  });
  open.push(srv);
  const call = (path: string, duties: string, body?: unknown, user = USER, tenant = T1, method = "POST") =>
    fetch(`${srv.base}/api/v1/agents/runs/${RUN}/actions/${path}`, { method, headers: headers(duties, user, tenant), ...(body === undefined ? {} : { body: typeof body === "string" ? body : JSON.stringify(body) }) });
  const getRun = async () => (await fetch(`${srv.base}/api/v1/agents/runs/${RUN}`, { headers: headers(READ) })).json() as Promise<{ actions: Array<ActionRecord & { reversible: boolean; reversibleReason: string; declined?: ActionDecline }> }>;
  return { store, mcp, actions: actionStore, srv, call, getRun };
}

const decision = (id: number) => `${idOf(id)}/decision`;
const undo = (id: number) => `${idOf(id)}:undo`;

describe("decline of an ungated action", () => {
  it("marks the action REJECTED with reason, actor and time, shows it on the run, leaves the domain alone and replays", async () => {
    const s = await setup();
    const first = await s.call(decision(1), READ, { decision: "REJECTED", reason: "wrong vendor" });
    expect(first.status).toBe(200);
    const body = (await first.json()) as Record<string, unknown>;
    expect(body).toEqual({ runId: RUN, actionId: idOf(1), decision: "REJECTED", declinedBy: USER, declinedAt: "2026-11-18T12:00:00.000Z", reason: "wrong vendor" });
    expect((await s.getRun()).actions[0]?.declined).toEqual({ by: USER, reason: "wrong vendor", at: "2026-11-18T12:00:00.000Z" });
    const second = await s.call(decision(1), READ, { decision: "REJECTED", reason: "something else" });
    expect(second.status).toBe(200);
    expect(await second.json()).toEqual(body);
    expect(s.actions.writes).toBe(1);
    expect(s.mcp.calls).toEqual([]);
    expect(s.mcp.connects).toEqual([]);
  });

  it("stores no reason when none is given", async () => {
    const s = await setup();
    const res = await s.call(decision(2), READ, { decision: "REJECTED" });
    expect(await res.json()).toMatchObject({ reason: null });
  });

  it("lets a user who is not the invoker decline when they hold agentapproval.decide", async () => {
    const s = await setup();
    const res = await s.call(decision(1), `${READ},${DECIDE}`, { decision: "REJECTED" }, OTHER);
    expect(res.status).toBe(200);
    expect(await res.json()).toMatchObject({ declinedBy: OTHER });
  });

  it("answers 403 and writes nothing for a caller without the read duty or neither invoker nor decide duty", async () => {
    const s = await setup();
    for (const [duties, user] of [["ap.invoice.read", USER], [READ, OTHER]] as const) {
      const res = await s.call(decision(1), duties, { decision: "REJECTED" }, user);
      expect(res.status).toBe(403);
      expect(await res.json()).toMatchObject({ code: "AGT-0203", status: 403, instance: expect.stringContaining("/decision") });
    }
    expect(s.actions.writes).toBe(0);
  });

  it("answers 401 without a token", async () => {
    const s = await setup();
    const res = await fetch(`${s.srv.base}/api/v1/agents/runs/${RUN}/actions/${decision(1)}`, { method: "POST", body: JSON.stringify({ decision: "REJECTED" }) });
    expect(res.status).toBe(401);
    expect(await res.json()).toMatchObject({ code: "AGT-0401" });
  });

  it("answers 404 for another tenant's run, an unknown action and a non-uuid path, and writes nothing", async () => {
    const s = await setup();
    const tenant = "00000000-0000-7000-8000-0000000000bb";
    expect((await s.call(decision(1), READ, { decision: "REJECTED" }, USER, tenant)).status).toBe(404);
    const unknown = await s.call(decision(9), READ, { decision: "REJECTED" });
    expect(unknown.status).toBe(404);
    expect(await unknown.json()).toMatchObject({ code: "AGT-0404" });
    expect((await s.call("not-a-uuid/decision", READ, { decision: "REJECTED" })).status).toBe(404);
    expect(s.actions.writes).toBe(0);
  });

  it.each([
    ["APPROVED", { decision: "APPROVED" }],
    ["a missing decision", {}],
    ["a numeric decision", { decision: 5 }],
    ["a numeric reason", { decision: "REJECTED", reason: 5 }],
    ["an object reason", { decision: "REJECTED", reason: {} }],
    ["a reason over 500 characters", { decision: "REJECTED", reason: "x".repeat(501) }],
    ["an unknown field", { decision: "REJECTED", extra: true }],
  ])("answers 400 for %s", async (_name, body) => {
    const s = await setup();
    const res = await s.call(decision(1), READ, body);
    expect(res.status).toBe(400);
    expect(await res.json()).toMatchObject({ code: "AGT-0400" });
    expect(s.actions.writes).toBe(0);
  });

  it("answers 422 and stores nothing when the reason holds an email address", async () => {
    const s = await setup();
    const res = await s.call(decision(1), READ, { decision: "REJECTED", reason: "ask jane.doe@example.com" });
    expect(res.status).toBe(422);
    expect(await res.json()).toMatchObject({ code: "AGT-0210" });
    expect(s.actions.writes).toBe(0);
  });

  it("answers 409 for a row that was not auto-committed", async () => {
    const s = await setup();
    const res = await s.call(decision(6), READ, { decision: "REJECTED" });
    expect(res.status).toBe(409);
    expect(await res.json()).toMatchObject({ code: "AGT-0409" });
    expect(s.actions.writes).toBe(0);
  });

  it("answers a generic 500 when the store fails", async () => {
    const s = await setup((store) => new ThrowingActions(store));
    const res = await s.call(decision(1), READ, { decision: "REJECTED" });
    expect(res.status).toBe(500);
    const body = (await res.json()) as { code: string; detail: string };
    expect(body).toMatchObject({ code: "AGT-0500", detail: "unexpected error" });
    expect(JSON.stringify(body)).not.toContain("db.internal");
  });

  it("keeps the gated flow: an action with an approval request is decided by its approver", async () => {
    const s = await setup();
    const zero = { amount: "0", currency: "USD" };
    const actionId = await s.store.open({ tenantId: T1, runId: RUN, seq: 7, tool: "depot.ap.invoice.propose_coding", invokerUserId: USER, approvalDuty: "ap.invoice.approve", amount: zero, selfApprovalLimit: zero, proposalJson: "{}" });
    const denied = await s.call(`${actionId}/decision`, READ, { decision: "REJECTED" }, OTHER);
    expect(denied.status).toBe(403);
    const ok = await s.call(`${actionId}/decision`, "ap.invoice.approve", { decision: "REJECTED" }, OTHER);
    expect(ok.status).toBe(200);
    expect(await ok.json()).toMatchObject({ decision: "REJECTED", approverUserId: OTHER });
    expect(s.actions.writes).toBe(0);
  });
});

describe("undo", () => {
  it.each(TOOLS.map((tool, i) => [tool, i + 1] as const))("answers 409 NOT_REVERSIBLE with the registry reason for %s", async (_tool, n) => {
    const s = await setup();
    const res = await s.call(undo(n), READ);
    expect(res.status).toBe(409);
    expect(res.headers.get("content-type")).toContain("application/problem+json");
    const body = (await res.json()) as { code: string; detail: string };
    expect(body.code).toBe("NOT_REVERSIBLE");
    expect(body.detail).toMatch(/^cannot undo: /);
    expect(s.mcp.calls).toEqual([]);
    expect((await s.getRun()).actions[n - 1]).toMatchObject({ reversible: false, reversibleReason: body.detail });
  });

  it("answers the fail-closed AP coding reason and leaves the action unchanged", async () => {
    const s = await setup();
    const res = await s.call(undo(1), READ, { reason: "coded wrong" });
    expect(await res.json()).toMatchObject({ status: 409, code: "NOT_REVERSIBLE", detail: "cannot undo: AP invoice coding update is not available yet" });
    expect((await s.getRun()).actions[0]).toMatchObject({ decision: "AUTO", reversible: false });
  });

  it("applies the same gate as decline: 401, 403 and 404", async () => {
    const s = await setup();
    const noToken = await fetch(`${s.srv.base}/api/v1/agents/runs/${RUN}/actions/${undo(1)}`, { method: "POST" });
    expect(noToken.status).toBe(401);
    expect((await s.call(undo(1), "ap.invoice.read")).status).toBe(403);
    expect((await s.call(undo(1), READ, undefined, OTHER)).status).toBe(403);
    expect((await s.call(undo(9), READ)).status).toBe(404);
    expect((await s.call(undo(1), READ, undefined, USER, "00000000-0000-7000-8000-0000000000bb")).status).toBe(404);
  });

  it("answers 405 with Allow POST on GET, 400 on an unknown field, a bad reason or invalid JSON, 422 on PII", async () => {
    const s = await setup();
    const get = await s.call(undo(1), READ, undefined, USER, T1, "GET");
    expect(get.status).toBe(405);
    expect(get.headers.get("allow")).toBe("POST");
    for (const body of [{ extra: 1 }, { reason: 5 }, { reason: "x".repeat(501) }, "{not json"]) {
      expect((await s.call(undo(1), READ, body)).status).toBe(400);
    }
    expect((await s.call(undo(1), READ, { reason: "mail jane.doe@example.com" })).status).toBe(422);
  });
});

describe("run view", () => {
  it("shows reversible false with a reason for every action", async () => {
    const s = await setup();
    const { actions } = await s.getRun();
    expect(actions).toHaveLength(6);
    for (const a of actions) expect(a).toMatchObject({ reversible: false, reversibleReason: expect.stringMatching(/^cannot undo: /) });
  });

  it("lists the same fields on the actions route", async () => {
    const s = await setup();
    const res = await fetch(`${s.srv.base}/api/v1/agents/runs/${RUN}/actions`, { headers: headers(READ) });
    const { items } = (await res.json()) as { items: Array<{ reversible: boolean }> };
    expect(items.every((i) => i.reversible === false)).toBe(true);
  });
});

describe("gateway entries", () => {
  const data = JSON.parse(readFileSync(new URL("../../../policies/authz/data.json", import.meta.url), "utf8")) as {
    duties: Record<string, { flag?: boolean; permissions: Array<{ resource: string; methods: string[] }> }>;
  };

  it("grants the decision and undo routes under agents.run.read and defines the agentapproval.decide flag duty", () => {
    const read = data.duties["agents.run.read"]?.permissions ?? [];
    expect(read).toContainEqual({ resource: "/api/v1/agents/runs/*/actions/*/decision", methods: ["POST"] });
    expect(read).toContainEqual({ resource: "/api/v1/agents/runs/*/actions/*:undo", methods: ["POST"] });
    expect(data.duties[DECIDE]).toEqual({ flag: true, permissions: [] });
  });

  it("holds a duty row for every operation in api/openapi.yaml", () => {
    const spec = readFileSync(new URL("../api/openapi.yaml", import.meta.url), "utf8").split("\n");
    const operations: Array<{ method: string; resource: string }> = [];
    let resource = "";
    for (const line of spec) {
      const path = /^ {2}(\/\S+):$/.exec(line);
      if (path !== null) resource = (path[1] ?? "").replace(/\{[^}]+\}/g, "*");
      const method = /^ {4}(get|post|put|delete):$/.exec(line);
      if (method !== null && resource !== "") operations.push({ method: (method[1] ?? "").toUpperCase(), resource });
    }
    const granted = Object.values(data.duties).flatMap((duty) => duty.permissions);
    const missing = operations.filter(
      (op) => !granted.some((permission) => permission.resource === op.resource && permission.methods.includes(op.method)),
    );
    expect(operations.length).toBeGreaterThanOrEqual(27);
    expect(missing).toEqual([]);
  });
});
