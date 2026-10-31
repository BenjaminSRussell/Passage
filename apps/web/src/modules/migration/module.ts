import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewPackagePage, PackageDetailPage, PackagesPage } from "./PackagePages";
import { NewRunPage, RunDetailPage, RunsPage } from "./RunPages";
import { NewTemplatePage, TemplateDetailPage, TemplatesPage } from "./TemplatePages";
import { NewSpecPage, SpecDetailPage, SpecsPage } from "./SpecPages";

// migration.run, migration.spec.write and migration.package.apply are enforced by the service but not seeded in
// policies/authz/data.json yet (handoff for #313), so the nav items carry no duty and the
// actions are gated on the routes below.
const migration: PassageModule = {
  id: "migration",
  title: "Data migration",
  description: "Map, validate, load and reconcile legacy AX and SAP data: versioned mapping specs, data templates, migration runs with their exception queue and re-run.",
  routes: [
    { path: "/migration", element: createElement(RunsPage), handle: { title: "Migration runs" } },
    { path: "/migration/runs/new", element: createElement(NewRunPage), handle: { title: "Start migration run", duty: "migration.run" } },
    { path: "/migration/runs/:id", element: createElement(RunDetailPage), handle: { title: "Migration run" } },
    { path: "/migration/specs", element: createElement(SpecsPage), handle: { title: "Mapping specs" } },
    { path: "/migration/specs/new", element: createElement(NewSpecPage), handle: { title: "Publish mapping spec", duty: "migration.spec.write" } },
    { path: "/migration/specs/:id", element: createElement(SpecDetailPage), handle: { title: "Mapping spec" } },
    { path: "/migration/templates", element: createElement(TemplatesPage), handle: { title: "Data templates" } },
    { path: "/migration/templates/new", element: createElement(NewTemplatePage), handle: { title: "Publish data template", duty: "migration.spec.write" } },
    { path: "/migration/templates/:id", element: createElement(TemplateDetailPage), handle: { title: "Data template" } },
    { path: "/migration/packages", element: createElement(PackagesPage), handle: { title: "Configuration packages" } },
    { path: "/migration/packages/new", element: createElement(NewPackagePage), handle: { title: "Apply configuration package", duty: "migration.package.apply" } },
    { path: "/migration/packages/:id", element: createElement(PackageDetailPage), handle: { title: "Configuration package" } },
  ],
  nav: [
    { label: "Migration runs", to: "/migration" },
    { label: "Mapping specs", to: "/migration/specs" },
    { label: "Data templates", to: "/migration/templates" },
    { label: "Configuration packages", to: "/migration/packages" },
  ],
};

export default migration;
