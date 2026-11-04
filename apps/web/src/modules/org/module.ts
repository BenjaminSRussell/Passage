import { createElement } from "react";
import type { PassageModule } from "../registry";
import { HierarchyDesignerPage } from "./HierarchyDesignerPage";
import { ReferenceDataPage } from "./ReferenceDataPage";
import { FiscalCalendarsPage } from "./FiscalCalendarsPage";
import { LegalEntitiesPage } from "./LegalEntityAdmin";
import { OrgHome, OrgLayout } from "./pages";
import { OperatingUnitsPage, SitesPage, WarehousesPage } from "./StructurePages";
import { TeamsPage } from "./TeamsPage";

const org: PassageModule = {
  id: "org",
  title: "Organization",
  description: "Legal entities, operating units, sites and warehouses, scoped by legal entity.",
  routes: [
    {
      element: createElement(OrgLayout),
      children: [
        { path: "/org", element: createElement(OrgHome) },
        { path: "/org/legal-entities", element: createElement(LegalEntitiesPage) },
        { path: "/org/operating-units", element: createElement(OperatingUnitsPage) },
        { path: "/org/sites", element: createElement(SitesPage) },
        { path: "/org/warehouses", element: createElement(WarehousesPage) },
        { path: "/org/fiscal-calendars", element: createElement(FiscalCalendarsPage) },
        { path: "/org/teams", element: createElement(TeamsPage) },
        { path: "/org/reference-data", element: createElement(ReferenceDataPage) },
        { path: "/org/hierarchy", element: createElement(HierarchyDesignerPage) },
      ],
    },
  ],
  nav: [
    { label: "Overview", to: "/org", duty: "org.read" },
    { label: "Legal entities", to: "/org/legal-entities", duty: "org.read" },
    { label: "Operating units", to: "/org/operating-units", duty: "org.read" },
    { label: "Sites", to: "/org/sites", duty: "org.read" },
    { label: "Warehouses", to: "/org/warehouses", duty: "org.read" },
    { label: "Fiscal calendars", to: "/org/fiscal-calendars", duty: "org.read" },
    { label: "Teams", to: "/org/teams", duty: "org.read" },
    { label: "Reference data", to: "/org/reference-data", duty: "org.read" },
    { label: "Hierarchies", to: "/org/hierarchy", duty: "org.read" },
  ],
};

export default org;
