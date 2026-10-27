import { describe, expect, it } from "vitest";
import swSource from "../../../../../warehouse-mobile/public/sw.js?raw";
import {
  HASHED_ASSET_PREFIX,
  NETWORK_ONLY_PREFIXES,
  SHELL_CACHE_PREFIX,
  SHELL_CACHE_VERSION,
  SHELL_DOCUMENT,
  SHELL_PRECACHE,
  shellAssetsFromHtml,
  shellCacheName,
  shellRoute,
  staleShellCaches,
  type ShellRequest,
} from "./shellCache";

const ORIGIN = "https://devices.passage.dev";

const req = (over: Partial<ShellRequest>): ShellRequest => ({
  method: "GET",
  path: "/",
  origin: ORIGIN,
  mode: "same-origin",
  ...over,
});

describe("shellRoute", () => {
  it("serves page navigations network first so a deploy is picked up", () => {
    expect(shellRoute(req({ mode: "navigate", path: "/" }), ORIGIN)).toBe("navigation");
  });

  it("serves hashed build assets cache first and other files network first", () => {
    expect(shellRoute(req({ path: "/assets/index-3f2a.js" }), ORIGIN)).toBe("asset");
    expect(shellRoute(req({ path: "/manifest.webmanifest" }), ORIGIN)).toBe("network-first");
  });

  it("never caches writes, even to a cached-looking path", () => {
    expect(shellRoute(req({ method: "POST", path: "/assets/x.js" }), ORIGIN)).toBe("network-only");
    expect(shellRoute(req({ method: "PUT", mode: "navigate" }), ORIGIN)).toBe("network-only");
  });

  it("sends live-data and offline-sync paths to the network", () => {
    for (const prefix of NETWORK_ONLY_PREFIXES) {
      expect(shellRoute(req({ path: `${prefix}work/1` }), ORIGIN), prefix).toBe("network-only");
    }
  });

  it("never caches cross-origin responses", () => {
    expect(shellRoute(req({ origin: "https://cdn.example.com", path: "/assets/lib.js" }), ORIGIN)).toBe("network-only");
  });
});

describe("shellAssetsFromHtml", () => {
  it("finds the built script and stylesheet and ignores everything else", () => {
    const html = `<!doctype html><link rel="manifest" href="/manifest.webmanifest" />
      <script type="module" crossorigin src="/assets/index-Bq1x.js"></script>
      <link rel="stylesheet" crossorigin href="/assets/index-C9z.css">
      <link rel="modulepreload" href='/assets/index-Bq1x.js'>
      <a href="https://example.com/assets/x.js">x</a><img src="/assets/logo.svg?v=2">`;
    expect(shellAssetsFromHtml(html)).toEqual(["/assets/index-Bq1x.js", "/assets/index-C9z.css", "/assets/logo.svg"]);
  });

  it("returns nothing for a dev-server page that loads source modules", () => {
    expect(shellAssetsFromHtml(`<script type="module" src="/src/main.tsx"></script>`)).toEqual([]);
  });
});

describe("shell caches", () => {
  it("names the cache by version", () => {
    expect(shellCacheName("v7")).toBe(`${SHELL_CACHE_PREFIX}v7`);
    expect(shellCacheName()).toBe(`${SHELL_CACHE_PREFIX}${SHELL_CACHE_VERSION}`);
  });

  it("deletes older shell caches and keeps the current one and unrelated caches", () => {
    const keys = [`${SHELL_CACHE_PREFIX}v0`, shellCacheName(), "other-app-cache", `${SHELL_CACHE_PREFIX}v2`];
    expect(staleShellCaches(keys, "v1")).toEqual([`${SHELL_CACHE_PREFIX}v0`, `${SHELL_CACHE_PREFIX}v2`]);
  });
});

// ---- The real worker (apps/warehouse-mobile/public/sw.js) run against fakes. ----

interface FakeRequest {
  url: string;
  method: string;
  mode: ShellRequest["mode"];
}

