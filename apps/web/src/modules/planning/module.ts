import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BufferProfilePage, BufferProfilesPage, DdmrpBuffersPage, NewBufferProfilePage } from "./BufferPages";
import { CalculatorsPage } from "./calculators";
import { CoverageGroupPage, CoverageGroupsPage, NewCoverageGroupPage } from "./CoverageGroupsPage";
import { ForecastPage } from "./ForecastPage";
import { ItemCoveragePage, ItemCoveragesPage, NewItemCoveragePage } from "./ItemCoveragePage";
import { PlanDiffPage } from "./PlanDiffPage";
import { NetRequirementsPage } from "./NetRequirementsPage";
import { SupplySchedulePage } from "./SupplySchedulePage";
import { MessagesPage, PlannedOrderPage, PlannedOrdersPage, PlanningHome, PlanRunsPage } from "./pages";
import { NewQuotaArrangementPage, QuotaArrangementPage, QuotaArrangementsPage } from "./QuotaPages";
import { RunPlanPage } from "./RunPlanPage";
import { NewScenarioPage, ScenarioPage, ScenariosPage } from "./ScenarioPages";
import { StrategiesPage, StrategyPage } from "./StrategyPages";

const planning: PassageModule = {
  id: "planning",
  title: "Master planning",
  description:
    "Run the plan in working days, review, change, approve and firm planned orders, read messages and net requirements, project the supply schedule and inventory value of items from the stored plan, explain any planned order back to its demand, maintain the forecast and its consensus, set up coverage groups with their fences and auto-firming, set up how each item is planned per site, warehouse or subcontractor, maintain DDMRP buffer profiles and see buffer status by zone and priority, split requirements across sources with quota arrangements, set how each planning strategy treats forecast against sales, and simulate what-if scenarios beside the operational plan.",
  routes: [
    { path: "/planning", element: createElement(PlanningHome) },
    { path: "/planning/planned-orders", element: createElement(PlannedOrdersPage) },
    { path: "/planning/planned-orders/:id", element: createElement(PlannedOrderPage) },
    { path: "/planning/messages", element: createElement(MessagesPage) },
    { path: "/planning/net-requirements", element: createElement(NetRequirementsPage) },
    { path: "/planning/supply-schedule", element: createElement(SupplySchedulePage) },
    { path: "/planning/runs", element: createElement(PlanRunsPage) },
    { path: "/planning/runs/compare", element: createElement(PlanDiffPage) },
    { path: "/planning/runs/new", element: createElement(RunPlanPage) },
    { path: "/planning/forecast", element: createElement(ForecastPage) },
    { path: "/planning/calculators", element: createElement(CalculatorsPage) },
    { path: "/planning/coverage-groups", element: createElement(CoverageGroupsPage) },
    { path: "/planning/coverage-groups/new", element: createElement(NewCoverageGroupPage) },
    { path: "/planning/coverage-groups/:code", element: createElement(CoverageGroupPage) },
    { path: "/planning/item-coverages", element: createElement(ItemCoveragesPage) },
    { path: "/planning/item-coverages/new", element: createElement(NewItemCoveragePage) },
    { path: "/planning/item-coverages/:id", element: createElement(ItemCoveragePage) },
    { path: "/planning/buffer-profiles", element: createElement(BufferProfilesPage) },
    { path: "/planning/buffer-profiles/new", element: createElement(NewBufferProfilePage) },
    { path: "/planning/buffer-profiles/:id", element: createElement(BufferProfilePage) },
    { path: "/planning/ddmrp-buffers", element: createElement(DdmrpBuffersPage) },
    { path: "/planning/quota-arrangements", element: createElement(QuotaArrangementsPage) },
    { path: "/planning/quota-arrangements/new", element: createElement(NewQuotaArrangementPage) },
    { path: "/planning/quota-arrangements/:id", element: createElement(QuotaArrangementPage) },
    { path: "/planning/strategies", element: createElement(StrategiesPage) },
    { path: "/planning/strategies/:code", element: createElement(StrategyPage) },
    { path: "/planning/scenarios", element: createElement(ScenariosPage) },
    { path: "/planning/scenarios/new", element: createElement(NewScenarioPage) },
    { path: "/planning/scenarios/:code", element: createElement(ScenarioPage) },
  ],
  nav: [
    { label: "Workspace", to: "/planning", duty: "planning.read" },
    { label: "Planned orders", to: "/planning/planned-orders", duty: "planning.read" },
    { label: "Action messages", to: "/planning/messages", duty: "planning.read" },
    { label: "Net requirements", to: "/planning/net-requirements", duty: "planning.read" },
    { label: "Supply schedule", to: "/planning/supply-schedule", duty: "planning.read" },
    { label: "Plan runs", to: "/planning/runs", duty: "planning.read" },
    { label: "Compare runs", to: "/planning/runs/compare", duty: "planning.read" },
    { label: "Run plan", to: "/planning/runs/new", duty: "planning.run" },
    { label: "Forecast", to: "/planning/forecast", duty: "planning.read" },
    { label: "Calculators", to: "/planning/calculators", duty: "planning.read" },
    { label: "Coverage groups", to: "/planning/coverage-groups", duty: "planning.read" },
    { label: "Item coverage", to: "/planning/item-coverages", duty: "planning.read" },
    { label: "Buffer profiles", to: "/planning/buffer-profiles", duty: "planning.read" },
    { label: "DDMRP buffers", to: "/planning/ddmrp-buffers", duty: "planning.read" },
    { label: "Quota arrangements", to: "/planning/quota-arrangements", duty: "planning.read" },
    { label: "Planning strategies", to: "/planning/strategies", duty: "planning.read" },
    { label: "Scenarios", to: "/planning/scenarios", duty: "planning.read" },
  ],
};

export default planning;
