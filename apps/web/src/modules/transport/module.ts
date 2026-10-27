import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BuildLoadsPage } from "./BuildLoadsPage";
import { CarrierInvoicesPage } from "./CarrierInvoicesPage";
import { CheckInPage } from "./CheckInPage";
import { FreightBillDetailPage } from "./FreightBillDetailPage";
import { FreightBillsPage } from "./FreightBillsPage";
import { GatePage } from "./GatePage";
import { GateVisitDetailPage } from "./GateVisitDetailPage";
import { GoodsInTransitDetailPage } from "./GoodsInTransitDetailPage";
import { GoodsInTransitPage } from "./GoodsInTransitPage";
import { LoadDetailPage } from "./LoadDetailPage";
import { LoadPlanPage } from "./LoadPlanPage";
import { LoadsPage } from "./LoadsPage";
import { LoadTrackingPage } from "./LoadTrackingPage";
import { NewCarrierInvoicePage } from "./NewCarrierInvoicePage";
import { NewFreightBillPage } from "./NewFreightBillPage";
import { NewGoodsInTransitPage } from "./NewGoodsInTransitPage";
import { NewRateCardPage } from "./NewRateCardPage";
import { NewRoutePlanPage } from "./NewRoutePlanPage";
import { NewTenderPage } from "./NewTenderPage";
import { NewTrackingEventPage } from "./NewTrackingEventPage";
import { NewVoyagePage } from "./NewVoyagePage";
import { RateCardsPage } from "./RateCardsPage";
import { RateShopPage } from "./RateShopPage";
import { RoutePlanDetailPage } from "./RoutePlanDetailPage";
import { RoutePlansPage } from "./RoutePlansPage";
import { ScreeningHoldDetailPage } from "./ScreeningHoldDetailPage";
import { ScreeningHoldsPage } from "./ScreeningHoldsPage";
import { TenderDetailPage } from "./TenderDetailPage";
import { TendersPage } from "./TendersPage";
import { TrackingPage } from "./TrackingPage";
import { TransportHome } from "./TransportHome";
import { VoyageDetailPage } from "./VoyageDetailPage";
import { VoyagesPage } from "./VoyagesPage";

const transport: PassageModule = {
  id: "transport",
  title: "Transportation",
  description:
    "Build loads, plan routes and trailer loads, check dangerous goods and screen parties, release screening holds, rate shop and tender loads to carriers, track shipments, check drivers in and out at the gate, reconcile carrier invoices, allocate landed cost and move goods in transit.",
  routes: [
    { path: "/transport", element: createElement(TransportHome), handle: { title: "Transportation" } },
    { path: "/transport/loads", element: createElement(LoadsPage), handle: { title: "Loads" } },
    { path: "/transport/loads/build", element: createElement(BuildLoadsPage), handle: { title: "Build loads" } },
    { path: "/transport/loads/:id", element: createElement(LoadDetailPage), handle: { title: "Load" } },
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
    { path: "/transport/goods-in-transit", element: createElement(GoodsInTransitPage), handle: { title: "Goods in transit" } },
    { path: "/transport/goods-in-transit/new", element: createElement(NewGoodsInTransitPage), handle: { title: "New goods-in-transit order" } },
    { path: "/transport/goods-in-transit/:id", element: createElement(GoodsInTransitDetailPage), handle: { title: "Goods in transit" } },
    { path: "/transport/gate", element: createElement(GatePage), handle: { title: "Gate" } },
    { path: "/transport/gate/check-in", element: createElement(CheckInPage), handle: { title: "Check in" } },
    { path: "/transport/gate/:id", element: createElement(GateVisitDetailPage), handle: { title: "Gate visit" } },
    { path: "/transport/screening-holds", element: createElement(ScreeningHoldsPage), handle: { title: "Screening holds" } },
    { path: "/transport/screening-holds/:id", element: createElement(ScreeningHoldDetailPage), handle: { title: "Screening hold" } },
    { path: "/transport/routes", element: createElement(RoutePlansPage), handle: { title: "Route plans" } },
    { path: "/transport/routes/new", element: createElement(NewRoutePlanPage), handle: { title: "Optimize routes" } },
    { path: "/transport/routes/:id", element: createElement(RoutePlanDetailPage), handle: { title: "Route plan" } },
    { path: "/transport/load-plans", element: createElement(LoadPlanPage), handle: { title: "Load plan" } },
  ],
  nav: [
    { label: "Transportation", to: "/transport", duty: "TMS_VIEW" },
    { label: "Loads", to: "/transport/loads", duty: "TMS_VIEW" },
    { label: "Rate shop", to: "/transport/rate-shop", duty: "TMS_RATE" },
    { label: "Rate cards", to: "/transport/rate-cards", duty: "TMS_RATE" },
    { label: "Freight bills", to: "/transport/freight-bills", duty: "TMS_VIEW" },
    { label: "Carrier invoices", to: "/transport/carrier-invoices", duty: "TMS_RECONCILE" },
    { label: "Tenders", to: "/transport/tenders", duty: "TMS_TENDER" },
    { label: "Tracking", to: "/transport/tracking", duty: "TMS_VIEW" },
    { label: "Landed cost voyages", to: "/transport/voyages", duty: "TMS_LANDED_COST" },
    { label: "Goods in transit", to: "/transport/goods-in-transit", duty: "TMS_LANDED_COST" },
    { label: "Gate", to: "/transport/gate", duty: "TMS_GATE" },
    { label: "Screening holds", to: "/transport/screening-holds", duty: "TMS_VIEW" },
    { label: "Route plans", to: "/transport/routes", duty: "TMS_PLAN" },
    { label: "Load plan", to: "/transport/load-plans", duty: "TMS_PLAN" },
  ],
};

export default transport;
