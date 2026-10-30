import { describe, expect, it } from "vitest";
import policyJson from "../../../../policies/authz/data.json?raw";
import { loadModules } from "./registry";
import { UNSEEDED_DUTIES } from "./unseededDuties";

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

  it("gives every module a description for the home page", () => {
    for (const m of loadModules()) {
      expect(m.description, m.id).toBeTruthy();
    }
  });

  describe("nav duties", () => {
    const policy = JSON.parse(policyJson) as {
      duties: Record<string, unknown>;
    };
    const declared = new Set(loadModules().flatMap((m) => m.nav.flatMap((n) => (n.duty === undefined ? [] : [n.duty]))));

    it("are seeded in policies/authz/data.json unless listed as unseeded", () => {
      const unknown = [...declared].filter((d) => !(d in policy.duties) && !UNSEEDED_DUTIES.includes(d));
      expect(unknown).toEqual([]);
    });

    it("keep the unseeded list honest", () => {
      const stale = UNSEEDED_DUTIES.filter((d) => d in policy.duties || !declared.has(d));
      expect(stale).toEqual([]);
    });
  });
});
