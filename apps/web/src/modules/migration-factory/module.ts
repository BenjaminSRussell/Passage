import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CutoverDecisionPage } from "./CutoverDecisionPage";
import { CutoverPlanDetailPage } from "./CutoverPlanDetailPage";
import { CutoverPlansPage } from "./CutoverPlansPage";
import { NewCutoverPlanPage } from "./NewCutoverPlanPage";
import { NewGLRunPage } from "./NewGLRunPage";
import { NewBankRunPage } from "./NewBankRunPage";
import { NewOnhandRunPage } from "./NewOnhandRunPage";
import { NewPackRunPage } from "./NewPackRunPage";
import { NewParallelRunPage } from "./NewParallelRunPage";
import { NewProjectRunPage } from "./NewProjectRunPage";
import { PackCatalogPage } from "./PackCatalogPage";
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
    "Move a customer off AX or SAP: the pack catalog, pack runs with control totals and quarantine release, GL opening balances reconciled with open items, on-hand stock reconciled with the inventory account, bank account balances, project beginning balances, parallel-run review, legacy references and the timed cutover runbook with its rollback plan.",
  routes: [
    { path: "/migration-factory", element: createElement(PackRunsPage), handle: { title: "Pack runs" } },
    { path: "/migration-factory/packs", element: createElement(PackCatalogPage), handle: { title: "Transactional packs" } },
    { path: "/migration-factory/runs/new", element: createElement(NewPackRunPage), handle: { title: "Start pack run" } },
    { path: "/migration-factory/runs/new-gl", element: createElement(NewGLRunPage), handle: { title: "Load GL balances" } },
    { path: "/migration-factory/runs/new-onhand", element: createElement(NewOnhandRunPage), handle: { title: "Load on-hand stock" } },
    { path: "/migration-factory/runs/new-bank", element: createElement(NewBankRunPage), handle: { title: "Load bank balances" } },
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
    { path: "/migration-factory/cutover/:id/go-no-go", element: createElement(CutoverDecisionPage), handle: { title: "Go/no-go decision" } },
  ],
  nav: [
    { label: "Pack runs", to: "/migration-factory", duty: "MigrationView" },
    { label: "Pack catalog", to: "/migration-factory/packs", duty: "MigrationView" },
    { label: "Parallel runs", to: "/migration-factory/parallel-runs", duty: "MigrationView" },
    // Any signed-in user may resolve a legacy id (9.49.5).
    { label: "Legacy reference", to: "/migration-factory/xref" },
    { label: "Cutover runbooks", to: "/migration-factory/cutover", duty: "CutoverManager" },
  ],
};

export default migrationFactory;
