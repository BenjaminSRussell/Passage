import { createElement } from "react";
import { useNavigate, useParams } from "react-router-dom";
import type { PassageModule } from "../registry";
import { EngineeringHome, WhereUsedPage } from "./pages";
import { RouteVersionPage, RouteVersionsPage } from "./screens";

function EngineeringRoute() {
  const navigate = useNavigate();
  return createElement(EngineeringHome, {
    onOpenWhereUsed: () => navigate("/engineering/where-used"),
    onOpenRoutes: () => navigate("/engineering/routes"),
  });
}

function RouteVersionsRoute() {
  const navigate = useNavigate();
  return createElement(RouteVersionsPage, { onOpen: (id: string) => navigate(`/engineering/routes/${encodeURIComponent(id)}`) });
}

function RouteVersionRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  return createElement(RouteVersionPage, { id, onBack: () => navigate("/engineering/routes") });
}

const engineering: PassageModule = {
  id: "engineering",
  title: "Engineering data",
  description: "Bills of material, routings and change orders.",
  routes: [
    { path: "/engineering", element: createElement(EngineeringRoute) },
    { path: "/engineering/where-used", element: createElement(WhereUsedPage) },
    { path: "/engineering/routes", element: createElement(RouteVersionsRoute) },
    { path: "/engineering/routes/:id", element: createElement(RouteVersionRoute), handle: { title: "Route version" } },
  ],
  nav: [
    { label: "Overview", to: "/engineering", duty: "engineering.read" },
    { label: "Routings", to: "/engineering/routes", duty: "engineering.read" },
    { label: "Where-used", to: "/engineering/where-used", duty: "engineering.read" },
  ],
};

export default engineering;
