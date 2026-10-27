import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AgingPage } from "./AgingPage";
import { CostingHome } from "./pages";
import { ValuationLedgersPage } from "./ValuationLedgersPage";

const costing: PassageModule = {
  id: "costing",
  title: "Inventory costing",
  description: "Preview issue costs under each costing method, compare valuation ledgers and age your inventory before close.",
  routes: [
    { path: "/costing", element: createElement(CostingHome), handle: { title: "Settlement preview" } },
    { path: "/costing/valuation-ledgers", element: createElement(ValuationLedgersPage), handle: { title: "Valuation ledgers" } },
    { path: "/costing/aging", element: createElement(AgingPage), handle: { title: "Inventory aging" } },
  ],
  nav: [
    { label: "Settlement preview", to: "/costing", duty: "costing.read" },
    { label: "Valuation ledgers", to: "/costing/valuation-ledgers", duty: "costing.read" },
    { label: "Inventory aging", to: "/costing/aging", duty: "costing.read" },
  ],
};

export default costing;
