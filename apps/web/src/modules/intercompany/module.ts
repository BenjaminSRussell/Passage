import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CentralPaymentsPage } from "./CentralPaymentsPage";
import { ChargesPage } from "./ChargesPage";
import { IntercompanyHome } from "./IntercompanyHome";
import { JournalPreviewPage } from "./JournalPreviewPage";
import { PlanningPage } from "./PlanningPage";
import { ReconciliationPage } from "./ReconciliationPage";

const intercompany: PassageModule = {
  id: "intercompany",
  title: "Intercompany",
  description:
    "Due-to and due-from journals between legal entities, reconciliation of both sides' balances, intercompany charges priced by transfer price, centralized payments and the planning group run order.",
  routes: [
    { path: "/intercompany", element: createElement(IntercompanyHome), handle: { title: "Intercompany workspace" } },
    { path: "/intercompany/journal", element: createElement(JournalPreviewPage), handle: { title: "Intercompany journal" } },
    { path: "/intercompany/reconciliation", element: createElement(ReconciliationPage), handle: { title: "Intercompany reconciliation" } },
    { path: "/intercompany/charges", element: createElement(ChargesPage), handle: { title: "Intercompany charges" } },
    { path: "/intercompany/centralized-payments", element: createElement(CentralPaymentsPage), handle: { title: "Centralized payments" } },
    { path: "/intercompany/planning", element: createElement(PlanningPage), handle: { title: "Intercompany planning" } },
  ],
  nav: [
    { label: "Intercompany workspace", to: "/intercompany", duty: "IntercompanyView" },
    { label: "Intercompany journal", to: "/intercompany/journal", duty: "IntercompanyPost" },
    { label: "Reconciliation", to: "/intercompany/reconciliation", duty: "IntercompanyReconcile" },
    { label: "Intercompany charges", to: "/intercompany/charges", duty: "IntercompanyPost" },
    { label: "Centralized payments", to: "/intercompany/centralized-payments", duty: "IntercompanyConfigure" },
    { label: "Intercompany planning", to: "/intercompany/planning", duty: "IntercompanyPlan" },
  ],
};

export default intercompany;
