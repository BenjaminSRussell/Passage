// Vite middleware that stands in for the mobile service during `vite serve` / Playwright.
import type { Plugin } from "vite";
import { createMobileStore } from "./devStore";

interface StubReq {
  method?: string;
  url?: string;
  headers: { [key: string]: string | string[] | undefined };
  on(event: "data", listener: (chunk: Uint8Array | string) => void): void;
  on(event: "end" | "error" | "close", listener: (err?: Error) => void): void;
}

interface StubRes {
  statusCode: number;
  setHeader(name: string, value: string): void;
  writeHead(status: number, headers: Record<string, string>): void;
  write(chunk: string): void;
  end(chunk?: string): void;
}

function readBody(req: StubReq): Promise<string> {
  return new Promise((resolve, reject) => {
    const parts: string[] = [];
    const decoder = new TextDecoder();
    req.on("data", (chunk) => {
      parts.push(typeof chunk === "string" ? chunk : decoder.decode(chunk, { stream: true }));
    });
    req.on("end", () => resolve(parts.join("") + decoder.decode()));
    req.on("error", (err) => reject(err ?? new Error("read body")));
  });
}

function send(res: StubRes, status: number, body: unknown): void {
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json");
  res.end(JSON.stringify(body));
}

function pathOf(req: StubReq): string {
  return (req.url ?? "").split("?")[0] ?? "";
}

/**
 * Serves GET/POST `/api/v1/mobile/{flows,menus,events}` and `POST .../offline:replay`
 * from an in-process store. Installed only when `vite serve` has no `PASSAGE_SVC_MOBILE_URL`
 * and `PASSAGE_MOBILE_STUB` is not `0`. Unhandled `/api/v1/mobile/*` paths fall through.
 */
export function mobileDevStub(): Plugin {
  const store = createMobileStore();
  return {
    name: "passage-mobile-dev-stub",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const stubReq = req as StubReq;
        const stubRes = res as StubRes;
        const path = pathOf(stubReq);
        if (!path.startsWith("/api/v1/mobile/")) {
          next();
          return;
        }
        void handle(stubReq, stubRes, next, path).catch((err: unknown) => {
          const status = typeof err === "object" && err !== null && "status" in err ? Number((err as { status: number }).status) : 500;
          send(stubRes, status, { detail: err instanceof Error ? err.message : "stub error" });
        });
      });
    },
  };

  async function handle(req: StubReq, res: StubRes, next: () => void, path: string): Promise<void> {
    if (req.method === "GET" && path === "/api/v1/mobile/events") {
      res.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        Connection: "keep-alive",
      });
      res.write(":\n\n");
      const stop = store.onPublish((event) => {
        res.write(`event: flow.published\ndata: ${JSON.stringify({ flow_id: event.flowId, code: event.code, version: event.version })}\n\n`);
      });
      req.on("close", stop);
      return;
    }
    if (req.method === "GET" && path.startsWith("/api/v1/mobile/menus/")) {
      const id = decodeURIComponent(path.slice("/api/v1/mobile/menus/".length));
      send(res, 200, store.menu(id || "default"));
      return;
    }
    if (req.method === "POST" && path === "/api/v1/mobile/offline:replay") {
      const body = JSON.parse(await readBody(req)) as { idempotencyKey: string; command: string };
      send(res, 200, store.replay(body));
      return;
    }
    if (req.method === "POST" && path === "/api/v1/mobile/flows") {
      const flow = JSON.parse(await readBody(req)) as { code: string; entryStepId: string; steps: never[] };
      send(res, 200, store.saveDraft(flow));
      return;
    }
    const publish = /^\/api\/v1\/mobile\/flows\/([^/]+):publish$/.exec(path);
    if (req.method === "POST" && publish) {
      const raw = req.headers["if-match"];
      send(res, 200, store.publish(decodeURIComponent(publish[1]), Array.isArray(raw) ? raw[0] : raw));
      return;
    }
    next();
  }
}
