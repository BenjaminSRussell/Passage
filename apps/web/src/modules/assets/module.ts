import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AddBookPage } from "./AddBookPage";
import { AssetDetailPage } from "./AssetDetailPage";
import { AssetsPage } from "./AssetsPage";
import { DisposePage } from "./DisposePage";
import { LeaseDetailPage } from "./LeaseDetailPage";
import { LeasesPage, NewLeasePage } from "./LeasesPage";
import { NewAssetPage } from "./NewAssetPage";

const assets: PassageModule = {
  id: "assets",
  title: "Fixed assets",
  description: "Keep the asset register, add depreciation books and preview their schedules, dispose of assets by sale or scrap, and recognize IFRS 16 leases.",
  routes: [
    { path: "/assets", element: createElement(AssetsPage), handle: { title: "Asset register" } },
    { path: "/assets/new", element: createElement(NewAssetPage), handle: { title: "New asset" } },
    { path: "/assets/leases", element: createElement(LeasesPage), handle: { title: "Leases" } },
    { path: "/assets/leases/new", element: createElement(NewLeasePage), handle: { title: "New lease" } },
    { path: "/assets/leases/:id", element: createElement(LeaseDetailPage), handle: { title: "Lease" } },
    { path: "/assets/:id", element: createElement(AssetDetailPage), handle: { title: "Asset" } },
    { path: "/assets/:id/books/new", element: createElement(AddBookPage), handle: { title: "Add book" } },
    { path: "/assets/:id/dispose", element: createElement(DisposePage), handle: { title: "Dispose of asset" } },
  ],
  nav: [
    { label: "Asset register", to: "/assets", duty: "AssetView" },
    { label: "New asset", to: "/assets/new", duty: "AssetMaintain" },
    { label: "Leases", to: "/assets/leases", duty: "LeaseMaintain" },
  ],
};

export default assets;
