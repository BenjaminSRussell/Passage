import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import { buildCatalog } from "./catalog.js";
import type { AuditEntry, AuditSink, GatewayDeps } from "./gateway.js";
import { createFetchUpstream } from "./upstream.js";
import { toolsFromOpenApi } from "./openapi.js";
import { startServer } from "./server.js";
import { createRateLimiter } from "./ratelimit.js";
import { isObject, type JsonObject } from "./tool.js";

export interface Config {
  apiUrl: string;
  openApiFile: string;
  host: string;
  port: number;
  build: string;
}

/** 12-factor config (kernel conventions). Invalid config is an error at startup. */
export function loadConfig(env: NodeJS.ProcessEnv): Config {
  const apiUrl = env.PASSAGE_API_URL ?? "";
  const openApiFile = env.PASSAGE_OPENAPI_JSON ?? "";
  if (!/^https?:\/\//.test(apiUrl)) throw new Error("PASSAGE_API_URL must be an http(s) URL");
  if (openApiFile === "") throw new Error("PASSAGE_OPENAPI_JSON must point to the OpenAPI document as JSON");
  const addr = env.HTTP_ADDR ?? "0.0.0.0:8080";
  const idx = addr.lastIndexOf(":");
  if (idx === -1) throw new Error("HTTP_ADDR must be host:port");
  const host = addr.slice(0, idx) || "0.0.0.0";
  const port = Number(addr.slice(idx + 1));
  if (!Number.isInteger(port) || port <= 0 || port > 65535) throw new Error("HTTP_ADDR must end in a valid port");
  return { apiUrl, openApiFile, host, port, build: env.SERVICE_VERSION ?? "0.1.0" };
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
  const server = await startServer(
    {
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
