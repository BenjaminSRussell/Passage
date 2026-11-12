import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import billing from "./module";

describe("billing module", () => {
  it("uses its folder name as id and declares routes and nav under /billing", () => {
    expect(billing.id).toBe("billing");
    expect(billing.routes.map((r) => r.path)).toEqual([
      "/billing",
      "/billing/free-text-invoices/new",
      "/billing/proposals/new",
      "/billing/invoices/:id",
      "/billing/invoices/:id/trace",
      "/billing/credit-notes",
      "/billing/credit-notes/new",
      "/billing/revenue-allocation",
      "/billing/revenue-contracts",
      "/billing/revenue-contracts/new",
      "/billing/revenue-contracts/:id",
      "/billing/revenue-contracts/:id/modify",
      "/billing/deferred-revenue",
    ]);
    expect(billing.nav.every((n) => n.to.startsWith("/billing"))).toBe(true);
  });

  it("guards each nav entry with the service duty that serves it", () => {
    expect(Object.fromEntries(billing.nav.map((n) => [n.to, n.duty]))).toEqual({
      "/billing": "billing.invoice.view",
      "/billing/proposals/new": "billing.invoice.maintain",
      "/billing/free-text-invoices/new": "billing.invoice.maintain",
      "/billing/credit-notes": "billing.invoice.view",
      "/billing/revenue-contracts": "revrec.view",
      "/billing/deferred-revenue": "revrec.view",
      "/billing/revenue-allocation": "revrec.view",
    });
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("billing");
  });
});
