import { createElement } from "react";
import type { PassageModule } from "../registry";
import { PurchaseAgreementCreatePage, PurchaseAgreementDetailPage, PurchaseAgreementsPage } from "./AgreementPages";
import { ApprovalInboxPage } from "./ApprovalPages";
import { CatalogImportCreatePage, CatalogImportDetailPage, CatalogImportsPage } from "./CatalogImportPages";
import { CatalogCreatePage, CatalogDetailPage, CatalogsPage } from "./CatalogPages";
import { AutoChargeCreatePage, AutoChargeDetailPage, AutoChargesPage, ChargeCodeCreatePage, ChargeCodeDetailPage, ChargeCodesPage } from "./ChargePages";
import { CategoryManagersPage, CategoryVendorCreatePage, CategoryVendorDetailPage, CategoryVendorsPage } from "./CategoryPages";
import { ConsolidationPage } from "./ConsolidationPage";
import { DeliverySchedulePage } from "./DeliverySchedulePage";
import { InboundChangeDetailPage, InboundChangesPage } from "./InboundPages";
import { InvoicingPlanPage } from "./InvoicingPlanPage";
import {
  BackorderedLinesPage,
  ChangeOrdersPage,
  ExpiringAgreementsPage,
  NearLimitAgreementsPage,
  OpenOrdersByVendorPage,
  OverdueLinesPage,
  PriceHistoryPage,
} from "./InquiryPages";
import { OpenSupplyPage } from "./OpenSupplyPage";
import { PolicyDetailPage, PolicyListPage } from "./PolicyPages";
import { PriceLookupPage } from "./PricingPage";
import { ReleaseRunCreatePage, ReleaseRunDetailPage, ReleaseRunsPage } from "./ReleasePages";
import { PurchaseOrderCreatePage, PurchaseOrderDetailPage, PurchaseOrderEditPage, PurchaseOrdersPage } from "./PurchaseOrderPages";
import { RepricePage, RepriceRunPage } from "./RepricePages";
import { RequisitionCreatePage, RequisitionDetailPage, RequisitionEditPage, RequisitionsPage } from "./RequisitionPages";
import { RFQCreatePage, RFQDetailPage, RFQsPage } from "./RfqPages";
import { RFQTypeCreatePage, RFQTypeDetailPage, RFQTypesPage } from "./RfqTypePages";
import {
  SchedulingAgreementCreatePage,
  SchedulingAgreementDetailPage,
  SchedulingAgreementsPage,
  SchedulingReleasePage,
} from "./SchedulingPages";
import {
  SigningLimitDetailPage,
  SigningLimitRequestCreatePage,
  SigningLimitRequestDetailPage,
  SigningLimitRequestsPage,
  SigningLimitsPage,
} from "./SigningPages";
import { TradeAgreementCreatePage, TradeAgreementDetailPage, TradeAgreementsPage } from "./TradeAgreementPages";
import { ModelServiceSpecCreatePage, ModelServiceSpecDetailPage, ModelServiceSpecsPage, ServiceSpecPage } from "./ServicePages";
import { WorkspacePage } from "./WorkspacePage";

