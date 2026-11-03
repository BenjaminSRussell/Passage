import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import vendorGroups from "./module";

describe("vendor groups module", () => {
  it("uses its folder name as id and declares routes and nav under /vendor-groups", () => {
    expect(vendorGroups.id).toBe("vendorgroups");
    expect(vendorGroups.routes.length).toBeGreaterThan(0);
    expect(vendorGroups.nav.every((n) => n.to.startsWith("/vendor-groups"))).toBe(true);
    expect(vendorGroups.routes.every((r) => Boolean(r.handle))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("vendorgroups");
  });
});
