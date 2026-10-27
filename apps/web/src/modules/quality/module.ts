import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AssociationsPage, NewAssociationPage } from "./AssociationsPage";
import { ComplianceListsPage, NewComplianceListPage } from "./ComplianceListsPage";
import { InstrumentsPage, NewInstrumentPage } from "./InstrumentsPage";
import { ItemCompliancePage, ItemLookupPage } from "./ItemCompliancePage";
import { NewNonconformancePage, NonconformanceDetailPage, NonconformancesPage } from "./NonconformancesPage";
import { NewOrderPage } from "./NewOrderPage";
import { OrderDetailPage } from "./OrderDetailPage";
import { OrdersPage } from "./OrdersPage";
import { SamplesPage } from "./SamplesPage";
import { SamplingPage } from "./SamplingPage";
import { SpcPage } from "./SpcPage";
import { NewStudyPage, StabilityStudiesPage, StudyDetailPage } from "./StabilityStudiesPage";
import { NewTestGroupPage, TestGroupsPage } from "./TestGroupsPage";

const quality: PassageModule = {
  id: "quality",
  title: "Quality",
  description:
    "Quality orders with results, incoming vendor certificates, physical samples and usage decisions, nonconformances with their 8D correction, test groups, instruments, quality associations, AQL sampling, product compliance, stability studies and SPC charts.",
  routes: [
    { path: "/quality", element: createElement(OrdersPage), handle: { title: "Quality orders" } },
    { path: "/quality/orders", element: createElement(OrdersPage) },
    { path: "/quality/orders/new", element: createElement(NewOrderPage), handle: { title: "New quality order" } },
    { path: "/quality/orders/:id", element: createElement(OrderDetailPage), handle: { title: "Quality order" } },
    { path: "/quality/test-groups", element: createElement(TestGroupsPage) },
    { path: "/quality/test-groups/new", element: createElement(NewTestGroupPage), handle: { title: "New test group version" } },
    { path: "/quality/instruments", element: createElement(InstrumentsPage) },
    { path: "/quality/instruments/new", element: createElement(NewInstrumentPage), handle: { title: "Register instrument" } },
    { path: "/quality/associations", element: createElement(AssociationsPage) },
    { path: "/quality/associations/new", element: createElement(NewAssociationPage), handle: { title: "New quality association" } },
    { path: "/quality/sampling", element: createElement(SamplingPage) },
    { path: "/quality/samples", element: createElement(SamplesPage), handle: { title: "Physical samples" } },
    { path: "/quality/compliance/lists", element: createElement(ComplianceListsPage), handle: { title: "Compliance lists" } },
    { path: "/quality/compliance/lists/new", element: createElement(NewComplianceListPage), handle: { title: "New compliance list version" } },
    { path: "/quality/compliance/items", element: createElement(ItemLookupPage), handle: { title: "Item compliance" } },
    { path: "/quality/compliance/items/:id", element: createElement(ItemCompliancePage), handle: { title: "Item compliance" } },
    { path: "/quality/stability", element: createElement(StabilityStudiesPage), handle: { title: "Stability studies" } },
    { path: "/quality/stability/new", element: createElement(NewStudyPage), handle: { title: "Plan a stability study" } },
    { path: "/quality/stability/:id", element: createElement(StudyDetailPage), handle: { title: "Stability study" } },
    { path: "/quality/spc", element: createElement(SpcPage), handle: { title: "SPC charts" } },
    { path: "/quality/nonconformances", element: createElement(NonconformancesPage), handle: { title: "Nonconformances" } },
    { path: "/quality/nonconformances/new", element: createElement(NewNonconformancePage), handle: { title: "Raise a nonconformance" } },
    { path: "/quality/nonconformances/:id", element: createElement(NonconformanceDetailPage), handle: { title: "Nonconformance" } },
  ],
  nav: [
    { label: "Quality orders", to: "/quality/orders", duty: "QM_VIEW" },
    { label: "New quality order", to: "/quality/orders/new", duty: "QM_ORDER_MAINTAIN" },
    { label: "Nonconformances", to: "/quality/nonconformances", duty: "QM_VIEW" },
    { label: "Test groups", to: "/quality/test-groups", duty: "QM_VIEW" },
    { label: "Instruments", to: "/quality/instruments", duty: "QM_VIEW" },
    { label: "Quality associations", to: "/quality/associations", duty: "QM_VIEW" },
    { label: "Sampling calculator", to: "/quality/sampling", duty: "QM_VIEW" },
    { label: "Physical samples", to: "/quality/samples", duty: "QM_VIEW" },
    { label: "Compliance lists", to: "/quality/compliance/lists", duty: "QM_VIEW" },
    { label: "Item compliance", to: "/quality/compliance/items", duty: "QM_VIEW" },
    { label: "Stability studies", to: "/quality/stability", duty: "QM_VIEW" },
    { label: "SPC charts", to: "/quality/spc", duty: "QM_VIEW" },
  ],
};

export default quality;
