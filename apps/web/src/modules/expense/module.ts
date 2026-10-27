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

const expense: PassageModule = {
  id: "expense",
  title: "Expense",
  description:
    "Expense policy checks, per diem for a trip, lodging cap, mileage, cash advance aging, travel requisition checks, card feed previews and card matching. Every screen is a preview; nothing is saved yet.",
  routes: [
    { path: "/expense", element: createElement(ExpenseHome), handle: { title: "Check an expense line" } },
    { path: "/expense/per-diem", element: createElement(PerDiemPage), handle: { title: "Per diem" } },
    { path: "/expense/lodging", element: createElement(LodgingCheckPage), handle: { title: "Lodging cap" } },
    { path: "/expense/mileage", element: createElement(MileagePage), handle: { title: "Mileage" } },
    { path: "/expense/cash-advances/aging", element: createElement(AdvanceAgingPage), handle: { title: "Cash advance aging" } },
    { path: "/expense/travel-requisitions/check", element: createElement(RequisitionCheckPage), handle: { title: "Travel requisition check" } },
    { path: "/expense/card-feeds/preview", element: createElement(CardFeedPage), handle: { title: "Card feed preview" } },
    { path: "/expense/card-matches", element: createElement(CardMatchPage), handle: { title: "Card matching" } },
  ],
  nav: [
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
