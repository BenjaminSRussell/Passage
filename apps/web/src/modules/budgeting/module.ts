import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BudgetCheckPage, CheckDetailPage } from "./CheckPage";
import { CostControlPage } from "./CostControlPage";
import { EncumbrancePage } from "./EncumbrancePage";
import { RegisterEntryPage } from "./RegisterEntryPage";
import { CreateProgramPage, ProgramDetailPage, ProgramsPage } from "./ProgramsPage";
import { CreateReservationPage, ReservationDetailPage, ReservationsPage } from "./ReservationsPage";
import { NewPlanPage, PlanDetailPage, PlanGridPage, PlansPage } from "./PlanPages";
import { RuleDetailPage } from "./RuleDetailPage";
import { CreateRulePage, RulesPage } from "./RulesPage";

const budgeting: PassageModule = {
  id: "budgeting",
  title: "Budgeting",
  description: "Budget control rules, register entries, available funds, budget checks, encumbrances, earmarked funds, investment programs and budget plans with a scenario grid.",
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
    { path: "/budgeting/cost-control", element: createElement(CostControlPage), handle: { title: "Cost control" } },
    { path: "/budgeting/plans", element: createElement(PlansPage), handle: { title: "Budget plans" } },
    { path: "/budgeting/plans/new", element: createElement(NewPlanPage), handle: { title: "New budget plan" } },
    { path: "/budgeting/plans/:id", element: createElement(PlanDetailPage), handle: { title: "Budget plan" } },
    { path: "/budgeting/plans/:id/scenarios/:sid", element: createElement(PlanGridPage), handle: { title: "Plan grid" } },
    { path: "/budgeting/programs", element: createElement(ProgramsPage), handle: { title: "Investment programs" } },
    { path: "/budgeting/programs/new", element: createElement(CreateProgramPage), handle: { title: "New investment program" } },
    { path: "/budgeting/programs/:id", element: createElement(ProgramDetailPage), handle: { title: "Investment program" } },
  ],
  nav: [
    { label: "Budget control rules", to: "/budgeting", duty: "budget.view" },
    { label: "Budget check", to: "/budgeting/check", duty: "budget.check" },
    { label: "Cost control", to: "/budgeting/cost-control", duty: "budget.view" },
    { label: "Budget plans", to: "/budgeting/plans", duty: "budget.view" },
    { label: "Earmarked funds", to: "/budgeting/reservations", duty: "budget.view" },
    { label: "Investment programs", to: "/budgeting/programs", duty: "budget.view" },
  ],
};

export default budgeting;
