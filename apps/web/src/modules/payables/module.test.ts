import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import payables from "./module";

describe("payables module", () => {
  it("uses its folder name as id and declares routes and nav", () => {
    expect(payables.id).toBe("payables");
    expect(payables.routes.length).toBeGreaterThan(0);
    expect(payables.nav.every((n) => n.to.startsWith("/payables"))).toBe(true);
  });

  it("renders a page for its route", () => {
    expect(payables.routes[0].element).not.toBeNull();
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("payables");
  });
});
