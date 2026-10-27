import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import warehouse from "./module";

describe("warehouse module", () => {
  it("uses its folder name as id and keeps every route under /warehouse", () => {
    expect(warehouse.id).toBe("warehouse");
    expect(warehouse.routes.map((r) => r.path)).toEqual([
      "/warehouse",
      "/warehouse/locations",
      "/warehouse/locations/new",
      "/warehouse/directives",
      "/warehouse/directives/new",
      "/warehouse/work",
      "/warehouse/work/new",
      "/warehouse/work/:id",
    ]);
    expect(warehouse.routes.every((r) => r.element != null)).toBe(true);
  });

  it("puts each list in the navigation behind the duty its API needs", () => {
    expect(warehouse.nav).toEqual([
      { label: "Directive simulator", to: "/warehouse", duty: "warehouse.setup.view" },
      { label: "Locations", to: "/warehouse/locations", duty: "warehouse.setup.view" },
      { label: "Location directives", to: "/warehouse/directives", duty: "warehouse.setup.view" },
      { label: "Work", to: "/warehouse/work", duty: "warehouse.inquiry.view" },
    ]);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("warehouse");
  });
});
