import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import apInvoices from "./module";

describe("ap-invoices module", () => {
  it("uses its folder name as id and declares routes and nav under /ap-invoices", () => {
    expect(apInvoices.id).toBe("ap-invoices");
    expect(apInvoices.routes.length).toBeGreaterThan(0);
    expect(apInvoices.nav.every((n) => n.to.startsWith("/ap-invoices"))).toBe(true);
    expect(apInvoices.routes.every((r) => Boolean(r.handle))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("ap-invoices");
  });
});
