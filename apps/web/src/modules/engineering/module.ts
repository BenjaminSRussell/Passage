import { createElement } from "react";
import { useNavigate } from "react-router-dom";
import type { PassageModule } from "../registry";
import { EngineeringHome, WhereUsedPage } from "./pages";

function EngineeringRoute() {
  const navigate = useNavigate();
  return createElement(EngineeringHome, { onOpenWhereUsed: () => navigate("/engineering/where-used") });
}

const engineering: PassageModule = {
  id: "engineering",
  title: "Engineering data",
  description: "Bills of material, routings and change orders.",
  routes: [
    { path: "/engineering", element: createElement(EngineeringRoute) },
    { path: "/engineering/where-used", element: createElement(WhereUsedPage) },
  ],
  nav: [
    { label: "Overview", to: "/engineering", duty: "engineering.read" },
    { label: "Where-used", to: "/engineering/where-used", duty: "engineering.read" },
  ],
};

export default engineering;
