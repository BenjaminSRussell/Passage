import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewProjectPage } from "./NewProjectPage";
import { ProjectDetailPage, ProjectsHome, ProjectsListPage } from "./pages";

const projects: PassageModule = {
  id: "projects",
  title: "Projects",
  description: "Create projects and sub-projects, list them by stage and type, and move them through their stages.",
  routes: [
    { path: "/projects", element: createElement(ProjectsHome), handle: { title: "Projects" } },
    { path: "/projects/list", element: createElement(ProjectsListPage), handle: { title: "All projects" } },
    { path: "/projects/new", element: createElement(NewProjectPage), handle: { title: "New project" } },
    { path: "/projects/:id", element: createElement(ProjectDetailPage), handle: { title: "Project" } },
  ],
  nav: [
    { label: "Overview", to: "/projects", duty: "ProjInquire" },
    { label: "All projects", to: "/projects/list", duty: "ProjInquire" },
    { label: "New project", to: "/projects/new", duty: "ProjMaintain" },
  ],
};

export default projects;
