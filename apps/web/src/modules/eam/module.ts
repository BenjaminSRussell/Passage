import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AssetDetailPage } from "./AssetDetailPage";
import { AssetsPage, NewAssetPage } from "./AssetsPage";
import { NewPlanPage, PlansPage } from "./PlansPage";
import { NewRequestPage, RequestsPage } from "./RequestsPage";
import { WorkOrderDetailPage } from "./WorkOrderDetailPage";
import { NewWorkOrderPage, WorkOrdersPage } from "./WorkOrdersPage";
import { WorkspacePage } from "./WorkspacePage";

const eam: PassageModule = {
  id: "eam",
  title: "Asset management",
  description: "Maintain plant assets with preventive plans, condition triggers, work orders and asset costs.",
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
  ],
  nav: [
    { label: "Overview", to: "/eam", duty: "EamInquire" },
    { label: "Assets", to: "/eam/assets", duty: "EamInquire" },
    { label: "Maintenance plans", to: "/eam/plans", duty: "EamInquire" },
    { label: "Requests", to: "/eam/requests", duty: "EamInquire" },
    { label: "Work orders", to: "/eam/work-orders", duty: "EamInquire" },
  ],
};

export default eam;
