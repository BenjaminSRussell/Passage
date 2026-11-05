import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AssetDetailPage } from "./AssetDetailPage";
import { AssetsPage, NewAssetPage } from "./AssetsPage";
import { FaultCatalogPage } from "./FaultCatalogPage";
import { LocationDetailPage, LocationsPage, NewLocationPage } from "./LocationPages";
import { NewPermitPage, PermitDetailPage, PermitsPage } from "./PermitPages";
import { KpiPage } from "./KpiPages";
import { PermitTypesPage } from "./PermitTypesPage";
import { NewPlanPage, PlansPage } from "./PlansPage";
import { NewRequestPage, RequestsPage } from "./RequestsPage";
import { NewRoundPage, RoundDetailPage, RoundsPage } from "./RoundPages";
import { NewSequencePage, SequenceDetailPage, SequencesPage } from "./SequencePages";
import { WorkOrderDetailPage } from "./WorkOrderDetailPage";
import { NewWorkOrderPage, WorkOrdersPage } from "./WorkOrdersPage";
import { WorkspacePage } from "./WorkspacePage";

const eam: PassageModule = {
  id: "eam",
  title: "Asset management",
  description: "Maintain plant assets with preventive plans, condition triggers, work orders, faults, warranties, permits to work with lockout/tagout, functional locations, maintenance sequences and rounds, asset costs, backlog and maintenance reminders.",
  routes: [
    { path: "/eam", element: createElement(WorkspacePage), handle: { title: "Asset management" } },
    { path: "/eam/assets", element: createElement(AssetsPage), handle: { title: "Assets" } },
    { path: "/eam/assets/new", element: createElement(NewAssetPage), handle: { title: "New asset" } },
    { path: "/eam/assets/:id", element: createElement(AssetDetailPage), handle: { title: "Asset" } },
    { path: "/eam/plans", element: createElement(PlansPage), handle: { title: "Maintenance plans" } },
    { path: "/eam/plans/new", element: createElement(NewPlanPage), handle: { title: "New maintenance plan" } },
    { path: "/eam/requests", element: createElement(RequestsPage), handle: { title: "Maintenance requests" } },
    { path: "/eam/requests/new", element: createElement(NewRequestPage), handle: { title: "New maintenance request" } },
    { path: "/eam/work-orders", element: createElement(WorkOrdersPage), handle: { title: "Work orders" } },
    { path: "/eam/work-orders/new", element: createElement(NewWorkOrderPage), handle: { title: "New work order" } },
    { path: "/eam/work-orders/:id", element: createElement(WorkOrderDetailPage), handle: { title: "Work order" } },
    { path: "/eam/fault-catalogs", element: createElement(FaultCatalogPage), handle: { title: "Fault catalog designer" } },
    { path: "/eam/permits", element: createElement(PermitsPage), handle: { title: "Permits to work" } },
    { path: "/eam/permits/new", element: createElement(NewPermitPage), handle: { title: "Request a permit" } },
    { path: "/eam/permits/:id", element: createElement(PermitDetailPage), handle: { title: "Permit" } },
    { path: "/eam/permit-types", element: createElement(PermitTypesPage), handle: { title: "Permit types" } },
    { path: "/eam/locations", element: createElement(LocationsPage), handle: { title: "Plant structure" } },
    { path: "/eam/locations/new", element: createElement(NewLocationPage), handle: { title: "New location" } },
    { path: "/eam/locations/:id", element: createElement(LocationDetailPage), handle: { title: "Functional location" } },
    { path: "/eam/sequences", element: createElement(SequencesPage), handle: { title: "Maintenance sequences" } },
    { path: "/eam/sequences/new", element: createElement(NewSequencePage), handle: { title: "New maintenance sequence" } },
    { path: "/eam/sequences/:id", element: createElement(SequenceDetailPage), handle: { title: "Maintenance sequence" } },
    { path: "/eam/rounds", element: createElement(RoundsPage), handle: { title: "Maintenance rounds" } },
    { path: "/eam/rounds/new", element: createElement(NewRoundPage), handle: { title: "New maintenance round" } },
    { path: "/eam/rounds/:id", element: createElement(RoundDetailPage), handle: { title: "Maintenance round" } },
    { path: "/eam/kpis", element: createElement(KpiPage), handle: { title: "Maintenance KPIs" } },
  ],
  nav: [
    { label: "Overview", to: "/eam", duty: "eam.inquire" },
    { label: "Plant structure", to: "/eam/locations", duty: "eam.inquire" },
    { label: "Assets", to: "/eam/assets", duty: "eam.inquire" },
    { label: "Maintenance plans", to: "/eam/plans", duty: "eam.inquire" },
    { label: "Sequences", to: "/eam/sequences", duty: "eam.inquire" },
    { label: "Rounds", to: "/eam/rounds", duty: "eam.inquire" },
    { label: "Requests", to: "/eam/requests", duty: "eam.inquire" },
    { label: "Work orders", to: "/eam/work-orders", duty: "eam.inquire" },
    { label: "Fault catalogs", to: "/eam/fault-catalogs", duty: "eam.inquire" },
    { label: "Permits", to: "/eam/permits", duty: "eam.inquire" },
    { label: "Permit types", to: "/eam/permit-types", duty: "eam.inquire" },
    { label: "KPIs", to: "/eam/kpis", duty: "eam.inquire" },
  ],
};

export default eam;
