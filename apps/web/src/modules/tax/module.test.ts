import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import tax from "./module";

describe("tax module", () => {
  it("uses its folder name as id and declares routes and nav", () => {
    expect(tax.id).toBe("tax");
    expect(tax.routes.length).toBeGreaterThan(0);
    expect(tax.nav.every((n) => n.to.startsWith("/tax"))).toBe(true);
  });

  it("renders a page for its route", () => {
    expect(tax.routes[0].element).not.toBeNull();
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("tax");
  });
});
