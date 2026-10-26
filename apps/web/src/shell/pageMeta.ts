import { matchPath } from "react-router-dom";
import type { NavItem, PassageModule } from "../modules/registry";
import { copy } from "./copy";

export interface PageMeta {
  title: string;
  workspace?: { title: string; to: string };
}

function isExact(to: string, pathname: string): boolean {
  return matchPath({ path: to, end: true }, pathname) !== null;
}

function workspaceOf(module: PassageModule): { title: string; to: string } {
  return { title: module.title, to: module.nav[0]?.to ?? "/" };
}

export function resolvePage(pathname: string, modules: PassageModule[], shellNav: NavItem[]): PageMeta {
  const shellEntry = shellNav.find((n) => isExact(n.to, pathname));
  if (shellEntry) {
    return { title: shellEntry.label };
  }
  for (const module of modules) {
    const entry = module.nav.find((n) => isExact(n.to, pathname));
    if (entry) {
      return { title: entry.label, workspace: workspaceOf(module) };
    }
  }
  for (const module of modules) {
    const route = module.routes.find((r) => r.path !== undefined && isExact(r.path, pathname));
    if (route) {
      const title = (route.handle as { title?: string } | undefined)?.title ?? module.title;
      return { title, workspace: workspaceOf(module) };
    }
  }
  return { title: copy.notFound.pageTitle };
}
