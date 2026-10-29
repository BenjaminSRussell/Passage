import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AgreementDetailPage } from "./AgreementDetailPage";
import { AgreementsPage, NewAgreementPage } from "./AgreementsPage";
import { CaseDetailPage } from "./CaseDetailPage";
import { CasesPage, OpenCasePage } from "./CasesPage";
import { KpisPage } from "./KpisPage";
import { NewSlaProfilePage, SlaProfilesPage } from "./SlaProfilesPage";
import { WorkOrderDetailPage } from "./WorkOrderDetailPage";
import { NewWorkOrderPage, WorkOrdersPage } from "./WorkOrdersPage";

const service: PassageModule = {
  id: "service",
  title: "Service",
  description: "Open service cases with entitlement and SLA clocks, pause and resolve them, maintain service agreements and SLA profiles, run work orders through to billing and follow-up, and track SLA attainment and first-time fix.",
  routes: [
    { path: "/service", element: createElement(CasesPage), handle: { title: "Service cases" } },
    { path: "/service/cases", element: createElement(CasesPage) },
    { path: "/service/cases/new", element: createElement(OpenCasePage), handle: { title: "Open service case" } },
    { path: "/service/cases/:id", element: createElement(CaseDetailPage), handle: { title: "Service case" } },
    { path: "/service/agreements", element: createElement(AgreementsPage) },
    { path: "/service/agreements/new", element: createElement(NewAgreementPage), handle: { title: "New service agreement" } },
    { path: "/service/agreements/:id", element: createElement(AgreementDetailPage), handle: { title: "Service agreement" } },
    { path: "/service/work-orders", element: createElement(WorkOrdersPage) },
    { path: "/service/work-orders/new", element: createElement(NewWorkOrderPage), handle: { title: "New service work order" } },
    { path: "/service/work-orders/:id", element: createElement(WorkOrderDetailPage), handle: { title: "Service work order" } },
    { path: "/service/sla-profiles", element: createElement(SlaProfilesPage) },
    { path: "/service/sla-profiles/new", element: createElement(NewSlaProfilePage), handle: { title: "New SLA profile" } },
    { path: "/service/kpis", element: createElement(KpisPage), handle: { title: "Service KPIs" } },
  ],
  nav: [
    { label: "Service cases", to: "/service/cases", duty: "SvcInquire" },
    { label: "Open case", to: "/service/cases/new", duty: "SvcCaseCreate" },
    { label: "Work orders", to: "/service/work-orders", duty: "SvcInquire" },
    { label: "Service agreements", to: "/service/agreements", duty: "SvcInquire" },
    { label: "SLA profiles", to: "/service/sla-profiles", duty: "SvcInquire" },
    { label: "Service KPIs", to: "/service/kpis", duty: "SvcInquire" },
  ],
};

export default service;
