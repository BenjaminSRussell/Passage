import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AndonPage } from "./AndonPage";
import { BundleDetailPage } from "./BundleDetailPage";
import { ClockedInPage } from "./ClockedInPage";
import { CorrectionsPage } from "./CorrectionsPage";
import { CorrectRegistrationPage } from "./CorrectRegistrationPage";
import { DowntimesPage } from "./DowntimesPage";
import { JobDetailPage } from "./JobDetailPage";
import { JobsPage } from "./JobsPage";
import { KioskPage } from "./KioskPage";
import { MachinesPage } from "./MachinesPage";
import { NewMachinePage } from "./NewMachinePage";
import { NewProcessMessagePage } from "./NewProcessMessagePage";
import { NewWorkerPage } from "./NewWorkerPage";
import { OeePage } from "./OeePage";
import { PayExportDetailPage } from "./PayExportDetailPage";
import { PayExportsPage } from "./PayExportsPage";
import { ProcessMessageDetailPage } from "./ProcessMessageDetailPage";
import { ProcessMessagesPage } from "./ProcessMessagesPage";
import { RevisePayPage } from "./RevisePayPage";
import { TimeApprovalPage } from "./TimeApprovalPage";
import { WorkerDetailPage } from "./WorkerDetailPage";
import { WorkersPage } from "./WorkersPage";

const shopfloor: PassageModule = {
  id: "shopfloor",
  title: "Shop floor",
  description:
    "Kiosk clock in and out; the job dispatch list with quantity, scrap and quality check reports; supervisor time corrections with a reason and pay revision with reversal lines; workers, who is clocked in, their time, job bundles, pay approval and payroll export; machines, downtimes, andon alerts and OEE; process messages and their deliveries.",
  routes: [
    { path: "/shopfloor/workers", element: createElement(WorkersPage), handle: { title: "Workers" } },
    { path: "/shopfloor/workers/new", element: createElement(NewWorkerPage), handle: { title: "New worker" } },
    { path: "/shopfloor/workers/:id", element: createElement(WorkerDetailPage), handle: { title: "Worker" } },
    { path: "/shopfloor/workers/:id/revise", element: createElement(RevisePayPage), handle: { title: "Revise pay", duty: "shopfloor.pay.revise" } },
    { path: "/shopfloor/registrations/:id/correct", element: createElement(CorrectRegistrationPage), handle: { title: "Correct registration", duty: "shopfloor.time.correct" } },
    { path: "/shopfloor/corrections", element: createElement(CorrectionsPage), handle: { title: "Time corrections", duty: "shopfloor.view" } },
    { path: "/shopfloor/bundles/:id", element: createElement(BundleDetailPage), handle: { title: "Bundle", duty: "shopfloor.view" } },
    { path: "/shopfloor/kiosk", element: createElement(KioskPage), handle: { title: "Kiosk" } },
    { path: "/shopfloor/jobs", element: createElement(JobsPage), handle: { title: "Dispatch list" } },
    { path: "/shopfloor/jobs/:id", element: createElement(JobDetailPage), handle: { title: "Job" } },
    { path: "/shopfloor/clocked-in", element: createElement(ClockedInPage), handle: { title: "Who is clocked in" } },
    { path: "/shopfloor/time", element: createElement(TimeApprovalPage), handle: { title: "Time approval" } },
    { path: "/shopfloor/pay-exports", element: createElement(PayExportsPage), handle: { title: "Payroll export" } },
    { path: "/shopfloor/pay-exports/:id", element: createElement(PayExportDetailPage), handle: { title: "Export batch" } },
    { path: "/shopfloor/machines", element: createElement(MachinesPage), handle: { title: "Machines" } },
    { path: "/shopfloor/machines/new", element: createElement(NewMachinePage), handle: { title: "Register machine" } },
    { path: "/shopfloor/downtimes", element: createElement(DowntimesPage), handle: { title: "Downtimes" } },
    { path: "/shopfloor/andon", element: createElement(AndonPage), handle: { title: "Andon board" } },
    { path: "/shopfloor/oee", element: createElement(OeePage), handle: { title: "OEE" } },
    { path: "/shopfloor/process-messages", element: createElement(ProcessMessagesPage), handle: { title: "Process messages" } },
    { path: "/shopfloor/process-messages/new", element: createElement(NewProcessMessagePage), handle: { title: "Record process message" } },
    { path: "/shopfloor/process-messages/:id", element: createElement(ProcessMessageDetailPage), handle: { title: "Process message" } },
  ],
  nav: [
    { label: "Kiosk", to: "/shopfloor/kiosk", duty: "shopfloor.kiosk.login" },
    { label: "Dispatch list", to: "/shopfloor/jobs", duty: "shopfloor.time.register" },
    { label: "Workers", to: "/shopfloor/workers", duty: "shopfloor.view" },
    { label: "Who is clocked in", to: "/shopfloor/clocked-in", duty: "shopfloor.view" },
    { label: "Time approval", to: "/shopfloor/time", duty: "shopfloor.pay.approve" },
    { label: "Time corrections", to: "/shopfloor/corrections", duty: "shopfloor.view" },
    { label: "Payroll export", to: "/shopfloor/pay-exports", duty: "shopfloor.pay.export" },
    { label: "Machines", to: "/shopfloor/machines", duty: "shopfloor.view" },
    { label: "Downtimes", to: "/shopfloor/downtimes", duty: "shopfloor.view" },
    { label: "Andon board", to: "/shopfloor/andon", duty: "shopfloor.view" },
    { label: "OEE", to: "/shopfloor/oee", duty: "shopfloor.view" },
    { label: "Process messages", to: "/shopfloor/process-messages", duty: "shopfloor.view" },
  ],
};

export default shopfloor;
