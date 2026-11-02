import { createElement } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import type { PassageModule } from "../registry";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { EngineeringHome, WhereUsedPage } from "./pages";
import { CalendarDetailPage, CalendarsPage, CapabilityDetailPage, ResourceGroupDetailPage } from "./detail";
import { CalendarDaysPage, CapabilitiesPage, ResourceDetailPage, ResourceGroupsPage, ResourcesPage } from "./resources";
import { BomVersionCreatePage, BomVersionPage, BomVersionsPage } from "./bom";
import { BomVersionEditPage } from "./bomEdit";
import { RouteVersionPage, RouteVersionsPage } from "./screens";
import { EcoCreatePage, EcoPage, EcosPage } from "./eco";
import { RouteVersionCreatePage } from "./routeCreate";
import { RouteVersionEditPage } from "./routeEdit";
import { CalendarCreatePage, CapabilityCreatePage, ResourceCreatePage, ResourceGroupCreatePage } from "./setup";

/** Every resource, group, capability and calendar write, including Generate days, is under this seeded duty. */
const dutyResourceWrite = "engineering.resource.write";

/** UX only: hides the New buttons from principals without the write duty; the service still enforces it. */
function useCreateAction(to: string): (() => void) | undefined {
  const navigate = useNavigate();
  return holdsDuty(usePrincipalDuties(), dutyResourceWrite) ? () => navigate(to) : undefined;
}

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
    onOpenCopy: (copyId: string) => navigate(`/engineering/boms/${encodeURIComponent(copyId)}`),
    onEdit: (versionId: string) => navigate(`/engineering/boms/${encodeURIComponent(versionId)}/edit`),
  });
}

function BomVersionEditRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  const back = () => navigate(`/engineering/boms/${encodeURIComponent(id)}`);
  return createElement(BomVersionEditPage, { id, onSaved: back, onCancel: back });
}

function BomVersionCreateRoute() {
  const navigate = useNavigate();
  return createElement(BomVersionCreatePage, {
    onCreated: (id: string) => navigate(`/engineering/boms/${encodeURIComponent(id)}`),
    onCancel: () => navigate("/engineering/boms"),
  });
}

function EcosRoute() {
  const navigate = useNavigate();
  return createElement(EcosPage, {
    onOpen: (id: string) => navigate(`/engineering/ecos/${encodeURIComponent(id)}`),
    onCreate: () => navigate("/engineering/ecos/new"),
  });
}

function EcoRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  return createElement(EcoPage, {
    id,
    onBack: () => navigate("/engineering/ecos"),
    onOpenBom: (versionId: string) => navigate(`/engineering/boms/${encodeURIComponent(versionId)}`),
  });
}

function EcoCreateRoute() {
  const navigate = useNavigate();
  return createElement(EcoCreatePage, {
    onCreated: (id: string) => navigate(`/engineering/ecos/${encodeURIComponent(id)}`),
    onCancel: () => navigate("/engineering/ecos"),
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

function RouteVersionEditRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  const back = () => navigate(`/engineering/routes/${encodeURIComponent(id)}`);
  return createElement(RouteVersionEditPage, { id, onSaved: back, onCancel: back });
}

function RouteVersionRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  return createElement(RouteVersionPage, {
    id,
    onBack: () => navigate("/engineering/routes"),
    onOpenCopy: (copyId: string) => navigate(`/engineering/routes/${encodeURIComponent(copyId)}`),
    onEdit: (versionId: string) => navigate(`/engineering/routes/${encodeURIComponent(versionId)}/edit`),
  });
}

function ResourcesRoute() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const onCreate = useCreateAction("/engineering/resources/new");
  const initial = Object.fromEntries(["siteId", "type", "groupId", "capabilityId"].flatMap((key) => (params.get(key) ? [[key, params.get(key)]] : [])));
  return createElement(ResourcesPage, {
    key: params.toString(),
    initial,
    onOpen: (id: string) => navigate(`/engineering/resources/${encodeURIComponent(id)}`),
    onCreate,
  });
}

function ResourceRoute() {
  const navigate = useNavigate();
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), dutyResourceWrite);
  return createElement(ResourceDetailPage, { id, onBack: () => navigate("/engineering/resources"), canWrite });
}

function ResourceGroupsRoute() {
  return createElement(ResourceGroupsPage, { onCreate: useCreateAction("/engineering/resource-groups/new") });
}

function CapabilitiesRoute() {
  return createElement(CapabilitiesPage, { onCreate: useCreateAction("/engineering/capabilities/new") });
}

