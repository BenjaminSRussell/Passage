import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BankStatementImportPage, BankStatementsPage } from "./BankStatements";
import { CaseDetailPage, CasesPage, NewCasePage } from "./Cases";
import { ConfirmationRunDetailPage, ConfirmationRunsPage, NewConfirmationRunPage } from "./Confirmations";
import { DeductionDetailPage, DeductionsPage, NewDeductionPage } from "./Deductions";
import { DisputeDetailPage, DisputesPage, NewDisputePage } from "./Disputes";
import { DownPaymentDetailPage, DownPaymentsPage, NewDownPaymentPage } from "./DownPayments";
import { DunningRunDetailPage, DunningRunsPage, NewDunningRunPage } from "./Dunning";
import { GLJournalsPage } from "./GLJournals";
import { InterestNotesPage, InterestRunPage } from "./Interest";
import { LockboxImportPage, LockboxPage } from "./Lockbox";
import { NewPaymentPage } from "./NewPaymentPage";
import { OpenItemsPage } from "./OpenItemsPage";
import { OpeningImportsPage } from "./OpeningImports";
import { PaymentDetailPage } from "./PaymentDetailPage";
import { PaymentsPage } from "./PaymentsPage";
import { RemittanceAdvicePage } from "./RemittanceAdvice";
import { BalanceSnapshotsPage, StatementPage } from "./Statements";
import { NewPromisePage, PromisesPage } from "./Promises";
import { WorklistPage } from "./WorklistPage";
import { WorkspacePage } from "./WorkspacePage";

const receivables: PassageModule = {
  id: "receivables",
  title: "Accounts receivable",
  description:
    "Open items (including those the migration loader imports at cutover), aging and cash application (manual, lockbox and camt.053 bank statements, payer remittance advices), the journals owed to the ledger, collections (worklist, cases, disputes, promises to pay, dunning and interest), the deductions workbench, balance confirmations, down payments and security deposits, customer statements and the nightly balance snapshots.",
  routes: [
    { path: "/receivables", element: createElement(WorkspacePage), handle: { title: "AR workspace" } },
    { path: "/receivables/open-items", element: createElement(OpenItemsPage), handle: { title: "Open items" } },
    { path: "/receivables/opening-items", element: createElement(OpeningImportsPage), handle: { title: "Imported opening items" } },
    { path: "/receivables/payments", element: createElement(PaymentsPage), handle: { title: "Payments" } },
    { path: "/receivables/payments/new", element: createElement(NewPaymentPage), handle: { title: "Record payment" } },
    { path: "/receivables/payments/:id", element: createElement(PaymentDetailPage), handle: { title: "Payment" } },
    { path: "/receivables/remittance-advice", element: createElement(RemittanceAdvicePage), handle: { title: "Remittance advice" } },
    { path: "/receivables/gl-journals", element: createElement(GLJournalsPage), handle: { title: "Ledger journals" } },
    { path: "/receivables/lockbox", element: createElement(LockboxPage), handle: { title: "Lockbox imports" } },
    { path: "/receivables/lockbox/new", element: createElement(LockboxImportPage), handle: { title: "Import lockbox file" } },
    { path: "/receivables/bank-statements", element: createElement(BankStatementsPage), handle: { title: "Bank statements" } },
    { path: "/receivables/bank-statements/new", element: createElement(BankStatementImportPage), handle: { title: "Import bank statement" } },
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
    { path: "/receivables/deductions", element: createElement(DeductionsPage), handle: { title: "Deductions" } },
    { path: "/receivables/deductions/new", element: createElement(NewDeductionPage), handle: { title: "Record deduction" } },
    { path: "/receivables/deductions/:id", element: createElement(DeductionDetailPage), handle: { title: "Deduction" } },
    { path: "/receivables/statements", element: createElement(StatementPage), handle: { title: "Customer statement" } },
    { path: "/receivables/balance-snapshots", element: createElement(BalanceSnapshotsPage), handle: { title: "Balance snapshots" } },
  ],
  nav: [
    { label: "AR workspace", to: "/receivables", duty: "receivables.view" },
    { label: "Open items", to: "/receivables/open-items", duty: "receivables.view" },
    { label: "Imported opening items", to: "/receivables/opening-items", duty: "receivables.view" },
    { label: "Payments", to: "/receivables/payments", duty: "receivables.view" },
    { label: "Record payment", to: "/receivables/payments/new", duty: "receivables.payment.post" },
    { label: "Remittance advice", to: "/receivables/remittance-advice", duty: "receivables.payment.post" },
    { label: "Lockbox imports", to: "/receivables/lockbox", duty: "receivables.view" },
    { label: "Bank statements", to: "/receivables/bank-statements", duty: "receivables.view" },
    { label: "Collections worklist", to: "/receivables/worklist", duty: "receivables.view" },
    { label: "Disputes", to: "/receivables/disputes", duty: "receivables.view" },
    { label: "Promises to pay", to: "/receivables/promises", duty: "receivables.view" },
    { label: "Dunning runs", to: "/receivables/dunning-runs", duty: "receivables.view" },
    { label: "Interest notes", to: "/receivables/interest-notes", duty: "receivables.view" },
    { label: "Collection cases", to: "/receivables/cases", duty: "receivables.view" },
    { label: "Balance confirmations", to: "/receivables/balance-confirmations", duty: "receivables.view" },
    { label: "Down payments", to: "/receivables/down-payments", duty: "receivables.view" },
    { label: "Deductions", to: "/receivables/deductions", duty: "receivables.view" },
    { label: "Customer statements", to: "/receivables/statements", duty: "receivables.view" },
    { label: "Balance snapshots", to: "/receivables/balance-snapshots", duty: "receivables.view" },
    { label: "Ledger journals", to: "/receivables/gl-journals", duty: "receivables.view" },
  ],
};

export default receivables;
