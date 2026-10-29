import { describe, expect, it } from "vitest";
import { buildCatalog, CatalogError, visibleTools } from "../src/catalog.js";
import { toolsFromOpenApi } from "../src/openapi.js";
import { stripTenantId, toMcpTool, validateToolDefinition } from "../src/tool.js";
import { readTool, writeTool } from "./fixtures.js";

describe("validateToolDefinition", () => {
  const cases: Array<{ name: string; tool: ReturnType<typeof readTool>; want: string | null }> = [
    { name: "valid read tool", tool: readTool("passage.ap.invoice.list", "APInvoiceRead"), want: null },
    { name: "valid three-segment tool", tool: readTool("passage.knowledge.search", "CopilotUse"), want: null },
    { name: "bad name", tool: readTool("ap.invoice.list", "APInvoiceRead"), want: "must match" },
    { name: "missing duty", tool: readTool("passage.ap.invoice.list", ""), want: "x-passage-duty" },
    { name: "write tool without approval", tool: writeTool("passage.ap.invoice.post", "APInvoicePost", { approvalMode: "none" }), want: "write tools" },
    { name: "GET marked as write", tool: readTool("passage.ap.invoice.list", "APInvoiceRead", { readOnly: false }), want: "GET tools" },
    {
      name: "tenant_id declared in schema",
      tool: readTool("passage.ap.invoice.list", "APInvoiceRead", {
        inputSchema: { type: "object", properties: { tenant_id: { type: "string" } } },
      }),
      want: "tenant_id",
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const errors = validateToolDefinition(c.tool);
      if (c.want === null) expect(errors).toEqual([]);
      else expect(errors.join(" ")).toContain(c.want);
    });
  }
});

describe("toMcpTool wire shape", () => {
  it("removes tenant_id from the schema and declares Passage extensions", () => {
    const t = readTool("passage.ap.invoice.list", "APInvoiceRead", {
      inputSchema: { type: "object", properties: { status: { type: "string" } }, required: ["status"] },
    });
    const wire = toMcpTool(t);
    expect(wire["x-passage-duty"]).toBe("APInvoiceRead");
    expect(wire["x-passage-approval"]).toBe("none");
    expect(wire.annotations).toEqual({ readOnlyHint: true, destructiveHint: false, idempotentHint: true });
    expect(stripTenantId({ properties: { tenant_id: {}, a: {} }, required: ["tenant_id", "a"] })).toEqual({
      properties: { a: {} },
      required: ["a"],
    });
  });
});

describe("buildCatalog and visibleTools (AC-47.1)", () => {
  it("lists only tools whose duty the user holds", () => {
    const cat = buildCatalog([
      readTool("passage.ap.invoice.list", "APInvoiceRead"),
      writeTool("passage.ap.invoice.post", "APInvoicePost", { approvalMode: "approve_before_commit" }),
    ]);
    expect(visibleTools(cat, new Set(["APInvoiceRead"])).map((t) => t.name)).toEqual(["passage.ap.invoice.list"]);
    expect(visibleTools(cat, new Set()).length).toBe(0);
  });

  it("rejects duplicate names and invalid definitions together", () => {
    const dup = readTool("passage.ap.invoice.list", "APInvoiceRead");
    expect(() => buildCatalog([dup, dup])).toThrow(CatalogError);
    expect(() => buildCatalog([readTool("bad", "")])).toThrow(/passage/);
  });
});

describe("toolsFromOpenApi", () => {
  const doc = {
    paths: {
      "/v1/ap/invoices/{id}": {
        get: {
          operationId: "getInvoice",
          summary: "Get a vendor invoice",
          "x-passage-tool": "passage.ap.invoice.get",
          "x-passage-duty": "APInvoiceRead",
          parameters: [{ name: "id", in: "path", required: true, schema: { type: "string" } }],
          responses: { "200": { content: { "application/json": { schema: { type: "object" } } } } },
        },
        patch: { operationId: "ignoredNoMarker", parameters: [] },
      },
      "/v1/ap/invoices/{id}:propose-coding": {
        post: {
          operationId: "proposeCoding",
          "x-passage-tool": "passage.ap.invoice.propose_coding",
          "x-passage-duty": "APInvoiceCode",
          "x-passage-approval": "suggest",
          parameters: [{ name: "id", in: "path", required: true, schema: { type: "string" } }],
          requestBody: {
            content: {
              "application/json": {
                schema: { type: "object", properties: { note: { type: "string" } }, required: ["note"] },
              },
            },
          },
        },
      },
    },
  };

  it("generates tools only for operations that carry x-passage-tool", () => {
    const tools = toolsFromOpenApi(doc);
    expect(tools.map((t) => t.name)).toEqual(["passage.ap.invoice.get", "passage.ap.invoice.propose_coding"]);
    const get = tools[0];
    expect(get?.readOnly).toBe(true);
    expect(get?.sourceRef).toBe("getInvoice");
    expect(get?.inputSchema.required).toEqual(["id"]);
    const propose = tools[1];
    expect(propose?.approvalMode).toBe("suggest");
    expect(propose?.readOnly).toBe(false);
    expect(propose?.inputSchema.required).toEqual(["id", "note"]);
    expect(validateToolDefinition(propose!)).toEqual([]);
  });

  it("drops tenant_id and header parameters from the generated input schema (FR-47.1)", () => {
    const [tool] = toolsFromOpenApi({
      paths: {
        "/v1/ap/invoices": {
          get: {
            "x-passage-tool": "passage.ap.invoice.list",
            "x-passage-duty": "APInvoiceRead",
            parameters: [
              { name: "tenant_id", in: "query", required: true, schema: { type: "string" } },
              { name: "Idempotency-Key", in: "header", schema: { type: "string" } },
              { name: "status", in: "query", schema: { type: "string" } },
            ],
          },
        },
      },
    });
    expect(Object.keys(tool?.inputSchema.properties as object)).toEqual(["status"]);
    expect(tool?.inputSchema.required).toEqual([]);
    expect(validateToolDefinition(tool!)).toEqual([]);
  });

  it("refuses an operation with no duty", () => {
    expect(() =>
      toolsFromOpenApi({ paths: { "/v1/x": { get: { "x-passage-tool": "passage.x.y.read" } } } }),
    ).toThrow(/x-passage-duty/);
  });
});
