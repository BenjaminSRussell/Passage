import { matchPath, type RouteObject } from "react-router-dom";
import type { NavItem, PassageModule } from "../modules/registry";

declare const __PASSAGE_DEV_SHOW_ALL_NAV__: boolean | undefined;

const showAllNav = typeof __PASSAGE_DEV_SHOW_ALL_NAV__ === "boolean" && __PASSAGE_DEV_SHOW_ALL_NAV__;

// UX hardening only: hiding entries and guarding routes never replaces server-side authorization.
export function holdsDuty(duties: readonly string[], duty: string | undefined): boolean {
  return showAllNav || duty === undefined || duties.includes(duty);
}

export function visibleNav(items: NavItem[], duties: readonly string[]): NavItem[] {
  return items.filter((n) => holdsDuty(duties, n.duty));
}

export function visibleModules(modules: PassageModule[], duties: readonly string[]): PassageModule[] {
  return modules
    .map((m) => ({ ...m, nav: visibleNav(m.nav, duties) }))
    .filter((m) => m.nav.length > 0);
}

export function routeDuty(route: RouteObject): string | undefined {
  return (route.handle as { duty?: string } | undefined)?.duty;
}

function longestNavMatch(pathname: string, modules: PassageModule[]): NavItem | undefined {
  let best: NavItem | undefined;
  for (const module of modules) {
    for (const entry of module.nav) {
      const matches = matchPath({ path: entry.to, end: false }, pathname) !== null;
      if (matches && (best === undefined || entry.to.length > best.to.length)) {
        best = entry;
      }
    }
  }
  return best;
}

export function hasEffectiveDutyScope(pathname: string, modules: PassageModule[]): boolean {
  return longestNavMatch(pathname, modules) !== undefined;
}

export function requiredDuty(pathname: string, modules: PassageModule[]): string | undefined {
  for (const module of modules) {
    for (const route of module.routes) {
      const declared = routeDuty(route);
      if (declared !== undefined && route.path !== undefined && matchPath({ path: route.path, end: true }, pathname) !== null) {
        return declared;
      }
    }
  }
  return longestNavMatch(pathname, modules)?.duty;
}
