import { readFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import type { ApprovalRequest, RecordedDecision } from "../src/api/decision.js";
import { createAgentsHandler, MAX_BODY_BYTES } from "../src/api/router.js";
import type { AgentEvent } from "../src/domain/events.js";
import { KillSwitchRegistry } from "../src/domain/killswitch.js";

const T1 = "00000000-0000-7000-8000-000000000001";
const ADMIN = "00000000-0000-7000-8000-0000000000a1";
const CLERK = "00000000-0000-7000-8000-0000000000c1";
const SUP = "00000000-0000-7000-8000-0000000000b1";
const RUN = "00000000-0000-7000-8000-00000000f001";
const ACT = "00000000-0000-7000-8000-00000000f002";
const NOW = new Date("2026-10-22T10:00:00Z");

describe("agents HTTP surface", () => {
  let server: Server;
  let base: string;
  const published: AgentEvent<unknown>[] = [];
  const registry = new KillSwitchRegistry(() => Date.now());
  let version = 0;
  const pending: ApprovalRequest = {
    invokerUserId: CLERK,
    requestedAtMs: NOW.getTime(),
    approvalDuty: "APInvoicePost",
    amount: { amount: "1200.00", currency: "USD" },
    selfApprovalLimit: { amount: "5000.00", currency: "USD" },
  };

  beforeAll(async () => {
    const handler = createAgentsHandler({
      registry,
      versions: { next: () => ++version },
      approvals: {
        async find(tenantId, runId, actionId) {
          return tenantId === T1 && runId === RUN && actionId === ACT ? pending : undefined;
        },
        async record(_t, _r, _a, decision: RecordedDecision) {
          pending.decided ??= decision;
          return pending.decided;
        },
      },
      publish: async (evt) => {
        published.push(evt);
      },
      now: () => NOW,
    });
    server = createServer((req, res) => {
      void handler(req, res);
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  });

  afterAll(async () => {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  });

  const admin = {
    "content-type": "application/json",
    "x-passage-tenant": T1,
    "x-passage-user": ADMIN,
    "x-passage-duties": "AgentAdmin",
  };
  const kill = (active: boolean, headers: Record<string, string> = admin) =>
    fetch(`${base}/api/v1/agents/kill-switch`, { method: "POST", headers, body: JSON.stringify({ scope: "TENANT", tenantId: T1, active }) });

  it("POST kill-switch halts the tenant, publishes the event and answers 200; GET shows it", async () => {
    const res = await kill(true);
    expect(res.status).toBe(200);
    expect(res.headers.get("cache-control")).toBe("no-store");
    expect(await res.json()).toMatchObject({ scope: "TENANT", tenantId: T1, active: true, version: 1, byUser: ADMIN });
    expect(published.map((e) => e.type)).toEqual(["dev.passage.agent.killswitch.changed.v1"]);
    expect(registry.isHalted(T1)).toBe(true);

    const again = await kill(true);
    expect(await again.json()).toMatchObject({ active: true, version: 1 });
    expect(published).toHaveLength(1);

    const status = await fetch(`${base}/api/v1/agents/kill-switch`, { headers: admin });
    expect(status.status).toBe(200);
    expect(await status.json()).toMatchObject({ tenantId: T1, halted: true, tenant: { active: true, version: 1, byUser: ADMIN }, global: { active: false } });
  });

  it("returns an RFC 9457 problem with a stable code for a refused caller", async () => {
    const res = await kill(false, { ...admin, "x-passage-duties": "" });
    expect(res.status).toBe(403);
    expect(res.headers.get("content-type")).toBe("application/problem+json");
    expect(await res.json()).toEqual({
      type: "https://passage.dev/problems/AGT-0203",
      title: "Forbidden",
      status: 403,
      code: "AGT-0203",
      detail: "I cannot do that: you do not hold duty AgentAdmin required by the tenant kill switch.",
      instance: "/api/v1/agents/kill-switch",
    });
  });

  it("answers 401 without edge headers, 404 for unknown routes and 405 with Allow for a wrong method", async () => {
    const anon = await kill(true, { "content-type": "application/json" });
    expect(anon.status).toBe(401);
    expect(await anon.json()).toMatchObject({ code: "AGT-0401" });
    const missing = await fetch(`${base}/api/v1/agents/runs/${RUN}`, { headers: admin });
    expect(missing.status).toBe(404);
    const badId = await fetch(`${base}/api/v1/agents/runs/%E0%A4%A/actions/${ACT}/decision`, { method: "POST", headers: admin, body: "{}" });
    expect(badId.status).toBe(404);
    const wrong = await fetch(`${base}/api/v1/agents/kill-switch`, { method: "DELETE", headers: admin });
    expect(wrong.status).toBe(405);
    expect(wrong.headers.get("allow")).toBe("GET, POST");
  });

  it("answers 400 for a body that is not JSON or is empty, and 413 above the limit", async () => {
    const bad = await fetch(`${base}/api/v1/agents/kill-switch`, { method: "POST", headers: admin, body: "{not json" });
    expect(bad.status).toBe(400);
    expect(await bad.json()).toMatchObject({ code: "AGT-0400" });
    const empty = await fetch(`${base}/api/v1/agents/kill-switch`, { method: "POST", headers: admin });
    expect(empty.status).toBe(400);
    const big = await fetch(`${base}/api/v1/agents/kill-switch`, { method: "POST", headers: admin, body: "x".repeat(MAX_BODY_BYTES + 1) });
    expect(big.status).toBe(413);
  });

  it("records an approval decision and answers 404 for an action that does not exist", async () => {
    const sup = { ...admin, "x-passage-user": SUP, "x-passage-duties": "APInvoicePost" };
    const ok = await fetch(`${base}/api/v1/agents/runs/${RUN}/actions/${ACT}/decision`, { method: "POST", headers: sup, body: JSON.stringify({ decision: "APPROVED" }) });
    expect(ok.status).toBe(200);
    expect(await ok.json()).toEqual({ runId: RUN, actionId: ACT, decision: "APPROVED", approverUserId: SUP, decidedAt: NOW.toISOString() });
    const conflict = await fetch(`${base}/api/v1/agents/runs/${RUN}/actions/${ACT}/decision`, {
      method: "POST",
      headers: sup,
      body: JSON.stringify({ decision: "REJECTED" }),
    });
    expect(conflict.status).toBe(409);
    const other = "00000000-0000-7000-8000-00000000f003";
    const missing = await fetch(`${base}/api/v1/agents/runs/${RUN}/actions/${other}/decision`, { method: "POST", headers: sup, body: JSON.stringify({ decision: "APPROVED" }) });
    expect(missing.status).toBe(404);
    expect(await missing.json()).toMatchObject({ code: "AGT-0404" });
  });

  it("never echoes an internal error", async () => {
    const failing = createAgentsHandler({
      registry: new KillSwitchRegistry(() => Date.now()),
      versions: { next: () => 1 },
      approvals: { find: async () => undefined, record: async (_t, _r, _a, d) => d },
      publish: async () => {
        throw new Error("secret connection string");
      },
    });
    const s = createServer((req, res) => void failing(req, res));
    await new Promise<void>((resolve) => s.listen(0, "127.0.0.1", resolve));
    try {
      const res = await fetch(`http://127.0.0.1:${(s.address() as AddressInfo).port}/api/v1/agents/kill-switch`, {
        method: "POST",
        headers: admin,
        body: JSON.stringify({ scope: "TENANT", tenantId: T1, active: true }),
      });
      expect(res.status).toBe(500);
      const text = await res.text();
      expect(text).toContain("AGT-0500");
      expect(text).not.toContain("secret");
    } finally {
      await new Promise<void>((resolve) => s.close(() => resolve()));
    }
  });
});

/** `METHOD /path` for every operation under `paths:` in api/openapi.yaml. */
function documentedOperations(): string[] {
  const text = readFileSync(new URL("../api/openapi.yaml", import.meta.url), "utf8");
  const paths = text.slice(text.indexOf("\npaths:\n"), text.indexOf("\ncomponents:\n"));
  const ops: string[] = [];
  let current = "";
  for (const line of paths.split("\n")) {
    const path = /^  (\/\S+):$/.exec(line);
    if (path?.[1]) current = path[1];
    const method = /^    (get|put|post|patch|delete):$/.exec(line);
    if (method?.[1]) ops.push(`${method[1].toUpperCase()} ${current}`);
  }
  return ops.sort();
}

describe("api/openapi.yaml matches the router", () => {
  it("documents exactly the operations the router serves", async () => {
    expect(documentedOperations()).toEqual([
      "GET /api/v1/agents/kill-switch",
      "POST /api/v1/agents/kill-switch",
      "POST /api/v1/agents/runs/{runId}/actions/{actionId}/decision",
    ]);
    const handler = createAgentsHandler({
      registry: new KillSwitchRegistry(() => Date.now()),
      versions: { next: () => 1 },
      approvals: { find: async () => undefined, record: async (_t, _r, _a, d) => d },
      publish: async () => undefined,
    });
    const s = createServer((req, res) => void handler(req, res));
    await new Promise<void>((resolve) => s.listen(0, "127.0.0.1", resolve));
    try {
      const base = `http://127.0.0.1:${(s.address() as AddressInfo).port}`;
      for (const op of documentedOperations()) {
        const [method, template] = op.split(" ") as [string, string];
        const url = template.replace("{runId}", RUN).replace("{actionId}", ACT);
        // Anonymous: a served route answers 401, an unserved one 404 or 405.
        const res = await fetch(`${base}${url}`, { method, ...(method === "GET" ? {} : { body: "{}" }) });
        expect(res.status, op).toBe(401);
      }
    } finally {
      await new Promise<void>((resolve) => s.close(() => resolve()));
    }
  });
});
