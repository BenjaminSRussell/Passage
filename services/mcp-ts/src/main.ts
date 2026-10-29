import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import { buildCatalog } from "./catalog.js";
import type { AuditEntry, AuditSink, GatewayDeps } from "./gateway.js";
import { createFetchUpstream } from "./upstream.js";
import { toolsFromOpenApi } from "./openapi.js";
import { startServer } from "./server.js";
import { createRateLimiter } from "./ratelimit.js";
import { Metrics, startAdminServer } from "./metrics.js";
import { isObject, type JsonObject } from "./tool.js";

export interface Config {
  apiUrl: string;
  openApiFile: string;
  host: string;
  port: number;
  build: string;
  /** Admin listener for /metrics (kernel convention, ADMIN_ADDR, default :9090). */
  adminHost: string;
  adminPort: number;
}

function parseAddr(value: string, name: string): { host: string; port: number } {
  const idx = value.lastIndexOf(":");
  if (idx === -1) throw new Error(`${name} must be host:port`);
  const host = value.slice(0, idx) || "0.0.0.0";
  const port = Number(value.slice(idx + 1));
  if (!Number.isInteger(port) || port <= 0 || port > 65535) throw new Error(`${name} must end in a valid port`);
  return { host, port };
}

/** 12-factor config (kernel conventions). Invalid config is an error at startup. */
export function loadConfig(env: NodeJS.ProcessEnv): Config {
  const apiUrl = env.PASSAGE_API_URL ?? "";
  const openApiFile = env.PASSAGE_OPENAPI_JSON ?? "";
  if (!/^https?:\/\//.test(apiUrl)) throw new Error("PASSAGE_API_URL must be an http(s) URL");
  if (openApiFile === "") throw new Error("PASSAGE_OPENAPI_JSON must point to the OpenAPI document as JSON");
  const { host, port } = parseAddr(env.HTTP_ADDR ?? "0.0.0.0:8080", "HTTP_ADDR");
  const admin = parseAddr(env.ADMIN_ADDR ?? "0.0.0.0:9090", "ADMIN_ADDR");
  if (admin.port === port) throw new Error("ADMIN_ADDR must use a different port from HTTP_ADDR");
  return { apiUrl, openApiFile, host, port, build: env.SERVICE_VERSION ?? "0.1.0", adminHost: admin.host, adminPort: admin.port };
}

/** Structured JSON line per audit entry; no tokens or argument values are logged. */
export const stdoutAudit: AuditSink = {
  async record(entry: AuditEntry): Promise<void> {
    process.stdout.write(`${JSON.stringify({ level: "info", message: "mcp.tool_call", service: "mcp-ts", ...entry })}\n`);
  },
};

async function main(): Promise<void> {
  const cfg = loadConfig(process.env);
  const doc = JSON.parse(await readFile(cfg.openApiFile, "utf8")) as unknown;
  if (!isObject(doc)) throw new Error("OpenAPI document must be a JSON object");
  const catalog = buildCatalog(toolsFromOpenApi(doc as JsonObject));
  const deps: GatewayDeps = {
    catalog,
    upstream: createFetchUpstream(cfg.apiUrl),
    audit: stdoutAudit,
    now: () => new Date(),
    sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  };
  const metrics = new Metrics();
  const admin = await startAdminServer(metrics, { host: cfg.adminHost, port: cfg.adminPort });
  const server = await startServer(
    {
      metrics,
      deps,
      build: cfg.build,
      ready: async () => catalog.tools.length > 0,
      limiter: createRateLimiter({ nowMs: () => Date.now() }),
    },
    { host: cfg.host, port: cfg.port },
  );
  process.stdout.write(`${JSON.stringify({ level: "info", message: "mcp listening", service: "mcp-ts", tools: catalog.tools.length })}\n`);
  // Kubernetes sends SIGTERM on rollout: stop accepting, let in-flight calls finish.
  process.once("SIGTERM", () => {
    admin.close();
    server.close(() => process.exit(0));
  });
}

// Only the entrypoint starts the server; importing this module (tests) has no side effects.
if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((err: unknown) => {
    process.stderr.write(`${JSON.stringify({ level: "error", message: "mcp failed to start", service: "mcp-ts", error: String(err) })}\n`);
    process.exit(1);
  });
}
