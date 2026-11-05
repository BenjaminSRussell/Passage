import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import compliance from "./module";

describe("compliance module", () => {
  it("uses its folder name as id and declares a page for each entity", () => {
    expect(compliance.id).toBe("compliance");
    const paths = compliance.routes.map((r) => r.path);
    for (const path of ["/compliance", "/compliance/sod/conflicts", "/compliance/sod/conflicts/:id", "/compliance/sod/check", "/compliance/sod/rules", "/compliance/packs", "/compliance/packs/:code", "/compliance/audit/run", "/compliance/audit/cases", "/compliance/break-glass", "/compliance/incidents/breach-clocks", "/compliance/tds", "/compliance/screening/hits", "/compliance/screening/hits/:id", "/compliance/screening/check", "/compliance/export-checks", "/compliance/privacy/requests", "/compliance/privacy/requests/new", "/compliance/privacy/requests/:id", "/compliance/campaigns", "/compliance/incidents/step", "/compliance/export-policy"]) {
      expect(paths).toContain(path);
    }
    expect(compliance.routes.every((r) => r.element !== null && r.element !== undefined)).toBe(true);
  });

  it("gates nav by the PRD duties", () => {
    expect(compliance.nav.every((n) => n.to.startsWith("/compliance"))).toBe(true);
    const duty = (to: string) => compliance.nav.find((n) => n.to === to)?.duty;
    expect(duty("/compliance/sod/conflicts")).toBe("compliance.sod.review");
    expect(duty("/compliance/sod/check")).toBe("compliance.security.admin");
    expect(duty("/compliance/packs")).toBe("localization.admin");
    expect(duty("/compliance/incidents/breach-clocks")).toBe("compliance.security-incident.manage");
    expect(duty("/compliance/tds")).toBe("localization.admin");
    expect(duty("/compliance/audit/run")).toBe("compliance.audit-policy.run");
    expect(duty("/compliance/audit/cases")).toBe("compliance.audit-case.manage");
    expect(duty("/compliance/break-glass")).toBe("compliance.break-glass.request");
    expect(duty("/compliance/screening/hits")).toBe("compliance.trade-screening.view");
    expect(duty("/compliance/screening/check")).toBe("compliance.trade-screening.view");
    expect(duty("/compliance/export-checks")).toBe("compliance.trade-screening.view");
    expect(duty("/compliance/privacy/requests")).toBe("compliance.privacy-request.process");
    expect(duty("/compliance/privacy/requests/new")).toBe("compliance.privacy-request.create");
    expect(duty("/compliance/campaigns")).toBe("compliance.sod.admin");
    expect(duty("/compliance/incidents/step")).toBe("compliance.security-incident.manage");
    expect(duty("/compliance/export-policy")).toBe("compliance.view");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("compliance");
  });
});
