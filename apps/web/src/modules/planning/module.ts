import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CalculatorsPage } from "./calculators";
import { ForecastPage } from "./ForecastPage";
import { MessagesPage, PlannedOrderPage, PlannedOrdersPage, PlanningHome, PlanRunsPage } from "./pages";
import { RunPlanPage } from "./RunPlanPage";

const planning: PassageModule = {
  id: "planning",
  title: "Master planning",
  description:
    "Run the plan, review, change, approve and firm planned orders, read messages, explain any planned order back to its demand, and maintain the forecast.",
  routes: [
    { path: "/planning", element: createElement(PlanningHome) },
    { path: "/planning/planned-orders", element: createElement(PlannedOrdersPage) },
    { path: "/planning/planned-orders/:id", element: createElement(PlannedOrderPage) },
    { path: "/planning/messages", element: createElement(MessagesPage) },
    { path: "/planning/runs", element: createElement(PlanRunsPage) },
    { path: "/planning/runs/new", element: createElement(RunPlanPage) },
    { path: "/planning/forecast", element: createElement(ForecastPage) },
    { path: "/planning/calculators", element: createElement(CalculatorsPage) },
  ],
  nav: [
    { label: "Workspace", to: "/planning", duty: "planning.read" },
    { label: "Planned orders", to: "/planning/planned-orders", duty: "planning.read" },
    { label: "Action messages", to: "/planning/messages", duty: "planning.read" },
    { label: "Plan runs", to: "/planning/runs", duty: "planning.read" },
    { label: "Run plan", to: "/planning/runs/new", duty: "planning.run" },
    { label: "Forecast", to: "/planning/forecast", duty: "planning.read" },
    { label: "Calculators", to: "/planning/calculators", duty: "planning.read" },
  ],
};

export default planning;
