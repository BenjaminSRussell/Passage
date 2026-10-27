import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CarrierInvoicesPage } from "./CarrierInvoicesPage";
import { FreightBillDetailPage } from "./FreightBillDetailPage";
import { FreightBillsPage } from "./FreightBillsPage";
import { NewCarrierInvoicePage } from "./NewCarrierInvoicePage";
import { NewFreightBillPage } from "./NewFreightBillPage";
import { NewRateCardPage } from "./NewRateCardPage";
import { RateCardsPage } from "./RateCardsPage";
import { RateShopPage } from "./RateShopPage";
import { TransportHome } from "./TransportHome";

const transport: PassageModule = {
  id: "transport",
  title: "Transportation",
  description: "Rate shop carriers, keep their rate cards, and reconcile carrier invoices against expected freight charges.",
  routes: [
    { path: "/transport", element: createElement(TransportHome), handle: { title: "Transportation" } },
    { path: "/transport/rate-shop", element: createElement(RateShopPage), handle: { title: "Rate shop" } },
    { path: "/transport/rate-cards", element: createElement(RateCardsPage), handle: { title: "Rate cards" } },
    { path: "/transport/rate-cards/new", element: createElement(NewRateCardPage), handle: { title: "New rate card" } },
    { path: "/transport/freight-bills", element: createElement(FreightBillsPage), handle: { title: "Freight bills" } },
    { path: "/transport/freight-bills/new", element: createElement(NewFreightBillPage), handle: { title: "New freight bill" } },
    { path: "/transport/freight-bills/:id", element: createElement(FreightBillDetailPage), handle: { title: "Freight bill" } },
    { path: "/transport/carrier-invoices", element: createElement(CarrierInvoicesPage), handle: { title: "Carrier invoices" } },
    { path: "/transport/carrier-invoices/new", element: createElement(NewCarrierInvoicePage), handle: { title: "Register carrier invoice" } },
  ],
  nav: [
    { label: "Transportation", to: "/transport", duty: "TMS_VIEW" },
    { label: "Rate shop", to: "/transport/rate-shop", duty: "TMS_RATE" },
    { label: "Rate cards", to: "/transport/rate-cards", duty: "TMS_RATE" },
    { label: "Freight bills", to: "/transport/freight-bills", duty: "TMS_VIEW" },
    { label: "Carrier invoices", to: "/transport/carrier-invoices", duty: "TMS_RECONCILE" },
  ],
};

export default transport;
