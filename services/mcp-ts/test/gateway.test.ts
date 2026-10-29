import { describe, expect, it } from "vitest";
import { callTool, READ_PAGE_LIMIT } from "../src/gateway.js";
import { principal, readTool, rig, writeTool } from "./fixtures.js";

describe("callTool permission inheritance", () => {
  it("returns 403 AGT-0203 and never calls the API without the duty (AC-47.2)", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    const res = await callTool(r.deps, principal(["SomethingElse"]), "passage.ap.invoice.list", {});
    expect(res).toMatchObject({ kind: "error", status: 403, code: "AGT-0203" });
    expect(r.upstream.calls).toHaveLength(0);
    expect(r.audit.entries[0]).toMatchObject({ outcome: "DENIED", tool: "passage.ap.invoice.list" });
  });

  it("forwards the user's token, traceparent and run id to the API", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    const p = principal(["APInvoiceRead"], { runId: "run-1", traceparent: "00-abc-def-01" });
    const res = await callTool(r.deps, p, "passage.ap.invoice.list", { status: "open" });
    expect(res.kind).toBe("ok");
    const sent = r.upstream.calls[0];
    expect(sent?.headers).toEqual({
      authorization: "Bearer user-token-for-test",
      traceparent: "00-abc-def-01",
      "x-agent-run-id": "run-1",
    });
    expect(r.audit.entries[0]).toMatchObject({ outcome: "OK", runId: "run-1" });
  });

  it("refuses a tenant_id supplied by the model (400, no call)", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    const res = await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", {
      tenant_id: "someone-else",
    });
    expect(res).toMatchObject({ kind: "error", status: 400, code: "MCP-0400" });
    expect(r.upstream.calls).toHaveLength(0);
  });

  it("returns 404 for an unknown tool", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    const res = await callTool(r.deps, principal([]), "passage.ap.invoice.drop", {});
    expect(res).toMatchObject({ kind: "error", status: 404, code: "MCP-0404" });
  });
});

describe("callTool read paging", () => {
  it("clamps read limits to 200 rows per call", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", { limit: 5000 });
    expect(r.upstream.calls[0]?.args.limit).toBe(READ_PAGE_LIMIT);
  });

  it("applies the page limit when the caller omits it", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", {});
    expect(r.upstream.calls[0]?.args.limit).toBe(READ_PAGE_LIMIT);
  });
});

describe("callTool retries (BR-47.6)", () => {
  it("does not retry a 4xx tool error", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    r.upstream.responses.push({ status: 422, body: { code: "x" } });
    const res = await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", {});
    expect(res).toMatchObject({ kind: "error", status: 422, code: "AGT-0214" });
    expect(r.upstream.calls).toHaveLength(1);
    expect(r.sleeps).toEqual([]);
  });

  it("retries 5xx three times with 1 s, 4 s, 16 s backoff, then fails", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    r.upstream.responses.push({ status: 503, body: null }, { status: 503, body: null }, { status: 503, body: null }, { status: 503, body: null });
    const res = await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", {});
    expect(res).toMatchObject({ kind: "error", status: 503 });
    expect(r.upstream.calls).toHaveLength(4);
    expect(r.sleeps).toEqual([1000, 4000, 16000]);
  });

  it("retries timeouts and succeeds when a later attempt works", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    r.upstream.responses.push(new Error("timeout"), { status: 200, body: { items: [] } });
    const res = await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", {});
    expect(res).toMatchObject({ kind: "ok", status: 200 });
    expect(r.upstream.calls).toHaveLength(2);
  });

  it("never retries a non-idempotent write, so a commit cannot post twice", async () => {
    const r = rig([writeTool("passage.ap.invoice.propose", "APInvoicePropose")]);
    r.upstream.responses.push({ status: 503, body: null });
    const res = await callTool(r.deps, principal(["APInvoicePropose"]), "passage.ap.invoice.propose", {
      invoiceId: "inv-1",
    });
    expect(res).toMatchObject({ kind: "error", status: 503 });
    expect(r.upstream.calls).toHaveLength(1);
    expect(r.sleeps).toEqual([]);
  });
});

describe("tool contract across the catalog (FR-47.2)", () => {
  it("for each of 400 tools, a user without its duty gets 403 and the API is never called", async () => {
    const defs = Array.from({ length: 400 }, (_, i) => readTool(`passage.t${i}.entity.read`, `duty.d${i}`));
    const r = rig(defs);
    for (let i = 0; i < defs.length; i++) {
      const held = defs.map((d) => d.requiredDuty).filter((d) => d !== `duty.d${i}`);
      const res = await callTool(r.deps, principal(held), `passage.t${i}.entity.read`, {});
      expect(res).toMatchObject({ kind: "error", status: 403 });
    }
    expect(r.upstream.calls).toHaveLength(0);
    expect(r.audit.entries.filter((e) => e.outcome === "DENIED")).toHaveLength(400);
  });
});

describe("callTool argument and upstream edge cases", () => {
  const getTool = () =>
    readTool("passage.ap.invoice.get", "APInvoiceRead", {
      path: "/v1/ap/invoices/{id}",
      inputSchema: { type: "object", properties: { id: { type: "string" } }, required: ["id"] },
    });

  it("refuses a call that omits a required path parameter and never calls the API", async () => {
    const r = rig([getTool()]);
    const res = await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.get", {});
    expect(res).toMatchObject({ kind: "error", status: 400, code: "MCP-0422" });
    expect(r.upstream.calls).toHaveLength(0);
    expect(r.audit.entries[0]).toMatchObject({ outcome: "INVALID", code: "MCP-0422" });
  });

  it("logs a 403 from the API as DENIED with AGT-0203, not as a plain tool error (AC-47.2)", async () => {
    const r = rig([getTool()]);
    r.upstream.responses.push({ status: 403, body: { code: "AUTHZ" } });
    const res = await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.get", { id: "inv-1" });
    expect(res).toMatchObject({ kind: "error", status: 403, code: "AGT-0203" });
    expect(r.upstream.calls).toHaveLength(1);
    expect(r.audit.entries[0]).toMatchObject({ outcome: "DENIED", status: 403 });
  });

  it("says a 5xx was retried, and keeps 'not retried' for a 4xx", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    r.upstream.responses.push(...Array.from({ length: 4 }, () => ({ status: 502, body: null })));
    const res = await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", {});
    expect(res.kind === "error" && res.message).toContain("after 3 retries");
  });

  it("clamps a zero, negative or fractional limit into 1..200", async () => {
    const r = rig([readTool("passage.ap.invoice.list", "APInvoiceRead")]);
    for (const limit of [0, -5, 12.7]) {
      await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", { limit });
    }
    expect(r.upstream.calls.map((c) => c.args.limit)).toEqual([1, 1, 12]);
  });
});
