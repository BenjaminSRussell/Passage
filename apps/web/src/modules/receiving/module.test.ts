import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import receiving from "./module";

describe("receiving module", () => {
  it("uses its folder name as id and declares routes and nav under /receiving", () => {
    expect(receiving.id).toBe("receiving");
    expect(receiving.routes.length).toBeGreaterThan(0);
    expect(receiving.nav.every((n) => n.to.startsWith("/receiving"))).toBe(true);
    expect(receiving.routes.every((r) => Boolean(r.handle))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("receiving");
  });
});
