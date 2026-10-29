import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CutoverPlanDetailPage } from "./CutoverPlanDetailPage";
import { CutoverPlansPage } from "./CutoverPlansPage";
import { NewCutoverPlanPage } from "./NewCutoverPlanPage";
import { NewPackRunPage } from "./NewPackRunPage";
import { PackRunDetailPage } from "./PackRunDetailPage";
import { PackRunsPage } from "./PackRunsPage";

const migrationFactory: PassageModule = {
  id: "migration-factory",
  title: "Migration factory",
  description: "Move a customer off AX or SAP: pack runs with control totals and quarantine, and the cutover runbook with go/no-go gates and rollback.",
  routes: [
    { path: "/migration-factory", element: createElement(PackRunsPage), handle: { title: "Pack runs" } },
    { path: "/migration-factory/runs/new", element: createElement(NewPackRunPage), handle: { title: "Start pack run" } },
    { path: "/migration-factory/runs/:id", element: createElement(PackRunDetailPage), handle: { title: "Pack run" } },
    { path: "/migration-factory/cutover", element: createElement(CutoverPlansPage), handle: { title: "Cutover runbooks" } },
    { path: "/migration-factory/cutover/new", element: createElement(NewCutoverPlanPage), handle: { title: "Generate cutover plan" } },
    { path: "/migration-factory/cutover/:id", element: createElement(CutoverPlanDetailPage), handle: { title: "Cutover runbook" } },
  ],
  nav: [
    { label: "Pack runs", to: "/migration-factory", duty: "MigrationView" },
    { label: "Cutover runbooks", to: "/migration-factory/cutover", duty: "CutoverManager" },
  ],
};

export default migrationFactory;
