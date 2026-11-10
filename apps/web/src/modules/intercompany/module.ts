import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CentralPaymentsPage } from "./CentralPaymentsPage";
import { ChainsPage } from "./ChainsPage";
import { ChargeLogPage } from "./ChargeLogPage";
import { ChargesPage } from "./ChargesPage";
import { IntercompanyHome } from "./IntercompanyHome";
import { JournalPreviewPage } from "./JournalPreviewPage";
import { JournalDetailPage, JournalsPage } from "./JournalsPage";
import { PartnerDetailPage } from "./PartnerDetailPage";
import { PartnerFormPage } from "./PartnerFormPage";
import { PartnersPage } from "./PartnersPage";
import { PlanningPage } from "./PlanningPage";
import { ReconciliationPage } from "./ReconciliationPage";
import { ReconRunPage, ReconRunsPage } from "./ReconRunsPage";
import { TransferPricingPage } from "./TransferPricingPage";

const intercompany: PassageModule = {
  id: "intercompany",
  title: "Intercompany",
  description:
    "Trading-partner pairings with their pricing and change tolerance, due-to and due-from journals between legal entities, reconciliation of both sides' balances, intercompany charges priced by transfer price, centralized payments and their reversal, direct delivery chains, transfer-pricing evidence and the planning group run order.",
  routes: [
    { path: "/intercompany", element: createElement(IntercompanyHome), handle: { title: "Intercompany workspace" } },
    { path: "/intercompany/partners", element: createElement(PartnersPage), handle: { title: "Trading partners" } },
    { path: "/intercompany/partners/new", element: createElement(PartnerFormPage), handle: { title: "New trading partner" } },
    { path: "/intercompany/partners/:id", element: createElement(PartnerDetailPage), handle: { title: "Trading partner" } },
    { path: "/intercompany/journal", element: createElement(JournalPreviewPage), handle: { title: "Intercompany journal" } },
    { path: "/intercompany/journals", element: createElement(JournalsPage), handle: { title: "Posted intercompany journals" } },
    { path: "/intercompany/journals/:id", element: createElement(JournalDetailPage), handle: { title: "Posted intercompany journal" } },
    { path: "/intercompany/charge-log", element: createElement(ChargeLogPage), handle: { title: "Intercompany charge log" } },
    { path: "/intercompany/reconciliation-runs", element: createElement(ReconRunsPage), handle: { title: "Reconciliation runs" } },
    { path: "/intercompany/reconciliation-runs/:id", element: createElement(ReconRunPage), handle: { title: "Reconciliation run" } },
    { path: "/intercompany/reconciliation", element: createElement(ReconciliationPage), handle: { title: "Intercompany reconciliation" } },
    { path: "/intercompany/charges", element: createElement(ChargesPage), handle: { title: "Intercompany charges" } },
    { path: "/intercompany/centralized-payments", element: createElement(CentralPaymentsPage), handle: { title: "Centralized payments" } },
    { path: "/intercompany/planning", element: createElement(PlanningPage), handle: { title: "Intercompany planning" } },
    { path: "/intercompany/chains", element: createElement(ChainsPage), handle: { title: "Direct delivery chains" } },
    { path: "/intercompany/transfer-pricing", element: createElement(TransferPricingPage), handle: { title: "Transfer-pricing evidence" } },
  ],
  nav: [
    { label: "Intercompany workspace", to: "/intercompany", duty: "intercompany.view" },
    { label: "Trading partners", to: "/intercompany/partners", duty: "intercompany.view" },
    { label: "Intercompany journal", to: "/intercompany/journal", duty: "intercompany.post" },
    { label: "Posted journals", to: "/intercompany/journals", duty: "intercompany.view" },
    { label: "Reconciliation", to: "/intercompany/reconciliation", duty: "intercompany.reconcile" },
    { label: "Reconciliation runs", to: "/intercompany/reconciliation-runs", duty: "intercompany.view" },
    { label: "Intercompany charges", to: "/intercompany/charges", duty: "intercompany.post" },
    { label: "Charge log", to: "/intercompany/charge-log", duty: "intercompany.view" },
    { label: "Centralized payments", to: "/intercompany/centralized-payments", duty: "intercompany.configure" },
    { label: "Intercompany planning", to: "/intercompany/planning", duty: "intercompany.plan" },
    { label: "Direct delivery chains", to: "/intercompany/chains", duty: "intercompany.view" },
    { label: "Transfer-pricing evidence", to: "/intercompany/transfer-pricing", duty: "intercompany.view" },
  ],
};

export default intercompany;
