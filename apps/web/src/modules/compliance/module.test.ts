import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import compliance from "./module";

describe("compliance module", () => {
  it("uses its folder name as id and declares a page for each entity", () => {
    expect(compliance.id).toBe("compliance");
    const paths = compliance.routes.map((r) => r.path);
    for (const path of ["/compliance", "/compliance/sod/conflicts", "/compliance/sod/conflicts/:id", "/compliance/sod/check", "/compliance/sod/rules", "/compliance/packs", "/compliance/packs/:code", "/compliance/incidents/breach-clocks", "/compliance/tds"]) {
      expect(paths).toContain(path);
    }
    expect(compliance.routes.every((r) => r.element !== null && r.element !== undefined)).toBe(true);
  });

  it("gates nav by the PRD duties", () => {
    expect(compliance.nav.every((n) => n.to.startsWith("/compliance"))).toBe(true);
    const duty = (to: string) => compliance.nav.find((n) => n.to === to)?.duty;
    expect(duty("/compliance/sod/conflicts")).toBe("SoDReview");
    expect(duty("/compliance/sod/check")).toBe("SecurityAdmin");
    expect(duty("/compliance/packs")).toBe("LocalizationAdmin");
    expect(duty("/compliance/incidents/breach-clocks")).toBe("SecurityIncidentManage");
    expect(duty("/compliance/tds")).toBe("LocalizationAdmin");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("compliance");
  });
});
