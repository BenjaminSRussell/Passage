import { createElement } from "react";
import type { PassageModule } from "../registry";
import { InboxPage } from "./InboxPage";
import { DesignerPage, JobMonitorPage, TemplatesPage, WorkflowHome } from "./pages";

const workflow: PassageModule = {
  id: "workflow",
  title: "Workflow and batch",
  description: "Approvals, batch jobs and message templates.",
  routes: [
    { path: "/workflow", element: createElement(WorkflowHome) },
    { path: "/workflow/inbox", element: createElement(InboxPage) },
    { path: "/workflow/designer", element: createElement(DesignerPage) },
    { path: "/workflow/jobs", element: createElement(JobMonitorPage) },
    { path: "/workflow/templates", element: createElement(TemplatesPage) },
  ],
  nav: [
    { label: "Workflow", to: "/workflow", duty: "workflow.read" },
    { label: "Inbox", to: "/workflow/inbox", duty: "inbox.read" },
    { label: "Designer", to: "/workflow/designer", duty: "workflow.write" },
    { label: "Job monitor", to: "/workflow/jobs", duty: "batch.read" },
    { label: "Templates", to: "/workflow/templates", duty: "notify.write" },
  ],
};

export default workflow;
