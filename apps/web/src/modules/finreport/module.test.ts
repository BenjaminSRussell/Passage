import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import finreport from "./module";

describe("finreport module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /finreport", () => {
    expect(finreport.id).toBe("finreport");
    expect(finreport.routes.every((r) => r.element != null && r.path?.startsWith("/finreport"))).toBe(true);
    expect(finreport.nav.every((n) => n.to.startsWith("/finreport"))).toBe(true);
  });

  it("has a list, a form and a detail page for statement runs, report definitions, derived hierarchies and schedules, the audit trail report and the ledger verification", () => {
    expect(finreport.routes.map((r) => r.path)).toEqual([
      "/finreport",
      "/finreport/runs/new",
      "/finreport/runs/:runId",
      "/finreport/definitions",
      "/finreport/definitions/new",
      "/finreport/definitions/:code",
      "/finreport/hierarchies",
      "/finreport/hierarchies/new",
      "/finreport/hierarchies/:code",
      "/finreport/schedules",
      "/finreport/schedules/new",
      "/finreport/schedules/:code",
      "/finreport/audit-trail",
      "/finreport/verifications",
      "/finreport/verifications/:id",
    ]);
  });

  it("guards the navigation with the duty the service enforces for each list", () => {
    expect(finreport.nav.map((n) => n.duty)).toEqual([
      "reporting.statement.view",
      "reporting.statement.view",
      "reporting.definition.maintain",
      "reporting.statement.schedule",
      "reporting.audit.view",
      "reporting.audit.view",
    ]);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("finreport");
  });
});
