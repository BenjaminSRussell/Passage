import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import close from "./module";

describe("close module", () => {
  it("uses its folder name as id and declares routes and nav", () => {
    expect(close.id).toBe("close");
    expect(close.routes.length).toBeGreaterThan(0);
    expect(close.nav.every((n) => n.to.startsWith("/close"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("close");
  });
});
