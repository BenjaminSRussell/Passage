import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AgreementDetailPage } from "./AgreementDetailPage";
import { AgreementsPage } from "./AgreementsPage";
import { NewAgreementPage } from "./NewAgreementPage";
import { SimulatePage } from "./SimulatePage";
import { WorkspacePage } from "./WorkspacePage";

const pricing: PassageModule = {
  id: "pricing",
  title: "Pricing",
  description: "Trade agreements, discounts and the explanation behind every price.",
  routes: [
    { path: "/pricing", element: createElement(WorkspacePage), handle: { title: "Pricing workspace" } },
    { path: "/pricing/agreements", element: createElement(AgreementsPage), handle: { title: "Trade agreements" } },
    { path: "/pricing/agreements/new", element: createElement(NewAgreementPage), handle: { title: "New trade agreement" } },
    { path: "/pricing/agreements/:id", element: createElement(AgreementDetailPage), handle: { title: "Trade agreement" } },
    { path: "/pricing/simulate", element: createElement(SimulatePage), handle: { title: "Price simulation" } },
  ],
  nav: [
    { label: "Overview", to: "/pricing", duty: "pricing.view" },
    { label: "Trade agreements", to: "/pricing/agreements", duty: "pricing.view" },
    { label: "Price simulation", to: "/pricing/simulate", duty: "pricing.view" },
  ],
};

export default pricing;
