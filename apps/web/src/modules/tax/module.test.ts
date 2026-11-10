import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import tax from "./module";

describe("tax module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /tax", () => {
    expect(tax.id).toBe("tax");
    expect(tax.routes.every((r) => r.path?.startsWith("/tax"))).toBe(true);
    expect(tax.nav.every((n) => n.to.startsWith("/tax"))).toBe(true);
    expect(tax.routes.every((r) => Boolean(r.handle))).toBe(true);
  });

  it("gates every route and nav entry on a duty the service seeds", () => {
    const seeded = new Set(["tax.config.read", "tax.vatreturn.read", "tax.intrastat.read"]);
    expect(tax.nav.every((n) => n.duty !== undefined && seeded.has(n.duty))).toBe(true);
    expect(tax.routes.every((r) => seeded.has((r.handle as { duty: string }).duty))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("tax");
  });
});
