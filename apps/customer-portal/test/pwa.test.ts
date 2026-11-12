import { describe, expect, it } from "vitest";
import indexHtml from "../index.html?raw";
import manifestRaw from "../public/manifest.webmanifest?raw";
import offlineHtml from "../public/offline.html?raw";
import workerSource from "../public/sw.js?raw";

// A binary file read as text starts with U+FFFD for the PNG signature's first byte; that and the name are enough to know it is there.
const icons = import.meta.glob<string>("../public/*.png", { query: "?raw", import: "default", eager: true });

describe("the web app manifest", () => {
  const manifest = JSON.parse(manifestRaw) as Record<string, unknown> & { icons: Array<{ src: string; sizes: string; type: string; purpose?: string }> };

  it("makes the portal installable: a name, a start page in scope, standalone display and 192 and 512 pixel icons", () => {
    expect(manifest["name"]).toBe("Passage Customer Portal");
    expect(manifest["short_name"]).toBeTruthy();
    expect(manifest["start_url"]).toBe("/");
    expect(manifest["scope"]).toBe("/");
    expect(manifest["display"]).toBe("standalone");
    expect(manifest["lang"]).toBe("en");
    const sizes = manifest.icons.map((i) => i.sizes);
    expect(sizes).toContain("192x192");
    expect(sizes).toContain("512x512");
    expect(manifest.icons.some((i) => i.purpose === "maskable")).toBe(true);
    for (const icon of manifest.icons) {
      const file = icons[`../public${icon.src}`];
      expect(file, icon.src).toBeDefined();
      expect(file?.slice(1, 4)).toBe("PNG");
      expect(icon.type).toBe("image/png");
    }
  });

  it("is linked from the page, with a theme colour", () => {
    const html = indexHtml;
    expect(html).toContain('<link rel="manifest" href="/manifest.webmanifest"');
    expect(html).toContain('name="theme-color"');
  });

  it("has an offline page that names no account data", () => {
    const html = offlineHtml;
    expect(html).toContain("You are offline");
    expect(html).not.toMatch(/\/api\/|fetch\(/);
  });
});

// The worker runs in a sandbox with fakes for the worker globals, so its caching rules are tested as they execute.
function worker() {
  const listeners: Record<string, (e: unknown) => void> = {};
  const stored = new Map<string, Map<string, unknown>>();
  const puts: string[] = [];
  const fetched: string[] = [];
  const cacheFor = (name: string) => {
    if (!stored.has(name)) stored.set(name, new Map());
    return stored.get(name) as Map<string, unknown>;
  };
  const caches = {
    open: async (name: string) => ({
      addAll: async (urls: string[]) => { for (const u of urls) cacheFor(name).set(new URL(u, "https://portal.test").href, `precached ${u}`); },
      put: async (req: { url: string }, res: unknown) => { puts.push(req.url); cacheFor(name).set(req.url, res); },
    }),
    match: async (req: string | { url: string }) => {
      const url = typeof req === "string" ? new URL(req, "https://portal.test").href : req.url;
      for (const c of stored.values()) if (c.has(url)) return c.get(url);
      return undefined;
    },
    keys: async () => [...stored.keys()],
    delete: async (name: string) => stored.delete(name),
  };
  let online = true;
  const self = {
    location: { origin: "https://portal.test" },
    addEventListener: (type: string, fn: (e: unknown) => void) => { listeners[type] = fn; },
    skipWaiting: async () => undefined,
    clients: { claim: async () => undefined },
  };
  const fakeFetch = async (req: { url: string }) => {
    fetched.push(req.url);
    if (!online) throw new Error("offline");
    return { ok: true, url: req.url, clone() { return this; } };
  };
  new Function("self", "caches", "fetch", "URL", workerSource)(self, caches, fakeFetch, URL);
  const request = (path: string, opts: { method?: string; mode?: string; origin?: string } = {}) => ({ url: `${opts.origin ?? "https://portal.test"}${path}`, method: opts.method ?? "GET", mode: opts.mode ?? "no-cors" });
  const fire = async (type: string, extra: Record<string, unknown> = {}) => {
    let promise: Promise<unknown> | undefined;
    let responded = false;
    listeners[type]?.({ ...extra, waitUntil: (p: Promise<unknown>) => { promise = p; }, respondWith: (p: Promise<unknown>) => { responded = true; promise = p; } });
    return { responded, result: await promise };
  };
  return { stored, puts, fetched, setOnline: (v: boolean) => { online = v; }, request, fire };
}

describe("the service worker", () => {
  it("precaches the offline page, the manifest and the icons, and drops the caches of an older version", async () => {
    const w = worker();
    w.stored.set("customer-portal-static-v0", new Map());
    w.stored.set("another-app-cache", new Map());
    await w.fire("install");
    expect([...(w.stored.get("customer-portal-static-v1")?.keys() ?? [])].map((u) => new URL(u).pathname).sort()).toEqual(["/icon-192.png", "/icon-512.png", "/manifest.webmanifest", "/offline.html"]);
    await w.fire("activate");
    expect([...w.stored.keys()].sort()).toEqual(["another-app-cache", "customer-portal-static-v1"]);
  });

  it("never touches the API, the session, a write or another origin", async () => {
    const w = worker();
    await w.fire("install");
    for (const req of [
      w.request("/api/v1/orders"), w.request("/api/v1/invoices/abc"), w.request("/bff/me"), w.request("/bff/login", { mode: "navigate" }),
      w.request("/api/v1/orders", { method: "POST" }), w.request("/assets/app.js", { origin: "https://cdn.example.invalid" }),
    ]) {
      expect((await w.fire("fetch", { request: req })).responded, `${req.method} ${req.url}`).toBe(false);
    }
    expect(w.puts).toEqual([]);
  });

  it("keeps hashed static files after the first fetch and serves them from the cache", async () => {
    const w = worker();
    await w.fire("install");
    const req = w.request("/assets/index-abc123.js");
    expect((await w.fire("fetch", { request: req })).responded).toBe(true);
    await new Promise((r) => setTimeout(r, 0));
    expect(w.puts).toEqual([req.url]);
    const before = w.fetched.length;
    await w.fire("fetch", { request: req });
    expect(w.fetched.length).toBe(before);
  });

  it("answers a page from the network, and the offline page without a connection, and keeps no page", async () => {
    const w = worker();
    await w.fire("install");
    const nav = w.request("/orders", { mode: "navigate" });
    expect(((await w.fire("fetch", { request: nav })).result as { url: string }).url).toBe(nav.url);
    w.setOnline(false);
    expect((await w.fire("fetch", { request: nav })).result).toBe("precached /offline.html");
    expect(w.puts).toEqual([]);
  });

  it("does not take over other requests such as a page's own scripts outside /assets", async () => {
    const w = worker();
    await w.fire("install");
    expect((await w.fire("fetch", { request: w.request("/something-else.js") })).responded).toBe(false);
  });
});