const keyOf = (k: string | FakeRequest) => new URL(typeof k === "string" ? k : k.url, ORIGIN).pathname;

class FakeCache {
  readonly entries = new Map<string, Response>();
  constructor(private readonly net: (url: string) => Promise<Response>) {}
  async put(k: string | FakeRequest, res: Response) {
    this.entries.set(keyOf(k), res);
  }
  async match(k: string | FakeRequest) {
    return this.entries.get(keyOf(k))?.clone();
  }
  async addAll(urls: string[]) {
    for (const u of urls) {
      const res = await this.net(u);
      if (!res.ok) throw new Error(`addAll ${u}: ${res.status}`);
      this.entries.set(keyOf(u), res);
    }
  }
}

type Listener = (event: Record<string, unknown>) => void;

/** Loads sw.js with fake self, caches and fetch. `online` and `pages` drive the fake network. */
function bootWorker(pages: Record<string, () => Response>) {
  const state = { online: true, fetched: [] as string[], skipWaiting: 0, claimed: 0 };
  const net = async (u: string | FakeRequest) => {
    const path = keyOf(u);
    state.fetched.push(path);
    if (!state.online) throw new TypeError("Failed to fetch");
    return pages[path]?.() ?? new Response("missing", { status: 404 });
  };
  const stores = new Map<string, FakeCache>();
  const caches = {
    async open(name: string) {
      if (!stores.has(name)) stores.set(name, new FakeCache(net));
      return stores.get(name) as FakeCache;
    },
    async keys() {
      return [...stores.keys()];
    },
    async delete(name: string) {
      return stores.delete(name);
    },
    async match(k: string | FakeRequest) {
      for (const c of stores.values()) {
        const hit = await c.match(k);
        if (hit) return hit;
      }
      return undefined;
    },
  };
  const listeners: Record<string, Listener> = {};
  const self = {
    location: { origin: ORIGIN },
    addEventListener: (type: string, fn: Listener) => (listeners[type] = fn),
    skipWaiting: async () => void state.skipWaiting++,
    clients: { claim: async () => void state.claimed++ },
  };
  new Function("self", "caches", "fetch", swSource)(self, caches, net);

  async function lifecycle(type: "install" | "activate") {
    const pending: Promise<unknown>[] = [];
    listeners[type]({ waitUntil: (p: Promise<unknown>) => pending.push(p) });
    await Promise.all(pending);
  }

  /** Dispatches a fetch; resolves to the worker's response, or "passthrough" when it did not answer. */
  async function request(path: string, over: Partial<FakeRequest> = {}): Promise<Response | "passthrough"> {
    let answer: Promise<Response> | undefined;
    const pending: Promise<unknown>[] = [];
    const request: FakeRequest = { url: new URL(path, ORIGIN).href, method: "GET", mode: "same-origin", ...over };
    listeners.fetch({ request, respondWith: (p: Promise<Response>) => (answer = p), waitUntil: (p: Promise<unknown>) => pending.push(p) });
    if (!answer) return "passthrough";
    const res = await answer;
    await Promise.all(pending);
    return res;
  }

  const shell = () => stores.get(shellCacheName()) as FakeCache;
  return { state, stores, caches, lifecycle, request, shell };
}

const SHELL_HTML = `<!doctype html><script type="module" src="/assets/index-A1.js"></script><link rel="stylesheet" href="/assets/index-B2.css">`;
const site = (html = SHELL_HTML) => ({
  [SHELL_DOCUMENT]: () => new Response(html, { status: 200, headers: { "Content-Type": "text/html" } }),
  "/manifest.webmanifest": () => new Response("{}", { status: 200 }),
  "/assets/index-A1.js": () => new Response("console.log(1)", { status: 200 }),
  "/assets/index-B2.css": () => new Response("body{}", { status: 200 }),
});

