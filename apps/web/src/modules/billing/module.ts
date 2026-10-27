import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AllocationPreviewPage } from "./AllocationPreviewPage";
import { CreditNotesPage } from "./CreditNotesPage";
import { DeferredRevenuePage } from "./DeferredRevenuePage";
import { InvoiceDetailPage } from "./InvoiceDetailPage";
import { InvoicesPage } from "./InvoicesPage";
import { NewCreditNotePage } from "./NewCreditNotePage";
import { NewFreeTextInvoicePage } from "./NewFreeTextInvoicePage";
import { NewRevenueContractPage } from "./NewRevenueContractPage";
import { RevenueContractDetailPage } from "./RevenueContractDetailPage";
import { RevenueContractsPage } from "./RevenueContractsPage";

const billing: PassageModule = {
  id: "billing",
  title: "Billing",
  description:
    "Free-text invoices with installment plans, invoice posting with gap-free numbers, credit notes, revenue contracts with milestones and schedules, and the deferred revenue roll-forward.",
  routes: [
    { path: "/billing", element: createElement(InvoicesPage), handle: { title: "Invoices" } },
    { path: "/billing/free-text-invoices/new", element: createElement(NewFreeTextInvoicePage), handle: { title: "New free-text invoice" } },
    { path: "/billing/invoices/:id", element: createElement(InvoiceDetailPage), handle: { title: "Invoice" } },
    { path: "/billing/credit-notes", element: createElement(CreditNotesPage), handle: { title: "Credit notes" } },
    { path: "/billing/credit-notes/new", element: createElement(NewCreditNotePage), handle: { title: "New credit note" } },
    { path: "/billing/revenue-allocation", element: createElement(AllocationPreviewPage), handle: { title: "Revenue allocation" } },
    { path: "/billing/revenue-contracts", element: createElement(RevenueContractsPage), handle: { title: "Revenue contracts" } },
    { path: "/billing/revenue-contracts/new", element: createElement(NewRevenueContractPage), handle: { title: "New revenue contract" } },
    { path: "/billing/revenue-contracts/:id", element: createElement(RevenueContractDetailPage), handle: { title: "Revenue contract" } },
    { path: "/billing/deferred-revenue", element: createElement(DeferredRevenuePage), handle: { title: "Deferred revenue" } },
  ],
  nav: [
    { label: "Invoices", to: "/billing", duty: "billing.invoice.view" },
    { label: "New free-text invoice", to: "/billing/free-text-invoices/new", duty: "billing.invoice.maintain" },
    { label: "Credit notes", to: "/billing/credit-notes", duty: "billing.invoice.view" },
    { label: "Revenue contracts", to: "/billing/revenue-contracts", duty: "revrec.view" },
    { label: "Deferred revenue", to: "/billing/deferred-revenue", duty: "revrec.view" },
    { label: "Revenue allocation", to: "/billing/revenue-allocation", duty: "revrec.view" },
  ],
};

export default billing;
