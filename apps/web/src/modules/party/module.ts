import { createElement } from "react";
import type { PassageModule } from "../registry";
import { DuplicatesPage } from "./DuplicatesPage";
import { DUTY_READ, DUTY_WRITE } from "./format";
import { PartyDetailPage } from "./PartyDetailPage";
import { PendingChangeDetailPage } from "./PendingChangeDetailPage";
import { PendingChangesPage } from "./PendingChangesPage";
import { SearchPage } from "./SearchPage";
import { TaxRegistrationsPage } from "./TaxRegistrationsPage";

const party: PassageModule = {
  id: "party",
  title: "Parties",
  description:
    "Organizations and people with vendor roles, tax-registration checks, duplicate review, and credit-limit change approval.",
  routes: [
    { path: "/party", element: createElement(SearchPage), handle: { title: "Parties", duty: DUTY_READ } },
    { path: "/party/tax-registrations", element: createElement(TaxRegistrationsPage), handle: { title: "Tax registrations", duty: DUTY_WRITE } },
    { path: "/party/duplicates", element: createElement(DuplicatesPage), handle: { title: "Duplicate candidates", duty: DUTY_READ } },
    { path: "/party/pending-changes", element: createElement(PendingChangesPage), handle: { title: "Credit limit approvals", duty: DUTY_READ } },
    { path: "/party/pending-changes/:id", element: createElement(PendingChangeDetailPage), handle: { title: "Credit limit change", duty: DUTY_READ } },
    { path: "/party/:id", element: createElement(PartyDetailPage), handle: { title: "Party", duty: DUTY_READ } },
  ],
  nav: [
    { label: "Parties", to: "/party", duty: DUTY_READ },
    { label: "Tax registrations", to: "/party/tax-registrations", duty: DUTY_WRITE },
    { label: "Duplicate candidates", to: "/party/duplicates", duty: DUTY_READ },
    { label: "Credit limit approvals", to: "/party/pending-changes", duty: DUTY_READ },
  ],
};

export default party;
