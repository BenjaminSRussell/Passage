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
import { ReceivingChecksPage } from "./ReceivingChecksPage";
import { SamplesPage } from "./SamplesPage";
import { SamplingPage } from "./SamplingPage";
import { SpcPage } from "./SpcPage";
import { NewStudyPage, StabilityStudiesPage, StudyDetailPage } from "./StabilityStudiesPage";
import { NewTestGroupPage, TestGroupsPage } from "./TestGroupsPage";

const quality: PassageModule = {
  id: "quality",
  title: "Quality",
  description:
    "Quality orders with results, incoming vendor certificates, physical samples, usage decisions and certificates of analysis, receipts against source inspection, nonconformances with their 8D correction, test groups, instruments and their calibration due list, quality associations, AQL sampling, product compliance, stability studies with their lifecycle and SPC charts.",
  routes: [
    { path: "/quality", element: createElement(OrdersPage), handle: { title: "Quality orders", duty: "quality.view" } },
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
    { path: "/quality/receiving", element: createElement(ReceivingChecksPage), handle: { title: "Receipts and source inspection" } },
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
    { label: "Quality orders", to: "/quality/orders", duty: "quality.view" },
    { label: "New quality order", to: "/quality/orders/new", duty: "quality.order.maintain" },
    { label: "Nonconformances", to: "/quality/nonconformances", duty: "quality.view" },
    { label: "Test groups", to: "/quality/test-groups", duty: "quality.view" },
    { label: "Instruments", to: "/quality/instruments", duty: "quality.view" },
    { label: "Quality associations", to: "/quality/associations", duty: "quality.view" },
    { label: "Sampling calculator", to: "/quality/sampling", duty: "quality.view" },
    { label: "Physical samples", to: "/quality/samples", duty: "quality.view" },
    { label: "Receipts and source inspection", to: "/quality/receiving", duty: "quality.view" },
    { label: "Compliance lists", to: "/quality/compliance/lists", duty: "quality.view" },
    { label: "Item compliance", to: "/quality/compliance/items", duty: "quality.view" },
    { label: "Stability studies", to: "/quality/stability", duty: "quality.view" },
    { label: "SPC charts", to: "/quality/spc", duty: "quality.view" },
  ],
};

export default quality;
