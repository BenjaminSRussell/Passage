import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import org from "./module";

describe("org module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /org", () => {
    expect(org.id).toBe("org");
    expect(org.nav.every((n) => n.to.startsWith("/org") && n.duty === "org.read")).toBe(true);
    const paths = (org.routes[0].children ?? []).map((r) => r.path);
    expect(paths).toEqual(["/org", "/org/legal-entities", "/org/operating-units", "/org/sites", "/org/warehouses", "/org/locations", "/org/fiscal-calendars", "/org/teams", "/org/reference-data", "/org/hierarchy", "/org/package"]);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("org");
  });
});
