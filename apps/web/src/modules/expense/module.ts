import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ExpenseHome } from "./pages";

const expense: PassageModule = {
  id: "expense",
  title: "Expense",
  description: "Expense reports, receipts and policy checks.",
  routes: [{ path: "/expense", element: createElement(ExpenseHome) }],
  nav: [{ label: "Check an expense line", to: "/expense", duty: "expense.report.write" }],
};

export default expense;
