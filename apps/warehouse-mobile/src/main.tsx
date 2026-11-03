import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./app.css";

/** Device settings provisioned on the handheld. Sign-in (Keycloak device flow) is a handoff. */
function setting(key: string, fallback: string): string {
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

const root = document.getElementById("root");
if (!root) throw new Error("missing #root");

// App-shell cache for offline start (FR-32.5, public/sw.js). Production builds only: in the
// dev server the worker would cache unbundled source modules and serve stale code. A failed
// registration leaves the app online-only.
if (import.meta.env.PROD && "serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => undefined);
  });
}
createRoot(root).render(
  <StrictMode>
    <App deviceId={setting("passage.deviceId", "dev-unregistered")} userId={setting("passage.userId", "unassigned")} />
  </StrictMode>,
);
