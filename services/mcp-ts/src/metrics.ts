import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";

/**
 * In-process Prometheus counters for the gateway. Kernel convention: /metrics
 * is served on the separate admin listener (port 9090), which only the
 * metrics namespace may reach, never on the public listener. Labels are
 * bounded: the route is one of a fixed set and the status is a three-digit
 * code, so cardinality cannot grow with caller input.
 */
export const METRIC_ROUTES = ["healthz", "readyz", "mcp", "other"] as const;
export type MetricRoute = (typeof METRIC_ROUTES)[number];

export const METRICS_CONTENT_TYPE = "text/plain; version=0.0.4; charset=utf-8";

export class Metrics {
  private readonly http = new Map<string, number>();
  private rateLimited = 0;

  /** Records one finished response. */
  recordResponse(route: MetricRoute, status: number): void {
    const key = `${route}\u0000${status}`;
    this.http.set(key, (this.http.get(key) ?? 0) + 1);
  }

  /** Records a 429 refusal from the per-pod limiter. */
  recordRateLimited(): void {
    this.rateLimited += 1;
  }

  /** The Prometheus text exposition format, sorted so output is stable. */
  render(): string {
    const lines: string[] = [
      "# HELP mcp_http_requests_total HTTP responses by route and status.",
      "# TYPE mcp_http_requests_total counter",
    ];
    const keys = [...this.http.keys()].sort();
    for (const key of keys) {
      const [route, status] = key.split("\u0000");
      lines.push(`mcp_http_requests_total{route="${route}",status="${status}"} ${this.http.get(key) ?? 0}`);
    }
    lines.push("# HELP mcp_rate_limited_total Requests refused with 429 by the per-pod limiter.");
    lines.push("# TYPE mcp_rate_limited_total counter");
    lines.push(`mcp_rate_limited_total ${this.rateLimited}`);
    return `${lines.join("\n")}\n`;
  }
}

/** Maps a request path to a bounded route label. */
export function routeLabel(path: string): MetricRoute {
  switch (path) {
    case "/healthz":
      return "healthz";
    case "/readyz":
      return "readyz";
    case "/mcp":
      return "mcp";
    default:
      return "other";
  }
}

/**
 * The admin listener's handler: `GET /metrics` in the Prometheus text format
 * and `GET /healthz`. Everything else is 404, other methods on /metrics 405.
 */
export function createAdminHandler(metrics: Metrics): (req: IncomingMessage, res: ServerResponse) => void {
  return (req, res) => {
    const path = new URL(req.url ?? "/", "http://localhost").pathname;
    if (path !== "/metrics" && path !== "/healthz") {
      res.writeHead(404, { "content-type": "text/plain; charset=utf-8" });
      res.end("not found\n");
      return;
    }
    if (req.method !== "GET" && req.method !== "HEAD") {
      res.writeHead(405, { allow: "GET, HEAD", "content-type": "text/plain; charset=utf-8" });
      res.end("method not allowed\n");
      return;
    }
    const body = path === "/metrics" ? metrics.render() : "ok\n";
    res.writeHead(200, {
      "content-type": path === "/metrics" ? METRICS_CONTENT_TYPE : "text/plain; charset=utf-8",
      "cache-control": "no-store",
      "content-length": Buffer.byteLength(body).toString(),
    });
    res.end(req.method === "HEAD" ? undefined : body);
  };
}

/** Starts the admin listener and resolves once it is listening. */
export function startAdminServer(metrics: Metrics, addr: { host: string; port: number }): Promise<Server> {
  const server = createServer(createAdminHandler(metrics));
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(addr.port, addr.host, () => {
      server.off("error", reject);
      resolve(server);
    });
  });
}
