import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewProposalPage } from "./NewProposalPage";
import { OpenItemsPage } from "./OpenItemsPage";
import { ProposalDetailPage } from "./ProposalDetailPage";
import { ProposalsPage } from "./ProposalsPage";
import { RunPage } from "./RunPage";

const payables: PassageModule = {
  id: "payables",
  title: "Accounts payable",
  description: "See what is owed to vendors and prepare, approve and track vendor payments.",
  routes: [
    { path: "/payables", element: createElement(ProposalsPage), handle: { title: "Payment proposals" } },
    { path: "/payables/open-items", element: createElement(OpenItemsPage), handle: { title: "Vendor open items" } },
    { path: "/payables/proposals/new", element: createElement(NewProposalPage), handle: { title: "New payment proposal", duty: "payment-proposal.write" } },
    { path: "/payables/proposals/:id", element: createElement(ProposalDetailPage), handle: { title: "Payment proposal" } },
    { path: "/payables/runs/:id", element: createElement(RunPage), handle: { title: "Payment run" } },
  ],
  nav: [
    { label: "Payment runs", to: "/payables", duty: "payment-proposal.read" },
    { label: "Vendor open items", to: "/payables/open-items", duty: "open-item.read" },
  ],
};

export default payables;
