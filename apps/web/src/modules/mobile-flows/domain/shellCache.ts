// App-shell caching policy for the device PWA (FR-32.5: the app must start without a network
// once it has been loaded). Pure. apps/warehouse-mobile/public/sw.js applies the same rules by
// hand, because a static worker cannot import this module; shellCache.test.ts runs that worker
// against these rules so the two cannot drift. The offline queue lives in IndexedDB and is
// never served from a cache, and requests that write never touch the cache.

/** Bump to drop every cached shell on the next worker activation (rollback path, §6.32.10). */
export const SHELL_CACHE_VERSION = "v1";
export const SHELL_CACHE_PREFIX = "passage-mobile-";

/** The shell document. Every page navigation of the single-page app is answered with it offline. */
export const SHELL_DOCUMENT = "/index.html";

/** Files cached at install besides the shell document and the build assets it references. */
export const SHELL_PRECACHE: readonly string[] = ["/manifest.webmanifest"];

/** Vite writes content-hashed build files here, so a cached copy never goes stale. */
export const HASHED_ASSET_PREFIX = "/assets/";

/** Path prefixes that always go to the network: live data and the offline sync endpoints. */
export const NETWORK_ONLY_PREFIXES: readonly string[] = ["/mobile/", "/api/", "/print/", "/rfid/", "/wms/"];

/**
 * - network-only: never cached.
 * - navigation: network first; offline, the cached shell document.
 * - asset: cache first (hashed build files), then the network, storing the copy.
 * - network-first: other same-origin files (manifest, icons): network, cached copy offline.
 */
export type ShellRoute = "network-only" | "navigation" | "asset" | "network-first";

export interface ShellRequest {
  method: string;
  /** Request URL path, e.g. "/assets/index-abc.js". */
  path: string;
  /** Origin of the request URL. */
  origin: string;
  /** Fetch mode: "navigate" for page loads, otherwise a resource request. */
  mode: "navigate" | "same-origin" | "cors" | "no-cors";
}

/**
 * Decides how the worker answers one request. Non-GET, cross-origin and live-data requests go to
 * the network untouched, so replay ordering and idempotency stay with the server (BR-32.3).
 */
export function shellRoute(req: ShellRequest, appOrigin: string): ShellRoute {
  if (req.method !== "GET") return "network-only";
  if (req.origin !== appOrigin) return "network-only";
  if (NETWORK_ONLY_PREFIXES.some((p) => req.path.startsWith(p))) return "network-only";
  if (req.mode === "navigate") return "navigation";
  if (req.path.startsWith(HASHED_ASSET_PREFIX)) return "asset";
  return "network-first";
}

/**
 * The build assets a shell document loads (`src` and `href` under /assets/), so the worker can
 * cache them at install. Without this the first visit's scripts were fetched before the worker
 * took control, and an offline cold start would find the document but not its code.
 */
export function shellAssetsFromHtml(html: string): string[] {
  const found = new Set<string>();
  for (const m of html.matchAll(/(?:src|href)\s*=\s*["']([^"'?#]+)/g)) {
    if (m[1].startsWith(HASHED_ASSET_PREFIX)) found.add(m[1]);
  }
  return [...found];
}

/** Name of the cache for a version, e.g. "passage-mobile-v1". */
export function shellCacheName(version: string = SHELL_CACHE_VERSION): string {
  return `${SHELL_CACHE_PREFIX}${version}`;
}

/** Cache names from earlier versions that the worker deletes on activate. Other apps' caches are kept. */
export function staleShellCaches(keys: readonly string[], version: string = SHELL_CACHE_VERSION): string[] {
  const current = shellCacheName(version);
  return keys.filter((k) => k.startsWith(SHELL_CACHE_PREFIX) && k !== current);
}
