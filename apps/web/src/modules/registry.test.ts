import { describe, expect, it } from "vitest";
import policyJson from "../../../../policies/authz/data.json?raw";
import { hasEffectiveDutyScope, routeDuty } from "../shell/dutyGate";
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

  it("gives every module a description for the home page", () => {
    for (const m of loadModules()) {
      expect(m.description, m.id).toBeTruthy();
    }
  });

  it("leaves no route of a gated module outside every nav prefix without its own duty", () => {
    const modules = loadModules();
    const unguarded = modules
      .filter((m) => m.nav.some((n) => n.duty !== undefined))
      .flatMap((m) =>
        m.routes
          .filter((r) => r.path !== undefined && r.path !== "*" && routeDuty(r) === undefined && !hasEffectiveDutyScope(r.path, modules))
          .map((r) => `${m.id} ${r.path}`),
      );
    expect(unguarded).toEqual([]);
  });

  describe("declared duties", () => {
    const policy = JSON.parse(policyJson) as {
      duties: Record<string, unknown>;
    };
    const modules = loadModules();
    const navDuties = new Set(modules.flatMap((m) => m.nav.flatMap((n) => (n.duty === undefined ? [] : [n.duty]))));
    const routeDuties = new Set(modules.flatMap((m) => m.routes.flatMap((r) => routeDuty(r) ?? [])));

    it("seed every nav duty in policies/authz/data.json", () => {
      expect([...navDuties].filter((d) => !(d in policy.duties))).toEqual([]);
    });

    it("seed every route handle duty in policies/authz/data.json", () => {
      expect([...routeDuties].filter((d) => !(d in policy.duties))).toEqual([]);
    });
  });
});
