import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewProjectPage } from "./NewProjectPage";
import { ProjectDetailPage, ProjectsHome, ProjectsListPage } from "./pages";
import { ProjectPlansPage, WbsPage } from "./PlanPages";
import { PostingProfilesPage, ProjectGroupsPage } from "./SetupPages";

const projects: PassageModule = {
  id: "projects",
  title: "Projects",
  description:
    "Create projects and sub-projects, move them through their stages, plan and schedule their WBS, and set up project groups and posting profiles.",
  routes: [
    { path: "/projects", element: createElement(ProjectsHome), handle: { title: "Projects" } },
    { path: "/projects/list", element: createElement(ProjectsListPage), handle: { title: "All projects" } },
    { path: "/projects/new", element: createElement(NewProjectPage), handle: { title: "New project" } },
    { path: "/projects/groups", element: createElement(ProjectGroupsPage), handle: { title: "Project groups" } },
    { path: "/projects/posting-profiles", element: createElement(PostingProfilesPage), handle: { title: "Posting profiles" } },
    { path: "/projects/:id", element: createElement(ProjectDetailPage), handle: { title: "Project" } },
    { path: "/projects/:id/plans", element: createElement(ProjectPlansPage), handle: { title: "Plans" } },
    { path: "/projects/:id/plans/:planId", element: createElement(WbsPage), handle: { title: "WBS" } },
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
