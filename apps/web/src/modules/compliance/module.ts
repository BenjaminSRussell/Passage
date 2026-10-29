import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BreachClocksPage } from "./BreachClocksPage";
import { ComplianceHome } from "./ComplianceHome";
import { ConflictDetailPage } from "./ConflictDetailPage";
import { ConflictsPage } from "./ConflictsPage";
import { PackDetailPage } from "./PackDetailPage";
import { PacksPage } from "./PacksPage";
import { RoleCheckPage } from "./RoleCheckPage";
import { RulesPage } from "./RulesPage";
import { TdsPage } from "./TdsPage";

const compliance: PassageModule = {
  id: "compliance",
  title: "Compliance",
  description: "Segregation-of-duties conflicts and mitigations, role assignment checks, SoD rules, breach notification clocks, country packs and the India TDS calculator.",
  routes: [
    { path: "/compliance", element: createElement(ComplianceHome) },
    { path: "/compliance/sod/conflicts", element: createElement(ConflictsPage) },
    { path: "/compliance/sod/conflicts/:id", element: createElement(ConflictDetailPage), handle: { title: "SoD conflict" } },
    { path: "/compliance/sod/check", element: createElement(RoleCheckPage) },
    { path: "/compliance/sod/rules", element: createElement(RulesPage) },
    { path: "/compliance/packs", element: createElement(PacksPage) },
    { path: "/compliance/packs/:code", element: createElement(PackDetailPage), handle: { title: "Country pack" } },
    { path: "/compliance/incidents/breach-clocks", element: createElement(BreachClocksPage) },
    { path: "/compliance/tds", element: createElement(TdsPage) },
  ],
  nav: [
    { label: "Overview", to: "/compliance", duty: "SoDReview" },
    { label: "SoD conflicts", to: "/compliance/sod/conflicts", duty: "SoDReview" },
    { label: "Role assignment check", to: "/compliance/sod/check", duty: "SecurityAdmin" },
    { label: "SoD rules", to: "/compliance/sod/rules", duty: "SoDReview" },
    { label: "Breach clocks", to: "/compliance/incidents/breach-clocks", duty: "SecurityIncidentManage" },
    { label: "Country packs", to: "/compliance/packs", duty: "LocalizationAdmin" },
    { label: "India TDS", to: "/compliance/tds", duty: "LocalizationAdmin" },
  ],
};

export default compliance;
