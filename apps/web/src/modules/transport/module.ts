import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CarrierInvoicesPage } from "./CarrierInvoicesPage";
import { FreightBillDetailPage } from "./FreightBillDetailPage";
import { FreightBillsPage } from "./FreightBillsPage";
import { LoadTrackingPage } from "./LoadTrackingPage";
import { NewCarrierInvoicePage } from "./NewCarrierInvoicePage";
import { NewFreightBillPage } from "./NewFreightBillPage";
import { NewRateCardPage } from "./NewRateCardPage";
import { NewTenderPage } from "./NewTenderPage";
import { NewTrackingEventPage } from "./NewTrackingEventPage";
import { NewVoyagePage } from "./NewVoyagePage";
import { RateCardsPage } from "./RateCardsPage";
import { RateShopPage } from "./RateShopPage";
import { TenderDetailPage } from "./TenderDetailPage";
import { TendersPage } from "./TendersPage";
import { TrackingPage } from "./TrackingPage";
import { TransportHome } from "./TransportHome";
import { VoyageDetailPage } from "./VoyageDetailPage";
import { VoyagesPage } from "./VoyagesPage";

const transport: PassageModule = {
  id: "transport",
  title: "Transportation",
  description: "Rate shop and tender loads to carriers, track shipments, reconcile carrier invoices and allocate landed cost.",
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
    { path: "/transport/tenders", element: createElement(TendersPage), handle: { title: "Tenders" } },
    { path: "/transport/tenders/new", element: createElement(NewTenderPage), handle: { title: "Start tender" } },
    { path: "/transport/tenders/:id", element: createElement(TenderDetailPage), handle: { title: "Tender" } },
    { path: "/transport/tracking", element: createElement(TrackingPage), handle: { title: "Tracking" } },
    { path: "/transport/tracking/new", element: createElement(NewTrackingEventPage), handle: { title: "Record status" } },
    { path: "/transport/tracking/:loadId", element: createElement(LoadTrackingPage), handle: { title: "Load tracking" } },
    { path: "/transport/voyages", element: createElement(VoyagesPage), handle: { title: "Landed cost voyages" } },
    { path: "/transport/voyages/new", element: createElement(NewVoyagePage), handle: { title: "New voyage" } },
    { path: "/transport/voyages/:id", element: createElement(VoyageDetailPage), handle: { title: "Voyage" } },
  ],
  nav: [
    { label: "Transportation", to: "/transport", duty: "TMS_VIEW" },
    { label: "Rate shop", to: "/transport/rate-shop", duty: "TMS_RATE" },
    { label: "Rate cards", to: "/transport/rate-cards", duty: "TMS_RATE" },
    { label: "Freight bills", to: "/transport/freight-bills", duty: "TMS_VIEW" },
    { label: "Carrier invoices", to: "/transport/carrier-invoices", duty: "TMS_RECONCILE" },
    { label: "Tenders", to: "/transport/tenders", duty: "TMS_TENDER" },
    { label: "Tracking", to: "/transport/tracking", duty: "TMS_VIEW" },
    { label: "Landed cost voyages", to: "/transport/voyages", duty: "TMS_LANDED_COST" },
  ],
};

export default transport;
