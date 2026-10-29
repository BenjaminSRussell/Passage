import { describe, expect, it } from "vitest";
import { callTool } from "../src/gateway.js";
import { schemaViolations } from "../src/schema.js";
import { principal, readTool, rig, writeTool } from "./fixtures.js";

const invoiceList = {
  type: "object",
  properties: {
    items: {
      type: "array",
      items: {
        type: "object",
        properties: {
          id: { type: "string" },
          amount: { type: "string" },
          status: { type: "string", enum: ["OPEN", "POSTED"] },
        },
        required: ["id", "amount"],
      },
    },
    nextCursor: { type: ["string", "null"] },
  },
  required: ["items"],
};

describe("schemaViolations (outputSchema conformance)", () => {
  it("accepts a conforming body", () => {
    const body = { items: [{ id: "i1", amount: "10.00", status: "OPEN" }], nextCursor: null };
    expect(schemaViolations(invoiceList, body)).toEqual([]);
  });

  it("reports a missing required property with its path", () => {
    expect(schemaViolations(invoiceList, { items: [{ id: "i1" }] })).toEqual(["$.items[0].amount is required"]);
  });

  it("reports a wrong type, a value outside enum, and a non-array items list", () => {
    expect(schemaViolations(invoiceList, { items: [{ id: 7, amount: "1" }] })).toEqual(["$.items[0].id must be string"]);
    expect(schemaViolations(invoiceList, { items: [{ id: "a", amount: "1", status: "DRAFT" }] })).toEqual([
      "$.items[0].status is not one of the allowed values",
    ]);
    expect(schemaViolations(invoiceList, { items: "none" })).toEqual(["$.items must be array"]);
  });

  it("treats integer as a whole number and rejects NaN for number", () => {
    expect(schemaViolations({ type: "integer" }, 2.5)).toEqual(["$ must be integer"]);
    expect(schemaViolations({ type: "number" }, Number.NaN)).toEqual(["$ must be number"]);
    expect(schemaViolations({ type: "integer" }, 3)).toEqual([]);
  });

  it("ignores keywords outside the supported subset", () => {
    expect(schemaViolations({ $ref: "#/x", oneOf: [] }, { anything: true })).toEqual([]);
  });

  it("admits null for an OpenAPI nullable property and nowhere else", () => {
    const schema = { type: "object", properties: { validTo: { type: "string", format: "date", nullable: true }, id: { type: "string" } } };
    expect(schemaViolations(schema, { validTo: null, id: "a" })).toEqual([]);
    expect(schemaViolations(schema, { validTo: "2026-01-01", id: null })).toEqual(["$.id must be string"]);
    expect(schemaViolations(schema, { validTo: 7 })).toEqual(["$.validTo must be string"]);
  });

  it("rejects a body whose top-level type differs", () => {
    expect(schemaViolations({ type: "object" }, [])).toEqual(["$ must be object"]);
  });
});

describe("callTool outputSchema enforcement", () => {
  it("returns MCP-0502 and audits UPSTREAM_ERROR when the body does not conform", async () => {
    const tool = readTool("passage.ap.invoice.list", "APInvoiceRead", { outputSchema: invoiceList });
    const r = rig([tool]);
    r.upstream.responses.push({ status: 200, body: { items: [{ amount: "1" }] } });
    const res = await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", {});
    expect(res).toMatchObject({ kind: "error", status: 502, code: "MCP-0502" });
    expect(r.audit.entries[0]).toMatchObject({ outcome: "UPSTREAM_ERROR", status: 502 });
  });

  it("returns the body when it conforms", async () => {
    const tool = readTool("passage.ap.invoice.list", "APInvoiceRead", { outputSchema: invoiceList });
    const r = rig([tool]);
    r.upstream.responses.push({ status: 200, body: { items: [{ id: "i1", amount: "1" }] } });
    const res = await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", {});
    expect(res).toMatchObject({ kind: "ok", status: 200 });
  });

  it("lists up to three violations in the message", async () => {
    const tool = readTool("passage.ap.invoice.list", "APInvoiceRead", { outputSchema: invoiceList });
    const r = rig([tool]);
    r.upstream.responses.push({ status: 200, body: { items: [{}, {}] } });
    const res = await callTool(r.deps, principal(["APInvoiceRead"]), "passage.ap.invoice.list", {});
    expect(res).toMatchObject({ kind: "error", code: "MCP-0502" });
    expect(res.kind === "error" ? res.message.split("; ") : []).toHaveLength(3);
  });

  it("does not check write tools: their upstream has already committed", async () => {
    const tool = writeTool("passage.ap.invoice.post", "APInvoicePost", { outputSchema: invoiceList });
    const r = rig([tool]);
    r.upstream.responses.push({ status: 200, body: { posted: true } });
    const res = await callTool(r.deps, principal(["APInvoicePost"]), "passage.ap.invoice.post", { invoiceId: "i1" });
    expect(res).toMatchObject({ kind: "ok", status: 200, body: { posted: true } });
    expect(r.audit.entries[0]).toMatchObject({ outcome: "OK" });
  });
});
