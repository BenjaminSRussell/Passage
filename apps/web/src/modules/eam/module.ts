import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AssetDetailPage } from "./AssetDetailPage";
import { AssetsPage, NewAssetPage } from "./AssetsPage";
import { FaultCatalogPage } from "./FaultCatalogPage";
import { NewPermitPage, PermitDetailPage, PermitsPage } from "./PermitPages";
import { PermitTypesPage } from "./PermitTypesPage";
import { NewPlanPage, PlansPage } from "./PlansPage";
import { NewRequestPage, RequestsPage } from "./RequestsPage";
import { WorkOrderDetailPage } from "./WorkOrderDetailPage";
import { NewWorkOrderPage, WorkOrdersPage } from "./WorkOrdersPage";
import { WorkspacePage } from "./WorkspacePage";

const eam: PassageModule = {
  id: "eam",
  title: "Asset management",
  description: "Maintain plant assets with preventive plans, condition triggers, work orders, faults, warranties, permits to work with lockout/tagout, and asset costs.",
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
  ],
  nav: [
    { label: "Overview", to: "/eam", duty: "EamInquire" },
    { label: "Assets", to: "/eam/assets", duty: "EamInquire" },
    { label: "Maintenance plans", to: "/eam/plans", duty: "EamInquire" },
    { label: "Requests", to: "/eam/requests", duty: "EamInquire" },
    { label: "Work orders", to: "/eam/work-orders", duty: "EamInquire" },
    { label: "Fault catalogs", to: "/eam/fault-catalogs", duty: "EamInquire" },
    { label: "Permits", to: "/eam/permits", duty: "EamInquire" },
    { label: "Permit types", to: "/eam/permit-types", duty: "EamInquire" },
  ],
};

export default eam;
