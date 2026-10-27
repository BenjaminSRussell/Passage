import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import pricing from "./module";

describe("pricing module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /pricing", () => {
    expect(pricing.id).toBe("pricing");
    expect(pricing.routes.every((r) => r.element != null && r.path?.startsWith("/pricing"))).toBe(true);
    expect(pricing.nav.every((n) => n.to.startsWith("/pricing"))).toBe(true);
  });

  it("has a list, a create form and a detail page for trade agreements, plus the workspace and simulation", () => {
    expect(pricing.routes.map((r) => r.path)).toEqual([
      "/pricing",
      "/pricing/agreements",
      "/pricing/agreements/new",
      "/pricing/agreements/:id",
      "/pricing/simulate",
    ]);
  });

  it("guards the navigation with the view duty", () => {
    expect(new Set(pricing.nav.map((n) => n.duty))).toEqual(new Set(["pricing.view"]));
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("pricing");
  });
});
