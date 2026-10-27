import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BudgetCheckPage, CheckDetailPage } from "./CheckPage";
import { EncumbrancePage } from "./EncumbrancePage";
import { RegisterEntryPage } from "./RegisterEntryPage";
import { RuleDetailPage } from "./RuleDetailPage";
import { CreateRulePage, RulesPage } from "./RulesPage";

const budgeting: PassageModule = {
  id: "budgeting",
  title: "Budgeting",
  description: "Budget control rules, register entries, available funds, budget checks and encumbrances.",
  routes: [
    { path: "/budgeting", element: createElement(RulesPage), handle: { title: "Budget control rules" } },
    { path: "/budgeting/rules/new", element: createElement(CreateRulePage), handle: { title: "New rule version" } },
    { path: "/budgeting/rules/:id", element: createElement(RuleDetailPage), handle: { title: "Budget control rule" } },
    { path: "/budgeting/rules/:id/entries/new", element: createElement(RegisterEntryPage), handle: { title: "Post register entry" } },
    { path: "/budgeting/check", element: createElement(BudgetCheckPage), handle: { title: "Budget check" } },
    { path: "/budgeting/checks/:id", element: createElement(CheckDetailPage), handle: { title: "Budget check result" } },
    { path: "/budgeting/encumbrances/:id", element: createElement(EncumbrancePage), handle: { title: "Encumbrance" } },
  ],
  nav: [
    { label: "Budget control rules", to: "/budgeting", duty: "budget.view" },
    { label: "Budget check", to: "/budgeting/check", duty: "budget.check" },
  ],
};

export default budgeting;
