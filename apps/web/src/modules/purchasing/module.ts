import { createElement } from "react";
import type { PassageModule } from "../registry";
import { PriceLookupPage } from "./PricingPage";
import { PurchaseOrderCreatePage, PurchaseOrderDetailPage, PurchaseOrdersPage } from "./PurchaseOrderPages";
import { RequisitionCreatePage, RequisitionDetailPage, RequisitionsPage } from "./RequisitionPages";
import { TradeAgreementCreatePage, TradeAgreementDetailPage, TradeAgreementsPage } from "./TradeAgreementPages";
import { WorkspacePage } from "./WorkspacePage";

const purchasing: PassageModule = {
  id: "purchasing",
  title: "Purchasing",
  description: "Requisitions, purchase orders, trade agreements and price lookup.",
  routes: [
    { path: "/purchasing", element: createElement(WorkspacePage) },
    { path: "/purchasing/requisitions", element: createElement(RequisitionsPage) },
    { path: "/purchasing/requisitions/new", element: createElement(RequisitionCreatePage), handle: { title: "New requisition" } },
    { path: "/purchasing/requisitions/:id", element: createElement(RequisitionDetailPage), handle: { title: "Requisition" } },
    { path: "/purchasing/purchase-orders", element: createElement(PurchaseOrdersPage) },
    { path: "/purchasing/purchase-orders/new", element: createElement(PurchaseOrderCreatePage), handle: { title: "New purchase order" } },
    { path: "/purchasing/purchase-orders/:id", element: createElement(PurchaseOrderDetailPage), handle: { title: "Purchase order" } },
    { path: "/purchasing/trade-agreements", element: createElement(TradeAgreementsPage) },
    { path: "/purchasing/trade-agreements/new", element: createElement(TradeAgreementCreatePage), handle: { title: "New price row" } },
    { path: "/purchasing/trade-agreements/:id", element: createElement(TradeAgreementDetailPage), handle: { title: "Price row" } },
    { path: "/purchasing/pricing", element: createElement(PriceLookupPage) },
  ],
  nav: [
    { label: "Overview", to: "/purchasing", duty: "PurchaseOrderInquire" },
    { label: "Requisitions", to: "/purchasing/requisitions", duty: "RequisitionCreate" },
    { label: "Purchase orders", to: "/purchasing/purchase-orders", duty: "PurchaseOrderInquire" },
    { label: "Trade agreements", to: "/purchasing/trade-agreements", duty: "TradeAgreementMaintain" },
    { label: "Price lookup", to: "/purchasing/pricing", duty: "PurchaseOrderMaintain" },
  ],
};

export default purchasing;
