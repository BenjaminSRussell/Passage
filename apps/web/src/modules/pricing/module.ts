import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AgreementDetailPage } from "./AgreementDetailPage";
import { AgreementsPage } from "./AgreementsPage";
import { JournalDetailPage } from "./JournalDetailPage";
import { JournalsPage } from "./JournalsPage";
import { NewAgreementPage } from "./NewAgreementPage";
import { NewJournalPage } from "./NewJournalPage";
import { NewRebatePage } from "./NewRebatePage";
import { RebateDetailPage } from "./RebateDetailPage";
import { RebatesPage } from "./RebatesPage";
import { SimulatePage } from "./SimulatePage";
import { WorkspacePage } from "./WorkspacePage";

const pricing: PassageModule = {
  id: "pricing",
  title: "Pricing",
  description: "Trade agreements, price journals, rebates and the explanation behind every price.",
  routes: [
    { path: "/pricing", element: createElement(WorkspacePage), handle: { title: "Pricing workspace" } },
    { path: "/pricing/agreements", element: createElement(AgreementsPage), handle: { title: "Trade agreements" } },
    { path: "/pricing/agreements/new", element: createElement(NewAgreementPage), handle: { title: "New trade agreement" } },
    { path: "/pricing/agreements/:id", element: createElement(AgreementDetailPage), handle: { title: "Trade agreement" } },
    { path: "/pricing/journals", element: createElement(JournalsPage), handle: { title: "Price journals" } },
    { path: "/pricing/journals/new", element: createElement(NewJournalPage), handle: { title: "New price journal" } },
    { path: "/pricing/journals/:id", element: createElement(JournalDetailPage), handle: { title: "Price journal" } },
    { path: "/pricing/rebates", element: createElement(RebatesPage), handle: { title: "Rebate agreements" } },
    { path: "/pricing/rebates/new", element: createElement(NewRebatePage), handle: { title: "New rebate agreement" } },
    { path: "/pricing/rebates/:id", element: createElement(RebateDetailPage), handle: { title: "Rebate agreement" } },
    { path: "/pricing/simulate", element: createElement(SimulatePage), handle: { title: "Price simulation" } },
  ],
  nav: [
    { label: "Overview", to: "/pricing", duty: "pricing.view" },
    { label: "Trade agreements", to: "/pricing/agreements", duty: "pricing.view" },
    { label: "Price journals", to: "/pricing/journals", duty: "pricing.view" },
    { label: "Rebate agreements", to: "/pricing/rebates", duty: "pricing.rebate.view" },
    { label: "Price simulation", to: "/pricing/simulate", duty: "pricing.view" },
  ],
};

export default pricing;
