import { readdirSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { ROUTE_CATALOG, type CatalogEntry } from "../src/domain/routeCatalog.js";

interface NavItem {
  to: string;
  duty: string | undefined;
}

interface RouteItem {
  path: string;
  duty: string | undefined;
  unparsedHandle: boolean;
}

interface Modules {
  count: number;
  nav: NavItem[];
  routes: RouteItem[];
}

type Entry = Pick<CatalogEntry, "routeKey" | "path" | "duty">;

const MAX_SHRINK = 0.8;
const snapshot = JSON.parse(readFileSync(new URL("./fixtures/web-modules-snapshot.json", import.meta.url), "utf8")) as { modules: number; nav: number; routes: number };
const MODULES_DIR = new URL("../../../apps/web/src/modules/", import.meta.url);
const NAV_ITEM = /\{\s*label:\s*"[^"]*",\s*to:\s*"([^"]+)"(?:,\s*duty:\s*"([^"]+)")?\s*\}/g;
const ROUTE_ITEM = /\{\s*path:\s*"([^"]+)"/g;
const CONST_STRING = /\bconst\s+(\w+)\s*=\s*"([^"]*)"/g;
const HANDLE_BODY = /\bhandle:\s*\{([^}]*)\}/;
const DUTY_VALUE = /\bduty:\s*(?:"([^"]+)"|([A-Za-z_]\w*))/;

function objectEnd(source: string, start: number): number {
  let depth = 0;
  let quote: string | undefined;
  for (let i = start; i < source.length; i += 1) {
    const c = source[i];
    if (quote !== undefined) {
      if (c === "\\") i += 1;
      else if (c === quote) quote = undefined;
    } else if (c === '"' || c === "'" || c === "`") quote = c;
    else if (c === "{") depth += 1;
    else if (c === "}" && --depth === 0) return i + 1;
  }
  return source.length;
}

function routesOf(source: string, constants: ReadonlyMap<string, string>): RouteItem[] {
  const routes: RouteItem[] = [];
  for (const m of source.matchAll(ROUTE_ITEM)) {
    const object = source.slice(m.index, objectEnd(source, m.index));
    const handle = HANDLE_BODY.exec(object);
    const written = handle === null ? null : DUTY_VALUE.exec(handle[1] ?? "");
    const duty = written?.[1] ?? (written?.[2] === undefined ? undefined : constants.get(written[2]));
    const unparsedHandle = (object.includes("handle:") && handle === null) || (written !== null && duty === undefined);
    routes.push({ path: m[1] ?? "", duty, unparsedHandle });
  }
  return routes;
}

function mergeConstants(sources: readonly string[]): Map<string, string> {
  const out = new Map<string, string>();
  const conflicting = new Set<string>();
  for (const source of sources) {
    for (const m of source.matchAll(CONST_STRING)) {
      const name = m[1] ?? "";
      const value = m[2] ?? "";
      if (out.has(name) && out.get(name) !== value) conflicting.add(name);
      out.set(name, value);
    }
  }
  for (const name of conflicting) out.delete(name);
  return out;
}

function constantsOf(dir: string): Map<string, string> {
  const sources: string[] = [];
  for (const f of readdirSync(new URL(`${dir}/`, MODULES_DIR))) {
    if (f.endsWith(".ts") && !f.endsWith(".test.ts")) sources.push(readFileSync(new URL(`${dir}/${f}`, MODULES_DIR), "utf8"));
  }
  return mergeConstants(sources);
}

function parseModules(): Modules {
  const out: Modules = { count: 0, nav: [], routes: [] };
  for (const dir of readdirSync(MODULES_DIR, { withFileTypes: true })) {
    if (!dir.isDirectory()) continue;
    let source: string;
    try {
      source = readFileSync(new URL(`${dir.name}/module.ts`, MODULES_DIR), "utf8");
    } catch {
      continue;
    }
    out.count += 1;
    for (const m of source.matchAll(NAV_ITEM)) out.nav.push({ to: m[1] ?? "", duty: m[2] });
    out.routes.push(...routesOf(source, constantsOf(dir.name)));
  }
  return out;
}

const underPath = (path: string, to: string): boolean => path === to || path.startsWith(`${to}/`);

function effectiveDuty(path: string, modules: Modules): string | undefined {
  const declared = modules.routes.find((r) => r.path === path && r.duty !== undefined)?.duty;
  if (declared !== undefined) return declared;
  let best: NavItem | undefined;
  for (const n of modules.nav) if (underPath(path, n.to) && (best === undefined || n.to.length > best.to.length)) best = n;
  return best?.duty;
}

