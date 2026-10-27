import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ConsolidationHome } from "./ConsolidationHome";
import { EliminationsPage } from "./EliminationsPage";
import { TranslationPage } from "./TranslationPage";

const consolidation: PassageModule = {
  id: "consolidation",
  title: "Consolidation",
  description:
    "Group consolidation arithmetic: IAS 21 translation with the CTA and its NCI share, and elimination entries E1 to E5 with goodwill and the non-controlling interest.",
  routes: [
    { path: "/consolidation", element: createElement(ConsolidationHome), handle: { title: "Consolidation workspace" } },
    { path: "/consolidation/eliminations", element: createElement(EliminationsPage), handle: { title: "Elimination entries" } },
    { path: "/consolidation/translation", element: createElement(TranslationPage), handle: { title: "Currency translation" } },
  ],
  nav: [
    { label: "Consolidation workspace", to: "/consolidation", duty: "ConsolidationView" },
    { label: "Elimination entries", to: "/consolidation/eliminations", duty: "ConsolidationRun" },
    { label: "Currency translation", to: "/consolidation/translation", duty: "ConsolidationRun" },
  ],
};

export default consolidation;
