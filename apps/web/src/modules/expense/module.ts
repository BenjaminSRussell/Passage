import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AdvanceAgingPage } from "./AdvanceAgingPage";
import { AdvanceDetailPage, AdvancesPage, NewAdvancePage } from "./advances";
import { CardImportPage, CardTransactionsPage } from "./cards";
import { CardFeedPage } from "./CardFeedPage";
import { CardMatchPage } from "./CardMatchPage";
import { LodgingCheckPage } from "./LodgingCheckPage";
import { MileagePage } from "./MileagePage";
import { ExpenseHome } from "./pages";
import { PerDiemPage } from "./PerDiemPage";
import { RequisitionCheckPage } from "./RequisitionCheckPage";
import { NewReportPage, ReportDetailPage, ReportsPage } from "./reports";
import { NewRequisitionPage, RequisitionDetailPage, RequisitionsPage } from "./requisitions";

const expense: PassageModule = {
  id: "expense",
  title: "Expense",
  description:
    "Expense reports with their lines, policy results and submit; cash advances with approval, AP payment and cash returns; travel requisitions with approval; card statement import into the cardholders' pools; plus previews that save nothing: policy checks, per diem for a trip, lodging cap, mileage, cash advance aging, travel requisition checks, card feed previews and card matching.",
  routes: [
    { path: "/expense", element: createElement(ExpenseHome), handle: { title: "Check an expense line" } },
    { path: "/expense/reports", element: createElement(ReportsPage), handle: { title: "Expense reports" } },
    { path: "/expense/reports/new", element: createElement(NewReportPage), handle: { title: "New expense report" } },
    { path: "/expense/reports/:id", element: createElement(ReportDetailPage), handle: { title: "Expense report" } },
    { path: "/expense/cash-advances", element: createElement(AdvancesPage), handle: { title: "Cash advances" } },
    { path: "/expense/cash-advances/new", element: createElement(NewAdvancePage), handle: { title: "Request a cash advance" } },
    { path: "/expense/cash-advances/:id", element: createElement(AdvanceDetailPage), handle: { title: "Cash advance" } },
    { path: "/expense/travel-requisitions", element: createElement(RequisitionsPage), handle: { title: "Travel requisitions" } },
    { path: "/expense/travel-requisitions/new", element: createElement(NewRequisitionPage), handle: { title: "New travel requisition" } },
    { path: "/expense/travel-requisitions/:id", element: createElement(RequisitionDetailPage), handle: { title: "Travel requisition" } },
    { path: "/expense/card-transactions", element: createElement(CardTransactionsPage), handle: { title: "Card transactions" } },
    { path: "/expense/card-transactions/import", element: createElement(CardImportPage), handle: { title: "Import a card statement" } },
    { path: "/expense/per-diem", element: createElement(PerDiemPage), handle: { title: "Per diem" } },
    { path: "/expense/lodging", element: createElement(LodgingCheckPage), handle: { title: "Lodging cap" } },
    { path: "/expense/mileage", element: createElement(MileagePage), handle: { title: "Mileage" } },
    { path: "/expense/cash-advances/aging", element: createElement(AdvanceAgingPage), handle: { title: "Cash advance aging" } },
    { path: "/expense/travel-requisitions/check", element: createElement(RequisitionCheckPage), handle: { title: "Travel requisition check" } },
    { path: "/expense/card-feeds/preview", element: createElement(CardFeedPage), handle: { title: "Card feed preview" } },
    { path: "/expense/card-matches", element: createElement(CardMatchPage), handle: { title: "Card matching" } },
  ],
  nav: [
    { label: "Expense reports", to: "/expense/reports", duty: "expense.report.write" },
    { label: "Cash advances", to: "/expense/cash-advances", duty: "expense.report.write" },
    { label: "Travel requisitions", to: "/expense/travel-requisitions", duty: "expense.report.write" },
    { label: "Card transactions", to: "/expense/card-transactions", duty: "expense.report.write" },
    { label: "Import a card statement", to: "/expense/card-transactions/import", duty: "expense.card.import" },
    { label: "Check an expense line", to: "/expense", duty: "expense.report.write" },
    { label: "Per diem", to: "/expense/per-diem", duty: "expense.report.write" },
    { label: "Lodging cap", to: "/expense/lodging", duty: "expense.report.write" },
    { label: "Mileage", to: "/expense/mileage", duty: "expense.report.write" },
    { label: "Cash advance aging", to: "/expense/cash-advances/aging", duty: "expense.report.write" },
    { label: "Travel requisition check", to: "/expense/travel-requisitions/check", duty: "expense.report.write" },
    { label: "Card feed preview", to: "/expense/card-feeds/preview", duty: "expense.card.import" },
    { label: "Card matching", to: "/expense/card-matches", duty: "expense.report.write" },
  ],
};

export default expense;
