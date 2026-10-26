import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CostingHome } from "./pages";

const costing: PassageModule = {
  id: "costing",
  title: "Inventory costing",
  description: "Preview issue costs under each costing method before inventory close.",
  routes: [{ path: "/costing", element: createElement(CostingHome) }],
  nav: [{ label: "Settlement preview", to: "/costing", duty: "costing.read" }],
};

export default costing;
