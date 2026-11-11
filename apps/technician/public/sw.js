// App-shell service worker for the technician PWA (FR-45.8: open the day without a network once loaded).
// The same policy as apps/warehouse-mobile/public/sw.js (apps/web/src/modules/mobile-flows/domain/shellCache.ts):
// the shell, its build assets and the manifest are cached at install; navigations go network first with the
// cached shell as the offline fallback. Every /api/ call and every write goes to the network: the day, the photos
// and the signature are kept by the app in IndexedDB, sealed under the device key, never here.
// apps/web runs this file in modules/technician/serviceWorker.test.ts.

const SHELL_CACHE_VERSION = "v1";
const SHELL_CACHE_PREFIX = "passage-technician-";
const SHELL_DOCUMENT = "/index.html";
const SHELL_PRECACHE = ["/manifest.webmanifest"];
const HASHED_ASSET_PREFIX = "/assets/";
const NETWORK_ONLY_PREFIXES = ["/api/"];
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
