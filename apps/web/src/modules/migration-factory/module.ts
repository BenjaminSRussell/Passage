import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CutoverPlanDetailPage } from "./CutoverPlanDetailPage";
import { CutoverPlansPage } from "./CutoverPlansPage";
import { NewCutoverPlanPage } from "./NewCutoverPlanPage";
import { NewPackRunPage } from "./NewPackRunPage";
import { NewParallelRunPage } from "./NewParallelRunPage";
import { NewProjectRunPage } from "./NewProjectRunPage";
import { PackRunDetailPage } from "./PackRunDetailPage";
import { PackRunsPage } from "./PackRunsPage";
import { ParallelRunDetailPage } from "./ParallelRunDetailPage";
import { ParallelRunsPage } from "./ParallelRunsPage";
import { ProjectOpeningPage } from "./ProjectOpeningPage";
import { XrefLookupPage } from "./XrefLookupPage";

const migrationFactory: PassageModule = {
  id: "migration-factory",
  title: "Migration factory",
  description:
    "Move a customer off AX or SAP: pack runs with control totals and quarantine release, project beginning balances, parallel-run review, legacy references and the cutover runbook.",
  routes: [
    { path: "/migration-factory", element: createElement(PackRunsPage), handle: { title: "Pack runs" } },
    { path: "/migration-factory/runs/new", element: createElement(NewPackRunPage), handle: { title: "Start pack run" } },
    { path: "/migration-factory/runs/new-project", element: createElement(NewProjectRunPage), handle: { title: "Load project beginning balances" } },
    { path: "/migration-factory/runs/:id", element: createElement(PackRunDetailPage), handle: { title: "Pack run" } },
    { path: "/migration-factory/runs/:id/projects/:project", element: createElement(ProjectOpeningPage), handle: { title: "Project opening balance" } },
    { path: "/migration-factory/parallel-runs", element: createElement(ParallelRunsPage), handle: { title: "Parallel runs" } },
    { path: "/migration-factory/parallel-runs/new", element: createElement(NewParallelRunPage), handle: { title: "Start parallel run" } },
    { path: "/migration-factory/parallel-runs/:id", element: createElement(ParallelRunDetailPage), handle: { title: "Parallel run" } },
    { path: "/migration-factory/xref", element: createElement(XrefLookupPage), handle: { title: "Legacy reference" } },
    { path: "/migration-factory/cutover", element: createElement(CutoverPlansPage), handle: { title: "Cutover runbooks" } },
    { path: "/migration-factory/cutover/new", element: createElement(NewCutoverPlanPage), handle: { title: "Generate cutover plan" } },
    { path: "/migration-factory/cutover/:id", element: createElement(CutoverPlanDetailPage), handle: { title: "Cutover runbook" } },
  ],
  nav: [
    { label: "Pack runs", to: "/migration-factory", duty: "MigrationView" },
    { label: "Parallel runs", to: "/migration-factory/parallel-runs", duty: "MigrationView" },
    // Any signed-in user may resolve a legacy id (9.49.5).
    { label: "Legacy reference", to: "/migration-factory/xref" },
    { label: "Cutover runbooks", to: "/migration-factory/cutover", duty: "CutoverManager" },
  ],
};

export default migrationFactory;
