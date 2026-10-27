import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BudgetCheckPage, CheckDetailPage } from "./CheckPage";
import { EncumbrancePage } from "./EncumbrancePage";
import { RegisterEntryPage } from "./RegisterEntryPage";
import { CreateReservationPage, ReservationDetailPage, ReservationsPage } from "./ReservationsPage";
import { RuleDetailPage } from "./RuleDetailPage";
import { CreateRulePage, RulesPage } from "./RulesPage";

const budgeting: PassageModule = {
  id: "budgeting",
  title: "Budgeting",
  description: "Budget control rules, register entries, available funds, budget checks, encumbrances and earmarked funds.",
  routes: [
    { path: "/budgeting", element: createElement(RulesPage), handle: { title: "Budget control rules" } },
    { path: "/budgeting/rules/new", element: createElement(CreateRulePage), handle: { title: "New rule version" } },
    { path: "/budgeting/rules/:id", element: createElement(RuleDetailPage), handle: { title: "Budget control rule" } },
    { path: "/budgeting/rules/:id/entries/new", element: createElement(RegisterEntryPage), handle: { title: "Post register entry" } },
    { path: "/budgeting/check", element: createElement(BudgetCheckPage), handle: { title: "Budget check" } },
    { path: "/budgeting/checks/:id", element: createElement(CheckDetailPage), handle: { title: "Budget check result" } },
    { path: "/budgeting/encumbrances/:id", element: createElement(EncumbrancePage), handle: { title: "Encumbrance" } },
    { path: "/budgeting/reservations", element: createElement(ReservationsPage), handle: { title: "Earmarked funds" } },
    { path: "/budgeting/reservations/new", element: createElement(CreateReservationPage), handle: { title: "New reservation" } },
    { path: "/budgeting/reservations/:id", element: createElement(ReservationDetailPage), handle: { title: "Reservation" } },
  ],
  nav: [
    { label: "Budget control rules", to: "/budgeting", duty: "budget.view" },
    { label: "Budget check", to: "/budgeting/check", duty: "budget.check" },
    { label: "Earmarked funds", to: "/budgeting/reservations", duty: "budget.view" },
  ],
};

export default budgeting;