const purchasing: PassageModule = {
  id: "purchasing",
  title: "Purchasing",
  description:
    "Requisitions with editing, withdrawal, approval, rejection and cancellation (of the whole requisition or one line), consolidation, requests for quotation with sealed bids, scoring, RFQ types and amendments that vendors acknowledge, purchase orders with approval, change versions, delivery warehouse per line and the open supply list, invoicing plans, delivery schedules and subsequent price changes, purchase, scheduling and trade agreements with releases, price lookup, procurement catalogs that requesters pick from, category vendor assignments and managers, charge codes and auto charges for freight, handling and fees, service lines with outlines, unplanned limits and model service specifications, the changes planning messages and closed drop-ships made to purchase orders, signing limits with their requests and recertification, and the purchasing policy per legal entity.",
  routes: [
    { path: "/purchasing", element: createElement(WorkspacePage) },
    { path: "/purchasing/requisitions", element: createElement(RequisitionsPage) },
    { path: "/purchasing/requisitions/new", element: createElement(RequisitionCreatePage), handle: { title: "New requisition" } },
    { path: "/purchasing/requisitions/:id/edit", element: createElement(RequisitionEditPage), handle: { title: "Edit requisition" } },
    { path: "/purchasing/requisitions/:id", element: createElement(RequisitionDetailPage), handle: { title: "Requisition" } },
    { path: "/purchasing/approvals", element: createElement(ApprovalInboxPage), handle: { title: "Requisition approvals" } },
    { path: "/purchasing/consolidation", element: createElement(ConsolidationPage) },
    { path: "/purchasing/releases", element: createElement(ReleaseRunsPage) },
    { path: "/purchasing/releases/new", element: createElement(ReleaseRunCreatePage), handle: { title: "Start a release run" } },
    { path: "/purchasing/releases/:id", element: createElement(ReleaseRunDetailPage), handle: { title: "Release run" } },
    { path: "/purchasing/open-supply", element: createElement(OpenSupplyPage), handle: { title: "Open supply" } },
    { path: "/purchasing/inquiries", element: createElement(OpenOrdersByVendorPage), handle: { title: "Open orders by vendor" } },
    { path: "/purchasing/inquiries/backordered", element: createElement(BackorderedLinesPage), handle: { title: "Backordered lines" } },
    { path: "/purchasing/inquiries/overdue", element: createElement(OverdueLinesPage), handle: { title: "Overdue lines" } },
    { path: "/purchasing/inquiries/expiring-agreements", element: createElement(ExpiringAgreementsPage), handle: { title: "Expiring agreements" } },
    { path: "/purchasing/inquiries/near-limit-agreements", element: createElement(NearLimitAgreementsPage), handle: { title: "Agreements near limit" } },
    { path: "/purchasing/inquiries/change-orders", element: createElement(ChangeOrdersPage), handle: { title: "Change orders" } },
    { path: "/purchasing/inquiries/price-history", element: createElement(PriceHistoryPage), handle: { title: "Order price history" } },
    { path: "/purchasing/inbound-changes", element: createElement(InboundChangesPage) },
    { path: "/purchasing/inbound-changes/:id", element: createElement(InboundChangeDetailPage), handle: { title: "Change from another service" } },
    { path: "/purchasing/rfqs", element: createElement(RFQsPage) },
    { path: "/purchasing/rfqs/new", element: createElement(RFQCreatePage), handle: { title: "New request for quotation" } },
    { path: "/purchasing/rfqs/:id", element: createElement(RFQDetailPage), handle: { title: "Request for quotation" } },
    { path: "/purchasing/rfq-types", element: createElement(RFQTypesPage) },
    { path: "/purchasing/rfq-types/new", element: createElement(RFQTypeCreatePage), handle: { title: "New RFQ type" } },
    { path: "/purchasing/rfq-types/:id", element: createElement(RFQTypeDetailPage), handle: { title: "RFQ type" } },
    { path: "/purchasing/purchase-orders", element: createElement(PurchaseOrdersPage) },
    { path: "/purchasing/purchase-orders/new", element: createElement(PurchaseOrderCreatePage), handle: { title: "New purchase order" } },
    { path: "/purchasing/purchase-orders/:id", element: createElement(PurchaseOrderDetailPage), handle: { title: "Purchase order" } },
    { path: "/purchasing/purchase-orders/:id/edit", element: createElement(PurchaseOrderEditPage), handle: { title: "Edit purchase order" } },
    {
      path: "/purchasing/purchase-orders/:id/lines/:lineNo/invoicing-plan",
      element: createElement(InvoicingPlanPage),
      handle: { title: "Invoicing plan" },
    },
    {
      path: "/purchasing/purchase-orders/:id/lines/:lineNo/delivery-schedule",
      element: createElement(DeliverySchedulePage),
      handle: { title: "Delivery schedule" },
    },
    { path: "/purchasing/purchase-orders/:id/reprice", element: createElement(RepricePage), handle: { title: "Price change" } },
    {
      path: "/purchasing/purchase-orders/:id/lines/:lineNo/service",
      element: createElement(ServiceSpecPage),
      handle: { title: "Service line" },
    },
    { path: "/purchasing/reprice", element: createElement(RepriceRunPage), handle: { title: "Price changes" } },
    { path: "/purchasing/model-service-specs", element: createElement(ModelServiceSpecsPage) },
    { path: "/purchasing/model-service-specs/new", element: createElement(ModelServiceSpecCreatePage), handle: { title: "New model service specification" } },
    { path: "/purchasing/model-service-specs/:id", element: createElement(ModelServiceSpecDetailPage), handle: { title: "Model service specification" } },
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
    { path: "/purchasing/catalogs", element: createElement(CatalogsPage) },
    { path: "/purchasing/catalogs/new", element: createElement(CatalogCreatePage), handle: { title: "New procurement catalog" } },
    { path: "/purchasing/catalogs/:id", element: createElement(CatalogDetailPage), handle: { title: "Procurement catalog" } },
    { path: "/purchasing/catalogs/:id/import", element: createElement(CatalogImportCreatePage), handle: { title: "Import vendor file" } },
    { path: "/purchasing/catalog-imports", element: createElement(CatalogImportsPage) },
    { path: "/purchasing/catalog-imports/:id", element: createElement(CatalogImportDetailPage), handle: { title: "Vendor catalog import" } },
    { path: "/purchasing/category-vendors", element: createElement(CategoryVendorsPage) },
    { path: "/purchasing/category-vendors/new", element: createElement(CategoryVendorCreatePage), handle: { title: "New category vendor assignment" } },
    { path: "/purchasing/category-vendors/:id", element: createElement(CategoryVendorDetailPage), handle: { title: "Category vendor assignment" } },
    { path: "/purchasing/category-managers", element: createElement(CategoryManagersPage) },
    { path: "/purchasing/charge-codes", element: createElement(ChargeCodesPage) },
    { path: "/purchasing/charge-codes/new", element: createElement(ChargeCodeCreatePage), handle: { title: "New charge code" } },
    { path: "/purchasing/charge-codes/:id", element: createElement(ChargeCodeDetailPage), handle: { title: "Charge code" } },
    { path: "/purchasing/auto-charges", element: createElement(AutoChargesPage) },
    { path: "/purchasing/auto-charges/new", element: createElement(AutoChargeCreatePage), handle: { title: "New auto charge" } },
    { path: "/purchasing/auto-charges/:id", element: createElement(AutoChargeDetailPage), handle: { title: "Auto charge" } },
    { path: "/purchasing/signing-limits", element: createElement(SigningLimitsPage) },
    { path: "/purchasing/signing-limits/:id", element: createElement(SigningLimitDetailPage), handle: { title: "Signing limit" } },
    { path: "/purchasing/signing-limit-requests", element: createElement(SigningLimitRequestsPage) },
    { path: "/purchasing/signing-limit-requests/new", element: createElement(SigningLimitRequestCreatePage), handle: { title: "Request a signing limit" } },
    { path: "/purchasing/signing-limit-requests/:id", element: createElement(SigningLimitRequestDetailPage), handle: { title: "Signing limit request" } },
    { path: "/purchasing/policies", element: createElement(PolicyListPage) },
    { path: "/purchasing/policies/:legalEntityId", element: createElement(PolicyDetailPage), handle: { title: "Purchasing policy" } },
  ],
  nav: [
    { label: "Overview", to: "/purchasing", duty: "procurement.purchase-order.inquire" },
    { label: "Requisitions", to: "/purchasing/requisitions", duty: "procurement.requisition.create" },
    { label: "Requisition approvals", to: "/purchasing/approvals", duty: "procurement.purchase-order.inquire" },
    { label: "Consolidation", to: "/purchasing/consolidation", duty: "procurement.purchase-order.maintain" },
    { label: "Release runs", to: "/purchasing/releases", duty: "procurement.purchase-order.inquire" },
    { label: "Requests for quotation", to: "/purchasing/rfqs", duty: "procurement.rfq.maintain" },
    { label: "RFQ types", to: "/purchasing/rfq-types", duty: "procurement.rfq.maintain" },
    { label: "Purchase orders", to: "/purchasing/purchase-orders", duty: "procurement.purchase-order.inquire" },
    { label: "Open supply", to: "/purchasing/open-supply", duty: "procurement.purchase-order.inquire" },
    { label: "Inquiries", to: "/purchasing/inquiries", duty: "procurement.purchase-order.inquire" },
    { label: "Changes from other services", to: "/purchasing/inbound-changes", duty: "procurement.purchase-order.inquire" },
    { label: "Price changes", to: "/purchasing/reprice", duty: "procurement.purchase-order.inquire" },
    { label: "Purchase agreements", to: "/purchasing/agreements", duty: "procurement.agreement.maintain" },
    { label: "Scheduling agreements", to: "/purchasing/scheduling-agreements", duty: "procurement.agreement.maintain" },
    { label: "Trade agreements", to: "/purchasing/trade-agreements", duty: "procurement.trade-agreement.maintain" },
    { label: "Price lookup", to: "/purchasing/pricing", duty: "procurement.purchase-order.maintain" },
    { label: "Catalogs", to: "/purchasing/catalogs", duty: "procurement.purchase-order.inquire" },
    { label: "Category vendors", to: "/purchasing/category-vendors", duty: "procurement.purchase-order.inquire" },
    { label: "Charge codes", to: "/purchasing/charge-codes", duty: "procurement.purchase-order.inquire" },
    { label: "Service models", to: "/purchasing/model-service-specs", duty: "procurement.purchase-order.inquire" },
    { label: "Signing limits", to: "/purchasing/signing-limits", duty: "procurement.purchase-order.inquire" },
    { label: "Policies", to: "/purchasing/policies", duty: "procurement.purchase-order.inquire" },
  ],
};

export default purchasing;
