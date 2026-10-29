import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AuditCasesPage } from "./AuditCasesPage";
import { AuditRunPage } from "./AuditRunPage";
import { BreachClocksPage } from "./BreachClocksPage";
import { BreakGlassPage } from "./BreakGlassPage";
import { ComplianceHome } from "./ComplianceHome";
import { ConflictDetailPage } from "./ConflictDetailPage";
import { ConflictsPage } from "./ConflictsPage";
import { ExportCheckPage } from "./ExportCheckPage";
import { PackDetailPage } from "./PackDetailPage";
import { PacksPage } from "./PacksPage";
import { RoleCheckPage } from "./RoleCheckPage";
import { RulesPage } from "./RulesPage";
import { ScreeningCheckPage } from "./ScreeningCheckPage";
import { ScreeningHitPage } from "./ScreeningHitPage";
import { ScreeningHitsPage } from "./ScreeningHitsPage";
import { TdsPage } from "./TdsPage";

const compliance: PassageModule = {
  id: "compliance",
  title: "Compliance",
  description: "Segregation-of-duties conflicts and mitigations, role assignment checks, SoD rules, audit rule runs and audit cases, break-glass access, breach notification clocks, sanctions screening hits and checks, export license checks, country packs and the India TDS calculator.",
  routes: [
    { path: "/compliance", element: createElement(ComplianceHome) },
    { path: "/compliance/sod/conflicts", element: createElement(ConflictsPage) },
    { path: "/compliance/sod/conflicts/:id", element: createElement(ConflictDetailPage), handle: { title: "SoD conflict" } },
    { path: "/compliance/sod/check", element: createElement(RoleCheckPage) },
    { path: "/compliance/sod/rules", element: createElement(RulesPage) },
    { path: "/compliance/packs", element: createElement(PacksPage) },
    { path: "/compliance/packs/:code", element: createElement(PackDetailPage), handle: { title: "Country pack" } },
    { path: "/compliance/audit/run", element: createElement(AuditRunPage) },
    { path: "/compliance/audit/cases", element: createElement(AuditCasesPage) },
    { path: "/compliance/break-glass", element: createElement(BreakGlassPage) },
    { path: "/compliance/incidents/breach-clocks", element: createElement(BreachClocksPage) },
    { path: "/compliance/tds", element: createElement(TdsPage) },
    { path: "/compliance/screening/hits", element: createElement(ScreeningHitsPage) },
    { path: "/compliance/screening/hits/:id", element: createElement(ScreeningHitPage), handle: { title: "Screening hit" } },
    { path: "/compliance/screening/check", element: createElement(ScreeningCheckPage) },
    { path: "/compliance/export-checks", element: createElement(ExportCheckPage) },
  ],
  nav: [
    { label: "Overview", to: "/compliance", duty: "SoDReview" },
    { label: "SoD conflicts", to: "/compliance/sod/conflicts", duty: "SoDReview" },
    { label: "Role assignment check", to: "/compliance/sod/check", duty: "SecurityAdmin" },
    { label: "SoD rules", to: "/compliance/sod/rules", duty: "SoDReview" },
    { label: "Audit rule run", to: "/compliance/audit/run", duty: "AuditPolicyRun" },
    { label: "Audit cases", to: "/compliance/audit/cases", duty: "AuditCaseManage" },
    { label: "Break-glass access", to: "/compliance/break-glass", duty: "BreakGlassRequest" },
    { label: "Breach clocks", to: "/compliance/incidents/breach-clocks", duty: "SecurityIncidentManage" },
    { label: "Country packs", to: "/compliance/packs", duty: "LocalizationAdmin" },
    { label: "India TDS", to: "/compliance/tds", duty: "LocalizationAdmin" },
    { label: "Screening hits", to: "/compliance/screening/hits", duty: "TradeComplianceView" },
    { label: "Screening check", to: "/compliance/screening/check", duty: "TradeComplianceView" },
    { label: "Export license check", to: "/compliance/export-checks", duty: "TradeComplianceView" },
  ],
};

export default compliance;
