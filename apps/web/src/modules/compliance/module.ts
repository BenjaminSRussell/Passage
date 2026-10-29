import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ComplianceHome } from "./ComplianceHome";
import { ConflictDetailPage } from "./ConflictDetailPage";
import { ConflictsPage } from "./ConflictsPage";
import { PackDetailPage } from "./PackDetailPage";
import { PacksPage } from "./PacksPage";
import { RoleCheckPage } from "./RoleCheckPage";
import { RulesPage } from "./RulesPage";

const compliance: PassageModule = {
  id: "compliance",
  title: "Compliance",
  description: "Segregation-of-duties conflicts and mitigations, role assignment checks, SoD rules and country packs.",
  routes: [
    { path: "/compliance", element: createElement(ComplianceHome) },
    { path: "/compliance/sod/conflicts", element: createElement(ConflictsPage) },
    { path: "/compliance/sod/conflicts/:id", element: createElement(ConflictDetailPage), handle: { title: "SoD conflict" } },
    { path: "/compliance/sod/check", element: createElement(RoleCheckPage) },
    { path: "/compliance/sod/rules", element: createElement(RulesPage) },
    { path: "/compliance/packs", element: createElement(PacksPage) },
    { path: "/compliance/packs/:code", element: createElement(PackDetailPage), handle: { title: "Country pack" } },
  ],
  nav: [
    { label: "Overview", to: "/compliance", duty: "SoDReview" },
    { label: "SoD conflicts", to: "/compliance/sod/conflicts", duty: "SoDReview" },
    { label: "Role assignment check", to: "/compliance/sod/check", duty: "SecurityAdmin" },
    { label: "SoD rules", to: "/compliance/sod/rules", duty: "SoDReview" },
    { label: "Country packs", to: "/compliance/packs", duty: "LocalizationAdmin" },
  ],
};

export default compliance;
