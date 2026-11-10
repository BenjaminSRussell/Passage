import type { PassageModule } from "../../modules/registry";

/** What the tenant switched on, by feature key. Undefined means the answer is not known, and everything is on. */
export type FeatureState = Readonly<Record<string, boolean>> | undefined;

// Where each feature shows up in the web app. A path prefix owns every page under it; the longest prefix wins, so
// "/integration/interchanges" (EDI) can sit inside a module that is otherwise always on. Modules and features are
// named in the feature catalog (platform/go/features); a feature with no entry here gates fields, not pages.
const PREFIX_FEATURES: readonly (readonly [string, string])[] = [
  ["/ledger", "finance"],
  ["/fx", "multi-currency"],
  ["/intercompany", "intercompany"],
  ["/sales", "sales"],
  ["/purchasing", "purchasing"],
  ["/inventory", "inventory"],
  ["/warehouse", "warehouse"],
  ["/production", "manufacturing"],
  ["/processmfg", "process-manufacturing"],
  ["/projects", "projects"],
  ["/payroll", "payroll"],
  ["/integration/partners", "edi"],
  ["/integration/interchanges", "edi"],
  ["/integration/partner-sla", "edi"],
  ["/integration/edi", "edi"],
];

export function featureForPath(path: string): string | undefined {
  let best: readonly [string, string] | undefined;
  for (const rule of PREFIX_FEATURES) {
    const covers = path === rule[0] || path.startsWith(`${rule[0]}/`);
    if (covers && (best === undefined || rule[0].length > best[0].length)) best = rule;
  }
  return best?.[1];
}

/** A feature that the tenant has not switched off is on, including one the catalog does not know. */
export function isFeatureOn(state: FeatureState, key: string): boolean {
  return state === undefined || state[key] !== false;
}

export function isPathEnabled(state: FeatureState, path: string): boolean {
  const key = featureForPath(path);
  return key === undefined || isFeatureOn(state, key);
}

/** Drops nav entries for switched-off features, and modules left with no entries. */
export function filterModulesByFeatures(modules: PassageModule[], state: FeatureState): PassageModule[] {
  if (state === undefined) return modules;
  return modules
    .map((m) => ({ ...m, nav: m.nav.filter((n) => isPathEnabled(state, n.to)) }))
    .filter((m) => m.nav.length > 0);
}

export function featureStateOf(features: readonly { key: string; enabled: boolean }[]): Record<string, boolean> {
  return Object.fromEntries(features.map((f) => [f.key, f.enabled]));
}
