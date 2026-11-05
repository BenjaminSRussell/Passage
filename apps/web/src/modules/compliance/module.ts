import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AuditCasesPage } from "./AuditCasesPage";
import { AuditRunPage } from "./AuditRunPage";
import { BreachClocksPage } from "./BreachClocksPage";
import { BreakGlassPage } from "./BreakGlassPage";
import { CampaignPage } from "./CampaignPage";
import { ComplianceHome } from "./ComplianceHome";
import { ConflictDetailPage } from "./ConflictDetailPage";
import { ConflictsPage } from "./ConflictsPage";
import { ExportCheckPage } from "./ExportCheckPage";
import { ExportPolicyPage } from "./ExportPolicyPage";
import { IncidentStepPage } from "./IncidentStepPage";
import { PackDetailPage } from "./PackDetailPage";
import { PacksPage } from "./PacksPage";
import { PrivacyRequestNewPage } from "./PrivacyRequestNewPage";
import { PrivacyRequestPage } from "./PrivacyRequestPage";
import { PrivacyRequestsPage } from "./PrivacyRequestsPage";
import { RoleCheckPage } from "./RoleCheckPage";
import { RulesPage } from "./RulesPage";
import { ScreeningCheckPage } from "./ScreeningCheckPage";
import { ScreeningHitPage } from "./ScreeningHitPage";
import { ScreeningHitsPage } from "./ScreeningHitsPage";
import { TdsPage } from "./TdsPage";

const compliance: PassageModule = {
  id: "compliance",
  title: "Compliance",
  description: "Segregation-of-duties conflicts and mitigations, role assignment checks, SoD rules, audit rule runs and audit cases, break-glass access, breach notification clocks, sanctions screening hits and checks, export license checks, privacy requests, certification campaigns, incident steps, export policy, country packs and the India TDS calculator.",
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
    { path: "/compliance/privacy/requests", element: createElement(PrivacyRequestsPage) },
    { path: "/compliance/privacy/requests/new", element: createElement(PrivacyRequestNewPage), handle: { title: "Record a privacy request" } },
    { path: "/compliance/privacy/requests/:id", element: createElement(PrivacyRequestPage), handle: { title: "Privacy request" } },
    { path: "/compliance/campaigns", element: createElement(CampaignPage) },
    { path: "/compliance/incidents/step", element: createElement(IncidentStepPage) },
    { path: "/compliance/export-policy", element: createElement(ExportPolicyPage) },
  ],
  nav: [
    { label: "Overview", to: "/compliance", duty: "compliance.sod.review" },
    { label: "SoD conflicts", to: "/compliance/sod/conflicts", duty: "compliance.sod.review" },
    { label: "Role assignment check", to: "/compliance/sod/check", duty: "compliance.security.admin" },
    { label: "SoD rules", to: "/compliance/sod/rules", duty: "compliance.sod.review" },
    { label: "Audit rule run", to: "/compliance/audit/run", duty: "compliance.audit-policy.run" },
    { label: "Audit cases", to: "/compliance/audit/cases", duty: "compliance.audit-case.manage" },
    { label: "Break-glass access", to: "/compliance/break-glass", duty: "compliance.break-glass.request" },
    { label: "Breach clocks", to: "/compliance/incidents/breach-clocks", duty: "compliance.security-incident.manage" },
    { label: "Country packs", to: "/compliance/packs", duty: "localization.admin" },
    { label: "India TDS", to: "/compliance/tds", duty: "localization.admin" },
    { label: "Screening hits", to: "/compliance/screening/hits", duty: "compliance.trade-screening.view" },
    { label: "Screening check", to: "/compliance/screening/check", duty: "compliance.trade-screening.view" },
    { label: "Export license check", to: "/compliance/export-checks", duty: "compliance.trade-screening.view" },
    { label: "Privacy requests", to: "/compliance/privacy/requests", duty: "compliance.privacy-request.process" },
    { label: "Record a privacy request", to: "/compliance/privacy/requests/new", duty: "compliance.privacy-request.create" },
    { label: "Certification campaign", to: "/compliance/campaigns", duty: "compliance.sod.admin" },
    { label: "Incident step check", to: "/compliance/incidents/step", duty: "compliance.security-incident.manage" },
    { label: "Export policy check", to: "/compliance/export-policy", duty: "compliance.view" },
  ],
};

export default compliance;
