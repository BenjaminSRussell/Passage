import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import sales from "./module";

describe("sales module", () => {
  it("uses its folder name as id and declares a page for each entity", () => {
    expect(sales.id).toBe("sales");
    const paths = sales.routes.map((r) => r.path);
    for (const path of ["/sales/orders", "/sales/orders/new", "/sales/orders/:id", "/sales/credit-holds", "/sales/credit-profiles", "/sales/credit-profiles/new", "/sales/atp", "/sales/orders/:id/change", "/sales/schedule-releases", "/sales/backorder-runs", "/sales/backorders", "/sales/orders/:id/promise", "/sales/product-listings", "/sales/product-listings/new", "/sales/agreements", "/sales/agreements/new", "/sales/agreements/:id", "/sales/quotations", "/sales/quotations/new", "/sales/quotations/:id", "/sales/supplementary-items", "/sales/supplementary-items/new", "/sales/product-proposals", "/sales/product-proposals/new", "/sales/allocation-quotas", "/sales/allocation-quotas/new", "/sales/allocation-quotas/:id"]) {
      expect(paths).toContain(path);
    }
    expect(sales.routes.every((r) => r.element !== null)).toBe(true);
  });

  it("gates every nav entry by a sales duty, the release queue by sales.credit.release", () => {
    expect(sales.nav.every((n) => n.to.startsWith("/sales/") && n.duty?.startsWith("sales."))).toBe(true);
    expect(sales.nav.find((n) => n.to === "/sales/credit-holds")?.duty).toBe("sales.credit.release");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("sales");
  });
});
