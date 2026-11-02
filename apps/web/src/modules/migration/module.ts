import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BindingDetailPage, BindingsPage, NewBindingPage } from "./BindingPages";
import { ConnectionDetailPage, ConnectionsPage, NewConnectionPage } from "./ConnectionPages";
import { NewPackagePage, PackageDetailPage, PackagesPage } from "./PackagePages";
import { NewProjectPage, ProjectDetailPage, ProjectsPage } from "./ProjectPages";
import { NewRunPage, RunDetailPage, RunsPage } from "./RunPages";
import { NewTemplatePage, TemplateDetailPage, TemplatesPage } from "./TemplatePages";
import { XrefPage } from "./XrefPages";
import { NewSpecPage, SpecDetailPage, SpecsPage } from "./SpecPages";

// migration.run is seeded in policies/authz/data.json, so the Connections and Entity bindings nav items is gated on it. migration.spec.write,
// migration.package.apply, migration.project.write and migration.connection.write are enforced by the service but not
// seeded there yet (handoff for #313): the other nav items carry no duty and these actions are gated on the buttons.
// migration.project.write and migration.connection.write are also left off their route handles: the registry test
// rejects a route duty missing from the policy file, which is outside wave 10 ownership.
const migration: PassageModule = {
  id: "migration",
  title: "Data migration",
  description: "Map, validate, load and reconcile legacy AX, SAP and custom ERP data: connections to the source system, entity bindings with a preview of the mapped records, versioned mapping specs, data templates, migration runs with their exception queue and re-run.",
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
    { path: "/migration/projects", element: createElement(ProjectsPage), handle: { title: "Migration projects" } },
    { path: "/migration/projects/new", element: createElement(NewProjectPage), handle: { title: "New migration project" } },
    { path: "/migration/projects/:id", element: createElement(ProjectDetailPage), handle: { title: "Migration project" } },
    { path: "/migration/connections", element: createElement(ConnectionsPage), handle: { title: "Connections" } },
    { path: "/migration/connections/new", element: createElement(NewConnectionPage), handle: { title: "New connection" } },
    { path: "/migration/connections/:id", element: createElement(ConnectionDetailPage), handle: { title: "Connection" } },
    { path: "/migration/bindings", element: createElement(BindingsPage), handle: { title: "Entity bindings" } },
    { path: "/migration/bindings/new", element: createElement(NewBindingPage), handle: { title: "New entity binding" } },
    { path: "/migration/bindings/:id", element: createElement(BindingDetailPage), handle: { title: "Entity binding" } },
    { path: "/migration/xref", element: createElement(XrefPage), handle: { title: "Legacy key cross-reference" } },
  ],
  nav: [
    { label: "Migration runs", to: "/migration" },
    { label: "Migration projects", to: "/migration/projects" },
    { label: "Connections", to: "/migration/connections", duty: "migration.run" },
    { label: "Entity bindings", to: "/migration/bindings", duty: "migration.run" },
    { label: "Mapping specs", to: "/migration/specs" },
    { label: "Data templates", to: "/migration/templates" },
    { label: "Configuration packages", to: "/migration/packages" },
    { label: "Legacy key cross-reference", to: "/migration/xref" },
  ],
};

export default migration;
