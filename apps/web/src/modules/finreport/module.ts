import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AuditTrailPage } from "./AuditTrailPage";
import { DefinitionEditorPage } from "./DefinitionEditorPage";
import { DefinitionsPage } from "./DefinitionsPage";
import { HierarchiesPage } from "./HierarchiesPage";
import { HierarchyEditorPage } from "./HierarchyEditorPage";
import { NewRunPage } from "./NewRunPage";
import { RunPage } from "./RunPage";
import { RunsPage } from "./RunsPage";
import { ScheduleEditorPage } from "./ScheduleEditorPage";
import { SchedulesPage } from "./SchedulesPage";

const finreport: PassageModule = {
  id: "finreport",
  title: "Financial statements",
  description: "Report definitions, statement runs over the journal with drill-down to the balances and journal lines behind every amount, downloads, distribution schedules and the audit trail.",
  routes: [
    { path: "/finreport", element: createElement(RunsPage), handle: { title: "Financial statements" } },
    { path: "/finreport/runs/new", element: createElement(NewRunPage), handle: { title: "Run a statement" } },
    { path: "/finreport/runs/:runId", element: createElement(RunPage), handle: { title: "Statement run" } },
    { path: "/finreport/definitions", element: createElement(DefinitionsPage), handle: { title: "Report definitions" } },
    { path: "/finreport/definitions/new", element: createElement(DefinitionEditorPage), handle: { title: "New report definition" } },
    { path: "/finreport/definitions/:code", element: createElement(DefinitionEditorPage), handle: { title: "Report definition" } },
    { path: "/finreport/hierarchies", element: createElement(HierarchiesPage), handle: { title: "Derived hierarchies" } },
    { path: "/finreport/hierarchies/new", element: createElement(HierarchyEditorPage), handle: { title: "New derived hierarchy" } },
    { path: "/finreport/hierarchies/:code", element: createElement(HierarchyEditorPage), handle: { title: "Derived hierarchy" } },
    { path: "/finreport/schedules", element: createElement(SchedulesPage), handle: { title: "Distribution schedules" } },
    { path: "/finreport/schedules/new", element: createElement(ScheduleEditorPage), handle: { title: "New schedule" } },
    { path: "/finreport/schedules/:code", element: createElement(ScheduleEditorPage), handle: { title: "Distribution schedule" } },
    { path: "/finreport/audit-trail", element: createElement(AuditTrailPage), handle: { title: "Audit trail" } },
  ],
  nav: [
    { label: "Statements", to: "/finreport", duty: "reporting.statement.view" },
    { label: "Report definitions", to: "/finreport/definitions", duty: "reporting.statement.view" },
    { label: "Derived hierarchies", to: "/finreport/hierarchies", duty: "reporting.definition.maintain" },
    { label: "Distribution schedules", to: "/finreport/schedules", duty: "reporting.statement.schedule" },
    { label: "Audit trail", to: "/finreport/audit-trail", duty: "reporting.audit.view" },
  ],
};

export default finreport;
