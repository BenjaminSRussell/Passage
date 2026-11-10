import { loadModules } from "../../modules/registry";
import { DELIVERY_REF, SPEC_REF } from "../plainLanguageRules";
import { helpForRoute, resolveHelp, routeHelp } from "./routeHelp";

const FLOW_MODULES = ["purchasing", "receiving", "ap-invoices", "payables", "sales", "fulfillment", "billing", "receivables"];

const CATCH_ALL_ALLOWED: readonly string[] = [];
const MODULE_CATCH_ALL = /^\/[^/]+\/\*$/;

const guides = import.meta.glob<string>("../../../../../docs/user/*.md", { query: "?raw", import: "default", eager: true });

const slug = (heading: string) =>
  heading
    .toLowerCase()
    .replace(/[^\p{L}\p{N} -]/gu, "")
    .trim()
    .replace(/ /g, "-");

function headingsOf(file: string): string[] {
  const source = Object.entries(guides).find(([path]) => path.endsWith(`/docs/user/${file}`))?.[1];
  if (source === undefined) return [];
  return source
    .split("\n")
    .filter((line) => /^#{1,6} /.test(line))
    .map((line) => slug(line.replace(/^#+ /, "")));
}

const PROBLEM_CODE = /\b[A-Z]{2,5}(?:-[A-Z]{2,5})*-\d{3,4}\b/;
const INTERNAL_WORDS = /\buuid\b/i;
const plainFault = (text: string) => SPEC_REF.test(text) || DELIVERY_REF.test(text) || PROBLEM_CODE.test(text) || INTERNAL_WORDS.test(text);

describe("route help map", () => {
  const modules = loadModules().filter((module) => FLOW_MODULES.includes(module.id));
  const flowRoutes = modules.flatMap((module) => module.routes.map((route) => ({ id: module.id, path: route.path ?? "" })));

  it("finds routes in every procure-to-pay and order-to-cash module", () => {
    expect(modules.map((module) => module.id).sort()).toEqual([...FLOW_MODULES].sort());
    for (const module of modules) expect(module.routes.length, module.id).toBeGreaterThan(0);
  });

  it("has a specific help entry for every route, not a module-wide catch-all", () => {
    const unresolved = flowRoutes.filter((route) => !helpForRoute(route.path)).map((route) => route.path);
    expect(unresolved).toEqual([]);
    const caughtAll = flowRoutes
      .filter((route) => MODULE_CATCH_ALL.test(resolveHelp(route.path)?.pattern ?? ""))
      .map((route) => route.path)
      .filter((path) => !CATCH_ALL_ALLOWED.includes(path));
    expect(caughtAll).toEqual([]);
  });

  it("prefers the most specific pattern", () => {
    expect(helpForRoute("/purchasing/purchase-orders/new")?.title).toBe("New purchase order");
    expect(helpForRoute("/purchasing/purchase-orders/abc")?.title).toBe("Purchase orders");
    expect(helpForRoute("/purchasing/purchase-orders/abc/reprice")?.title).toBe("Purchase order line details");
    expect(helpForRoute("/receivables/payments/42?tab=1")?.title).toBe("Payment");
    expect(helpForRoute("/purchasing/unknown-page")?.title).toBe("Purchasing set-up and reports");
    expect(helpForRoute("/admin")).toBeUndefined();
  });

  it("keeps entries short", () => {
    for (const entry of routeHelp) {
      expect(entry.howTo.length, entry.title).toBeGreaterThan(0);
      expect(entry.howTo.length, entry.title).toBeLessThanOrEqual(5);
      expect(entry.patterns.length, entry.title).toBeGreaterThan(0);
    }
  });

  it("links every entry to an existing guide and heading", () => {
    const broken = routeHelp.flatMap((entry) => {
      const [file = "", anchor = ""] = entry.guide.split("#");
      const headings = headingsOf(file);
      if (headings.length === 0) return [`${entry.title}: no guide file ${file}`];
      return headings.includes(anchor) ? [] : [`${entry.title}: no heading #${anchor} in ${file}`];
    });
    expect(broken).toEqual([]);
  });

  it("uses plain language in help text", () => {
    const offenders = routeHelp.flatMap((entry) => [entry.title, entry.purpose, ...entry.howTo].filter(plainFault).map((text) => `${entry.title}: ${text}`));
    expect(offenders).toEqual([]);
  });

  it("keeps guides free of spec ids, delivery references and bare problem codes", () => {
    const offenders = Object.entries(guides).flatMap(([path, source]) =>
      source
        .split("\n")
        .filter(plainFault)
        .map((line) => `${path}: ${line}`),
    );
    expect(Object.keys(guides).length).toBeGreaterThanOrEqual(6);
    expect(offenders).toEqual([]);
  });
});
