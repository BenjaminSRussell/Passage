import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AddBookPage } from "./AddBookPage";
import { NewAdditionPage } from "./AdditionPage";
import { AdjustPage } from "./AdjustPage";
import { AssetDetailPage } from "./AssetDetailPage";
import { AssetsPage } from "./AssetsPage";
import { CapexDetailPage, CapexListPage, NewCapexPage } from "./CapexPage";
import { DisposePage } from "./DisposePage";
import { IndexSeriesPage, NewPolicyPage } from "./InsurancePage";
import { LeaseDetailPage } from "./LeaseDetailPage";
import { LeasesPage, NewLeasePage } from "./LeasesPage";
import { NewAssetPage } from "./NewAssetPage";
import { RunDetailPage, RunsPage } from "./RunsPage";

const assets: PassageModule = {
  id: "assets",
  title: "Fixed assets",
  description:
    "Keep the asset register, add depreciation books and preview their schedules, post adjustments, impairments and additions, run and post depreciation, record insurance policies, raise capex requests, dispose of assets by sale or scrap, and recognize IFRS 16 leases.",
  routes: [
    { path: "/assets", element: createElement(AssetsPage), handle: { title: "Asset register" } },
    { path: "/assets/new", element: createElement(NewAssetPage), handle: { title: "New asset" } },
    { path: "/assets/leases", element: createElement(LeasesPage), handle: { title: "Leases" } },
    { path: "/assets/leases/new", element: createElement(NewLeasePage), handle: { title: "New lease" } },
    { path: "/assets/leases/:id", element: createElement(LeaseDetailPage), handle: { title: "Lease" } },
    { path: "/assets/depreciation-runs", element: createElement(RunsPage), handle: { title: "Depreciation runs" } },
    { path: "/assets/depreciation-runs/:id", element: createElement(RunDetailPage), handle: { title: "Depreciation run" } },
    { path: "/assets/capex", element: createElement(CapexListPage), handle: { title: "Capex requests" } },
    { path: "/assets/capex/new", element: createElement(NewCapexPage), handle: { title: "New capex request" } },
    { path: "/assets/capex/:id", element: createElement(CapexDetailPage), handle: { title: "Capex request" } },
    { path: "/assets/index-series", element: createElement(IndexSeriesPage), handle: { title: "Index series" } },
    { path: "/assets/:id", element: createElement(AssetDetailPage), handle: { title: "Asset" } },
    { path: "/assets/:id/books/new", element: createElement(AddBookPage), handle: { title: "Add book" } },
    { path: "/assets/:id/dispose", element: createElement(DisposePage), handle: { title: "Dispose of asset" } },
    { path: "/assets/:id/adjust", element: createElement(AdjustPage), handle: { title: "Adjust book" } },
    { path: "/assets/:id/additions/new", element: createElement(NewAdditionPage), handle: { title: "New addition" } },
    { path: "/assets/:id/insurance/new", element: createElement(NewPolicyPage), handle: { title: "New insurance policy" } },
  ],
  nav: [
    { label: "Asset register", to: "/assets", duty: "AssetView" },
    { label: "New asset", to: "/assets/new", duty: "AssetMaintain" },
    { label: "Depreciation runs", to: "/assets/depreciation-runs", duty: "AssetDepreciate" },
    { label: "Capex requests", to: "/assets/capex", duty: "CapexRequestCreate" },
    { label: "Index series", to: "/assets/index-series", duty: "AssetInsuranceMaintain" },
    { label: "Leases", to: "/assets/leases", duty: "LeaseMaintain" },
  ],
};

export default assets;
