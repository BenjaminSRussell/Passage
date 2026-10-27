import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CaseDetailPage, CasesPage, NewCasePage } from "./Cases";
import { ConfirmationRunDetailPage, ConfirmationRunsPage, NewConfirmationRunPage } from "./Confirmations";
import { DisputeDetailPage, DisputesPage, NewDisputePage } from "./Disputes";
import { DownPaymentDetailPage, DownPaymentsPage, NewDownPaymentPage } from "./DownPayments";
import { DunningRunDetailPage, DunningRunsPage, NewDunningRunPage } from "./Dunning";
import { InterestNotesPage, InterestRunPage } from "./Interest";
import { LockboxImportPage, LockboxPage } from "./Lockbox";
import { NewPaymentPage } from "./NewPaymentPage";
import { OpenItemsPage } from "./OpenItemsPage";
import { PaymentDetailPage } from "./PaymentDetailPage";
import { PaymentsPage } from "./PaymentsPage";
import { NewPromisePage, PromisesPage } from "./Promises";
import { WorklistPage } from "./WorklistPage";
import { WorkspacePage } from "./WorkspacePage";

const receivables: PassageModule = {
  id: "receivables",
  title: "Accounts receivable",
  description:
    "Open items, aging and cash application (manual and lockbox), collections (worklist, cases, disputes, promises to pay, dunning and interest), balance confirmations, and down payments and security deposits.",
  routes: [
    { path: "/receivables", element: createElement(WorkspacePage), handle: { title: "AR workspace" } },
    { path: "/receivables/open-items", element: createElement(OpenItemsPage), handle: { title: "Open items" } },
    { path: "/receivables/payments", element: createElement(PaymentsPage), handle: { title: "Payments" } },
    { path: "/receivables/payments/new", element: createElement(NewPaymentPage), handle: { title: "Record payment" } },
    { path: "/receivables/payments/:id", element: createElement(PaymentDetailPage), handle: { title: "Payment" } },
    { path: "/receivables/lockbox", element: createElement(LockboxPage), handle: { title: "Lockbox imports" } },
    { path: "/receivables/lockbox/new", element: createElement(LockboxImportPage), handle: { title: "Import lockbox file" } },
    { path: "/receivables/worklist", element: createElement(WorklistPage), handle: { title: "Collections worklist" } },
    { path: "/receivables/disputes", element: createElement(DisputesPage), handle: { title: "Disputes" } },
    { path: "/receivables/disputes/new", element: createElement(NewDisputePage), handle: { title: "Open dispute" } },
    { path: "/receivables/disputes/:id", element: createElement(DisputeDetailPage), handle: { title: "Dispute" } },
    { path: "/receivables/promises", element: createElement(PromisesPage), handle: { title: "Promises to pay" } },
    { path: "/receivables/promises/new", element: createElement(NewPromisePage), handle: { title: "Record promise" } },
    { path: "/receivables/dunning-runs", element: createElement(DunningRunsPage), handle: { title: "Dunning runs" } },
    { path: "/receivables/dunning-runs/new", element: createElement(NewDunningRunPage), handle: { title: "New dunning run" } },
    { path: "/receivables/dunning-runs/:id", element: createElement(DunningRunDetailPage), handle: { title: "Dunning run" } },
    { path: "/receivables/interest-notes", element: createElement(InterestNotesPage), handle: { title: "Interest notes" } },
    { path: "/receivables/interest-notes/new", element: createElement(InterestRunPage), handle: { title: "Calculate interest" } },
    { path: "/receivables/cases", element: createElement(CasesPage), handle: { title: "Collection cases" } },
    { path: "/receivables/cases/new", element: createElement(NewCasePage), handle: { title: "Open collection case" } },
    { path: "/receivables/cases/:id", element: createElement(CaseDetailPage), handle: { title: "Collection case" } },
    { path: "/receivables/balance-confirmations", element: createElement(ConfirmationRunsPage), handle: { title: "Balance confirmations" } },
    { path: "/receivables/balance-confirmations/new", element: createElement(NewConfirmationRunPage), handle: { title: "New balance confirmation run" } },
    { path: "/receivables/balance-confirmations/:id", element: createElement(ConfirmationRunDetailPage), handle: { title: "Balance confirmation run" } },
    { path: "/receivables/down-payments", element: createElement(DownPaymentsPage), handle: { title: "Down payments and deposits" } },
    { path: "/receivables/down-payments/new", element: createElement(NewDownPaymentPage), handle: { title: "Request down payment" } },
    { path: "/receivables/down-payments/:id", element: createElement(DownPaymentDetailPage), handle: { title: "Down payment" } },
  ],
  nav: [
    { label: "AR workspace", to: "/receivables", duty: "ARView" },
    { label: "Open items", to: "/receivables/open-items", duty: "ARView" },
    { label: "Payments", to: "/receivables/payments", duty: "ARView" },
    { label: "Record payment", to: "/receivables/payments/new", duty: "ARPaymentPost" },
    { label: "Lockbox imports", to: "/receivables/lockbox", duty: "ARView" },
    { label: "Collections worklist", to: "/receivables/worklist", duty: "ARView" },
    { label: "Disputes", to: "/receivables/disputes", duty: "ARView" },
    { label: "Promises to pay", to: "/receivables/promises", duty: "ARView" },
    { label: "Dunning runs", to: "/receivables/dunning-runs", duty: "ARView" },
    { label: "Interest notes", to: "/receivables/interest-notes", duty: "ARView" },
    { label: "Collection cases", to: "/receivables/cases", duty: "ARView" },
    { label: "Balance confirmations", to: "/receivables/balance-confirmations", duty: "ARView" },
    { label: "Down payments", to: "/receivables/down-payments", duty: "ARView" },
  ],
};

export default receivables;
