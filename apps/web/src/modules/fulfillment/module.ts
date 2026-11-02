import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BackordersPage } from "./BackordersPage";
import { ContainerTypesPage } from "./ContainerTypesPage";
import { DeliveredNotInvoicedPage } from "./DeliveredNotInvoicedPage";
import { DropShipDetailPage } from "./DropShipDetailPage";
import { DropShipsPage } from "./DropShipsPage";
import { LegalEntitySettingsPage } from "./LegalEntitySettingsPage";
import { PackagingFeesPage } from "./PackagingFeesPage";
import { PackingRulesPage } from "./PackingRulesPage";
import { PackingSlipDetailPage } from "./PackingSlipDetailPage";
import { PackingSlipsPage } from "./PackingSlipsPage";
import { ProductPackagingPage } from "./ProductPackagingPage";
import { ReleasePage } from "./ReleasePage";
import { ReturnableAgingPage } from "./ReturnableAgingPage";
import { ReturnablePackagingPage } from "./ReturnablePackagingPage";
import { ScanPage } from "./ScanPage";
import { ShipmentDetailPage } from "./ShipmentDetailPage";
import { ShipmentsPage } from "./ShipmentsPage";
import { SsccRegisterPage } from "./SsccRegisterPage";
import { WarehouseSettingsPage } from "./WarehouseSettingsPage";
import { WaveDetailPage } from "./WaveDetailPage";
import { WavesPage } from "./WavesPage";
import { WorkbenchPage } from "./WorkbenchPage";
import { WorkspacePage } from "./WorkspacePage";

const fulfillment: PassageModule = {
  id: "fulfillment",
  title: "Fulfillment",
  description: "Release orders to the warehouse, group them into pick waves, pick, pack into SSCC-labelled containers by packing rule, post full or partial packing slips with packaging fees, ship drop-ships from vendors, report delivered-not-invoiced cost and short picks, track carriers and returnable packaging with its aging.",
  routes: [
    { path: "/fulfillment", element: createElement(WorkspacePage), handle: { title: "Fulfillment" } },
    { path: "/fulfillment/release", element: createElement(ReleasePage), handle: { title: "Release lines" } },
    { path: "/fulfillment/workbench", element: createElement(WorkbenchPage), handle: { title: "Picking workbench" } },
    { path: "/fulfillment/waves", element: createElement(WavesPage), handle: { title: "Pick waves" } },
    { path: "/fulfillment/waves/:id", element: createElement(WaveDetailPage), handle: { title: "Pick wave" } },
    { path: "/fulfillment/shipments", element: createElement(ShipmentsPage), handle: { title: "Shipments" } },
    { path: "/fulfillment/shipments/:id", element: createElement(ShipmentDetailPage), handle: { title: "Shipment" } },
    { path: "/fulfillment/packing-slips", element: createElement(PackingSlipsPage), handle: { title: "Packing slips" } },
    { path: "/fulfillment/packing-slips/:id", element: createElement(PackingSlipDetailPage), handle: { title: "Packing slip" } },
    { path: "/fulfillment/drop-ships", element: createElement(DropShipsPage), handle: { title: "Drop-ships" } },
    { path: "/fulfillment/drop-ships/:id", element: createElement(DropShipDetailPage), handle: { title: "Drop-ship" } },
    { path: "/fulfillment/delivered-not-invoiced", element: createElement(DeliveredNotInvoicedPage), handle: { title: "Delivered not invoiced" } },
    { path: "/fulfillment/backorders", element: createElement(BackordersPage), handle: { title: "Short picks and backorders" } },
    { path: "/fulfillment/sscc-register", element: createElement(SsccRegisterPage), handle: { title: "SSCC register" } },
    { path: "/fulfillment/scan", element: createElement(ScanPage), handle: { title: "Scan a label" } },
    { path: "/fulfillment/container-types", element: createElement(ContainerTypesPage), handle: { title: "Container types" } },
    { path: "/fulfillment/packing-rules", element: createElement(PackingRulesPage), handle: { title: "Packing rules" } },
    { path: "/fulfillment/packaging-materials", element: createElement(ProductPackagingPage), handle: { title: "Packaging materials" } },
    { path: "/fulfillment/packaging-fees", element: createElement(PackagingFeesPage), handle: { title: "Packaging fees" } },
    { path: "/fulfillment/returnable-packaging", element: createElement(ReturnablePackagingPage), handle: { title: "Returnable packaging" } },
    { path: "/fulfillment/warehouse-settings", element: createElement(WarehouseSettingsPage), handle: { title: "Warehouse settings" } },
    { path: "/fulfillment/legal-entity-settings", element: createElement(LegalEntitySettingsPage), handle: { title: "Posting settings" } },
    { path: "/fulfillment/returnable-aging", element: createElement(ReturnableAgingPage), handle: { title: "Returnable packaging aging" } },
  ],
  nav: [
    { label: "Overview", to: "/fulfillment", duty: "WarehouseView" },
    { label: "Release lines", to: "/fulfillment/release", duty: "WarehouseRelease" },
    { label: "Picking workbench", to: "/fulfillment/workbench", duty: "WarehouseRelease" },
    { label: "Pick waves", to: "/fulfillment/waves", duty: "WarehouseView" },
    { label: "Shipments", to: "/fulfillment/shipments", duty: "WarehouseView" },
    { label: "Packing slips", to: "/fulfillment/packing-slips", duty: "WarehouseView" },
    { label: "Drop-ships", to: "/fulfillment/drop-ships", duty: "WarehouseView" },
    { label: "Delivered not invoiced", to: "/fulfillment/delivered-not-invoiced", duty: "WarehouseView" },
    { label: "Short picks and backorders", to: "/fulfillment/backorders", duty: "WarehouseView" },
    { label: "SSCC register", to: "/fulfillment/sscc-register", duty: "WarehouseView" },
    { label: "Scan a label", to: "/fulfillment/scan", duty: "WarehousePick" },
    { label: "Container types", to: "/fulfillment/container-types", duty: "WarehouseView" },
    { label: "Packing rules", to: "/fulfillment/packing-rules", duty: "WarehouseView" },
    { label: "Packaging materials", to: "/fulfillment/packaging-materials", duty: "WarehouseView" },
    { label: "Packaging fees", to: "/fulfillment/packaging-fees", duty: "PackagingFeeMaintain" },
    { label: "Returnable packaging", to: "/fulfillment/returnable-packaging", duty: "WarehouseView" },
    { label: "Returnable aging", to: "/fulfillment/returnable-aging", duty: "WarehouseView" },
    { label: "Warehouse settings", to: "/fulfillment/warehouse-settings", duty: "WarehouseView" },
    { label: "Posting settings", to: "/fulfillment/legal-entity-settings", duty: "WarehouseView" },
  ],
};

export default fulfillment;
