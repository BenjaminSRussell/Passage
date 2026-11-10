import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { createAgentsHandler, type AgentsApiDeps } from "./api/router.js";

const ROUTES = ["healthz", "readyz", "kill-switch", "start-run", "run", "chat", "decision", "other"] as const;
type Route = (typeof ROUTES)[number];

/** Bounded route label so metric cardinality cannot grow with caller input. */
export function routeLabel(path: string): Route {
  if (path === "/healthz") return "healthz";
  if (path === "/readyz") return "readyz";
  if (path === "/api/v1/agents/kill-switch") return "kill-switch";
  if (path === "/api/v1/copilot/chat") return "chat";
  if (/^\/api\/v1\/agents\/[a-z][a-z0-9_]*\/runs$/.test(path)) return "start-run";
  if (/\/decision$/.test(path)) return "decision";
  if (/^\/api\/v1\/agents\/runs\//.test(path)) return "run";
  return "other";
}

/** In-process counters served on the admin listener (kernel convention: /metrics on :9090, never on the public port). */
export class Metrics {
  private readonly http = new Map<string, number>();

  record(route: Route, status: number): void {
    const key = `${route}\u0000${status}`;
    this.http.set(key, (this.http.get(key) ?? 0) + 1);
  }

  render(): string {
    const lines = ["# HELP agents_http_requests_total HTTP responses by route and status.", "# TYPE agents_http_requests_total counter"];
    for (const key of [...this.http.keys()].sort()) {
      const [route, status] = key.split("\u0000");
      lines.push(`agents_http_requests_total{route="${route}",status="${status}"} ${this.http.get(key) ?? 0}`);
    }
    return `${lines.join("\n")}\n`;
  }
}

export interface ServerDeps {
  api: AgentsApiDeps;
  metrics: Metrics;
  /** Readiness: the database answers and the kill switch feed is not stale. */
  ready: () => Promise<boolean>;
}

/** The public listener's handler: probes plus the agents API. */
export function createRequestHandler(deps: ServerDeps): (req: IncomingMessage, res: ServerResponse) => void {
  const api = createAgentsHandler(deps.api);
  return (req, res) => {
    const path = new URL(req.url ?? "/", "http://agents.internal").pathname;
    const label = routeLabel(path);
    res.on("finish", () => deps.metrics.record(label, res.statusCode));
    const plain = (status: number, body: string): void => {
      res.writeHead(status, { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store" });
      res.end(body);
    };
    if (label === "healthz") return plain(200, "ok");
    if (label === "readyz") {
      deps.ready().then((ok) => plain(ok ? 200 : 503, ok ? "ready" : "not ready"), () => plain(503, "not ready"));
      return;
    }
    void api(req, res);
  };
}

export function listen(server: Server, host: string, port: number): Promise<Server> {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, host, () => resolve(server));
  });
}

/** Admin listener: GET /metrics and GET /healthz only. */
export function createAdminServer(metrics: Metrics): Server {
  return createServer((req, res) => {
    const path = new URL(req.url ?? "/", "http://admin.internal").pathname;
    if (req.method === "GET" && path === "/metrics") {
      res.writeHead(200, { "content-type": "text/plain; version=0.0.4; charset=utf-8" });
      res.end(metrics.render());
      return;
    }
    if (req.method === "GET" && path === "/healthz") {
      res.writeHead(200, { "content-type": "text/plain" });
      res.end("ok");
      return;
    }
    res.writeHead(404);
    res.end();
  });
}
