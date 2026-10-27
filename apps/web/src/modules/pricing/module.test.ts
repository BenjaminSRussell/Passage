import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import pricing from "./module";

describe("pricing module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /pricing", () => {
    expect(pricing.id).toBe("pricing");
    expect(pricing.routes.every((r) => r.element != null && r.path?.startsWith("/pricing"))).toBe(true);
    expect(pricing.nav.every((n) => n.to.startsWith("/pricing"))).toBe(true);
  });

  it("has a list, a create form and a detail page for trade agreements, campaigns, procedures, price journals, rebates and vendor rebate claims, sales groups, trade funds and promotions, plus the commission inquiry, the workspace and simulation", () => {
    expect(pricing.routes.map((r) => r.path)).toEqual([
      "/pricing",
      "/pricing/agreements",
      "/pricing/agreements/new",
      "/pricing/agreements/:id",
      "/pricing/campaigns",
      "/pricing/campaigns/new",
      "/pricing/campaigns/:id",
      "/pricing/procedures",
      "/pricing/procedures/new",
      "/pricing/procedures/:id",
      "/pricing/journals",
      "/pricing/journals/new",
      "/pricing/journals/:id",
      "/pricing/rebates",
      "/pricing/rebates/new",
      "/pricing/rebates/claims",
      "/pricing/rebates/claims/:id",
      "/pricing/rebates/:id",
      "/pricing/commission/sales-groups",
      "/pricing/commission/sales-groups/new",
      "/pricing/commission/sales-groups/:id",
      "/pricing/commission/transactions",
      "/pricing/funds",
      "/pricing/funds/new",
      "/pricing/funds/:id",
      "/pricing/promotions",
      "/pricing/promotions/new",
      "/pricing/promotions/:id",
      "/pricing/simulate",
    ]);
  });

  it("guards the navigation with the view duties the service enforces", () => {
    expect(Object.fromEntries(pricing.nav.map((n) => [n.to, n.duty]))).toEqual({
      "/pricing": "pricing.view",
      "/pricing/agreements": "pricing.view",
      "/pricing/campaigns": "pricing.view",
      "/pricing/procedures": "pricing.view",
      "/pricing/journals": "pricing.view",
      "/pricing/rebates": "pricing.rebate.view",
      "/pricing/rebates/claims": "pricing.rebate.view",
      "/pricing/commission/sales-groups": "pricing.commission.view",
      "/pricing/commission/transactions": "pricing.commission.view",
      "/pricing/funds": "pricing.view",
      "/pricing/promotions": "pricing.view",
      "/pricing/simulate": "pricing.view",
    });
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("pricing");
  });
});
