import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewPaymentPage } from "./NewPaymentPage";
import { OpenItemsPage } from "./OpenItemsPage";
import { PaymentDetailPage } from "./PaymentDetailPage";
import { PaymentsPage } from "./PaymentsPage";
import { WorkspacePage } from "./WorkspacePage";

const receivables: PassageModule = {
  id: "receivables",
  title: "Accounts receivable",
  description: "Open items, aging and cash application: record customer payments, score the matches and apply them.",
  routes: [
    { path: "/receivables", element: createElement(WorkspacePage), handle: { title: "AR workspace" } },
    { path: "/receivables/open-items", element: createElement(OpenItemsPage), handle: { title: "Open items" } },
    { path: "/receivables/payments", element: createElement(PaymentsPage), handle: { title: "Payments" } },
    { path: "/receivables/payments/new", element: createElement(NewPaymentPage), handle: { title: "Record payment" } },
    { path: "/receivables/payments/:id", element: createElement(PaymentDetailPage), handle: { title: "Payment" } },
  ],
  nav: [
    { label: "AR workspace", to: "/receivables", duty: "ARView" },
    { label: "Open items", to: "/receivables/open-items", duty: "ARView" },
    { label: "Payments", to: "/receivables/payments", duty: "ARView" },
    { label: "Record payment", to: "/receivables/payments/new", duty: "ARPaymentPost" },
  ],
};

export default receivables;