describe("sw.js mirrors shellCache.ts", () => {
  it("declares the same constants", () => {
    const constant = (name: string) => new Function(`${/const [\s\S]*?const CURRENT_CACHE[^\n]*\n/.exec(swSource)?.[0] ?? ""}; return ${name};`)();
    expect(constant("SHELL_CACHE_VERSION")).toBe(SHELL_CACHE_VERSION);
    expect(constant("SHELL_CACHE_PREFIX")).toBe(SHELL_CACHE_PREFIX);
    expect(constant("SHELL_DOCUMENT")).toBe(SHELL_DOCUMENT);
    expect(constant("SHELL_PRECACHE")).toEqual(SHELL_PRECACHE);
    expect(constant("HASHED_ASSET_PREFIX")).toBe(HASHED_ASSET_PREFIX);
    expect(constant("NETWORK_ONLY_PREFIXES")).toEqual(NETWORK_ONLY_PREFIXES);
  });

  it("leaves every request the policy calls network-only to the browser", async () => {
    const w = bootWorker(site());
    expect(await w.request("/api/v1/mobile/offline:replay", { method: "POST" })).toBe("passthrough");
    for (const prefix of NETWORK_ONLY_PREFIXES) expect(await w.request(`${prefix}work/1`), prefix).toBe("passthrough");
    expect(await w.request("https://cdn.example.com/assets/lib.js")).toBe("passthrough");
  });
});

describe("sw.js lifecycle and offline start", () => {
  it("caches the shell document, its build assets and the manifest at install", async () => {
    const w = bootWorker(site());
    await w.lifecycle("install");
    expect([...w.shell().entries.keys()].sort()).toEqual(["/assets/index-A1.js", "/assets/index-B2.css", SHELL_DOCUMENT, "/manifest.webmanifest"].sort());
    expect(w.state.skipWaiting).toBe(1);
  });

  it("fails the install when the shell document cannot be fetched, so an old worker keeps serving", async () => {
    const w = bootWorker({ ...site(), [SHELL_DOCUMENT]: () => new Response("down", { status: 503 }) });
    await expect(w.lifecycle("install")).rejects.toThrow("503");
    expect(w.state.skipWaiting).toBe(0);
  });

  it("drops older shell caches on activate and keeps other caches", async () => {
    const w = bootWorker(site());
    await w.caches.open(`${SHELL_CACHE_PREFIX}v0`);
    await w.caches.open("other-app");
    await w.lifecycle("install");
    await w.lifecycle("activate");
    expect(await w.caches.keys()).toEqual(["other-app", shellCacheName()]);
    expect(w.state.claimed).toBe(1);
  });

  it("starts offline: a navigation gets the cached shell and its assets come from the cache", async () => {
    const w = bootWorker(site());
    await w.lifecycle("install");
    w.state.online = false;
    const page = await w.request("/mobile/receive", { mode: "navigate" });
    // A navigation under a live-data prefix is still network-only; the app's own routes are not.
    expect(page).toBe("passthrough");
    const home = (await w.request("/", { mode: "navigate" })) as Response;
    expect(await home.text()).toBe(SHELL_HTML);
    const script = (await w.request("/assets/index-A1.js")) as Response;
    expect(await script.text()).toBe("console.log(1)");
  });

  it("refreshes the cached shell on an online navigation but never stores an error page", async () => {
    let html = "<p>v2</p>";
    let status = 200;
    const w = bootWorker({ ...site(), [SHELL_DOCUMENT]: () => new Response(html, { status }), "/": () => new Response(html, { status }) });
    await w.request("/", { mode: "navigate" });
    expect(await (await w.shell().match(SHELL_DOCUMENT))?.text()).toBe("<p>v2</p>");
    html = "server error";
    status = 500;
    await w.request("/", { mode: "navigate" });
    expect(await (await w.shell().match(SHELL_DOCUMENT))?.text()).toBe("<p>v2</p>");
  });

  it("serves a cached build asset without touching the network", async () => {
    const w = bootWorker(site());
    await w.lifecycle("install");
    w.state.fetched = [];
    await w.request("/assets/index-B2.css");
    expect(w.state.fetched).toEqual([]);
  });
});
