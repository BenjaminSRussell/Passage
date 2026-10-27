import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewWorkerPage } from "./NewWorkerPage";
import { TimeApprovalPage } from "./TimeApprovalPage";
import { WorkerDetailPage } from "./WorkerDetailPage";
import { WorkersPage } from "./WorkersPage";

const shopfloor: PassageModule = {
  id: "shopfloor",
  title: "Shop floor",
  description: "Shop floor workers, their time registrations, pay calculation and supervisor time approval.",
  routes: [
    { path: "/shopfloor/workers", element: createElement(WorkersPage), handle: { title: "Workers" } },
    { path: "/shopfloor/workers/new", element: createElement(NewWorkerPage), handle: { title: "New worker" } },
    { path: "/shopfloor/workers/:id", element: createElement(WorkerDetailPage), handle: { title: "Worker" } },
    { path: "/shopfloor/time", element: createElement(TimeApprovalPage), handle: { title: "Time approval" } },
  ],
  nav: [
    { label: "Workers", to: "/shopfloor/workers", duty: "shopfloor.view" },
    { label: "Time approval", to: "/shopfloor/time", duty: "shopfloor.pay.approve" },
  ],
};

export default shopfloor;
