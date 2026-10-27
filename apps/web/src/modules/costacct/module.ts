import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AllocationRunDetailPage, AllocationRunsPage, NewAllocationRunPage } from "./AllocationRunsPage";
import { CostElementsPage, CreateCostElementPage } from "./CostElementsPage";
import { CostObjectDetailPage, CostObjectsPage, CreateCostObjectPage } from "./CostObjectsPage";
import { ReconciliationPage } from "./ReconciliationPage";
import { NewTopDownRunPage, TopDownRunDetailPage, TopDownRunsPage } from "./TopDownRunsPage";

const costacct: PassageModule = {
  id: "costacct",
  title: "Cost accounting",
  description: "Cost objects and elements, mirrored journal cost, reciprocal allocation runs, top-down distribution, internal order settlement and the cost ledger reconciliation.",
  routes: [
    { path: "/costacct", element: createElement(CostObjectsPage), handle: { title: "Cost objects" } },
    { path: "/costacct/cost-objects/new", element: createElement(CreateCostObjectPage), handle: { title: "New cost object" } },
    { path: "/costacct/cost-objects/:id", element: createElement(CostObjectDetailPage), handle: { title: "Cost object" } },
    { path: "/costacct/cost-elements", element: createElement(CostElementsPage), handle: { title: "Cost elements" } },
    { path: "/costacct/cost-elements/new", element: createElement(CreateCostElementPage), handle: { title: "New cost element" } },
    { path: "/costacct/allocation-runs", element: createElement(AllocationRunsPage), handle: { title: "Allocation runs" } },
    { path: "/costacct/allocation-runs/new", element: createElement(NewAllocationRunPage), handle: { title: "New allocation run" } },
    { path: "/costacct/allocation-runs/:id", element: createElement(AllocationRunDetailPage), handle: { title: "Allocation run" } },
    { path: "/costacct/top-down-runs", element: createElement(TopDownRunsPage), handle: { title: "Top-down distribution" } },
    { path: "/costacct/top-down-runs/new", element: createElement(NewTopDownRunPage), handle: { title: "New top-down run" } },
    { path: "/costacct/top-down-runs/:id", element: createElement(TopDownRunDetailPage), handle: { title: "Top-down run" } },
    { path: "/costacct/reconciliation", element: createElement(ReconciliationPage), handle: { title: "Cost ledger reconciliation" } },
  ],
  nav: [
    { label: "Cost objects", to: "/costacct", duty: "controlling.view" },
    { label: "Cost elements", to: "/costacct/cost-elements", duty: "controlling.view" },
    { label: "Allocation runs", to: "/costacct/allocation-runs", duty: "controlling.view" },
    { label: "Top-down distribution", to: "/costacct/top-down-runs", duty: "controlling.view" },
    { label: "Reconciliation", to: "/costacct/reconciliation", duty: "controlling.view" },
  ],
};

export default costacct;
