import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CategoriesPage, ProposalDetailPage, ProposalsPage, TransactionsPage } from "./BillingPages";
import { ChangeOrderDetailPage, ChangeOrdersPage, NewChangeOrderPage } from "./ChangeOrderPages";
import { ForecastDetailPage, ForecastsPage } from "./ForecastPages";
import { NewProjectPage } from "./NewProjectPage";
import { ProjectDetailPage, ProjectsHome, ProjectsListPage } from "./pages";
import { ProjectPlansPage, WbsPage } from "./PlanPages";
import { RisksPage } from "./RiskPages";
import { PostingProfilesPage, ProjectGroupsPage } from "./SetupPages";
import { TransferPage } from "./TransferPage";

const projects: PassageModule = {
  id: "projects",
  title: "Projects",
  description:
    "Create projects and sub-projects, move them through their stages, plan and schedule their WBS, transfer simulations into the working plan, record and apply change orders and claims, keep a risk and issue register and forecast models compared with actuals, record project transactions, build and approve invoice proposals, and set up project groups, posting profiles and categories.",
  routes: [
    { path: "/projects", element: createElement(ProjectsHome), handle: { title: "Projects" } },
    { path: "/projects/list", element: createElement(ProjectsListPage), handle: { title: "All projects" } },
    { path: "/projects/new", element: createElement(NewProjectPage), handle: { title: "New project" } },
    { path: "/projects/groups", element: createElement(ProjectGroupsPage), handle: { title: "Project groups" } },
    { path: "/projects/posting-profiles", element: createElement(PostingProfilesPage), handle: { title: "Posting profiles" } },
    { path: "/projects/categories", element: createElement(CategoriesPage), handle: { title: "Project categories" } },
    { path: "/projects/:id", element: createElement(ProjectDetailPage), handle: { title: "Project" } },
    { path: "/projects/:id/plans", element: createElement(ProjectPlansPage), handle: { title: "Plans" } },
    { path: "/projects/:id/plans/:planId", element: createElement(WbsPage), handle: { title: "WBS" } },
    { path: "/projects/:id/plans/:planId/transfer", element: createElement(TransferPage), handle: { title: "Transfer simulation" } },
    { path: "/projects/:id/change-orders", element: createElement(ChangeOrdersPage), handle: { title: "Change orders" } },
    { path: "/projects/:id/change-orders/new", element: createElement(NewChangeOrderPage), handle: { title: "New change order" } },
    { path: "/projects/:id/change-orders/:coId", element: createElement(ChangeOrderDetailPage), handle: { title: "Change order" } },
    { path: "/projects/:id/risks", element: createElement(RisksPage), handle: { title: "Risks and issues" } },
    { path: "/projects/:id/forecasts", element: createElement(ForecastsPage), handle: { title: "Forecasts" } },
    { path: "/projects/:id/forecasts/:modelId", element: createElement(ForecastDetailPage), handle: { title: "Forecast" } },
    { path: "/projects/:id/transactions", element: createElement(TransactionsPage), handle: { title: "Transactions" } },
    { path: "/projects/:id/invoice-proposals", element: createElement(ProposalsPage), handle: { title: "Invoice proposals" } },
    { path: "/projects/:id/invoice-proposals/:proposalId", element: createElement(ProposalDetailPage), handle: { title: "Invoice proposal" } },
  ],
  nav: [
    { label: "Overview", to: "/projects", duty: "ProjInquire" },
    { label: "All projects", to: "/projects/list", duty: "ProjInquire" },
    { label: "New project", to: "/projects/new", duty: "ProjMaintain" },
    { label: "Project groups", to: "/projects/groups", duty: "ProjInquire" },
    { label: "Posting profiles", to: "/projects/posting-profiles", duty: "ProjInquire" },
    { label: "Project categories", to: "/projects/categories", duty: "ProjInquire" },
  ],
};

export default projects;
