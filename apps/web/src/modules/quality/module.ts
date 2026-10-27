import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AssociationsPage, NewAssociationPage } from "./AssociationsPage";
import { InstrumentsPage, NewInstrumentPage } from "./InstrumentsPage";
import { NewOrderPage } from "./NewOrderPage";
import { OrderDetailPage } from "./OrderDetailPage";
import { OrdersPage } from "./OrdersPage";
import { SamplingPage } from "./SamplingPage";
import { NewTestGroupPage, TestGroupsPage } from "./TestGroupsPage";

const quality: PassageModule = {
  id: "quality",
  title: "Quality",
  description: "Quality orders with results and usage decisions, test groups, instruments, quality associations and AQL sampling.",
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
  ],
  nav: [
    { label: "Quality orders", to: "/quality/orders", duty: "QM_VIEW" },
    { label: "New quality order", to: "/quality/orders/new", duty: "QM_ORDER_MAINTAIN" },
    { label: "Test groups", to: "/quality/test-groups", duty: "QM_VIEW" },
    { label: "Instruments", to: "/quality/instruments", duty: "QM_VIEW" },
    { label: "Quality associations", to: "/quality/associations", duty: "QM_VIEW" },
    { label: "Sampling calculator", to: "/quality/sampling", duty: "QM_VIEW" },
  ],
};

export default quality;
