import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CampaignDetailPage } from "./CampaignDetailPage";
import { CampaignsPage } from "./CampaignsPage";
import { CapacityBlocksPage } from "./CapacityBlocksPage";
import { CapacityLoadPage } from "./CapacityLoadPage";
import { CleanoutRulesPage } from "./CleanoutRulesPage";
import { NetworkDetailPage } from "./NetworkDetailPage";
import { NetworksPage } from "./NetworksPage";
import { NewCampaignPage } from "./NewCampaignPage";
import { NewCapacityBlockPage } from "./NewCapacityBlockPage";
import { NewCleanoutRulePage } from "./NewCleanoutRulePage";
import { NewNetworkPage } from "./NewNetworkPage";
import { NewOperationPage } from "./NewOperationPage";
import { NewResourcePage } from "./NewResourcePage";
import { NewRunPage } from "./NewRunPage";
import { OperationsPage } from "./OperationsPage";
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
    "Finite capacity scheduling: the Gantt workspace, schedule runs, capacity load, resources, operations, campaigns, setup matrices, cleanout rules, resource networks and capacity blocks.",
  routes: [
    { path: "/scheduling", element: createElement(WorkspacePage), handle: { title: "Scheduling workspace" } },
    { path: "/scheduling/load", element: createElement(CapacityLoadPage), handle: { title: "Capacity load" } },
    { path: "/scheduling/runs", element: createElement(RunsPage), handle: { title: "Schedule runs" } },
    { path: "/scheduling/runs/new", element: createElement(NewRunPage), handle: { title: "Start a schedule run" } },
    { path: "/scheduling/runs/:id", element: createElement(RunDetailPage), handle: { title: "Schedule run" } },
    { path: "/scheduling/resources", element: createElement(ResourcesPage), handle: { title: "Resources" } },
    { path: "/scheduling/resources/new", element: createElement(NewResourcePage), handle: { title: "New resource" } },
    { path: "/scheduling/resources/:id", element: createElement(ResourceDetailPage), handle: { title: "Resource" } },
    { path: "/scheduling/operations", element: createElement(OperationsPage), handle: { title: "Operations" } },
    { path: "/scheduling/operations/new", element: createElement(NewOperationPage), handle: { title: "New operation" } },
    { path: "/scheduling/campaigns", element: createElement(CampaignsPage), handle: { title: "Campaigns" } },
    { path: "/scheduling/campaigns/new", element: createElement(NewCampaignPage), handle: { title: "New campaign" } },
    { path: "/scheduling/campaigns/:id", element: createElement(CampaignDetailPage), handle: { title: "Campaign" } },
    { path: "/scheduling/setup-matrices", element: createElement(SetupMatricesPage), handle: { title: "Setup matrices" } },
    { path: "/scheduling/setup-matrices/new", element: createElement(NewSetupMatrixPage), handle: { title: "New setup matrix version" } },
    { path: "/scheduling/setup-matrices/:id", element: createElement(SetupMatrixDetailPage), handle: { title: "Setup matrix" } },
    { path: "/scheduling/cleanout-rules", element: createElement(CleanoutRulesPage), handle: { title: "Cleanout rules" } },
    { path: "/scheduling/cleanout-rules/new", element: createElement(NewCleanoutRulePage), handle: { title: "New cleanout rule" } },
    { path: "/scheduling/networks", element: createElement(NetworksPage), handle: { title: "Resource networks" } },
    { path: "/scheduling/networks/new", element: createElement(NewNetworkPage), handle: { title: "New resource network" } },
    { path: "/scheduling/networks/:id", element: createElement(NetworkDetailPage), handle: { title: "Resource network" } },
    { path: "/scheduling/capacity-blocks", element: createElement(CapacityBlocksPage), handle: { title: "Capacity blocks" } },
    { path: "/scheduling/capacity-blocks/new", element: createElement(NewCapacityBlockPage), handle: { title: "New capacity block" } },
  ],
  nav: [
    { label: "Gantt", to: "/scheduling", duty: "scheduling.read" },
    { label: "Capacity load", to: "/scheduling/load", duty: "scheduling.read" },
    { label: "Schedule runs", to: "/scheduling/runs", duty: "scheduling.read" },
    { label: "Resources", to: "/scheduling/resources", duty: "scheduling.read" },
    { label: "Operations", to: "/scheduling/operations", duty: "scheduling.read" },
    { label: "Campaigns", to: "/scheduling/campaigns", duty: "scheduling.read" },
    { label: "Setup matrices", to: "/scheduling/setup-matrices", duty: "scheduling.read" },
    { label: "Cleanout rules", to: "/scheduling/cleanout-rules", duty: "scheduling.read" },
    { label: "Resource networks", to: "/scheduling/networks", duty: "scheduling.read" },
    { label: "Capacity blocks", to: "/scheduling/capacity-blocks", duty: "scheduling.read" },
  ],
};

export default scheduling;
