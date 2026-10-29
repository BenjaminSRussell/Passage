import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import compliance from "./module";

describe("compliance module", () => {
  it("uses its folder name as id and declares a page for each entity", () => {
    expect(compliance.id).toBe("compliance");
    const paths = compliance.routes.map((r) => r.path);
    for (const path of ["/compliance", "/compliance/sod/conflicts", "/compliance/sod/conflicts/:id", "/compliance/sod/check", "/compliance/sod/rules", "/compliance/packs", "/compliance/packs/:code", "/compliance/audit/run", "/compliance/audit/cases", "/compliance/break-glass", "/compliance/incidents/breach-clocks", "/compliance/tds", "/compliance/screening/hits", "/compliance/screening/hits/:id", "/compliance/screening/check", "/compliance/export-checks"]) {
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
    expect(duty("/compliance/audit/run")).toBe("AuditPolicyRun");
    expect(duty("/compliance/audit/cases")).toBe("AuditCaseManage");
    expect(duty("/compliance/break-glass")).toBe("BreakGlassRequest");
    expect(duty("/compliance/screening/hits")).toBe("TradeComplianceView");
    expect(duty("/compliance/screening/check")).toBe("TradeComplianceView");
    expect(duty("/compliance/export-checks")).toBe("TradeComplianceView");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("compliance");
  });
});
