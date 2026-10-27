import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AgingPage } from "./AgingPage";
import { AgingSnapshotsPage, BucketSetsPage, CreateSnapshotPage, SnapshotDetailPage } from "./AgingSnapshotsPage";
import { CostTransactionsPage } from "./CostTransactionsPage";
import { CostingHome } from "./pages";
import { ValuationLedgersPage } from "./ValuationLedgersPage";
import { CreateCategoryPage, CreateLedgerPage, ValuationSetupPage } from "./ValuationSetupPage";
import { VersionDetailPage } from "./VersionDetailPage";
import { ValueReportPage } from "./ValueReportPage";
import { CreateVersionPage, VersionsPage } from "./VersionsPage";

const costing: PassageModule = {
  id: "costing",
  title: "Inventory costing",
  description:
    "Preview issue costs under each costing method, compare valuation ledgers, age your inventory and store month-end aging snapshots, set up valuation ledgers and categories, maintain and activate standard cost versions, and report inventory value by dimension with its general ledger tie-out.",
  routes: [
    { path: "/costing", element: createElement(CostingHome), handle: { title: "Settlement preview" } },
    { path: "/costing/valuation-ledgers", element: createElement(ValuationLedgersPage), handle: { title: "Valuation ledgers" } },
    { path: "/costing/aging", element: createElement(AgingPage), handle: { title: "Inventory aging" } },
    { path: "/costing/aging/snapshots", element: createElement(AgingSnapshotsPage), handle: { title: "Aging snapshots" } },
    { path: "/costing/aging/snapshots/new", element: createElement(CreateSnapshotPage), handle: { title: "New aging snapshot" } },
    { path: "/costing/aging/snapshots/:id", element: createElement(SnapshotDetailPage), handle: { title: "Aging snapshot" } },
    { path: "/costing/aging/bucket-sets", element: createElement(BucketSetsPage), handle: { title: "Aging bucket sets" } },
    { path: "/costing/valuation-setup", element: createElement(ValuationSetupPage), handle: { title: "Valuation setup" } },
    { path: "/costing/valuation-setup/ledgers/new", element: createElement(CreateLedgerPage), handle: { title: "New valuation ledger" } },
    { path: "/costing/valuation-setup/categories/new", element: createElement(CreateCategoryPage), handle: { title: "New valuation category" } },
    { path: "/costing/versions", element: createElement(VersionsPage), handle: { title: "Costing versions" } },
    { path: "/costing/versions/new", element: createElement(CreateVersionPage), handle: { title: "New costing version" } },
    { path: "/costing/versions/:id", element: createElement(VersionDetailPage), handle: { title: "Costing version" } },
    { path: "/costing/value-report", element: createElement(ValueReportPage), handle: { title: "Inventory value report" } },
    { path: "/costing/cost-transactions", element: createElement(CostTransactionsPage), handle: { title: "Cost transactions" } },
  ],
  nav: [
    { label: "Settlement preview", to: "/costing", duty: "costing.read" },
    { label: "Valuation ledgers", to: "/costing/valuation-ledgers", duty: "costing.read" },
    { label: "Inventory aging", to: "/costing/aging", duty: "costing.read" },
    { label: "Aging snapshots", to: "/costing/aging/snapshots", duty: "costing.read" },
    { label: "Valuation setup", to: "/costing/valuation-setup", duty: "costing.read" },
    { label: "Costing versions", to: "/costing/versions", duty: "costing.read" },
    { label: "Inventory value report", to: "/costing/value-report", duty: "costing.read" },
    { label: "Cost transactions", to: "/costing/cost-transactions", duty: "costing.read" },
  ],
};

export default costing;
