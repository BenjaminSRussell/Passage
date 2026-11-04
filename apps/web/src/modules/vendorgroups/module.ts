import { createElement } from "react";
import type { PassageModule } from "../registry";
import { VendorGroupsPage } from "./VendorGroupsPage";

/**
 * Vendor groups of the party master with their short codes (#421). A module of its own so it lives beside the
 * party workspace (`modules/party`, #7) without sharing its files; it can move under `/party` once that lands.
 */
const vendorGroups: PassageModule = {
  id: "vendorgroups",
  title: "Vendor groups",
  description: "Vendor groups and the short codes posted on the vendor-control account for the per-group AP reconciliation.",
  routes: [{ path: "/vendor-groups", element: createElement(VendorGroupsPage), handle: { title: "Vendor groups", duty: "party.read" } }],
  nav: [{ label: "Vendor groups", to: "/vendor-groups", duty: "party.read" }],
};

export default vendorGroups;
