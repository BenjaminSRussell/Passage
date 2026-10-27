import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AgingPage } from "./AgingPage";
import { CostingHome } from "./pages";
import { ValuationLedgersPage } from "./ValuationLedgersPage";
import { VersionDetailPage } from "./VersionDetailPage";
import { CreateVersionPage, VersionsPage } from "./VersionsPage";

const costing: PassageModule = {
  id: "costing",
  title: "Inventory costing",
  description:
    "Preview issue costs under each costing method, compare valuation ledgers, age your inventory, and maintain and activate standard cost versions.",
  routes: [
    { path: "/costing", element: createElement(CostingHome), handle: { title: "Settlement preview" } },
    { path: "/costing/valuation-ledgers", element: createElement(ValuationLedgersPage), handle: { title: "Valuation ledgers" } },
    { path: "/costing/aging", element: createElement(AgingPage), handle: { title: "Inventory aging" } },
    { path: "/costing/versions", element: createElement(VersionsPage), handle: { title: "Costing versions" } },
    { path: "/costing/versions/new", element: createElement(CreateVersionPage), handle: { title: "New costing version" } },
    { path: "/costing/versions/:id", element: createElement(VersionDetailPage), handle: { title: "Costing version" } },
  ],
  nav: [
    { label: "Settlement preview", to: "/costing", duty: "costing.read" },
    { label: "Valuation ledgers", to: "/costing/valuation-ledgers", duty: "costing.read" },
    { label: "Inventory aging", to: "/costing/aging", duty: "costing.read" },
    { label: "Costing versions", to: "/costing/versions", duty: "costing.read" },
  ],
};

export default costing;
