import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ChecklistPage, DemoCompanyPage, FeaturesPage, SetupWizardPage } from "./pages";

const setup: PassageModule = {
  id: "setup",
  title: "Company setup",
  description: "The setup guide, the checklist of what is missing, features and the demo company.",
  routes: [
    { path: "/setup", element: createElement(SetupWizardPage), handle: { title: "Set up your company" } },
    { path: "/setup/checklist", element: createElement(ChecklistPage), handle: { title: "Setup checklist" } },
    { path: "/setup/features", element: createElement(FeaturesPage), handle: { title: "Features" } },
    { path: "/setup/demo-company", element: createElement(DemoCompanyPage), handle: { title: "Demo company" } },
  ],
  nav: [
    { label: "Setup guide", to: "/setup", duty: "identity.setup.maintain" },
    { label: "Setup checklist", to: "/setup/checklist", duty: "identity.setup.maintain" },
    { label: "Features", to: "/setup/features", duty: "identity.feature.maintain" },
    { label: "Demo company", to: "/setup/demo-company", duty: "org.demo-company.manage" },
  ],
};

export default setup;
