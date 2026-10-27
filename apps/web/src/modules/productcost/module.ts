import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ActivationPreviewPage } from "./ActivationPreviewPage";
import { ComparePage } from "./ComparePage";
import { ConversionPage } from "./ConversionPage";
import { CostingHome } from "./CostingHome";
import { MixedCostPage } from "./MixedCostPage";
import { PostingsPage } from "./PostingsPage";
import { ReconciliationPage } from "./ReconciliationPage";
import { RollPage } from "./RollPage";
import { SalesEstimatePage } from "./SalesEstimatePage";
import { VariancePage } from "./VariancePage";
import { VersionsPage } from "./VersionsPage";

const productcost: PassageModule = {
  id: "productcost",
  title: "Product costing",
  description:
    "Standard cost roll-up from BOMs, routings and costing sheets with the cost component split, mixed costing, sales order estimates, estimate comparison, production variance decomposition, component postings, the standard cost reconciliation, the revaluation preview of an activation, costing versions and the standard cost conversion checklist.",
  routes: [
    { path: "/productcost", element: createElement(CostingHome), handle: { title: "Costing workspace" } },
    { path: "/productcost/roll", element: createElement(RollPage), handle: { title: "Cost roll-up" } },
    { path: "/productcost/mixed-cost", element: createElement(MixedCostPage), handle: { title: "Mixed cost" } },
    { path: "/productcost/sales-order-estimate", element: createElement(SalesEstimatePage), handle: { title: "Sales order estimate" } },
    { path: "/productcost/compare", element: createElement(ComparePage), handle: { title: "Estimate comparison" } },
    { path: "/productcost/variances", element: createElement(VariancePage), handle: { title: "Production variances" } },
    { path: "/productcost/postings", element: createElement(PostingsPage), handle: { title: "Component postings" } },
    { path: "/productcost/reconciliation", element: createElement(ReconciliationPage), handle: { title: "Standard cost reconciliation" } },
    { path: "/productcost/activation-preview", element: createElement(ActivationPreviewPage), handle: { title: "Activation preview" } },
    { path: "/productcost/versions", element: createElement(VersionsPage), handle: { title: "Costing versions" } },
    { path: "/productcost/conversion", element: createElement(ConversionPage), handle: { title: "Standard cost conversion" } },
  ],
  nav: [
    { label: "Costing workspace", to: "/productcost", duty: "CostingView" },
    { label: "Cost roll-up", to: "/productcost/roll", duty: "CostingCalculate" },
    { label: "Mixed cost", to: "/productcost/mixed-cost", duty: "CostingCalculate" },
    { label: "Sales order estimate", to: "/productcost/sales-order-estimate", duty: "CostingCalculate" },
    { label: "Estimate comparison", to: "/productcost/compare", duty: "CostingView" },
    { label: "Production variances", to: "/productcost/variances", duty: "CostingSystem" },
    { label: "Component postings", to: "/productcost/postings", duty: "CostingCalculate" },
    { label: "Standard cost reconciliation", to: "/productcost/reconciliation", duty: "CostingView" },
    { label: "Activation preview", to: "/productcost/activation-preview", duty: "CostingActivate" },
    { label: "Costing versions", to: "/productcost/versions", duty: "CostingMaintain" },
    { label: "Standard cost conversion", to: "/productcost/conversion", duty: "CostingActivate" },
  ],
};

export default productcost;
