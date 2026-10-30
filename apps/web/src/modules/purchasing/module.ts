import { createElement } from "react";
import type { PassageModule } from "../registry";
import { PurchaseAgreementCreatePage, PurchaseAgreementDetailPage, PurchaseAgreementsPage } from "./AgreementPages";
import { ConsolidationPage } from "./ConsolidationPage";
import { InvoicingPlanPage } from "./InvoicingPlanPage";
import { PolicyDetailPage, PolicyListPage } from "./PolicyPages";
import { PriceLookupPage } from "./PricingPage";
import { PurchaseOrderCreatePage, PurchaseOrderDetailPage, PurchaseOrderEditPage, PurchaseOrdersPage } from "./PurchaseOrderPages";
import { RepricePage, RepriceRunPage } from "./RepricePages";
import { RequisitionCreatePage, RequisitionDetailPage, RequisitionsPage } from "./RequisitionPages";
import { RFQCreatePage, RFQDetailPage, RFQsPage } from "./RfqPages";
import {
  SchedulingAgreementCreatePage,
  SchedulingAgreementDetailPage,
  SchedulingAgreementsPage,
  SchedulingReleasePage,
} from "./SchedulingPages";
import { TradeAgreementCreatePage, TradeAgreementDetailPage, TradeAgreementsPage } from "./TradeAgreementPages";
import { WorkspacePage } from "./WorkspacePage";

const purchasing: PassageModule = {
  id: "purchasing",
  title: "Purchasing",
  description:
    "Requisitions, consolidation, requests for quotation with sealed bids and scoring, purchase orders with approval, change versions, invoicing plans and subsequent price changes, purchase, scheduling and trade agreements with releases, price lookup, and the purchasing policy per legal entity.",
  routes: [
    { path: "/purchasing", element: createElement(WorkspacePage) },
    { path: "/purchasing/requisitions", element: createElement(RequisitionsPage) },
    { path: "/purchasing/requisitions/new", element: createElement(RequisitionCreatePage), handle: { title: "New requisition" } },
    { path: "/purchasing/requisitions/:id", element: createElement(RequisitionDetailPage), handle: { title: "Requisition" } },
    { path: "/purchasing/consolidation", element: createElement(ConsolidationPage) },
    { path: "/purchasing/rfqs", element: createElement(RFQsPage) },
    { path: "/purchasing/rfqs/new", element: createElement(RFQCreatePage), handle: { title: "New request for quotation" } },
    { path: "/purchasing/rfqs/:id", element: createElement(RFQDetailPage), handle: { title: "Request for quotation" } },
    { path: "/purchasing/purchase-orders", element: createElement(PurchaseOrdersPage) },
    { path: "/purchasing/purchase-orders/new", element: createElement(PurchaseOrderCreatePage), handle: { title: "New purchase order" } },
    { path: "/purchasing/purchase-orders/:id", element: createElement(PurchaseOrderDetailPage), handle: { title: "Purchase order" } },
    { path: "/purchasing/purchase-orders/:id/edit", element: createElement(PurchaseOrderEditPage), handle: { title: "Edit purchase order" } },
    {
      path: "/purchasing/purchase-orders/:id/lines/:lineNo/invoicing-plan",
      element: createElement(InvoicingPlanPage),
      handle: { title: "Invoicing plan" },
    },
    { path: "/purchasing/purchase-orders/:id/reprice", element: createElement(RepricePage), handle: { title: "Price change" } },
    { path: "/purchasing/reprice", element: createElement(RepriceRunPage), handle: { title: "Price changes" } },
    { path: "/purchasing/agreements", element: createElement(PurchaseAgreementsPage) },
    { path: "/purchasing/agreements/new", element: createElement(PurchaseAgreementCreatePage), handle: { title: "New purchase agreement" } },
    { path: "/purchasing/agreements/:id", element: createElement(PurchaseAgreementDetailPage), handle: { title: "Purchase agreement" } },
    { path: "/purchasing/scheduling-agreements", element: createElement(SchedulingAgreementsPage) },
    {
      path: "/purchasing/scheduling-agreements/new",
      element: createElement(SchedulingAgreementCreatePage),
      handle: { title: "New scheduling agreement" },
    },
    { path: "/purchasing/scheduling-agreements/:id", element: createElement(SchedulingAgreementDetailPage), handle: { title: "Scheduling agreement" } },
    {
      path: "/purchasing/scheduling-agreements/:id/releases/:releaseNo",
      element: createElement(SchedulingReleasePage),
      handle: { title: "Scheduling agreement release" },
    },
    { path: "/purchasing/trade-agreements", element: createElement(TradeAgreementsPage) },
    { path: "/purchasing/trade-agreements/new", element: createElement(TradeAgreementCreatePage), handle: { title: "New price row" } },
    { path: "/purchasing/trade-agreements/:id", element: createElement(TradeAgreementDetailPage), handle: { title: "Price row" } },
    { path: "/purchasing/pricing", element: createElement(PriceLookupPage) },
    { path: "/purchasing/policies", element: createElement(PolicyListPage) },
    { path: "/purchasing/policies/:legalEntityId", element: createElement(PolicyDetailPage), handle: { title: "Purchasing policy" } },
  ],
  nav: [
    { label: "Overview", to: "/purchasing", duty: "PurchaseOrderInquire" },
    { label: "Requisitions", to: "/purchasing/requisitions", duty: "RequisitionCreate" },
    { label: "Consolidation", to: "/purchasing/consolidation", duty: "PurchaseOrderMaintain" },
    { label: "Requests for quotation", to: "/purchasing/rfqs", duty: "RFQMaintain" },
    { label: "Purchase orders", to: "/purchasing/purchase-orders", duty: "PurchaseOrderInquire" },
    { label: "Price changes", to: "/purchasing/reprice", duty: "PurchaseOrderInquire" },
    { label: "Purchase agreements", to: "/purchasing/agreements", duty: "AgreementMaintain" },
    { label: "Scheduling agreements", to: "/purchasing/scheduling-agreements", duty: "AgreementMaintain" },
    { label: "Trade agreements", to: "/purchasing/trade-agreements", duty: "TradeAgreementMaintain" },
    { label: "Price lookup", to: "/purchasing/pricing", duty: "PurchaseOrderMaintain" },
    { label: "Policies", to: "/purchasing/policies", duty: "PurchaseOrderInquire" },
  ],
};

export default purchasing;
