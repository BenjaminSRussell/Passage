import { createElement } from "react";
import type { PassageModule } from "../registry";
import { DefinitionEditorPage } from "./DefinitionEditorPage";
import { DefinitionsPage } from "./DefinitionsPage";
import { HierarchiesPage } from "./HierarchiesPage";
import { HierarchyEditorPage } from "./HierarchyEditorPage";
import { NewRunPage } from "./NewRunPage";
import { RunPage } from "./RunPage";
import { RunsPage } from "./RunsPage";

const finreport: PassageModule = {
  id: "finreport",
  title: "Financial statements",
  description: "Report definitions and statement runs over the journal, with drill-down from every amount to the balances behind it.",
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
  ],
  nav: [
    { label: "Statements", to: "/finreport", duty: "reporting.statement.view" },
    { label: "Report definitions", to: "/finreport/definitions", duty: "reporting.statement.view" },
    { label: "Derived hierarchies", to: "/finreport/hierarchies", duty: "reporting.definition.maintain" },
  ],
};

export default finreport;
