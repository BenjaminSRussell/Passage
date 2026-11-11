import type { RouteObject } from "react-router-dom";
import type { PassageModule } from "../../modules/registry";
import { holdsDuty, requiredDuty } from "../dutyGate";

/** One "New …" action: a create page the caller may open. */
export interface NewAction {
  id: string;
  /** "New purchase order": the title the page itself shows. */
  label: string;
  to: string;
  /** The module that owns the page, so the palette can say where it lives. */
  moduleTitle: string;
}

const STATIC_NEW_PAGE = /^\/[^:*]+\/new$/;

/** The static paths of a module's create pages: ".../new" with no parameter in front of it. */
function newPaths(routes: RouteObject[]): { path: string; title: string | undefined }[] {
  return routes.flatMap((route) => {
    const own = route.path !== undefined && STATIC_NEW_PAGE.test(route.path) ? [{ path: route.path, title: (route.handle as { title?: string } | undefined)?.title }] : [];
    return [...own, ...newPaths(route.children ?? [])];
  });
}

const titled = (title: string | undefined): string | undefined => (title !== undefined && /^New\s/i.test(title.trim()) ? title.trim() : undefined);

/**
 * The create pages of the visible modules that the caller holds the duty for, as "New …" actions. A page whose title
 * does not start with "New" is left out: the palette would have no honest name for it.
 */
export function newActions(modules: PassageModule[], allModules: PassageModule[], duties: readonly string[]): NewAction[] {
  const seen = new Set<string>();
  return modules
    .flatMap((module) => newPaths(module.routes).map((found) => ({ ...found, moduleTitle: module.title })))
    .filter((found) => {
      const label = titled(found.title);
      if (!label || seen.has(found.path) || !holdsDuty(duties, requiredDuty(found.path, allModules))) {
        return false;
      }
      seen.add(found.path);
      return true;
    })
    .map((found) => ({ id: `new:${found.path}`, label: titled(found.title) ?? "", to: found.path, moduleTitle: found.moduleTitle }))
    .sort((a, b) => a.label.localeCompare(b.label));
}
