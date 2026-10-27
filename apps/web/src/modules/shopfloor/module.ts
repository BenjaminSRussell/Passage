import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AndonPage } from "./AndonPage";
import { DowntimesPage } from "./DowntimesPage";
import { MachinesPage } from "./MachinesPage";
import { NewMachinePage } from "./NewMachinePage";
import { NewWorkerPage } from "./NewWorkerPage";
import { OeePage } from "./OeePage";
import { PayExportDetailPage } from "./PayExportDetailPage";
import { PayExportsPage } from "./PayExportsPage";
import { TimeApprovalPage } from "./TimeApprovalPage";
import { WorkerDetailPage } from "./WorkerDetailPage";
import { WorkersPage } from "./WorkersPage";

const shopfloor: PassageModule = {
  id: "shopfloor",
  title: "Shop floor",
  description: "Workers and their time, pay approval and payroll export; machines, downtimes, andon alerts and OEE.",
  routes: [
    { path: "/shopfloor/workers", element: createElement(WorkersPage), handle: { title: "Workers" } },
    { path: "/shopfloor/workers/new", element: createElement(NewWorkerPage), handle: { title: "New worker" } },
    { path: "/shopfloor/workers/:id", element: createElement(WorkerDetailPage), handle: { title: "Worker" } },
    { path: "/shopfloor/time", element: createElement(TimeApprovalPage), handle: { title: "Time approval" } },
    { path: "/shopfloor/pay-exports", element: createElement(PayExportsPage), handle: { title: "Payroll export" } },
    { path: "/shopfloor/pay-exports/:id", element: createElement(PayExportDetailPage), handle: { title: "Export batch" } },
    { path: "/shopfloor/machines", element: createElement(MachinesPage), handle: { title: "Machines" } },
    { path: "/shopfloor/machines/new", element: createElement(NewMachinePage), handle: { title: "Register machine" } },
    { path: "/shopfloor/downtimes", element: createElement(DowntimesPage), handle: { title: "Downtimes" } },
    { path: "/shopfloor/andon", element: createElement(AndonPage), handle: { title: "Andon board" } },
    { path: "/shopfloor/oee", element: createElement(OeePage), handle: { title: "OEE" } },
  ],
  nav: [
    { label: "Workers", to: "/shopfloor/workers", duty: "shopfloor.view" },
    { label: "Time approval", to: "/shopfloor/time", duty: "shopfloor.pay.approve" },
    { label: "Payroll export", to: "/shopfloor/pay-exports", duty: "shopfloor.pay.export" },
    { label: "Machines", to: "/shopfloor/machines", duty: "shopfloor.view" },
    { label: "Downtimes", to: "/shopfloor/downtimes", duty: "shopfloor.view" },
    { label: "Andon board", to: "/shopfloor/andon", duty: "shopfloor.view" },
    { label: "OEE", to: "/shopfloor/oee", duty: "shopfloor.view" },
  ],
};

export default shopfloor;
