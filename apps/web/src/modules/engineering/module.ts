import { createElement } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import type { PassageModule } from "../registry";
import { EngineeringHome, WhereUsedPage } from "./pages";
import { CalendarDetailPage, CalendarsPage, CapabilityDetailPage, ResourceGroupDetailPage } from "./detail";
import { CalendarDaysPage, CapabilitiesPage, ResourceDetailPage, ResourceGroupsPage, ResourcesPage } from "./resources";
import { BomVersionCreatePage, BomVersionPage, BomVersionsPage } from "./bom";
import { RouteVersionPage, RouteVersionsPage } from "./screens";
import { RouteVersionCreatePage } from "./routeCreate";

function EngineeringRoute() {
  const navigate = useNavigate();
  return createElement(EngineeringHome, {
    onOpenBoms: () => navigate("/engineering/boms"),
    onOpenWhereUsed: () => navigate("/engineering/where-used"),
    onOpenRoutes: () => navigate("/engineering/routes"),
    onOpenResources: () => navigate("/engineering/resources"),
    onOpenResourceGroups: () => navigate("/engineering/resource-groups"),
    onOpenCapabilities: () => navigate("/engineering/capabilities"),
    onOpenCalendars: () => navigate("/engineering/calendars"),
  });
}

function BomVersionsRoute() {
  const navigate = useNavigate();
  return createElement(BomVersionsPage, {
    onOpen: (id: string) => navigate(`/engineering/boms/${encodeURIComponent(id)}`),
    onCreate: () => navigate("/engineering/boms/new"),
  });
}

function BomVersionRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  return createElement(BomVersionPage, {
    id,
    onBack: () => navigate("/engineering/boms"),
    onWhereUsed: (component: string) => navigate(`/engineering/where-used?component=${encodeURIComponent(component)}`),
  });
}

function BomVersionCreateRoute() {
  const navigate = useNavigate();
  return createElement(BomVersionCreatePage, {
    onCreated: (id: string) => navigate(`/engineering/boms/${encodeURIComponent(id)}`),
    onCancel: () => navigate("/engineering/boms"),
  });
}

function WhereUsedRoute() {
  const [params] = useSearchParams();
  const component = params.get("component") ?? "";
  return createElement(WhereUsedPage, { key: component, initialComponent: component });
}

function RouteVersionsRoute() {
  const navigate = useNavigate();
  return createElement(RouteVersionsPage, {
    onOpen: (id: string) => navigate(`/engineering/routes/${encodeURIComponent(id)}`),
    onCreate: () => navigate("/engineering/routes/new"),
  });
}

function RouteVersionCreateRoute() {
  const navigate = useNavigate();
  return createElement(RouteVersionCreatePage, {
    onCreated: (id: string) => navigate(`/engineering/routes/${encodeURIComponent(id)}`),
    onCancel: () => navigate("/engineering/routes"),
  });
}

function RouteVersionRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  return createElement(RouteVersionPage, { id, onBack: () => navigate("/engineering/routes") });
}

function ResourcesRoute() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const initial = Object.fromEntries(["siteId", "type", "groupId", "capabilityId"].flatMap((key) => (params.get(key) ? [[key, params.get(key)]] : [])));
  return createElement(ResourcesPage, {
    key: params.toString(),
    initial,
    onOpen: (id: string) => navigate(`/engineering/resources/${encodeURIComponent(id)}`),
  });
}

function ResourceRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  return createElement(ResourceDetailPage, { id, onBack: () => navigate("/engineering/resources") });
}

function CalendarRoute() {
  const { id = "" } = useParams();
  return createElement(CalendarDaysPage, { id });
}

function GroupRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  return createElement(ResourceGroupDetailPage, { id, onBack: () => navigate("/engineering/resource-groups") });
}

function CapabilityRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  return createElement(CapabilityDetailPage, { id, onBack: () => navigate("/engineering/capabilities") });
}

function CalendarDetailRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  return createElement(CalendarDetailPage, { id, onBack: () => navigate("/engineering/calendars") });
}

const engineering: PassageModule = {
  id: "engineering",
  title: "Engineering data",
  description: "Bills of material, routings and change orders.",
  routes: [
    { path: "/engineering", element: createElement(EngineeringRoute) },
    { path: "/engineering/where-used", element: createElement(WhereUsedRoute) },
    { path: "/engineering/boms", element: createElement(BomVersionsRoute), handle: { title: "Bills of material" } },
    { path: "/engineering/boms/new", element: createElement(BomVersionCreateRoute), handle: { title: "New BOM version" } },
    { path: "/engineering/boms/:id", element: createElement(BomVersionRoute), handle: { title: "BOM version" } },
    { path: "/engineering/routes", element: createElement(RouteVersionsRoute) },
    { path: "/engineering/routes/new", element: createElement(RouteVersionCreateRoute), handle: { title: "New route version" } },
    { path: "/engineering/routes/:id", element: createElement(RouteVersionRoute), handle: { title: "Route version" } },
    { path: "/engineering/resources", element: createElement(ResourcesRoute), handle: { title: "Resources" } },
    { path: "/engineering/resources/:id", element: createElement(ResourceRoute), handle: { title: "Resource" } },
    { path: "/engineering/resource-groups", element: createElement(ResourceGroupsPage), handle: { title: "Resource groups" } },
    { path: "/engineering/capabilities", element: createElement(CapabilitiesPage), handle: { title: "Capabilities" } },
    { path: "/engineering/resource-groups/:id", element: createElement(GroupRoute), handle: { title: "Resource group" } },
    { path: "/engineering/capabilities/:id", element: createElement(CapabilityRoute), handle: { title: "Capability" } },
    { path: "/engineering/calendars", element: createElement(CalendarsPage), handle: { title: "Work calendars" } },
    { path: "/engineering/calendars/:id", element: createElement(CalendarDetailRoute), handle: { title: "Calendar" } },
    { path: "/engineering/calendars/:id/days", element: createElement(CalendarRoute), handle: { title: "Calendar days" } },
  ],
  nav: [
    { label: "Overview", to: "/engineering", duty: "engineering.read" },
    { label: "Bills of material", to: "/engineering/boms", duty: "engineering.read" },
    { label: "Routings", to: "/engineering/routes", duty: "engineering.read" },
    { label: "Resources", to: "/engineering/resources", duty: "engineering.read" },
    { label: "Resource groups", to: "/engineering/resource-groups", duty: "engineering.read" },
    { label: "Capabilities", to: "/engineering/capabilities", duty: "engineering.read" },
    { label: "Work calendars", to: "/engineering/calendars", duty: "engineering.read" },
    { label: "Where-used", to: "/engineering/where-used", duty: "engineering.read" },
  ],
};

export default engineering;
