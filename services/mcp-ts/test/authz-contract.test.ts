import { describe, expect, it } from "vitest";
import { buildCatalog, visibleTools } from "../src/catalog.js";
import { callTool, type Principal } from "../src/gateway.js";
import type { ToolDefinition } from "../src/tool.js";
import { readTool, rig, writeTool } from "./fixtures.js";

// Wave 47 proof: "an agent acting for a user can never exceed that user's
// permissions" (FR-47.2, AC-47.1, AC-47.2), as a seeded contract test over a
// generated catalog and generated users. The expected visibility and the
// expected upstream calls come from the generator's own duty map.

/** mulberry32: seeded so every machine runs the same cases. */
function prng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const DUTIES = Array.from({ length: 12 }, (_, i) => `Duty${i}`);
const r = prng(4702);
const pick = <T>(xs: readonly T[]): T => xs[Math.floor(r() * xs.length)] as T;
const dutyOf = new Map<string, string>();
const defs: ToolDefinition[] = Array.from({ length: 60 }, (_, i) => {
  const name = `passage.m${i % 6}.entity${i}.${i % 2 === 0 ? "read" : "post"}`;
  const duty = pick(DUTIES);
  dutyOf.set(name, duty);
  return i % 2 === 0 ? readTool(name, duty) : writeTool(name, duty);
});

const user = (i: number, duties: Set<string>): Principal => ({
  userId: `00000000-0000-7000-8000-${String(i).padStart(12, "0")}`,
  tenantId: i % 2 === 0 ? "00000000-0000-7000-8000-0000000000aa" : "00000000-0000-7000-8000-0000000000bb",
  duties,
  userToken: `token-of-user-${i}`,
  runId: `run-${i}`,
});

describe("agent permission contract over generated users (AC2)", () => {
  it("for 300 users, tools/list shows exactly the tools their duties allow", () => {
    const cat = buildCatalog(defs);
    for (let i = 0; i < 300; i++) {
      const duties = new Set(DUTIES.filter(() => r() < 0.3));
      const listed = visibleTools(cat, duties).map((t) => t.name).sort();
      const expected = [...dutyOf].filter(([, d]) => duties.has(d)).map(([n]) => n).sort();
      expect(listed, `user ${i}`).toEqual(expected);
    }
  });

  it("for 300 users x 8 calls, the API is reached only with the caller's own token and only for a held duty", async () => {
    const g = rig(defs);
    let reached = 0;
    let denied = 0;
    let apiDenied = 0;
    for (let i = 0; i < 300; i++) {
      const p = user(i, new Set(DUTIES.filter(() => r() < 0.3)));
      for (let k = 0; k < 8; k++) {
        const name = pick(defs).name;
        const held = p.duties.has(dutyOf.get(name)!);
        // OPA at the API may still refuse (data scope): the gateway reports that as a denial too.
        const opaRefuses = r() < 0.15;
        if (opaRefuses) g.upstream.responses.push({ status: 403, body: {} });
        const before = g.upstream.calls.length;
        const audits = g.audit.entries.length;
        const res = await callTool(g.deps, p, name, { invoiceId: "INV-1" });
        const sent = g.upstream.calls.slice(before);

        if (!held) {
          if (opaRefuses) g.upstream.responses.pop();
          expect(res).toMatchObject({ kind: "error", status: 403, code: "AGT-0203" });
          expect(sent).toHaveLength(0);
          denied++;
        } else {
          expect(sent).toHaveLength(1);
          expect(sent[0]!.headers.authorization).toBe(`Bearer ${p.userToken}`);
          expect(sent[0]!.headers["x-agent-run-id"]).toBe(p.runId);
          expect(sent[0]!.args).not.toHaveProperty("tenant_id");
          if (opaRefuses) {
            expect(res).toMatchObject({ kind: "error", status: 403, code: "AGT-0203" });
            apiDenied++;
          } else {
            expect(res.kind).toBe("ok");
            reached++;
          }
        }
        // Every call, allowed or not, leaves one audit entry for this user and tenant.
        const entry = g.audit.entries.slice(audits);
        expect(entry).toHaveLength(1);
        expect(entry[0]).toMatchObject({ userId: p.userId, tenantId: p.tenantId, tool: name, runId: p.runId });
        expect(entry[0]!.outcome).toBe(!held || opaRefuses ? "DENIED" : "OK");
      }
    }
    // Every branch ran on a meaningful share of the 2,400 calls.
    expect(reached).toBeGreaterThan(100);
    expect(denied).toBeGreaterThan(100);
    expect(apiDenied).toBeGreaterThan(20);
  });

  it("a model-supplied tenant_id is refused even when the user holds the duty, so no call crosses tenants", async () => {
    const g = rig(defs);
    for (const d of defs) {
      const p = user(1, new Set(DUTIES));
      const res = await callTool(g.deps, p, d.name, { invoiceId: "INV-1", tenant_id: "00000000-0000-7000-8000-0000000000aa" });
      expect(res).toMatchObject({ kind: "error", status: 400 });
    }
    expect(g.upstream.calls).toHaveLength(0);
  });
});
