import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CampaignDetailPage } from "./CampaignDetailPage";
import { CampaignProposalsPage } from "./CampaignProposalsPage";
import { CampaignsPage } from "./CampaignsPage";
import { CapacityBlockDetailPage } from "./CapacityBlockDetailPage";
import { CapacityBlocksPage } from "./CapacityBlocksPage";
import { CapacityLoadJobsPage } from "./CapacityLoadJobsPage";
import { CapacityLoadPage } from "./CapacityLoadPage";
import { CapacityOverridesPage } from "./CapacityOverridesPage";
import { CleanoutRulesPage } from "./CleanoutRulesPage";
import { EditResourcePage } from "./EditResourcePage";
import { LateOrdersPage } from "./LateOrdersPage";
import { NetworkDetailPage } from "./NetworkDetailPage";
import { NetworksPage } from "./NetworksPage";
import { NewCampaignPage } from "./NewCampaignPage";
import { NewCapacityBlockPage } from "./NewCapacityBlockPage";
import { NewCapacityOverridePage } from "./NewCapacityOverridePage";
import { NewCleanoutRulePage } from "./NewCleanoutRulePage";
import { NewNetworkPage } from "./NewNetworkPage";
import { NewOperationPage } from "./NewOperationPage";
import { NewOrderPegPage } from "./NewOrderPegPage";
import { NewResourcePage } from "./NewResourcePage";
import { NewRunPage } from "./NewRunPage";
import { OperationsPage } from "./OperationsPage";
import { OrderImportDetailPage } from "./OrderImportDetailPage";
import { OrderImportsPage } from "./OrderImportsPage";
import { OrderPegsPage } from "./OrderPegsPage";
import { ResourceDetailPage } from "./ResourceDetailPage";
import { ResourcesPage } from "./ResourcesPage";
import { RunDetailPage } from "./RunDetailPage";
import { RunsPage } from "./RunsPage";
import { NewSetupMatrixPage } from "./NewSetupMatrixPage";
import { SetupMatricesPage } from "./SetupMatricesPage";
import { SetupMatrixDetailPage } from "./SetupMatrixDetailPage";
import { WorkspacePage } from "./WorkspacePage";

