import { devIdentity } from "../shell/principal";
import { installDemoBackend } from "./backend";
import { LEGAL_ENTITY } from "./company";
import "./handlers";

installDemoBackend();

// Open on the Production planning workspace (shell/workspaces/WorkspaceNav.tsx prefs) unless the viewer picked one.
try {
  const key = "passage.workspaces.v1";
  if (window.localStorage.getItem(key) === null) window.localStorage.setItem(key, JSON.stringify({ active: "production-planner" }));
} catch {
  // Storage blocked: the shell falls back to its own default workspace.
}

// Start the visitor in Cedar Ridge Pumps, Inc. (the top bar context, shell/lookup/workContext.tsx), as a user would after picking it
// once: the demo company has three legal entities, so nothing is chosen automatically and every form would open without one.
try {
  const key = `passage.workContext.${devIdentity()}`;
  if (devIdentity() !== "" && window.localStorage.getItem(key) === null) {
    window.localStorage.setItem(key, JSON.stringify({ legalEntityId: LEGAL_ENTITY.id, siteId: "" }));
  }
} catch {
  // Storage blocked: the visitor picks a legal entity in the top bar.
}
