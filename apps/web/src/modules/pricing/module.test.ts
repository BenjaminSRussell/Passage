import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import pricing from "./module";

describe("pricing module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /pricing", () => {
    expect(pricing.id).toBe("pricing");
    expect(pricing.routes.every((r) => r.element != null && r.path?.startsWith("/pricing"))).toBe(true);
    expect(pricing.nav.every((n) => n.to.startsWith("/pricing"))).toBe(true);
  });

  it("has a list, a create form and a detail page for trade agreements, campaigns, free goods rules, commodity cost basis types and price calculations, procedures and their determination per sales channel, price journals, rebates and vendor rebate claims, broker contracts and royalty agreements with their claims, sales groups, trade funds and promotions, plus the commission inquiry and settlement export, the journal CSV import, the workspace and simulation", () => {
    expect(pricing.routes.map((r) => r.path)).toEqual([
      "/pricing",
      "/pricing/agreements",
      "/pricing/agreements/new",
      "/pricing/agreements/:id",
      "/pricing/campaigns",
      "/pricing/campaigns/new",
      "/pricing/campaigns/:id",
      "/pricing/free-goods",
      "/pricing/free-goods/new",
      "/pricing/free-goods/:id",
      "/pricing/commodity/bases",
      "/pricing/commodity/bases/new",
      "/pricing/commodity/bases/:id",
      "/pricing/commodity/calculations",
      "/pricing/commodity/calculations/new",
      "/pricing/commodity/calculations/:id",
      "/pricing/procedures",
      "/pricing/procedures/new",
      "/pricing/procedures/:id",
      "/pricing/procedure-determination",
      "/pricing/journals",
      "/pricing/journals/new",
      "/pricing/journals/import",
      "/pricing/journals/:id",
      "/pricing/rebates",
      "/pricing/rebates/new",
      "/pricing/rebates/claims",
      "/pricing/rebates/claims/:id",
      "/pricing/rebates/:id",
      "/pricing/brokers",
      "/pricing/brokers/new",
      "/pricing/brokers/:id",
      "/pricing/royalties",
      "/pricing/royalties/new",
      "/pricing/royalties/:id",
      "/pricing/contract-claims",
      "/pricing/contract-claims/:id",
      "/pricing/commission/sales-groups",
      "/pricing/commission/sales-groups/new",
      "/pricing/commission/sales-groups/:id",
      "/pricing/commission/transactions",
      "/pricing/commission/settlement",
      "/pricing/funds",
      "/pricing/funds/new",
      "/pricing/funds/:id",
      "/pricing/promotions",
      "/pricing/promotions/new",
      "/pricing/promotions/:id",
      "/pricing/parameters",
      "/pricing/simulate",
    ]);
  });

  it("guards the navigation with the view duties the service enforces", () => {
    expect(Object.fromEntries(pricing.nav.map((n) => [n.to, n.duty]))).toEqual({
      "/pricing": "pricing.view",
      "/pricing/agreements": "pricing.view",
      "/pricing/campaigns": "pricing.view",
      "/pricing/commodity/bases": "pricing.view",
      "/pricing/commodity/calculations": "pricing.view",
      "/pricing/free-goods": "pricing.view",
      "/pricing/procedures": "pricing.view",
      "/pricing/procedure-determination": "pricing.view",
      "/pricing/journals": "pricing.view",
      "/pricing/parameters": "pricing.view",
      "/pricing/rebates": "pricing.rebate.view",
      "/pricing/rebates/claims": "pricing.rebate.view",
      "/pricing/brokers": "pricing.rebate.view",
      "/pricing/royalties": "pricing.rebate.view",
      "/pricing/contract-claims": "pricing.rebate.view",
      "/pricing/commission/sales-groups": "pricing.commission.view",
      "/pricing/commission/transactions": "pricing.commission.view",
      "/pricing/commission/settlement": "pricing.commission.view",
      "/pricing/funds": "pricing.view",
      "/pricing/promotions": "pricing.view",
      "/pricing/simulate": "pricing.view",
    });
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("pricing");
  });
});