const scheduling: PassageModule = {
  id: "scheduling",
  title: "Scheduling",
  description:
    "Finite capacity scheduling: the Gantt workspace, schedule runs, capacity load by week or day with its job drill-down, late orders, production orders received from wave 21, run utilization and lateness measures, resources, operations, order pegs, campaigns and campaign proposals, setup matrices, cleanout rules, resource networks, capacity blocks and capacity overrides (downtimes and extra shifts).",
  routes: [
    { path: "/scheduling", element: createElement(WorkspacePage), handle: { title: "Scheduling workspace" } },
    { path: "/scheduling/load", element: createElement(CapacityLoadPage), handle: { title: "Capacity load" } },
    { path: "/scheduling/load/jobs", element: createElement(CapacityLoadJobsPage), handle: { title: "Capacity load detail" } },
    { path: "/scheduling/late-orders", element: createElement(LateOrdersPage), handle: { title: "Late orders" } },
    { path: "/scheduling/runs", element: createElement(RunsPage), handle: { title: "Schedule runs" } },
    { path: "/scheduling/runs/new", element: createElement(NewRunPage), handle: { title: "Start a schedule run" } },
    { path: "/scheduling/runs/:id", element: createElement(RunDetailPage), handle: { title: "Schedule run" } },
    { path: "/scheduling/resources", element: createElement(ResourcesPage), handle: { title: "Resources" } },
    { path: "/scheduling/resources/new", element: createElement(NewResourcePage), handle: { title: "New resource" } },
    { path: "/scheduling/resources/:id", element: createElement(ResourceDetailPage), handle: { title: "Resource" } },
    { path: "/scheduling/resources/:id/edit", element: createElement(EditResourcePage), handle: { title: "Edit resource" } },
    { path: "/scheduling/operations", element: createElement(OperationsPage), handle: { title: "Operations" } },
    { path: "/scheduling/operations/new", element: createElement(NewOperationPage), handle: { title: "New operation" } },
    { path: "/scheduling/order-imports", element: createElement(OrderImportsPage), handle: { title: "Production orders received" } },
    { path: "/scheduling/order-imports/:orderId", element: createElement(OrderImportDetailPage), handle: { title: "Production order" } },
    { path: "/scheduling/order-pegs", element: createElement(OrderPegsPage), handle: { title: "Order pegs" } },
    { path: "/scheduling/order-pegs/new", element: createElement(NewOrderPegPage), handle: { title: "New order peg" } },
    { path: "/scheduling/campaigns", element: createElement(CampaignsPage), handle: { title: "Campaigns" } },
    { path: "/scheduling/campaigns/new", element: createElement(NewCampaignPage), handle: { title: "New campaign" } },
    { path: "/scheduling/campaign-proposals", element: createElement(CampaignProposalsPage), handle: { title: "Campaign proposals" } },
    { path: "/scheduling/campaigns/:id", element: createElement(CampaignDetailPage), handle: { title: "Campaign" } },
    { path: "/scheduling/setup-matrices", element: createElement(SetupMatricesPage), handle: { title: "Setup matrices" } },
    { path: "/scheduling/setup-matrices/new", element: createElement(NewSetupMatrixPage), handle: { title: "New setup matrix version" } },
    { path: "/scheduling/setup-matrices/:id", element: createElement(SetupMatrixDetailPage), handle: { title: "Setup matrix" } },
    { path: "/scheduling/cleanout-rules", element: createElement(CleanoutRulesPage), handle: { title: "Cleanout rules" } },
    { path: "/scheduling/cleanout-rules/new", element: createElement(NewCleanoutRulePage), handle: { title: "New cleanout rule" } },
    { path: "/scheduling/networks", element: createElement(NetworksPage), handle: { title: "Resource networks" } },
    { path: "/scheduling/networks/new", element: createElement(NewNetworkPage), handle: { title: "New resource network" } },
    { path: "/scheduling/networks/:id", element: createElement(NetworkDetailPage), handle: { title: "Resource network" } },
    { path: "/scheduling/capacity-overrides", element: createElement(CapacityOverridesPage), handle: { title: "Capacity overrides" } },
    { path: "/scheduling/capacity-overrides/new", element: createElement(NewCapacityOverridePage), handle: { title: "New capacity override" } },
    { path: "/scheduling/capacity-blocks", element: createElement(CapacityBlocksPage), handle: { title: "Capacity blocks" } },
    { path: "/scheduling/capacity-blocks/new", element: createElement(NewCapacityBlockPage), handle: { title: "New capacity block" } },
    { path: "/scheduling/capacity-blocks/:id", element: createElement(CapacityBlockDetailPage), handle: { title: "Capacity block" } },
  ],
  nav: [
    { label: "Gantt", to: "/scheduling", duty: "scheduling.read" },
    { label: "Capacity load", to: "/scheduling/load", duty: "scheduling.read" },
    { label: "Late orders", to: "/scheduling/late-orders", duty: "scheduling.read" },
    { label: "Schedule runs", to: "/scheduling/runs", duty: "scheduling.read" },
    { label: "Resources", to: "/scheduling/resources", duty: "scheduling.read" },
    { label: "Operations", to: "/scheduling/operations", duty: "scheduling.read" },
    { label: "Production orders", to: "/scheduling/order-imports", duty: "scheduling.read" },
    { label: "Order pegs", to: "/scheduling/order-pegs", duty: "scheduling.read" },
    { label: "Campaigns", to: "/scheduling/campaigns", duty: "scheduling.read" },
    { label: "Campaign proposals", to: "/scheduling/campaign-proposals", duty: "scheduling.read" },
    { label: "Setup matrices", to: "/scheduling/setup-matrices", duty: "scheduling.read" },
    { label: "Cleanout rules", to: "/scheduling/cleanout-rules", duty: "scheduling.read" },
    { label: "Resource networks", to: "/scheduling/networks", duty: "scheduling.read" },
    { label: "Capacity blocks", to: "/scheduling/capacity-blocks", duty: "scheduling.read" },
    { label: "Capacity overrides", to: "/scheduling/capacity-overrides", duty: "scheduling.read" },
  ],
};

export default scheduling;
