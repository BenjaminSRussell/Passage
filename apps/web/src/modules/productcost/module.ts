import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ActivationPreviewPage } from "./ActivationPreviewPage";
import { AdHocEstimatePage } from "./AdHocEstimatePage";
import { ComparePage } from "./ComparePage";
import { ConversionPage } from "./ConversionPage";
import { CostComponentsPage, CostGroupsPage } from "./CostStructurePages";
import { CostingHome } from "./CostingHome";
import { MixedCostPage } from "./MixedCostPage";
import { PostingsPage } from "./PostingsPage";
import { ReconciliationPage } from "./ReconciliationPage";
import { RollPage } from "./RollPage";
import { SalesEstimatePage } from "./SalesEstimatePage";
import { SalesVersionsPage } from "./SalesVersionsPage";
import { VariancePage } from "./VariancePage";
import { VersionsPage } from "./VersionsPage";

const productcost: PassageModule = {
  id: "productcost",
  title: "Product costing",
  description:
    "Standard cost roll-up from BOMs, routings and costing sheets with the cost component split, mixed costing, sales order estimates with their versions and freeze at confirmation, ad-hoc estimates from costing models, estimate comparison, production variance decomposition, component postings, the standard cost reconciliation, the revaluation preview of an activation, the stored cost components, cost groups and costing versions, and the standard cost conversion checklist.",
  routes: [
    { path: "/productcost", element: createElement(CostingHome), handle: { title: "Costing workspace" } },
    { path: "/productcost/roll", element: createElement(RollPage), handle: { title: "Cost roll-up" } },
    { path: "/productcost/mixed-cost", element: createElement(MixedCostPage), handle: { title: "Mixed cost" } },
    { path: "/productcost/sales-order-estimate", element: createElement(SalesEstimatePage), handle: { title: "Sales order estimate" } },
    {
      path: "/productcost/sales-order-versions",
      element: createElement(SalesVersionsPage),
      handle: { title: "Sales order estimate versions" },
    },
    { path: "/productcost/estimates", element: createElement(AdHocEstimatePage), handle: { title: "Ad-hoc estimate" } },
    { path: "/productcost/compare", element: createElement(ComparePage), handle: { title: "Estimate comparison" } },
    { path: "/productcost/variances", element: createElement(VariancePage), handle: { title: "Production variances" } },
    { path: "/productcost/postings", element: createElement(PostingsPage), handle: { title: "Component postings" } },
    { path: "/productcost/reconciliation", element: createElement(ReconciliationPage), handle: { title: "Standard cost reconciliation" } },
    { path: "/productcost/activation-preview", element: createElement(ActivationPreviewPage), handle: { title: "Activation preview" } },
    { path: "/productcost/cost-components", element: createElement(CostComponentsPage), handle: { title: "Cost components" } },
    { path: "/productcost/cost-groups", element: createElement(CostGroupsPage), handle: { title: "Cost groups" } },
    { path: "/productcost/versions", element: createElement(VersionsPage), handle: { title: "Costing versions" } },
    { path: "/productcost/conversion", element: createElement(ConversionPage), handle: { title: "Standard cost conversion" } },
  ],
  nav: [
    { label: "Costing workspace", to: "/productcost", duty: "productcost.view" },
    { label: "Cost roll-up", to: "/productcost/roll", duty: "productcost.calculate" },
    { label: "Mixed cost", to: "/productcost/mixed-cost", duty: "productcost.calculate" },
    { label: "Sales order estimate", to: "/productcost/sales-order-estimate", duty: "productcost.calculate" },
    { label: "Sales order estimate versions", to: "/productcost/sales-order-versions", duty: "productcost.calculate" },
    { label: "Ad-hoc estimate", to: "/productcost/estimates", duty: "productcost.calculate" },
    { label: "Estimate comparison", to: "/productcost/compare", duty: "productcost.view" },
    { label: "Production variances", to: "/productcost/variances", duty: "productcost.variance.compute" },
    { label: "Component postings", to: "/productcost/postings", duty: "productcost.calculate" },
    { label: "Standard cost reconciliation", to: "/productcost/reconciliation", duty: "productcost.view" },
    { label: "Activation preview", to: "/productcost/activation-preview", duty: "productcost.activate" },
    { label: "Cost components", to: "/productcost/cost-components", duty: "productcost.view" },
    { label: "Cost groups", to: "/productcost/cost-groups", duty: "productcost.view" },
    { label: "Costing versions", to: "/productcost/versions", duty: "productcost.view" },
    { label: "Standard cost conversion", to: "/productcost/conversion", duty: "productcost.activate" },
  ],
};

export default productcost;
