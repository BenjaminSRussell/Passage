import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BreakGlassPage, LicensesPage, OpsHome, SloPage } from "./pages";

// The operations console (#50). Every entry needs ops.region.view, an operator-only duty: the ops routes serve the platform
// operator tenant only, so the console shows nothing in a customer tenant.
const ops: PassageModule = {
  id: "ops",
  title: "Operations console",
  description: "Platform operations: which region is primary and how far standbys are behind, break-glass windows, service levels and company licences.",
  routes: [
    { path: "/ops", element: createElement(OpsHome), handle: { title: "Operations console" } },
    { path: "/ops/break-glass", element: createElement(BreakGlassPage), handle: { title: "Break-glass windows" } },
    { path: "/ops/slo", element: createElement(SloPage), handle: { title: "Service levels" } },
    { path: "/ops/licenses", element: createElement(LicensesPage), handle: { title: "Licences" } },
  ],
  nav: [
    { label: "Failover status", to: "/ops", duty: "ops.region.view" },
    { label: "Break-glass windows", to: "/ops/break-glass", duty: "ops.region.view" },
    { label: "Service levels", to: "/ops/slo", duty: "ops.region.view" },
    { label: "Licences", to: "/ops/licenses", duty: "ops.region.view" },
  ],
};

export default ops;
