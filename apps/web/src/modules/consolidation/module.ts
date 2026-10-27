import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ConsolidationHome } from "./ConsolidationHome";
import { EliminationsPage } from "./EliminationsPage";
import { IcDifferencesPage } from "./IcDifferencesPage";
import { InvestmentsPage } from "./InvestmentsPage";
import { ProfitInInventoryPage } from "./ProfitInInventoryPage";
import { TranslationPage } from "./TranslationPage";
import { ValidationPage } from "./ValidationPage";

const consolidation: PassageModule = {
  id: "consolidation",
  title: "Consolidation",
  description:
    "Group consolidation arithmetic: IAS 21 translation, elimination entries E1 to E5, profit in inventory per receipt layer, two-sided intercompany differences, the equity method and divestitures, and unit validation.",
  routes: [
    { path: "/consolidation", element: createElement(ConsolidationHome), handle: { title: "Consolidation workspace" } },
    { path: "/consolidation/eliminations", element: createElement(EliminationsPage), handle: { title: "Elimination entries" } },
    { path: "/consolidation/translation", element: createElement(TranslationPage), handle: { title: "Currency translation" } },
    { path: "/consolidation/profit-in-inventory", element: createElement(ProfitInInventoryPage), handle: { title: "Profit in inventory" } },
    { path: "/consolidation/ic-differences", element: createElement(IcDifferencesPage), handle: { title: "Intercompany differences" } },
    { path: "/consolidation/investments", element: createElement(InvestmentsPage), handle: { title: "Investments" } },
    { path: "/consolidation/validation", element: createElement(ValidationPage), handle: { title: "Unit validation" } },
  ],
  nav: [
    { label: "Consolidation workspace", to: "/consolidation", duty: "ConsolidationView" },
    { label: "Elimination entries", to: "/consolidation/eliminations", duty: "ConsolidationRun" },
    { label: "Currency translation", to: "/consolidation/translation", duty: "ConsolidationRun" },
    { label: "Profit in inventory", to: "/consolidation/profit-in-inventory", duty: "ConsolidationRun" },
    { label: "Intercompany differences", to: "/consolidation/ic-differences", duty: "ConsolidationRun" },
    { label: "Investments", to: "/consolidation/investments", duty: "ConsolidationRun" },
    { label: "Unit validation", to: "/consolidation/validation", duty: "ConsolidationRun" },
  ],
};

export default consolidation;
