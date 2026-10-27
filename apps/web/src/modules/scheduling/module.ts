import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CapacityLoadPage } from "./CapacityLoadPage";
import { NewOperationPage } from "./NewOperationPage";
import { NewResourcePage } from "./NewResourcePage";
import { NewRunPage } from "./NewRunPage";
import { OperationsPage } from "./OperationsPage";
import { ResourceDetailPage } from "./ResourceDetailPage";
import { ResourcesPage } from "./ResourcesPage";
import { RunDetailPage } from "./RunDetailPage";
import { RunsPage } from "./RunsPage";
import { WorkspacePage } from "./WorkspacePage";

const scheduling: PassageModule = {
  id: "scheduling",
  title: "Scheduling",
  description: "Finite capacity scheduling: the Gantt workspace, schedule runs, capacity load, resources and operations.",
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
  ],
  nav: [
    { label: "Gantt", to: "/scheduling", duty: "scheduling.read" },
    { label: "Capacity load", to: "/scheduling/load", duty: "scheduling.read" },
    { label: "Schedule runs", to: "/scheduling/runs", duty: "scheduling.read" },
    { label: "Resources", to: "/scheduling/resources", duty: "scheduling.read" },
    { label: "Operations", to: "/scheduling/operations", duty: "scheduling.read" },
  ],
};

export default scheduling;
