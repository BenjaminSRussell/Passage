import { createElement } from "react";
import type { PassageModule } from "../registry";
import { PackingSlipDetailPage } from "./PackingSlipDetailPage";
import { PackingSlipsPage } from "./PackingSlipsPage";
import { ReleasePage } from "./ReleasePage";
import { ScanPage } from "./ScanPage";
import { ShipmentDetailPage } from "./ShipmentDetailPage";
import { ShipmentsPage } from "./ShipmentsPage";
import { WorkspacePage } from "./WorkspacePage";

const fulfillment: PassageModule = {
  id: "fulfillment",
  title: "Fulfillment",
  description: "Release orders to the warehouse, pick, pack into SSCC-labelled containers and post packing slips.",
  routes: [
    { path: "/fulfillment", element: createElement(WorkspacePage), handle: { title: "Fulfillment" } },
    { path: "/fulfillment/release", element: createElement(ReleasePage), handle: { title: "Release lines" } },
    { path: "/fulfillment/shipments", element: createElement(ShipmentsPage), handle: { title: "Shipments" } },
    { path: "/fulfillment/shipments/:id", element: createElement(ShipmentDetailPage), handle: { title: "Shipment" } },
    { path: "/fulfillment/packing-slips", element: createElement(PackingSlipsPage), handle: { title: "Packing slips" } },
    { path: "/fulfillment/packing-slips/:id", element: createElement(PackingSlipDetailPage), handle: { title: "Packing slip" } },
    { path: "/fulfillment/scan", element: createElement(ScanPage), handle: { title: "Scan a label" } },
  ],
  nav: [
    { label: "Overview", to: "/fulfillment", duty: "WarehouseView" },
    { label: "Release lines", to: "/fulfillment/release", duty: "WarehouseRelease" },
    { label: "Shipments", to: "/fulfillment/shipments", duty: "WarehouseView" },
    { label: "Packing slips", to: "/fulfillment/packing-slips", duty: "WarehouseView" },
    { label: "Scan a label", to: "/fulfillment/scan", duty: "WarehousePick" },
  ],
};

export default fulfillment;
