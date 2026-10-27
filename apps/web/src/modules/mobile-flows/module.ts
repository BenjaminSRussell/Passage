import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewPrinterPage } from "./NewPrinterPage";
import { NewPrintJobPage } from "./NewPrintJobPage";
import { NewRoutingRulePage } from "./NewRoutingRulePage";
import { OfflineConflictsPage } from "./OfflineConflictsPage";
import { FlowDesigner } from "./pages";
import { PrintersPage } from "./PrintersPage";
import { PrinterStatusPage } from "./PrinterStatusPage";
import { PrintJobDetailPage } from "./PrintJobDetailPage";
import { PrintJobsPage } from "./PrintJobsPage";
import { RoutingRulesPage } from "./RoutingRulesPage";

const mobileFlows: PassageModule = {
  id: "mobile-flows",
  title: "Mobile flows",
  description:
    "Design, validate and simulate warehouse handheld flows without an app release; register label printers and queues, route documents to them, follow printer status and the print job log, and resolve offline replay conflicts.",
  routes: [
    { path: "/mobile-flows", element: createElement(FlowDesigner), handle: { title: "Flow designer" } },
    { path: "/mobile-flows/printers", element: createElement(PrintersPage), handle: { title: "Printers" } },
    { path: "/mobile-flows/printers/new", element: createElement(NewPrinterPage), handle: { title: "Register printer" } },
    { path: "/mobile-flows/printer-status", element: createElement(PrinterStatusPage), handle: { title: "Printer status" } },
    { path: "/mobile-flows/routing-rules", element: createElement(RoutingRulesPage), handle: { title: "Routing rules" } },
    { path: "/mobile-flows/routing-rules/new", element: createElement(NewRoutingRulePage), handle: { title: "Add routing rule" } },
    { path: "/mobile-flows/print-jobs", element: createElement(PrintJobsPage), handle: { title: "Print jobs" } },
    { path: "/mobile-flows/print-jobs/new", element: createElement(NewPrintJobPage), handle: { title: "Print label" } },
    { path: "/mobile-flows/print-jobs/:id", element: createElement(PrintJobDetailPage), handle: { title: "Print job" } },
    { path: "/mobile-flows/conflicts", element: createElement(OfflineConflictsPage), handle: { title: "Offline conflicts" } },
  ],
  nav: [
    { label: "Flow designer", to: "/mobile-flows", duty: "WMS_MOBILE_ADMIN" },
    { label: "Printers", to: "/mobile-flows/printers", duty: "WMS_PRINT" },
    { label: "Printer status", to: "/mobile-flows/printer-status", duty: "WMS_PRINT" },
    { label: "Routing rules", to: "/mobile-flows/routing-rules", duty: "WMS_PRINT" },
    { label: "Print jobs", to: "/mobile-flows/print-jobs", duty: "WMS_PRINT" },
    { label: "Offline conflicts", to: "/mobile-flows/conflicts", duty: "WMS_WORK_SUPERVISE" },
  ],
};

export default mobileFlows;
