import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./app.css";

const root = document.getElementById("root");
if (!root) throw new Error("missing #root");

// App-shell cache for opening the day offline (FR-45.8, public/sw.js). Production builds only: the dev server
// would cache unbundled source modules and serve stale code. A failed registration leaves the app online-only.
if (import.meta.env.PROD && "serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => undefined);
  });
}
createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
