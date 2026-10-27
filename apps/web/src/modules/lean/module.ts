import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BoardPage } from "./BoardPage";
import { FlowDetailPage, FlowsPage, NewFlowPage } from "./FlowPages";
import { LeanHome } from "./pages";
import { NewRulePage, RuleDetailPage, RulesPage } from "./RulePages";

const lean: PassageModule = {
  id: "lean",
  title: "Lean manufacturing",
  description: "Production flows, kanban rules and the kanban board for work cells.",
  routes: [
    { path: "/lean", element: createElement(LeanHome), handle: { title: "Lean manufacturing" } },
    { path: "/lean/board", element: createElement(BoardPage), handle: { title: "Kanban board" } },
    { path: "/lean/rules", element: createElement(RulesPage), handle: { title: "Kanban rules" } },
    { path: "/lean/rules/new", element: createElement(NewRulePage), handle: { title: "New kanban rule" } },
    { path: "/lean/rules/:id", element: createElement(RuleDetailPage), handle: { title: "Kanban rule" } },
    { path: "/lean/flows", element: createElement(FlowsPage), handle: { title: "Production flows" } },
    { path: "/lean/flows/new", element: createElement(NewFlowPage), handle: { title: "New production flow" } },
    { path: "/lean/flows/:id", element: createElement(FlowDetailPage), handle: { title: "Production flow" } },
  ],
  nav: [
    { label: "Overview", to: "/lean", duty: "lean.read" },
    { label: "Kanban board", to: "/lean/board", duty: "lean.kanban.work" },
    { label: "Kanban rules", to: "/lean/rules", duty: "lean.read" },
    { label: "Production flows", to: "/lean/flows", duty: "lean.read" },
  ],
};

export default lean;
