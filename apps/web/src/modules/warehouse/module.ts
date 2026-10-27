import { createElement } from "react";
import type { PassageModule } from "../registry";
import { DockSchedulePage, NewAppointmentPage } from "./DockPages";
import { DirectiveSimulatorPage } from "./pages";
import { NewReceiptPage, ReceiptDetailPage, ReceiptsPage } from "./ReceivingPages";
import { DirectivesPage, LocationsPage, NewDirectivePage, NewLocationsPage } from "./SetupPages";
import { NewWorkPage, WorkDetailPage, WorkListPage } from "./WorkPages";

const warehouse: PassageModule = {
  id: "warehouse",
  title: "Warehouse management",
  description:
    "Locations, location directives and directed work: create, assign, confirm and short pick, with a simulator that explains every rule; ASN receiving and the dock schedule.",
  routes: [
    { path: "/warehouse", element: createElement(DirectiveSimulatorPage), handle: { title: "Location directive simulator" } },
    { path: "/warehouse/locations", element: createElement(LocationsPage), handle: { title: "Locations" } },
    { path: "/warehouse/locations/new", element: createElement(NewLocationsPage), handle: { title: "Add locations" } },
    { path: "/warehouse/directives", element: createElement(DirectivesPage), handle: { title: "Location directives" } },
    { path: "/warehouse/directives/new", element: createElement(NewDirectivePage), handle: { title: "New location directive" } },
    { path: "/warehouse/work", element: createElement(WorkListPage), handle: { title: "Work" } },
    { path: "/warehouse/work/new", element: createElement(NewWorkPage), handle: { title: "Create work" } },
    { path: "/warehouse/work/:id", element: createElement(WorkDetailPage), handle: { title: "Work details" } },
    { path: "/warehouse/receipts", element: createElement(ReceiptsPage), handle: { title: "Receiving" } },
    { path: "/warehouse/receipts/new", element: createElement(NewReceiptPage), handle: { title: "Register ASN" } },
    { path: "/warehouse/receipts/:id", element: createElement(ReceiptDetailPage), handle: { title: "Receipt" } },
    { path: "/warehouse/docks", element: createElement(DockSchedulePage), handle: { title: "Dock schedule" } },
    { path: "/warehouse/docks/new", element: createElement(NewAppointmentPage), handle: { title: "Book a dock appointment" } },
  ],
  nav: [
    { label: "Directive simulator", to: "/warehouse", duty: "warehouse.setup.view" },
    { label: "Locations", to: "/warehouse/locations", duty: "warehouse.setup.view" },
    { label: "Location directives", to: "/warehouse/directives", duty: "warehouse.setup.view" },
    { label: "Work", to: "/warehouse/work", duty: "warehouse.inquiry.view" },
    { label: "Receiving", to: "/warehouse/receipts", duty: "warehouse.inquiry.view" },
    { label: "Dock schedule", to: "/warehouse/docks", duty: "warehouse.inquiry.view" },
  ],
};

export default warehouse;
