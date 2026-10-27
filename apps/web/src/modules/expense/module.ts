import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AdvanceAgingPage } from "./AdvanceAgingPage";
import { CardFeedPage } from "./CardFeedPage";
import { ExpenseHome } from "./pages";
import { PerDiemPage } from "./PerDiemPage";
import { RequisitionCheckPage } from "./RequisitionCheckPage";

const expense: PassageModule = {
  id: "expense",
  title: "Expense",
  description:
    "Expense policy checks, per diem for a trip, cash advance aging, travel requisition checks and card feed previews. Every screen is a preview; nothing is saved yet.",
  routes: [
    { path: "/expense", element: createElement(ExpenseHome), handle: { title: "Check an expense line" } },
    { path: "/expense/per-diem", element: createElement(PerDiemPage), handle: { title: "Per diem" } },
    { path: "/expense/cash-advances/aging", element: createElement(AdvanceAgingPage), handle: { title: "Cash advance aging" } },
    { path: "/expense/travel-requisitions/check", element: createElement(RequisitionCheckPage), handle: { title: "Travel requisition check" } },
    { path: "/expense/card-feeds/preview", element: createElement(CardFeedPage), handle: { title: "Card feed preview" } },
  ],
  nav: [
    { label: "Check an expense line", to: "/expense", duty: "expense.report.write" },
    { label: "Per diem", to: "/expense/per-diem", duty: "expense.report.write" },
    { label: "Cash advance aging", to: "/expense/cash-advances/aging", duty: "expense.report.write" },
    { label: "Travel requisition check", to: "/expense/travel-requisitions/check", duty: "expense.report.write" },
    { label: "Card feed preview", to: "/expense/card-feeds/preview", duty: "expense.card.import" },
  ],
};

export default expense;
