import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ConsolidationHome } from "./ConsolidationHome";
import { DividendsPage } from "./DividendsPage";
import { EliminationsPage } from "./EliminationsPage";
import { GroupJournalPage } from "./GroupJournalPage";
import { IcDifferencesPage } from "./IcDifferencesPage";
import { InvestmentsPage } from "./InvestmentsPage";
import { ProfitInInventoryPage } from "./ProfitInInventoryPage";
import { ReclassificationPage } from "./ReclassificationPage";
import { TranslationPage } from "./TranslationPage";
import { ValidationPage } from "./ValidationPage";

const consolidation: PassageModule = {
  id: "consolidation",
  title: "Consolidation",
  description:
    "Group consolidation arithmetic: IAS 21 translation, elimination entries E1 to E6, profit in inventory per receipt layer, two-sided intercompany differences, the equity method and divestitures, unit validation, reclassification and top-side journals.",
  routes: [
    { path: "/consolidation", element: createElement(ConsolidationHome), handle: { title: "Consolidation workspace" } },
    { path: "/consolidation/eliminations", element: createElement(EliminationsPage), handle: { title: "Elimination entries" } },
    { path: "/consolidation/translation", element: createElement(TranslationPage), handle: { title: "Currency translation" } },
    { path: "/consolidation/profit-in-inventory", element: createElement(ProfitInInventoryPage), handle: { title: "Profit in inventory" } },
    { path: "/consolidation/ic-differences", element: createElement(IcDifferencesPage), handle: { title: "Intercompany differences" } },
    { path: "/consolidation/investments", element: createElement(InvestmentsPage), handle: { title: "Investments" } },
    { path: "/consolidation/validation", element: createElement(ValidationPage), handle: { title: "Unit validation" } },
    { path: "/consolidation/dividends", element: createElement(DividendsPage), handle: { title: "Dividends within the group" } },
    { path: "/consolidation/reclassification", element: createElement(ReclassificationPage), handle: { title: "Reclassification" } },
    { path: "/consolidation/group-journals", element: createElement(GroupJournalPage), handle: { title: "Top-side group journals" } },
  ],
  nav: [
    { label: "Consolidation workspace", to: "/consolidation", duty: "consolidation.view" },
    { label: "Elimination entries", to: "/consolidation/eliminations", duty: "consolidation.run" },
    { label: "Currency translation", to: "/consolidation/translation", duty: "consolidation.run" },
    { label: "Profit in inventory", to: "/consolidation/profit-in-inventory", duty: "consolidation.run" },
    { label: "Intercompany differences", to: "/consolidation/ic-differences", duty: "consolidation.run" },
    { label: "Investments", to: "/consolidation/investments", duty: "consolidation.run" },
    { label: "Unit validation", to: "/consolidation/validation", duty: "consolidation.run" },
    { label: "Dividends within the group", to: "/consolidation/dividends", duty: "consolidation.run" },
    { label: "Reclassification", to: "/consolidation/reclassification", duty: "consolidation.run" },
    { label: "Top-side group journals", to: "/consolidation/group-journals", duty: "consolidation.journal.maintain" },
  ],
};

export default consolidation;