function CalendarsRoute() {
  return createElement(CalendarsPage, { onCreate: useCreateAction("/engineering/calendars/new") });
}

function createRoute(Page: typeof ResourceCreatePage, list: string, detail: string) {
  return function CreateRoute() {
    const navigate = useNavigate();
    return createElement(Page, {
      onCreated: (id: string) => navigate(`/engineering/${detail}/${encodeURIComponent(id)}`),
      onCancel: () => navigate(`/engineering/${list}`),
    });
  };
}

const ResourceCreateRoute = createRoute(ResourceCreatePage, "resources", "resources");
const ResourceGroupCreateRoute = createRoute(ResourceGroupCreatePage, "resource-groups", "resource-groups");
const CapabilityCreateRoute = createRoute(CapabilityCreatePage, "capabilities", "capabilities");
const CalendarCreateRoute = createRoute(CalendarCreatePage, "calendars", "calendars");

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
  const canGenerate = holdsDuty(usePrincipalDuties(), dutyResourceWrite);
  return createElement(CalendarDetailPage, { id, onBack: () => navigate("/engineering/calendars"), canGenerate });
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
    { path: "/engineering/boms/:id/edit", element: createElement(BomVersionEditRoute), handle: { title: "Edit BOM draft" } },
    { path: "/engineering/boms/:id", element: createElement(BomVersionRoute), handle: { title: "BOM version" } },
    { path: "/engineering/ecos", element: createElement(EcosRoute), handle: { title: "Change orders" } },
    { path: "/engineering/ecos/new", element: createElement(EcoCreateRoute), handle: { title: "New change order" } },
    { path: "/engineering/ecos/:id", element: createElement(EcoRoute), handle: { title: "Change order" } },
    { path: "/engineering/routes", element: createElement(RouteVersionsRoute) },
    { path: "/engineering/routes/new", element: createElement(RouteVersionCreateRoute), handle: { title: "New route version" } },
    { path: "/engineering/routes/:id/edit", element: createElement(RouteVersionEditRoute), handle: { title: "Edit route draft" } },
    { path: "/engineering/routes/:id", element: createElement(RouteVersionRoute), handle: { title: "Route version" } },
    { path: "/engineering/resources", element: createElement(ResourcesRoute), handle: { title: "Resources" } },
    { path: "/engineering/resources/new", element: createElement(ResourceCreateRoute), handle: { title: "New resource", duty: dutyResourceWrite } },
    { path: "/engineering/resources/:id", element: createElement(ResourceRoute), handle: { title: "Resource" } },
    { path: "/engineering/resource-groups", element: createElement(ResourceGroupsRoute), handle: { title: "Resource groups" } },
    { path: "/engineering/capabilities", element: createElement(CapabilitiesRoute), handle: { title: "Capabilities" } },
    { path: "/engineering/resource-groups/new", element: createElement(ResourceGroupCreateRoute), handle: { title: "New resource group", duty: dutyResourceWrite } },
    { path: "/engineering/resource-groups/:id", element: createElement(GroupRoute), handle: { title: "Resource group" } },
    { path: "/engineering/capabilities/new", element: createElement(CapabilityCreateRoute), handle: { title: "New capability", duty: dutyResourceWrite } },
    { path: "/engineering/capabilities/:id", element: createElement(CapabilityRoute), handle: { title: "Capability" } },
    { path: "/engineering/calendars", element: createElement(CalendarsRoute), handle: { title: "Work calendars" } },
    { path: "/engineering/calendars/new", element: createElement(CalendarCreateRoute), handle: { title: "New calendar", duty: dutyResourceWrite } },
    { path: "/engineering/calendars/:id", element: createElement(CalendarDetailRoute), handle: { title: "Calendar" } },
    { path: "/engineering/calendars/:id/days", element: createElement(CalendarRoute), handle: { title: "Calendar days" } },
  ],
  nav: [
    { label: "Overview", to: "/engineering", duty: "engineering.read" },
    { label: "Bills of material", to: "/engineering/boms", duty: "engineering.read" },
    { label: "Change orders", to: "/engineering/ecos", duty: "engineering.read" },
    { label: "Routings", to: "/engineering/routes", duty: "engineering.read" },
    { label: "Resources", to: "/engineering/resources", duty: "engineering.read" },
    { label: "Resource groups", to: "/engineering/resource-groups", duty: "engineering.read" },
    { label: "Capabilities", to: "/engineering/capabilities", duty: "engineering.read" },
    { label: "Work calendars", to: "/engineering/calendars", duty: "engineering.read" },
    { label: "Where-used", to: "/engineering/where-used", duty: "engineering.read" },
  ],
};

export default engineering;
