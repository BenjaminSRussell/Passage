import { createElement } from "react";
import type { PassageModule } from "../registry";
import { EngineeringHome, WhereUsedPage } from "./pages";

const engineering: PassageModule = {
  id: "engineering",
  title: "Engineering data",
  description: "Bills of material, routings and change orders.",
  routes: [
    { path: "/engineering", element: createElement(EngineeringHome) },
    { path: "/engineering/where-used", element: createElement(WhereUsedPage) },
  ],
  nav: [
    { label: "Overview", to: "/engineering", duty: "engineering.read" },
    { label: "Where-used", to: "/engineering/where-used", duty: "engineering.read" },
  ],
};

export default engineering;
