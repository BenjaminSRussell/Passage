// Service worker of the supplier portal: an installable shell and nothing else. It keeps the app's own static files (the hashed
// scripts and styles under /assets/, the icons, the manifest and an offline page) so the shell opens quickly and shows an offline
// page without a connection. It NEVER stores anything from /api/ or /bff/, nor any page: an order, an invoice or a session must not
// outlive the contact's sign-in on a shared device. Pages and data always come from the network.
const VERSION = "v1";
const STATIC_CACHE = `supplier-portal-static-${VERSION}`;
const PRECACHE = ["/offline.html", "/manifest.webmanifest", "/icon-192.png", "/icon-512.png"];

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(STATIC_CACHE).then((cache) => cache.addAll(PRECACHE)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((names) => Promise.all(names.filter((n) => n.startsWith("supplier-portal-") && n !== STATIC_CACHE).map((n) => caches.delete(n))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;
  // Private data and the session are the network's alone.
  if (url.pathname.startsWith("/api/") || url.pathname.startsWith("/bff/")) return;

  if (request.mode === "navigate") {
    event.respondWith(fetch(request).catch(() => caches.match("/offline.html")));
    return;
  }
  if (url.pathname.startsWith("/assets/") || PRECACHE.includes(url.pathname)) {
    event.respondWith(
      caches.match(request).then(
        (hit) =>
          hit ||
          fetch(request).then((response) => {
            if (response.ok) {
              const copy = response.clone();
              caches.open(STATIC_CACHE).then((cache) => cache.put(request, copy));
            }
            return response;
          }),
      ),
    );
  }
});
