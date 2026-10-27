import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AllocationPreviewPage } from "./AllocationPreviewPage";
import { InvoiceDetailPage } from "./InvoiceDetailPage";
import { InvoicesPage } from "./InvoicesPage";
import { NewFreeTextInvoicePage } from "./NewFreeTextInvoicePage";

const billing: PassageModule = {
  id: "billing",
  title: "Billing",
  description: "Free-text invoices, invoice posting with gap-free numbers, and revenue allocation previews.",
  routes: [
    { path: "/billing", element: createElement(InvoicesPage), handle: { title: "Invoices" } },
    { path: "/billing/free-text-invoices/new", element: createElement(NewFreeTextInvoicePage), handle: { title: "New free-text invoice" } },
    { path: "/billing/invoices/:id", element: createElement(InvoiceDetailPage), handle: { title: "Invoice" } },
    { path: "/billing/revenue-allocation", element: createElement(AllocationPreviewPage), handle: { title: "Revenue allocation" } },
  ],
  nav: [
    { label: "Invoices", to: "/billing", duty: "billing.invoice.view" },
    { label: "New free-text invoice", to: "/billing/free-text-invoices/new", duty: "billing.invoice.maintain" },
    { label: "Revenue allocation", to: "/billing/revenue-allocation", duty: "revrec.view" },
  ],
};

export default billing;
