import type { RouteObject } from "react-router-dom";

/** What every module exports from `modules/<name>/module.ts`. */
export interface NavItem {
  label: string;
  to: string;
  /** Duty the user must hold to see the entry; omitted = visible to all. */
  duty?: string;
}

export interface PassageModule {
  /** Stable id, equal to the folder name. */
  id: string;
  title: string;
  routes: RouteObject[];
  nav: NavItem[];
}

/** Auto-discovers every module. Modules never register themselves anywhere else. */
export function loadModules(): PassageModule[] {
  const found = import.meta.glob<{ default: PassageModule }>("./*/module.ts", { eager: true });
  return Object.values(found)
    .map((m) => m.default)
    .sort((a, b) => a.title.localeCompare(b.title));
}
