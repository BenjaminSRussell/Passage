import { createElement } from "react";
import type { PassageModule } from "../registry";
import { IntercompanyHome } from "./IntercompanyHome";
import { JournalPreviewPage } from "./JournalPreviewPage";
import { ReconciliationPage } from "./ReconciliationPage";

const intercompany: PassageModule = {
  id: "intercompany",
  title: "Intercompany",
  description:
    "Due-to and due-from journals between legal entities from the intercompany accounting matrix, and reconciliation of both sides' balances into matched, timing and mismatch.",
  routes: [
    { path: "/intercompany", element: createElement(IntercompanyHome), handle: { title: "Intercompany workspace" } },
    { path: "/intercompany/journal", element: createElement(JournalPreviewPage), handle: { title: "Intercompany journal" } },
    { path: "/intercompany/reconciliation", element: createElement(ReconciliationPage), handle: { title: "Intercompany reconciliation" } },
  ],
  nav: [
    { label: "Intercompany workspace", to: "/intercompany", duty: "IntercompanyView" },
    { label: "Intercompany journal", to: "/intercompany/journal", duty: "IntercompanyPost" },
    { label: "Reconciliation", to: "/intercompany/reconciliation", duty: "IntercompanyReconcile" },
  ],
};

export default intercompany;
