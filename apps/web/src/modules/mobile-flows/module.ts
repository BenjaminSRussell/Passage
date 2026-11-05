import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewPrintDocumentPage } from "./NewPrintDocumentPage";
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
    "Design, validate and simulate warehouse handheld flows without an app release; register label and document printers and queues, route documents to them, print labels and PDF documents, follow printer status and the print job log, and resolve offline replay conflicts.",
  routes: [
    { path: "/mobile-flows", element: createElement(FlowDesigner), handle: { title: "Flow designer" } },
    { path: "/mobile-flows/printers", element: createElement(PrintersPage), handle: { title: "Printers" } },
    { path: "/mobile-flows/printers/new", element: createElement(NewPrinterPage), handle: { title: "Register printer" } },
    { path: "/mobile-flows/printer-status", element: createElement(PrinterStatusPage), handle: { title: "Printer status" } },
    { path: "/mobile-flows/routing-rules", element: createElement(RoutingRulesPage), handle: { title: "Routing rules" } },
    { path: "/mobile-flows/routing-rules/new", element: createElement(NewRoutingRulePage), handle: { title: "Add routing rule" } },
    { path: "/mobile-flows/print-jobs", element: createElement(PrintJobsPage), handle: { title: "Print jobs" } },
    { path: "/mobile-flows/print-jobs/new", element: createElement(NewPrintJobPage), handle: { title: "Print label" } },
    { path: "/mobile-flows/print-documents/new", element: createElement(NewPrintDocumentPage), handle: { title: "Print document" } },
    { path: "/mobile-flows/print-jobs/:id", element: createElement(PrintJobDetailPage), handle: { title: "Print job" } },
    { path: "/mobile-flows/conflicts", element: createElement(OfflineConflictsPage), handle: { title: "Offline conflicts" } },
  ],
  nav: [
    { label: "Flow designer", to: "/mobile-flows", duty: "warehouse.mobile-flow.admin" },
    { label: "Printers", to: "/mobile-flows/printers", duty: "print.job.submit" },
    { label: "Printer status", to: "/mobile-flows/printer-status", duty: "print.job.submit" },
    { label: "Routing rules", to: "/mobile-flows/routing-rules", duty: "print.job.submit" },
    { label: "Print jobs", to: "/mobile-flows/print-jobs", duty: "print.job.submit" },
    { label: "Offline conflicts", to: "/mobile-flows/conflicts", duty: "warehouse.offline-conflict.resolve" },
  ],
};

export default mobileFlows;
