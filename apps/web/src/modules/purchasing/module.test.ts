import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import purchasing from "./module";

describe("purchasing module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /purchasing", () => {
    expect(purchasing.id).toBe("purchasing");
    expect(purchasing.routes.every((r) => r.path?.startsWith("/purchasing"))).toBe(true);
    expect(purchasing.nav.every((n) => n.to.startsWith("/purchasing"))).toBe(true);
  });

  it("gives every nav entry a route and a PRD duty", () => {
    const paths = new Set(purchasing.routes.map((r) => r.path));
    const duties = new Set(["PurchaseOrderInquire", "RequisitionCreate", "PurchaseOrderMaintain", "TradeAgreementMaintain", "AgreementMaintain", "RFQMaintain"]);
    for (const n of purchasing.nav) {
      expect(paths.has(n.to)).toBe(true);
      expect(duties.has(n.duty ?? "")).toBe(true);
    }
  });

  it("has a list, a create form and a detail page for each document", () => {
    const paths = purchasing.routes.map((r) => r.path);
    for (const doc of ["requisitions", "rfqs", "purchase-orders", "agreements", "scheduling-agreements", "trade-agreements"]) {
      expect(paths).toEqual(expect.arrayContaining([`/purchasing/${doc}`, `/purchasing/${doc}/new`, `/purchasing/${doc}/:id`]));
    }
  });

  it("routes the release documentation of a scheduling agreement", () => {
    expect(purchasing.routes.map((r) => r.path)).toContain("/purchasing/scheduling-agreements/:id/releases/:releaseNo");
  });

  it("routes the purchase order edit page", () => {
    expect(purchasing.routes.map((r) => r.path)).toContain("/purchasing/purchase-orders/:id/edit");
  });

  it("routes the invoicing plan page of a purchase order line", () => {
    expect(purchasing.routes.map((r) => r.path)).toContain("/purchasing/purchase-orders/:id/lines/:lineNo/invoicing-plan");
  });

  it("routes the price change pages for one order and for the run", () => {
    expect(purchasing.routes.map((r) => r.path)).toEqual(
      expect.arrayContaining(["/purchasing/purchase-orders/:id/reprice", "/purchasing/reprice"]),
    );
  });

  it("routes the consolidation board", () => {
    expect(purchasing.routes.map((r) => r.path)).toContain("/purchasing/consolidation");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("purchasing");
  });
});

describe("todayIso", () => {
  it("uses the local calendar date, not the UTC one", async () => {
    const { todayIso } = await import("./parts");
    expect(todayIso(new Date(2026, 9, 26, 23, 30))).toBe("2026-10-26");
    expect(todayIso(new Date(2027, 0, 5, 0, 5))).toBe("2027-01-05");
  });
});
