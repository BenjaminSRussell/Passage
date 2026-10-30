import { matchPath } from "react-router-dom";
import type { NavItem, PassageModule } from "../modules/registry";

// UX hardening only: hiding entries and guarding routes never replaces server-side authorization.
export function holdsDuty(duties: readonly string[], duty: string | undefined): boolean {
  return duty === undefined || duties.includes(duty);
}

export function visibleNav(items: NavItem[], duties: readonly string[]): NavItem[] {
  return items.filter((n) => holdsDuty(duties, n.duty));
}

export function visibleModules(modules: PassageModule[], duties: readonly string[]): PassageModule[] {
  return modules
    .map((m) => ({ ...m, nav: visibleNav(m.nav, duties) }))
    .filter((m) => m.nav.length > 0);
}

export function requiredDuty(pathname: string, modules: PassageModule[]): string | undefined {
  let best: NavItem | undefined;
  for (const module of modules) {
    for (const entry of module.nav) {
      const matches = matchPath({ path: entry.to, end: false }, pathname) !== null;
      if (matches && (best === undefined || entry.to.length > best.to.length)) {
        best = entry;
      }
    }
  }
  return best?.duty;
}
