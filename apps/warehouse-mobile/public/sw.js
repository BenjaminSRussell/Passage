// App-shell service worker for the device PWA (FR-32.5: start without a network once loaded).
// It applies the policy in apps/web/src/modules/mobile-flows/domain/shellCache.ts, which a
// static worker cannot import; apps/web runs this file against that module in
// shellCache.test.ts, so a change here that breaks the policy fails the web checks.
// Served from the site root so its scope covers the whole app. Writes and live data never
// enter the cache, and the offline queue stays in IndexedDB.

const SHELL_CACHE_VERSION = "v1";
const SHELL_CACHE_PREFIX = "passage-mobile-";
const SHELL_DOCUMENT = "/index.html";
const SHELL_PRECACHE = ["/manifest.webmanifest"];
const HASHED_ASSET_PREFIX = "/assets/";
const NETWORK_ONLY_PREFIXES = ["/mobile/", "/api/", "/print/", "/rfid/", "/wms/"];
const CURRENT_CACHE = SHELL_CACHE_PREFIX + SHELL_CACHE_VERSION;

function shellRoute(request, appOrigin) {
  const url = new URL(request.url);
  if (request.method !== "GET") return "network-only";
  if (url.origin !== appOrigin) return "network-only";
  if (NETWORK_ONLY_PREFIXES.some((p) => url.pathname.startsWith(p))) return "network-only";
  if (request.mode === "navigate") return "navigation";
  if (url.pathname.startsWith(HASHED_ASSET_PREFIX)) return "asset";
  return "network-first";
}

function shellAssetsFromHtml(html) {
  const found = new Set();
  for (const m of html.matchAll(/(?:src|href)\s*=\s*["']([^"'?#]+)/g)) {
    if (m[1].startsWith(HASHED_ASSET_PREFIX)) found.add(m[1]);
  }
  return [...found];
}

// Install caches the shell document, the build assets it references and the manifest, so the
// first offline start has everything the page loads. A failed fetch fails the install and the
// previous worker (if any) stays in charge.
async function precache() {
  const cache = await caches.open(CURRENT_CACHE);
  const res = await fetch(SHELL_DOCUMENT, { cache: "reload" });
  if (!res.ok) throw new Error("shell document returned " + res.status);
  const html = await res.clone().text();
  await cache.put(SHELL_DOCUMENT, res);
  await cache.addAll([...SHELL_PRECACHE, ...shellAssetsFromHtml(html)]);
}

self.addEventListener("install", (event) => {
  event.waitUntil(precache().then(() => self.skipWaiting()));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k.startsWith(SHELL_CACHE_PREFIX) && k !== CURRENT_CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

async function store(key, res) {
  if (!res.ok) return;
  const cache = await caches.open(CURRENT_CACHE);
  await cache.put(key, res);
}

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const route = shellRoute(request, self.location.origin);
  if (route === "network-only") return;
  if (route === "navigation") {
    event.respondWith(
      fetch(request)
        .then((res) => {
          event.waitUntil(store(SHELL_DOCUMENT, res.clone()));
          return res;
        })
        .catch(() => caches.match(SHELL_DOCUMENT).then((hit) => hit || Response.error())),
    );
    return;
  }
  if (route === "asset") {
    event.respondWith(
      caches.match(request).then(
        (hit) =>
          hit ||
          fetch(request).then((res) => {
            event.waitUntil(store(request, res.clone()));
            return res;
          }),
      ),
    );
    return;
  }
  event.respondWith(
    fetch(request)
      .then((res) => {
        event.waitUntil(store(request, res.clone()));
        return res;
      })
      .catch(() => caches.match(request).then((hit) => hit || Response.error())),
  );
});
