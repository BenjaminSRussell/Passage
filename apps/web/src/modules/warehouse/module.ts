import { createElement } from "react";
import type { PassageModule } from "../registry";
import { DirectiveSimulatorPage } from "./pages";

const warehouse: PassageModule = {
  id: "warehouse",
  title: "Warehouse management",
  description: "Location directives for put-away and picking, with a simulator that explains every rule it applies.",
  routes: [{ path: "/warehouse", element: createElement(DirectiveSimulatorPage), handle: { title: "Location directive simulator" } }],
  nav: [{ label: "Directive simulator", to: "/warehouse", duty: "warehouse.setup.view" }],
};

export default warehouse;
