import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ActivationPreviewPage } from "./ActivationPreviewPage";
import { CostingHome } from "./CostingHome";
import { RollPage } from "./RollPage";
import { VariancePage } from "./VariancePage";

const productcost: PassageModule = {
  id: "productcost",
  title: "Product costing",
  description:
    "Standard cost roll-up from BOMs, routings and costing sheets with the cost component split, production variance decomposition by type and component, and the revaluation preview of a standard cost activation.",
  routes: [
    { path: "/productcost", element: createElement(CostingHome), handle: { title: "Costing workspace" } },
    { path: "/productcost/roll", element: createElement(RollPage), handle: { title: "Cost roll-up" } },
    { path: "/productcost/variances", element: createElement(VariancePage), handle: { title: "Production variances" } },
    { path: "/productcost/activation-preview", element: createElement(ActivationPreviewPage), handle: { title: "Activation preview" } },
  ],
  nav: [
    { label: "Costing workspace", to: "/productcost", duty: "CostingView" },
    { label: "Cost roll-up", to: "/productcost/roll", duty: "CostingCalculate" },
    { label: "Production variances", to: "/productcost/variances", duty: "CostingSystem" },
    { label: "Activation preview", to: "/productcost/activation-preview", duty: "CostingActivate" },
  ],
};

export default productcost;
