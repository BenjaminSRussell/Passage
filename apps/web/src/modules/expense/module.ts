import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AdvanceAgingPage } from "./AdvanceAgingPage";
import { CardFeedPage } from "./CardFeedPage";
import { CardMatchPage } from "./CardMatchPage";
import { LodgingCheckPage } from "./LodgingCheckPage";
import { MileagePage } from "./MileagePage";
import { ExpenseHome } from "./pages";
import { PerDiemPage } from "./PerDiemPage";
import { RequisitionCheckPage } from "./RequisitionCheckPage";
import { NewReportPage, ReportDetailPage, ReportsPage } from "./reports";

const expense: PassageModule = {
  id: "expense",
  title: "Expense",
  description:
    "Expense reports with their lines, policy results and submit, plus previews that save nothing: policy checks, per diem for a trip, lodging cap, mileage, cash advance aging, travel requisition checks, card feed previews and card matching.",
  routes: [
    { path: "/expense", element: createElement(ExpenseHome), handle: { title: "Check an expense line" } },
    { path: "/expense/reports", element: createElement(ReportsPage), handle: { title: "Expense reports" } },
    { path: "/expense/reports/new", element: createElement(NewReportPage), handle: { title: "New expense report" } },
    { path: "/expense/reports/:id", element: createElement(ReportDetailPage), handle: { title: "Expense report" } },
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
