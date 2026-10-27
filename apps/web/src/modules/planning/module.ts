import { createElement } from "react";
import type { PassageModule } from "../registry";
import { MessagesPage, PlannedOrderPage, PlannedOrdersPage, PlanningHome, PlanRunsPage } from "./pages";
import { RunPlanPage } from "./RunPlanPage";

const planning: PassageModule = {
  id: "planning",
  title: "Master planning",
  description: "Run the plan, review planned orders and messages, and explain any planned order back to its demand.",
  routes: [
    { path: "/planning", element: createElement(PlanningHome) },
    { path: "/planning/planned-orders", element: createElement(PlannedOrdersPage) },
    { path: "/planning/planned-orders/:id", element: createElement(PlannedOrderPage) },
    { path: "/planning/messages", element: createElement(MessagesPage) },
    { path: "/planning/runs", element: createElement(PlanRunsPage) },
    { path: "/planning/runs/new", element: createElement(RunPlanPage) },
  ],
  nav: [
    { label: "Workspace", to: "/planning", duty: "planning.read" },
    { label: "Planned orders", to: "/planning/planned-orders", duty: "planning.read" },
    { label: "Action messages", to: "/planning/messages", duty: "planning.read" },
    { label: "Plan runs", to: "/planning/runs", duty: "planning.read" },
    { label: "Run plan", to: "/planning/runs/new", duty: "planning.run" },
  ],
};

export default planning;
