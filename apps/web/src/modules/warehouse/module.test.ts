import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import warehouse from "./module";

describe("warehouse module", () => {
  it("uses its folder name as id and keeps its route and nav under /warehouse", () => {
    expect(warehouse.id).toBe("warehouse");
    expect(warehouse.routes.map((r) => r.path)).toEqual(["/warehouse"]);
    expect(warehouse.routes.every((r) => r.element != null)).toBe(true);
    expect(warehouse.nav).toEqual([{ label: "Directive simulator", to: "/warehouse", duty: "warehouse.setup.view" }]);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("warehouse");
  });
});
