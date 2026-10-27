import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ChangeOrderDetailPage, ChangeOrdersPage, NewChangeOrderPage } from "./ChangeOrderPages";
import { NewProjectPage } from "./NewProjectPage";
import { ProjectDetailPage, ProjectsHome, ProjectsListPage } from "./pages";
import { ProjectPlansPage, WbsPage } from "./PlanPages";
import { RisksPage } from "./RiskPages";
import { PostingProfilesPage, ProjectGroupsPage } from "./SetupPages";

const projects: PassageModule = {
  id: "projects",
  title: "Projects",
  description:
    "Create projects and sub-projects, move them through their stages, plan and schedule their WBS, record and apply change orders and claims, keep a risk and issue register, and set up project groups and posting profiles.",
  routes: [
    { path: "/projects", element: createElement(ProjectsHome), handle: { title: "Projects" } },
    { path: "/projects/list", element: createElement(ProjectsListPage), handle: { title: "All projects" } },
    { path: "/projects/new", element: createElement(NewProjectPage), handle: { title: "New project" } },
    { path: "/projects/groups", element: createElement(ProjectGroupsPage), handle: { title: "Project groups" } },
    { path: "/projects/posting-profiles", element: createElement(PostingProfilesPage), handle: { title: "Posting profiles" } },
    { path: "/projects/:id", element: createElement(ProjectDetailPage), handle: { title: "Project" } },
    { path: "/projects/:id/plans", element: createElement(ProjectPlansPage), handle: { title: "Plans" } },
    { path: "/projects/:id/plans/:planId", element: createElement(WbsPage), handle: { title: "WBS" } },
    { path: "/projects/:id/change-orders", element: createElement(ChangeOrdersPage), handle: { title: "Change orders" } },
    { path: "/projects/:id/change-orders/new", element: createElement(NewChangeOrderPage), handle: { title: "New change order" } },
    { path: "/projects/:id/change-orders/:coId", element: createElement(ChangeOrderDetailPage), handle: { title: "Change order" } },
    { path: "/projects/:id/risks", element: createElement(RisksPage), handle: { title: "Risks and issues" } },
  ],
  nav: [
    { label: "Overview", to: "/projects", duty: "ProjInquire" },
    { label: "All projects", to: "/projects/list", duty: "ProjInquire" },
    { label: "New project", to: "/projects/new", duty: "ProjMaintain" },
    { label: "Project groups", to: "/projects/groups", duty: "ProjInquire" },
    { label: "Posting profiles", to: "/projects/posting-profiles", duty: "ProjInquire" },
  ],
};

export default projects;
