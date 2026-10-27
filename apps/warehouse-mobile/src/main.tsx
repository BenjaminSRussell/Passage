import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";

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
createRoot(root).render(
  <StrictMode>
    <App deviceId={setting("passage.deviceId", "dev-unregistered")} userId={setting("passage.userId", "unassigned")} />
  </StrictMode>,
);
