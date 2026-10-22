import { describe, expect, it } from "vitest";
import { loadModules } from "./registry";

describe("module registry", () => {
  it("discovers modules with unique ids and sorted titles", () => {
    const mods = loadModules();
    const ids = mods.map((m) => m.id);
    expect(new Set(ids).size).toBe(ids.length);
    const titles = mods.map((m) => m.title);
    expect(titles).toEqual([...titles].sort((a, b) => a.localeCompare(b)));
    for (const m of mods) {
      expect(m.routes.length).toBeGreaterThan(0);
      expect(m.nav.length).toBeGreaterThan(0);
    }
  });
});
