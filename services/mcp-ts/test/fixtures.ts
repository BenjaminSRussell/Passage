import { buildCatalog, type Catalog } from "../src/catalog.js";
import type { AuditEntry, AuditSink, GatewayDeps, Upstream, UpstreamRequest, UpstreamResponse } from "../src/gateway.js";
import type { ToolDefinition } from "../src/tool.js";

export function readTool(name: string, duty: string, extra: Partial<ToolDefinition> = {}): ToolDefinition {
  return {
    name,
    description: `${name} test tool`,
    version: 1,
    method: "GET",
    path: `/v1/${name.split(".").slice(1).join("/")}`,
    sourceRef: name,
    inputSchema: {
      type: "object",
      properties: { limit: { type: "integer" }, status: { type: "string" } },
      required: [],
    },
    outputSchema: { type: "object" },
    requiredDuty: duty,
    approvalMode: "none",
    readOnly: true,
    idempotent: true,
    ...extra,
  };
}

export function writeTool(name: string, duty: string, extra: Partial<ToolDefinition> = {}): ToolDefinition {
  return {
    ...readTool(name, duty),
    method: "POST",
    readOnly: false,
    idempotent: false,
    approvalMode: "suggest",
    inputSchema: { type: "object", properties: { invoiceId: { type: "string" } }, required: ["invoiceId"] },
    ...extra,
  };
}

export class FakeUpstream implements Upstream {
  readonly calls: UpstreamRequest[] = [];
  readonly responses: Array<UpstreamResponse | Error> = [];

  async send(req: UpstreamRequest): Promise<UpstreamResponse> {
    this.calls.push(req);
    const next = this.responses.shift() ?? { status: 200, body: { ok: true } };
    if (next instanceof Error) throw next;
    return next;
  }
}

export class MemoryAudit implements AuditSink {
  readonly entries: AuditEntry[] = [];

  async record(entry: AuditEntry): Promise<void> {
    this.entries.push(entry);
  }
}

export interface TestRig {
  deps: GatewayDeps;
  upstream: FakeUpstream;
  audit: MemoryAudit;
  sleeps: number[];
}

export function rig(catalog: Catalog | ToolDefinition[]): TestRig {
  const cat = Array.isArray(catalog) ? buildCatalog(catalog) : catalog;
  const upstream = new FakeUpstream();
  const audit = new MemoryAudit();
  const sleeps: number[] = [];
  const deps: GatewayDeps = {
    catalog: cat,
    upstream,
    audit,
    now: () => new Date("2026-10-21T00:00:00Z"),
    sleep: async (ms) => {
      sleeps.push(ms);
    },
  };
  return { deps, upstream, audit, sleeps };
}

export const principal = (duties: string[], extra: { runId?: string; traceparent?: string } = {}) => ({
  userId: "00000000-0000-7000-8000-000000000001",
  tenantId: "00000000-0000-7000-8000-0000000000aa",
  duties: new Set(duties),
  userToken: "user-token-for-test",
  ...extra,
});
