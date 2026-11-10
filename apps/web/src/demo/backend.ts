// In-browser stand-in for the Passage services, used only by the GitHub Pages demo build (`vite build --mode demo`).
// It patches window.fetch: requests to /api/... are answered by the handlers registered with route(); anything
// else goes to the network. Handlers live in ./handlers, one file per domain, and keep their data in memory, so
// a page reload resets the demo.

export interface DemoRequest {
  method: string;
  path: string;
  /** Path parameters named in the route pattern, already URI-decoded. */
  params: Record<string, string>;
  query: URLSearchParams;
  headers: Headers;
  /** Parsed JSON body, the raw string if it was not JSON, or undefined. */
  body: unknown;
}

/** Return this from a handler to control the status or headers; any other value is sent as a 200 JSON body. */
export class DemoResponse {
  constructor(
    readonly status: number,
    readonly body?: unknown,
    readonly headers: Record<string, string> = {},
  ) {}
}

/** One server-sent event for reply.events(). data is JSON-encoded unless it is already a string. */
export interface DemoEvent {
  event?: string;
  data: unknown;
  id?: string;
}

/** Marks a handler result as an event stream (see FakeEventSource below). */
export class DemoEvents {
  constructor(
    readonly events: DemoEvent[],
    /** Re-run the handler this often (ms) while the stream is open; omit for a one-shot stream. */
    readonly everyMs?: number,
  ) {}
}

export const reply = {
  ok: (body: unknown, headers: Record<string, string> = {}) => new DemoResponse(200, body, headers),
  created: (body: unknown, headers: Record<string, string> = {}) => new DemoResponse(201, body, headers),
  accepted: (body: unknown = undefined) => new DemoResponse(202, body),
  noContent: () => new DemoResponse(204),
  /** A body with an ETag of the given version, the way the services answer versioned reads and writes. */
  versioned: (body: unknown, version: number, status = 200) => new DemoResponse(status, body, { ETag: `"${version}"` }),
  /** A non-JSON body such as a CSV export; sent as-is with the given content type. */
  text: (body: string, contentType = "text/plain; charset=utf-8", filename?: string) =>
    new DemoResponse(200, new RawBody(body), { "Content-Type": contentType, ...(filename ? { "Content-Disposition": `attachment; filename="${filename}"` } : {}) }),
  /** Server-sent events for an EventSource opened on this route; the handler runs again every everyMs while open. */
  events: (events: DemoEvent[], everyMs?: number) => new DemoEvents(events, everyMs),
  problem: (status: number, code: string, detail: string, errors?: Record<string, string>) =>
    new DemoResponse(status, { type: "about:blank", title: detail, status, code, detail, ...(errors ? { errors } : {}) }, { "Content-Type": "application/problem+json" }),
};

/** A body sent verbatim (reply.text) instead of JSON-encoded. */
export class RawBody {
  constructor(readonly text: string) {}
}

type Handler = (req: DemoRequest) => unknown;

interface Route {
  method: string;
  pattern: string;
  regex: RegExp;
  keys: string[];
  handler: Handler;
}

const routes: Route[] = [];

function compile(pattern: string): { regex: RegExp; keys: string[] } {
  const keys: string[] = [];
  const source = pattern
    .split(/(\{[A-Za-z0-9_]+\})/)
    .map((part) => {
      const m = /^\{([A-Za-z0-9_]+)\}$/.exec(part);
      if (m) {
        keys.push(m[1]);
        return "([^/]+?)";
      }
      return part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    })
    .join("");
  return { regex: new RegExp(`^${source}/?$`), keys };
}

/**
 * Registers a handler. Patterns are full paths with {name} parameters, for example
 * route("POST", "/api/v1/sales/orders/{id}:confirm", ...). Earlier registrations win, so register
 * literal paths (".../orders/new-defaults") before parameterised ones (".../orders/{id}").
 */
export function route(method: string, pattern: string, handler: Handler): void {
  const { regex, keys } = compile(pattern);
  routes.push({ method: method.toUpperCase(), pattern, regex, keys, handler });
}

/** Requests no handler answered, for checking coverage from the console or Playwright: window.__passageDemoMisses. */
export const misses: string[] = [];

const UNSUPPORTED_READ = "This screen has no sample data in the demo. The demo tour on the home page lists the screens that do.";
const UNSUPPORTED_WRITE = "This action isn't part of the demo. The demo tour on the home page lists the screens you can try.";

