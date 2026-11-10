// @vitest-environment node
import { readdirSync, readFileSync } from "node:fs";
import { loadModules } from "../../modules/registry";
import { helpForRoute, routeHelp } from "./routeHelp";

const FLOW_MODULES = ["purchasing", "receiving", "ap-invoices", "payables", "sales", "fulfillment", "billing", "receivables"];

const guideDir = new URL("../../../../../docs/user/", import.meta.url);
const guides: Record<string, string> = Object.fromEntries(
  readdirSync(guideDir)
    .filter((name) => name.endsWith(".md"))
    .map((name) => [`/docs/user/${name}`, readFileSync(new URL(name, guideDir), "utf8")]),
);

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

const SPEC_REF = /\b(?:FR|BR|AC)-\d+\.\d+/;
const DELIVERY_REF = /\b(?:wave|slice)[ -]?\d+/i;
const PROBLEM_CODE = /\b[A-Z]{2,5}(?:-[A-Z]{2,5})*-\d{3,4}\b/;
const INTERNAL_WORDS = /\buuid\b/i;

describe("route help map", () => {
  const flowRoutes = loadModules()
    .filter((module) => FLOW_MODULES.includes(module.id))
    .flatMap((module) => module.routes.map((route) => ({ id: module.id, path: route.path ?? "" })));

  it("covers the procure-to-pay and order-to-cash modules", () => {
    expect(new Set(flowRoutes.map((route) => route.id))).toEqual(new Set(FLOW_MODULES));
    expect(flowRoutes.length).toBeGreaterThan(100);
  });

  it("has a help entry for every procure-to-pay and order-to-cash route", () => {
    const missing = flowRoutes.filter((route) => !helpForRoute(route.path)).map((route) => route.path);
    expect(missing).toEqual([]);
  });

  it("prefers the most specific pattern", () => {
    expect(helpForRoute("/purchasing/purchase-orders/new")?.title).toBe("New purchase order");
    expect(helpForRoute("/purchasing/purchase-orders/abc")?.title).toBe("Purchase orders");
    expect(helpForRoute("/purchasing/purchase-orders/abc/reprice")?.title).toBe("Purchase order line details");
    expect(helpForRoute("/receivables/payments/42?tab=1")?.title).toBe("Payment");
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

  it("uses plain language: no spec ids, delivery references or problem codes", () => {
    const offenders = routeHelp.flatMap((entry) =>
      [entry.title, entry.purpose, ...entry.howTo]
        .filter((text) => SPEC_REF.test(text) || DELIVERY_REF.test(text) || PROBLEM_CODE.test(text) || INTERNAL_WORDS.test(text))
        .map((text) => `${entry.title}: ${text}`),
    );
    expect(offenders).toEqual([]);
  });

  it("keeps guides free of spec ids, delivery references and bare problem codes", () => {
    const offenders = Object.entries(guides).flatMap(([path, source]) =>
      source
        .split("\n")
        .filter((line) => SPEC_REF.test(line) || DELIVERY_REF.test(line) || PROBLEM_CODE.test(line) || INTERNAL_WORDS.test(line))
        .map((line) => `${path}: ${line}`),
    );
    expect(Object.keys(guides).length).toBeGreaterThanOrEqual(6);
    expect(offenders).toEqual([]);
  });
});
