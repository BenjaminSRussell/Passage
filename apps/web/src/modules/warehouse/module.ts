import { createElement } from "react";
import type { PassageModule } from "../registry";
import { DirectiveSimulatorPage } from "./pages";
import { DirectivesPage, LocationsPage, NewDirectivePage, NewLocationsPage } from "./SetupPages";
import { NewWorkPage, WorkDetailPage, WorkListPage } from "./WorkPages";

const warehouse: PassageModule = {
  id: "warehouse",
  title: "Warehouse management",
  description: "Locations, location directives and directed work: create, assign, confirm and short pick, with a simulator that explains every rule.",
  routes: [
    { path: "/warehouse", element: createElement(DirectiveSimulatorPage), handle: { title: "Location directive simulator" } },
    { path: "/warehouse/locations", element: createElement(LocationsPage), handle: { title: "Locations" } },
    { path: "/warehouse/locations/new", element: createElement(NewLocationsPage), handle: { title: "Add locations" } },
    { path: "/warehouse/directives", element: createElement(DirectivesPage), handle: { title: "Location directives" } },
    { path: "/warehouse/directives/new", element: createElement(NewDirectivePage), handle: { title: "New location directive" } },
    { path: "/warehouse/work", element: createElement(WorkListPage), handle: { title: "Work" } },
    { path: "/warehouse/work/new", element: createElement(NewWorkPage), handle: { title: "Create work" } },
    { path: "/warehouse/work/:id", element: createElement(WorkDetailPage), handle: { title: "Work details" } },
  ],
  nav: [
    { label: "Directive simulator", to: "/warehouse", duty: "warehouse.setup.view" },
    { label: "Locations", to: "/warehouse/locations", duty: "warehouse.setup.view" },
    { label: "Location directives", to: "/warehouse/directives", duty: "warehouse.setup.view" },
    { label: "Work", to: "/warehouse/work", duty: "warehouse.inquiry.view" },
  ],
};

export default warehouse;