async function dispatch(method: string, url: URL, headers: Headers, rawBody: string | undefined): Promise<DemoResponse | DemoEvents> {
  const path = url.pathname;
  for (const r of routes) {
    if (r.method !== method) continue;
    const m = r.regex.exec(path);
    if (!m) continue;
    const params: Record<string, string> = {};
    r.keys.forEach((k, i) => (params[k] = decodeURIComponent(m[i + 1])));
    let body: unknown = rawBody;
    if (rawBody !== undefined && rawBody !== "") {
      try {
        body = JSON.parse(rawBody);
      } catch {
        body = rawBody;
      }
    }
    try {
      const out = await r.handler({ method, path, params, query: url.searchParams, headers, body });
      return out instanceof DemoResponse || out instanceof DemoEvents ? out : new DemoResponse(200, out);
    } catch (err) {
      console.error(`[demo] ${method} ${path} failed`, err);
      return reply.problem(500, "DEMO-0500", `Demo handler error: ${(err as Error).message}`);
    }
  }
  misses.push(`${method} ${path}${url.search}`);
  console.info(`[demo] no handler for ${method} ${path}${url.search}`);
  return method === "GET" || method === "HEAD" ? reply.problem(404, "DEMO-0404", UNSUPPORTED_READ) : reply.problem(501, "DEMO-0501", UNSUPPORTED_WRITE);
}

function toResponse(result: DemoResponse | DemoEvents): Response {
  const out = result instanceof DemoEvents ? reply.problem(406, "DEMO-0406", "This route streams events; open it with EventSource.") : result;
  const noBody = out.status === 204 || out.body === undefined;
  const raw = out.body instanceof RawBody;
  return new Response(noBody ? null : raw ? (out.body as RawBody).text : JSON.stringify(out.body), {
    status: out.status,
    headers: { ...(noBody || raw ? {} : { "Content-Type": "application/json" }), ...out.headers },
  });
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/**
 * Stands in for EventSource on /api/ URLs: runs the route's handler and emits the events it returns, again every
 * everyMs while open. Other URLs get the browser's EventSource.
 */
function fakeEventSource(Real: typeof EventSource): typeof EventSource {
  class FakeEventSource extends EventTarget {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSED = 2;
    readonly CONNECTING = 0;
    readonly OPEN = 1;
    readonly CLOSED = 2;
    readonly url: string;
    readonly withCredentials = false;
    readyState = 0;
    onopen: ((ev: Event) => unknown) | null = null;
    onmessage: ((ev: MessageEvent) => unknown) | null = null;
    onerror: ((ev: Event) => unknown) | null = null;
    private timer: ReturnType<typeof setTimeout> | undefined;

    constructor(url: string | URL) {
      super();
      this.url = new URL(String(url), window.location.href).href;
      void this.pump(true);
    }

    private fire(ev: Event, handler: ((ev: never) => unknown) | null) {
      this.dispatchEvent(ev);
      (handler as ((e: Event) => unknown) | null)?.call(this, ev);
    }

    private async pump(first: boolean) {
      if (this.readyState === 2) return;
      const out = await dispatch("GET", new URL(this.url), new Headers({ Accept: "text/event-stream" }), undefined);
      if (this.readyState === 2) return;
      if (!(out instanceof DemoEvents)) {
        this.readyState = 2;
        this.fire(new Event("error"), this.onerror);
        return;
      }
      if (first) {
        this.readyState = 1;
        this.fire(new Event("open"), this.onopen);
      }
      for (const e of out.events) {
        const data = typeof e.data === "string" ? e.data : JSON.stringify(e.data);
        const msg = new MessageEvent(e.event ?? "message", { data, lastEventId: e.id ?? "" });
        this.fire(msg, e.event === undefined || e.event === "message" ? this.onmessage : null);
      }
      if (out.everyMs) this.timer = setTimeout(() => void this.pump(false), out.everyMs);
    }

    close() {
      this.readyState = 2;
      clearTimeout(this.timer);
    }
  }
  return new Proxy(Real, {
    construct(target, args: [string | URL, EventSourceInit?]) {
      const url = new URL(String(args[0]), window.location.href);
      return url.origin === window.location.origin && url.pathname.startsWith("/api/") ? new FakeEventSource(args[0]) : new target(...args);
    },
  });
}

/** Replaces window.fetch (and EventSource) for /api/ requests. Call once, before the app renders. */
export function installDemoBackend(): void {
  const realFetch = globalThis.fetch.bind(globalThis);
  (globalThis as { __passageDemoMisses?: string[] }).__passageDemoMisses = misses;
  if (typeof globalThis.EventSource === "function") globalThis.EventSource = fakeEventSource(globalThis.EventSource);
  globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const href = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const url = new URL(href, window.location.href);
    if (url.origin !== window.location.origin || !url.pathname.startsWith("/api/")) return realFetch(input, init);
    const req = input instanceof Request ? input : undefined;
    const method = (init?.method ?? req?.method ?? "GET").toUpperCase();
    const headers = new Headers(init?.headers ?? req?.headers);
    let rawBody: string | undefined;
    if (typeof init?.body === "string") rawBody = init.body;
    else if (init?.body == null && req && method !== "GET" && method !== "HEAD") rawBody = await req.clone().text();
    await sleep(80 + Math.random() * 120);
    if (init?.signal?.aborted) throw new DOMException("The operation was aborted.", "AbortError");
    return toResponse(await dispatch(method, url, headers, rawBody));
  };
}
