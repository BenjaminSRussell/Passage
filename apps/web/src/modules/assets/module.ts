import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AddBookPage } from "./AddBookPage";
import { NewAdditionPage } from "./AdditionPage";
import { AdjustPage } from "./AdjustPage";
import { AssetDetailPage } from "./AssetDetailPage";
import { AssetsPage } from "./AssetsPage";
import { CapexDetailPage, CapexListPage, NewCapexPage } from "./CapexPage";
import { CountSessionDetailPage, CountSessionsPage, NewCountSessionPage } from "./CountPage";
import { DisposePage } from "./DisposePage";
import { GroupAssetDetailPage, GroupAssetsPage, NewGroupAssetPage } from "./GroupAssetsPage";
import { IndexSeriesPage, NewPolicyPage } from "./InsurancePage";
import { InsuranceReportPage } from "./InsuranceReportPage";
import { LeaseDetailPage, ModifyLeasePage } from "./LeaseDetailPage";
import { LeaseProfilePage, LeasePostingPage } from "./LeasePostingPage";
import { LeasesPage, NewLeasePage } from "./LeasesPage";
import { MidQuarterPage } from "./MidQuarterPage";
import { NewAssetPage } from "./NewAssetPage";
import { RunDetailPage, RunsPage } from "./RunsPage";
import { TerminateLeasePage } from "./TerminateLeasePage";
import { TransferPage } from "./TransferPage";

const assets: PassageModule = {
  id: "assets",
  title: "Fixed assets",
  description:
    "Keep the asset register, add depreciation books and preview their schedules, post adjustments, impairments and additions, run and post depreciation, run the mid-quarter test, record insurance policies and review the insurance report, count assets on site, raise, approve and release capex requests, transfer or split assets, pool assets into group assets for tax depreciation, dispose of assets by sale or scrap, and recognize, remeasure, terminate and book IFRS 16 leases in the general ledger.",
  routes: [
    { path: "/assets", element: createElement(AssetsPage), handle: { title: "Asset register" } },
    { path: "/assets/new", element: createElement(NewAssetPage), handle: { title: "New asset" } },
    { path: "/assets/leases", element: createElement(LeasesPage), handle: { title: "Leases" } },
    { path: "/assets/leases/new", element: createElement(NewLeasePage), handle: { title: "New lease" } },
    { path: "/assets/leases/posting", element: createElement(LeasePostingPage), handle: { title: "Lease ledger posting" } },
    { path: "/assets/leases/posting-profile", element: createElement(LeaseProfilePage), handle: { title: "Lease accounts" } },
    { path: "/assets/leases/:id", element: createElement(LeaseDetailPage), handle: { title: "Lease" } },
    { path: "/assets/leases/:id/modify", element: createElement(ModifyLeasePage), handle: { title: "Modify lease" } },
    { path: "/assets/leases/:id/terminate", element: createElement(TerminateLeasePage), handle: { title: "Terminate lease" } },
    { path: "/assets/count-sessions", element: createElement(CountSessionsPage), handle: { title: "Count sessions" } },
    { path: "/assets/count-sessions/new", element: createElement(NewCountSessionPage), handle: { title: "New count session" } },
    { path: "/assets/count-sessions/:id", element: createElement(CountSessionDetailPage), handle: { title: "Count session" } },
    { path: "/assets/insurance-report", element: createElement(InsuranceReportPage), handle: { title: "Insurance report" } },
    { path: "/assets/depreciation-runs", element: createElement(RunsPage), handle: { title: "Depreciation runs" } },
    { path: "/assets/depreciation-runs/:id", element: createElement(RunDetailPage), handle: { title: "Depreciation run" } },
    { path: "/assets/capex", element: createElement(CapexListPage), handle: { title: "Capex requests" } },
    { path: "/assets/capex/approvals", element: createElement(CapexListPage, { initialStatus: "InApproval" }), handle: { title: "Capex approvals" } },
    { path: "/assets/capex/new", element: createElement(NewCapexPage), handle: { title: "New capex request" } },
    { path: "/assets/capex/:id", element: createElement(CapexDetailPage), handle: { title: "Capex request" } },
    { path: "/assets/index-series", element: createElement(IndexSeriesPage), handle: { title: "Index series" } },
    { path: "/assets/mid-quarter-test", element: createElement(MidQuarterPage), handle: { title: "Mid-quarter test" } },
    { path: "/assets/group-assets", element: createElement(GroupAssetsPage), handle: { title: "Group assets" } },
    { path: "/assets/group-assets/new", element: createElement(NewGroupAssetPage), handle: { title: "New group asset" } },
    { path: "/assets/group-assets/:id", element: createElement(GroupAssetDetailPage), handle: { title: "Group asset" } },
    { path: "/assets/:id", element: createElement(AssetDetailPage), handle: { title: "Asset" } },
    { path: "/assets/:id/books/new", element: createElement(AddBookPage), handle: { title: "Add book" } },
    { path: "/assets/:id/dispose", element: createElement(DisposePage), handle: { title: "Dispose of asset" } },
    { path: "/assets/:id/transfer", element: createElement(TransferPage), handle: { title: "Transfer or split" } },
    { path: "/assets/:id/adjust", element: createElement(AdjustPage), handle: { title: "Adjust book" } },
    { path: "/assets/:id/additions/new", element: createElement(NewAdditionPage), handle: { title: "New addition" } },
    { path: "/assets/:id/insurance/new", element: createElement(NewPolicyPage), handle: { title: "New insurance policy" } },
  ],
  nav: [
    { label: "Asset register", to: "/assets", duty: "assets.asset.view" },
    { label: "New asset", to: "/assets/new", duty: "assets.asset.maintain" },
    { label: "Depreciation runs", to: "/assets/depreciation-runs", duty: "assets.asset.depreciate" },
    { label: "Capex requests", to: "/assets/capex", duty: "assets.capex-request.create" },
    { label: "Capex approvals", to: "/assets/capex/approvals", duty: "assets.capex-request.approve" },
    { label: "Mid-quarter test", to: "/assets/mid-quarter-test", duty: "assets.asset.depreciate" },
    { label: "Group assets", to: "/assets/group-assets", duty: "assets.asset.configure" },
    { label: "Index series", to: "/assets/index-series", duty: "assets.insurance.maintain" },
    { label: "Insurance report", to: "/assets/insurance-report", duty: "assets.asset.view" },
    { label: "Count sessions", to: "/assets/count-sessions", duty: "assets.asset.count" },
    { label: "Leases", to: "/assets/leases", duty: "assets.lease.maintain" },
    { label: "Lease ledger posting", to: "/assets/leases/posting", duty: "assets.journal.post" },
    { label: "Lease accounts", to: "/assets/leases/posting-profile", duty: "assets.lease.maintain" },
  ],
};

export default assets;
