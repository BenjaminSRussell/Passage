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
        "/scheduling/late-orders",
        "/scheduling/order-imports",
        "/scheduling/order-imports/:orderId",
        "/scheduling/runs",
        "/scheduling/runs/new",
        "/scheduling/runs/:id",
        "/scheduling/resources",
        "/scheduling/resources/new",
        "/scheduling/resources/:id",
        "/scheduling/operations",
        "/scheduling/operations/new",
        "/scheduling/order-pegs",
        "/scheduling/order-pegs/new",
        "/scheduling/campaigns",
        "/scheduling/campaigns/new",
        "/scheduling/campaigns/:id",
        "/scheduling/campaign-proposals",
        "/scheduling/setup-matrices",
        "/scheduling/setup-matrices/new",
        "/scheduling/setup-matrices/:id",
        "/scheduling/cleanout-rules",
        "/scheduling/cleanout-rules/new",
        "/scheduling/networks",
        "/scheduling/networks/new",
        "/scheduling/networks/:id",
        "/scheduling/capacity-blocks",
        "/scheduling/capacity-blocks/new",
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
