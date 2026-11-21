import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { createAgentsHandler, type AgentsApiDeps } from "./api/router.js";
import type { TaskFallbackReason } from "./runtime/copilot.js";
import type { GuardDecision } from "./runtime/egressGuard.js";

const ROUTES = ["healthz", "readyz", "kill-switch", "ai-data-policy", "start-run", "run", "chat", "conversations", "messages", "decision", "budgets", "definitions", "briefings", "other"] as const;
type Route = (typeof ROUTES)[number];

/** Bounded route label so metric cardinality cannot grow with caller input. */
export function routeLabel(path: string): Route {
  if (path === "/healthz") return "healthz";
  if (path === "/readyz") return "readyz";
  if (path === "/api/v1/agents/kill-switch") return "kill-switch";
  if (path === "/api/v1/agents/ai-data-policy") return "ai-data-policy";
  if (path === "/api/v1/copilot/chat") return "chat";
  if (/^\/api\/v1\/agents\/briefings([/:]|$)/.test(path)) return "briefings";
  if (path === "/api/v1/agents/copilot/conversations") return "conversations";
  if (/^\/api\/v1\/agents\/copilot\/conversations\/[0-9a-fA-F-]{36}\/messages$/.test(path)) return "messages";
  if (/^\/api\/v1\/agents\/definitions(\/[a-z][a-z0-9_]*:(disable|enable))?$/.test(path)) return "definitions";
  if (/^\/api\/v1\/agents\/budgets(\/[a-z][a-z0-9_]*)?$/.test(path)) return "budgets";
  if (/^\/api\/v1\/agents\/[a-z][a-z0-9_]*\/runs$/.test(path)) return "start-run";
  if (/\/decision$/.test(path)) return "decision";
  if (/^\/api\/v1\/agents\/runs\//.test(path)) return "run";
  return "other";
}

/** In-process counters served on the admin listener (kernel convention: /metrics on :9090, never on the public port). */
const GUARD_BUCKETS = [0.005, 0.025, 0.1, 0.5, 2];

export class Metrics {
  private readonly http = new Map<string, number>();
  private readonly guard = new Map<string, { buckets: number[]; sum: number; count: number }>();
  private readonly taskFallback = new Map<TaskFallbackReason, number>();

  countTaskFallback(reason: TaskFallbackReason): void {
    this.taskFallback.set(reason, (this.taskFallback.get(reason) ?? 0) + 1);
  }

  observeGuard(seconds: number, decision: GuardDecision): void {
    const h = this.guard.get(decision) ?? { buckets: GUARD_BUCKETS.map(() => 0), sum: 0, count: 0 };
    GUARD_BUCKETS.forEach((le, i) => {
      if (seconds <= le) h.buckets[i] = (h.buckets[i] ?? 0) + 1;
    });
    h.sum += seconds;
    h.count += 1;
    this.guard.set(decision, h);
  }

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
    lines.push("# HELP agents_egress_guard_duration_seconds Egress guard duration by decision.", "# TYPE agents_egress_guard_duration_seconds histogram");
    for (const decision of [...this.guard.keys()].sort()) {
      const h = this.guard.get(decision);
      if (h === undefined) continue;
      GUARD_BUCKETS.forEach((le, i) => lines.push(`agents_egress_guard_duration_seconds_bucket{decision="${decision}",le="${le}"} ${h.buckets[i] ?? 0}`));
      lines.push(`agents_egress_guard_duration_seconds_bucket{decision="${decision}",le="+Inf"} ${h.count}`);
      lines.push(`agents_egress_guard_duration_seconds_sum{decision="${decision}"} ${h.sum}`, `agents_egress_guard_duration_seconds_count{decision="${decision}"} ${h.count}`);
    }
    lines.push("# HELP agents_copilot_task_fallback_total Task-mode chats answered without a tool, by reason.", "# TYPE agents_copilot_task_fallback_total counter");
    for (const reason of [...this.taskFallback.keys()].sort()) lines.push(`agents_copilot_task_fallback_total{reason="${reason}"} ${this.taskFallback.get(reason) ?? 0}`);
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
