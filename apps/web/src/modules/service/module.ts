import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ActivitiesPage, ActivityDetailPage, NewActivityPage } from "./ActivitiesPage";
import { AgreementDetailPage } from "./AgreementDetailPage";
import { AgreementsPage, NewAgreementPage } from "./AgreementsPage";
import { CaseDetailPage } from "./CaseDetailPage";
import { CaseMailboxPage } from "./CaseMailboxPage";
import { CaseSetupPage, NewCaseCategoryPage, NewCaseProcessPage } from "./CaseSetupPage";
import { CasesPage, OpenCasePage } from "./CasesPage";
import { GenericCaseDetailPage } from "./GenericCaseDetailPage";
import { GenericCasesPage, OpenGenericCasePage } from "./GenericCasesPage";
import { ArticleDetailPage, KnowledgePage, NewArticlePage } from "./KnowledgePage";
import { KpisPage } from "./KpisPage";
import { ServiceObjectDetailPage } from "./ServiceObjectDetailPage";
import { NewServiceObjectPage, ReceiveShipmentPage, ServiceObjectsPage } from "./ServiceObjectsPage";
import { NewSlaProfilePage, SlaProfilesPage } from "./SlaProfilesPage";
import { SupplierClaimDetailPage } from "./SupplierClaimDetailPage";
import { NewSupplierClaimPage, SupplierClaimsPage } from "./SupplierClaimsPage";
import { WorkOrderDetailPage } from "./WorkOrderDetailPage";
import { NewWorkOrderPage, WorkOrdersPage } from "./WorkOrdersPage";

const service: PassageModule = {
  id: "service",
  title: "Service",
  description: "Open service cases with entitlement and SLA clocks, pause and resolve them, maintain service agreements and SLA profiles, run work orders through to billing and follow-up, track SLA attainment and first-time fix, keep the installed base with its warranties, plan activities on any record, maintain the knowledge base, run HR, collections, audit and other cases through their category's stages, recover warranty cost from vendors with supplier claims, and turn inbound email into cases.",
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
    { path: "/service/objects", element: createElement(ServiceObjectsPage), handle: { title: "Installed base" } },
    { path: "/service/objects/new", element: createElement(NewServiceObjectPage), handle: { title: "Register service object" } },
    { path: "/service/objects/receive", element: createElement(ReceiveShipmentPage), handle: { title: "Receive shipment" } },
    { path: "/service/objects/:id", element: createElement(ServiceObjectDetailPage), handle: { title: "Service object" } },
    { path: "/service/activities", element: createElement(ActivitiesPage), handle: { title: "My activities" } },
    { path: "/service/activities/new", element: createElement(NewActivityPage), handle: { title: "New activity" } },
    { path: "/service/activities/:id", element: createElement(ActivityDetailPage), handle: { title: "Activity" } },
    { path: "/service/knowledge", element: createElement(KnowledgePage), handle: { title: "Knowledge base" } },
    { path: "/service/knowledge/new", element: createElement(NewArticlePage), handle: { title: "New knowledge article" } },
    { path: "/service/knowledge/:id", element: createElement(ArticleDetailPage), handle: { title: "Knowledge article" } },
    { path: "/service/case-management", element: createElement(GenericCasesPage), handle: { title: "Cases" } },
    { path: "/service/case-management/new", element: createElement(OpenGenericCasePage), handle: { title: "Open case" } },
    { path: "/service/case-management/:id", element: createElement(GenericCaseDetailPage), handle: { title: "Case" } },
    { path: "/service/case-setup", element: createElement(CaseSetupPage), handle: { title: "Case setup" } },
    { path: "/service/case-setup/categories/new", element: createElement(NewCaseCategoryPage), handle: { title: "New case category" } },
    { path: "/service/case-setup/processes/new", element: createElement(NewCaseProcessPage), handle: { title: "New case process" } },
    { path: "/service/supplier-claims", element: createElement(SupplierClaimsPage), handle: { title: "Supplier warranty claims" } },
    { path: "/service/supplier-claims/new", element: createElement(NewSupplierClaimPage), handle: { title: "New supplier claim" } },
    { path: "/service/supplier-claims/:id", element: createElement(SupplierClaimDetailPage), handle: { title: "Supplier claim" } },
    { path: "/service/case-mailbox", element: createElement(CaseMailboxPage), handle: { title: "Case mailbox" } },
  ],
  nav: [
    { label: "Service cases", to: "/service/cases", duty: "SvcInquire" },
    { label: "Open case", to: "/service/cases/new", duty: "SvcCaseCreate" },
    { label: "Work orders", to: "/service/work-orders", duty: "SvcInquire" },
    { label: "Service agreements", to: "/service/agreements", duty: "SvcInquire" },
    { label: "SLA profiles", to: "/service/sla-profiles", duty: "SvcInquire" },
    { label: "Service KPIs", to: "/service/kpis", duty: "SvcInquire" },
    { label: "Installed base", to: "/service/objects", duty: "SvcInquire" },
    { label: "My activities", to: "/service/activities", duty: "ActivityMaintain" },
    { label: "Knowledge base", to: "/service/knowledge", duty: "KbRead" },
    { label: "Cases (all categories)", to: "/service/case-management", duty: "CaseMaintain" },
    { label: "Case setup", to: "/service/case-setup", duty: "CaseSetupAdmin" },
    { label: "Supplier claims", to: "/service/supplier-claims", duty: "SvcClaimMaintain" },
    { label: "Case mailbox", to: "/service/case-mailbox", duty: "CaseMaintain" },
  ],
};

export default service;
