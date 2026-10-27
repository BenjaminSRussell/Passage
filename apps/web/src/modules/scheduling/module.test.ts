import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import scheduling from "./module";

describe("scheduling module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /scheduling", () => {
    expect(scheduling.id).toBe("scheduling");
    expect(scheduling.routes.every((r) => r.element != null && r.path?.startsWith("/scheduling"))).toBe(true);
    expect(scheduling.nav.every((n) => n.to.startsWith("/scheduling"))).toBe(true);
  });

  it("has a list and a detail or create page for each entity", () => {
    expect(scheduling.routes.map((r) => r.path)).toEqual(
      expect.arrayContaining([
        "/scheduling",
        "/scheduling/load",
        "/scheduling/runs",
        "/scheduling/runs/new",
        "/scheduling/runs/:id",
        "/scheduling/resources",
        "/scheduling/resources/new",
        "/scheduling/resources/:id",
        "/scheduling/operations",
        "/scheduling/operations/new",
      ]),
    );
  });

  it("guards the navigation with the read duty", () => {
    expect(new Set(scheduling.nav.map((n) => n.duty))).toEqual(new Set(["scheduling.read"]));
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("scheduling");
  });
});
