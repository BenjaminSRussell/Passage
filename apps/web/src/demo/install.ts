import { installDemoBackend } from "./backend";
import "./handlers";

installDemoBackend();

// Open on the Production planning workspace (shell/workspaces/WorkspaceNav.tsx prefs) unless the viewer picked one.
try {
  const key = "passage.workspaces.v1";
  if (window.localStorage.getItem(key) === null) window.localStorage.setItem(key, JSON.stringify({ active: "production-planner" }));
} catch {
  // Storage blocked: the shell falls back to its own default workspace.
}
