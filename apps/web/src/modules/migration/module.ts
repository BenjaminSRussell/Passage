import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewRunPage, RunDetailPage, RunsPage } from "./RunPages";
import { NewSpecPage, SpecDetailPage, SpecsPage } from "./SpecPages";

// migration.run and migration.spec.write are enforced by the service but not seeded in
// policies/authz/data.json yet (handoff for #313), so the nav items carry no duty and the
// actions are gated on the routes below.
const migration: PassageModule = {
  id: "migration",
  title: "Data migration",
  description: "Map, validate, load and reconcile legacy AX and SAP data: versioned mapping specs, migration runs with their exception queue and re-run.",
  routes: [
    { path: "/migration", element: createElement(RunsPage), handle: { title: "Migration runs" } },
    { path: "/migration/runs/new", element: createElement(NewRunPage), handle: { title: "Start migration run", duty: "migration.run" } },
    { path: "/migration/runs/:id", element: createElement(RunDetailPage), handle: { title: "Migration run" } },
    { path: "/migration/specs", element: createElement(SpecsPage), handle: { title: "Mapping specs" } },
    { path: "/migration/specs/new", element: createElement(NewSpecPage), handle: { title: "Publish mapping spec", duty: "migration.spec.write" } },
    { path: "/migration/specs/:id", element: createElement(SpecDetailPage), handle: { title: "Mapping spec" } },
  ],
  nav: [
    { label: "Migration runs", to: "/migration" },
    { label: "Mapping specs", to: "/migration/specs" },
  ],
};

export default migration;