function driftOf(catalog: readonly Entry[], modules: Modules): string[] {
  const drift: string[] = [];
  for (const e of catalog) {
    const backed = modules.nav.some((n) => n.to === e.path) || modules.routes.some((r) => r.path === e.path);
    if (!backed) {
      drift.push(`${e.routeKey}: path ${e.path} is neither a nav item nor a route in apps/web modules`);
      continue;
    }
    if (modules.routes.some((r) => r.path === e.path && r.unparsedHandle)) {
      drift.push(`${e.routeKey}: the handle of the route ${e.path} in apps/web modules could not be parsed, so its duty is unknown`);
      continue;
    }
    const duty = effectiveDuty(e.path, modules);
    if (duty !== e.duty) drift.push(`${e.routeKey}: catalog duty ${e.duty} differs from the apps/web duty ${duty ?? "(none)"} for ${e.path}`);
  }
  return drift;
}

describe("ROUTE_CATALOG against apps/web module.ts", () => {
  const modules = parseModules();

  it("parses the web modules, so an unmatched regex cannot pass the drift checks", () => {
    const replacement = `{ "modules": ${modules.count}, "nav": ${modules.nav.length}, "routes": ${modules.routes.length} }`;
    const refresh = `If apps/web modules legitimately shrank, replace the contents of test/fixtures/web-modules-snapshot.json with this line: ${replacement} ; otherwise a module.ts regex stopped matching.`;
    expect(modules.count, `web modules parsed ${modules.count}, snapshot ${snapshot.modules}. ${refresh}`).toBeGreaterThanOrEqual(snapshot.modules * MAX_SHRINK);
    expect(modules.nav.length, `nav items parsed ${modules.nav.length}, snapshot ${snapshot.nav}. ${refresh}`).toBeGreaterThanOrEqual(snapshot.nav * MAX_SHRINK);
    expect(modules.routes.length, `routes parsed ${modules.routes.length}, snapshot ${snapshot.routes}. ${refresh}`).toBeGreaterThanOrEqual(snapshot.routes * MAX_SHRINK);
  });

  it.each(ROUTE_CATALOG.map((e) => [e.routeKey, e] as const))("%s keeps the path and duty the shell enforces", (_key, entry) => {
    expect(driftOf([entry], modules)).toEqual([]);
  });

  it("reports a path no module backs and a duty that differs from the nav item", () => {
    const drift = driftOf(
      [
        { routeKey: "x.gone", path: "/receivables/no-such-page", duty: "receivables.view" },
        { routeKey: "x.duty", path: "/receivables/open-items", duty: "receivables.other" },
      ],
      modules,
    );
    expect(drift).toHaveLength(2);
    expect(drift[0]).toContain("x.gone");
    expect(drift[1]).toContain("x.duty");
  });

  it("does not let a nav item without a duty satisfy a catalog entry that demands one", () => {
    const open: Modules = { count: 1, nav: [{ to: "/a", duty: undefined }], routes: [] };
    expect(driftOf([{ routeKey: "a", path: "/a", duty: "a.view" }], open)).toHaveLength(1);
  });

  it("reads a multi-line route whose handle sits on its own line and whose duty is a constant", () => {
    const source = `{
      path: "/party",
      element: createElement(SearchPage),
      handle: { title: "Parties", duty: DUTY_READ },
    },`;
    expect(routesOf(source, new Map([["DUTY_READ", "party.read"]]))).toEqual([{ path: "/party", duty: "party.read", unparsedHandle: false }]);
  });

  it("flags a catalog path whose route handle names a duty that cannot be resolved", () => {
    const source = `{
      path: "/party",
      handle: { title: "Parties", duty: DUTY_READ },
    },`;
    const routes = routesOf(source, new Map());
    expect(routes).toEqual([{ path: "/party", duty: undefined, unparsedHandle: true }]);
    const drift = driftOf([{ routeKey: "party.search", path: "/party", duty: "party.read" }], { count: 1, nav: [], routes });
    expect(drift).toHaveLength(1);
    expect(drift[0]).toContain("could not be parsed");
  });

  it("leaves a constant unresolved when two files give it different values, so the route is flagged", () => {
    const constants = mergeConstants(['const DUTY_READ = "party.read";', 'const DUTY_READ = "party.other";', 'const SAME = "x"; const OTHER = "y";', 'const SAME = "x";']);
    expect(constants.has("DUTY_READ")).toBe(false);
    expect(constants.get("SAME")).toBe("x");
    expect(constants.get("OTHER")).toBe("y");
    const routes = routesOf(`{ path: "/party", handle: { duty: DUTY_READ } },`, constants);
    expect(routes).toEqual([{ path: "/party", duty: undefined, unparsedHandle: true }]);
  });
});
