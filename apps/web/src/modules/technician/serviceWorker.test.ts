import { describe, expect, it } from "vitest";
import swSource from "../../../../technician/public/sw.js?raw";

// The technician worker (apps/technician/public/sw.js) run against fakes: it caches the shell at install, serves it
// when a navigation finds no network, and never touches /api/ calls or writes.
const ORIGIN = "https://tech.passage.dev";
type Listener = (event: Record<string, unknown>) => void;
interface Req {
  url: string;
  method: string;
  mode: string;
}

function boot(pages: Record<string, string>) {
  const state = { online: true };
  const entries = new Map<string, Response>();
  const path = (k: string | Req) => new URL(typeof k === "string" ? k : k.url, ORIGIN).pathname;
  const net = async (k: string | Req) => {
    if (!state.online) throw new TypeError("Failed to fetch");
    const body = pages[path(k)];
    return body === undefined ? new Response("missing", { status: 404 }) : new Response(body, { status: 200 });
  };
  const cache = {
    put: async (k: string | Req, r: Response) => void entries.set(path(k), r),
    match: async (k: string | Req) => entries.get(path(k))?.clone(),
    addAll: async (urls: string[]) => {
      for (const u of urls) entries.set(path(u), await net(u));
    },
  };
  const caches = { open: async () => cache, keys: async () => [], delete: async () => true, match: (k: string | Req) => cache.match(k) };
  const listeners: Record<string, Listener> = {};
  const self = { location: { origin: ORIGIN }, addEventListener: (t: string, fn: Listener) => (listeners[t] = fn), skipWaiting: async () => undefined, clients: { claim: async () => undefined } };
  new Function("self", "caches", "fetch", swSource)(self, caches, net);
  async function install() {
    const pending: Promise<unknown>[] = [];
    listeners.install!({ waitUntil: (p: Promise<unknown>) => pending.push(p) });
    await Promise.all(pending);
  }
  async function request(p: string, over: Partial<Req> = {}): Promise<Response | "passthrough"> {
    let answer: Promise<Response> | undefined;
    const pending: Promise<unknown>[] = [];
    const request: Req = { url: new URL(p, ORIGIN).href, method: "GET", mode: "same-origin", ...over };
    listeners.fetch!({ request, respondWith: (a: Promise<Response>) => (answer = a), waitUntil: (a: Promise<unknown>) => pending.push(a) });
    if (!answer) return "passthrough";
    const res = await answer;
    await Promise.all(pending);
    return res;
  }
  return { state, entries, install, request };
}

const site = { "/index.html": `<!doctype html><script type="module" src="/assets/index-A1.js"></script>`, "/manifest.webmanifest": "{}", "/assets/index-A1.js": "1" };

describe("technician sw.js", () => {
  it("caches the shell and its build assets at install, then opens the app offline", async () => {
    const w = boot(site);
    await w.install();
    expect([...w.entries.keys()].sort()).toEqual(["/assets/index-A1.js", "/index.html", "/manifest.webmanifest"]);
    w.state.online = false;
    const res = await w.request("/", { mode: "navigate" });
    expect(res).not.toBe("passthrough");
    expect(await (res as Response).text()).toContain("/assets/index-A1.js");
  });

  it("leaves API calls, uploads and other origins to the browser", async () => {
    const w = boot(site);
    await w.install();
    expect(await w.request("/api/v1/service/technicians/me/day")).toBe("passthrough");
    expect(await w.request("/api/v1/service/work-orders/1/actions", { method: "POST" })).toBe("passthrough");
    expect(await w.request("/api/v1/service/work-orders/1/captures/2", { method: "PUT" })).toBe("passthrough");
    expect(await w.request("https://cdn.example.com/assets/x.js")).toBe("passthrough");
  });
});
