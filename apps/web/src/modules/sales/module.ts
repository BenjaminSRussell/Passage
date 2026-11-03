import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AllocationQuotaDetailPage } from "./AllocationQuotaDetailPage";
import { AllocationQuotasPage, NewAllocationQuotaPage } from "./AllocationQuotasPage";
import { AgreementDetailPage } from "./AgreementDetailPage";
import { AgreementsPage, NewAgreementPage } from "./AgreementsPage";
import { AtpCheckPage } from "./AtpCheckPage";
import { BackordersPage } from "./BackordersPage";
import { BackorderRunDetailPage } from "./BackorderRunDetailPage";
import { BackorderRunPage } from "./BackorderRunPage";
import { BackorderRunsPage, NewBackorderRunPage } from "./BackorderRunsPage";
import { ChangeOrderPage } from "./ChangeOrderPage";
import { ChangePolicyPage } from "./ChangePolicyPage";
import { ChangeRequestDetailPage } from "./ChangeRequestDetailPage";
import { ChangeRequestsPage } from "./ChangeRequestsPage";
import { DeliveryTermsDetailPage, DeliveryTermsListPage, NewDeliveryTermsPage } from "./DeliveryTermsPage";
import { CreditProfilePage, NewCreditProfilePage } from "./CreditProfileFormPage";
import { CreditProfilesPage } from "./CreditProfilesPage";
import { NewOrderPage } from "./NewOrderPage";
import { NewOrderProgressPage, OrderProgressListPage } from "./OrderProgressPages";
import { OrderDetailPage } from "./OrderDetailPage";
import { IntakeErrorDetailPage } from "./IntakeErrorDetailPage";
import { IntakeErrorsPage } from "./IntakeErrorsPage";
import { HoldQueuePage } from "./HoldQueuePage";
import { OrdersPage } from "./OrdersPage";
import { NewProductListingPage, ProductListingsPage } from "./ProductListingsPage";
import { AlternativeItemsPage, NewAlternativeRulePage } from "./AlternativeItemsPage";
import { NewProposalRulePage, ProposalRulesPage } from "./ProposalRulesPage";
import { ParametersPage } from "./ParametersPage";
import { PromiseLinePage } from "./PromiseLinePage";
import { SalesWorkspacePage } from "./SalesWorkspacePage";
import { QuotationDetailPage } from "./QuotationDetailPage";
import { NewQuotationPage, QuotationsPage } from "./QuotationsPage";
import { ScheduleReleaseDetailPage } from "./ScheduleReleaseDetailPage";
import { ScheduleReleasePage } from "./ScheduleReleasePage";
import { NewScheduleReleasePage, ScheduleReleasesPage } from "./ScheduleReleasesPage";
import { NewSupplementaryRulePage, SupplementaryRulesPage } from "./SupplementaryRulesPage";

const sales: PassageModule = {
  id: "sales",
  title: "Sales",
  description:
    "See the sales workspace and quotation pipeline, expire lapsed quotations, quote and convert to orders, enter, confirm, change and cancel sales orders, approve or reject changes of customers that need change approval, apply holds and work the release queue, maintain credit profiles, the per legal entity sales parameters, product listings, customer delivery terms, supplementary items, product proposals, allocation quotas and sales agreements, record deliveries and invoices against orders, check available-to-promise, reconcile scheduling releases and store them against sales agreements, preview backorder runs and plan and commit them over the open order lines.",
  routes: [
    { path: "/sales", element: createElement(OrdersPage), handle: { title: "Sales orders", duty: "sales.order.read" } },
    { path: "/sales/workspace", element: createElement(SalesWorkspacePage), handle: { title: "Sales workspace", duty: "sales.order.read" } },
    { path: "/sales/orders", element: createElement(OrdersPage) },
    { path: "/sales/orders/new", element: createElement(NewOrderPage), handle: { title: "New sales order" } },
    { path: "/sales/orders/:id", element: createElement(OrderDetailPage), handle: { title: "Sales order" } },
    { path: "/sales/orders/:id/change", element: createElement(ChangeOrderPage), handle: { title: "Change sales order" } },
    { path: "/sales/orders/:id/progress/new", element: createElement(NewOrderProgressPage), handle: { title: "Record delivery or invoice" } },
    { path: "/sales/orders/:id/promise", element: createElement(PromiseLinePage), handle: { title: "Promise order line" } },
    { path: "/sales/order-progress", element: createElement(OrderProgressListPage), handle: { title: "Deliveries and invoices", duty: "sales.order.read" } },
    { path: "/sales/change-requests", element: createElement(ChangeRequestsPage) },
    { path: "/sales/change-requests/:id", element: createElement(ChangeRequestDetailPage), handle: { title: "Change request" } },
    { path: "/sales/change-policy", element: createElement(ChangePolicyPage), handle: { title: "Customer change approval", duty: "sales.order.read" } },
    { path: "/sales/backorders", element: createElement(BackordersPage), handle: { title: "Backorders" } },
    { path: "/sales/credit-holds", element: createElement(HoldQueuePage) },
    { path: "/sales/credit-profiles", element: createElement(CreditProfilesPage) },
    { path: "/sales/credit-profiles/new", element: createElement(NewCreditProfilePage), handle: { title: "New credit profile" } },
    { path: "/sales/credit-profiles/:legalEntityId/:customerId", element: createElement(CreditProfilePage), handle: { title: "Credit profile" } },
    { path: "/sales/atp", element: createElement(AtpCheckPage) },
    { path: "/sales/schedule-releases", element: createElement(ScheduleReleasePage), handle: { title: "Scheduling release check" } },
    { path: "/sales/backorder-runs", element: createElement(BackorderRunPage), handle: { title: "Backorder run preview" } },
    { path: "/sales/backorder-processing", element: createElement(BackorderRunsPage), handle: { title: "Backorder runs" } },
    { path: "/sales/backorder-processing/new", element: createElement(NewBackorderRunPage), handle: { title: "New backorder run" } },
    { path: "/sales/backorder-processing/:id", element: createElement(BackorderRunDetailPage), handle: { title: "Backorder run" } },
    { path: "/sales/product-listings", element: createElement(ProductListingsPage) },
    { path: "/sales/product-listings/new", element: createElement(NewProductListingPage), handle: { title: "New listing entry" } },
    { path: "/sales/agreements", element: createElement(AgreementsPage) },
    { path: "/sales/agreements/new", element: createElement(NewAgreementPage), handle: { title: "New sales agreement" } },
    { path: "/sales/agreements/:id", element: createElement(AgreementDetailPage), handle: { title: "Sales agreement" } },
    { path: "/sales/agreements/:id/schedule-releases", element: createElement(ScheduleReleasesPage), handle: { title: "Scheduling releases" } },
    { path: "/sales/agreements/:id/schedule-releases/new", element: createElement(NewScheduleReleasePage), handle: { title: "New scheduling release" } },
    { path: "/sales/agreements/:id/schedule-releases/:releaseId", element: createElement(ScheduleReleaseDetailPage), handle: { title: "Scheduling release" } },
    { path: "/sales/quotations", element: createElement(QuotationsPage) },
    { path: "/sales/quotations/new", element: createElement(NewQuotationPage), handle: { title: "New sales quotation" } },
    { path: "/sales/quotations/:id", element: createElement(QuotationDetailPage), handle: { title: "Sales quotation" } },
    { path: "/sales/supplementary-items", element: createElement(SupplementaryRulesPage) },
    { path: "/sales/supplementary-items/new", element: createElement(NewSupplementaryRulePage), handle: { title: "New supplementary item rule" } },
    { path: "/sales/product-proposals", element: createElement(ProposalRulesPage) },
    { path: "/sales/alternative-items", element: createElement(AlternativeItemsPage) },
    { path: "/sales/alternative-items/new", element: createElement(NewAlternativeRulePage), handle: { title: "New alternative item rule" } },
    { path: "/sales/intake-errors", element: createElement(IntakeErrorsPage), handle: { title: "Intake errors", duty: "sales.order.read" } },
    { path: "/sales/intake-errors/:id", element: createElement(IntakeErrorDetailPage), handle: { title: "Intake error" } },
    { path: "/sales/parameters", element: createElement(ParametersPage), handle: { title: "Sales parameters" } },
    { path: "/sales/allocation-quotas", element: createElement(AllocationQuotasPage) },
    { path: "/sales/allocation-quotas/new", element: createElement(NewAllocationQuotaPage), handle: { title: "New allocation quota" } },
    { path: "/sales/allocation-quotas/:id", element: createElement(AllocationQuotaDetailPage), handle: { title: "Allocation quota" } },
    { path: "/sales/delivery-terms", element: createElement(DeliveryTermsListPage), handle: { title: "Delivery terms", duty: "sales.order.read" } },
    { path: "/sales/delivery-terms/new", element: createElement(NewDeliveryTermsPage), handle: { title: "New delivery terms" } },
    { path: "/sales/delivery-terms/:id", element: createElement(DeliveryTermsDetailPage), handle: { title: "Delivery terms" } },
    { path: "/sales/product-proposals/new", element: createElement(NewProposalRulePage), handle: { title: "New product proposal rule" } },
  ],
  nav: [
    { label: "Sales workspace", to: "/sales/workspace", duty: "sales.order.read" },
    { label: "Sales orders", to: "/sales/orders", duty: "sales.order.read" },
    { label: "New order", to: "/sales/orders/new", duty: "sales.order.write" },
    { label: "Deliveries and invoices", to: "/sales/order-progress", duty: "sales.order.read" },
    { label: "Change requests", to: "/sales/change-requests", duty: "sales.order.read" },
    { label: "Quotations", to: "/sales/quotations", duty: "sales.order.read" },
    { label: "Credit release queue", to: "/sales/credit-holds", duty: "sales.credit.release" },
    { label: "Credit profiles", to: "/sales/credit-profiles", duty: "sales.order.read" },
    { label: "ATP check", to: "/sales/atp", duty: "sales.order.read" },
    { label: "Scheduling release check", to: "/sales/schedule-releases", duty: "sales.agreement.maintain" },
    { label: "Backorders", to: "/sales/backorders", duty: "sales.order.read" },
    { label: "Backorder run preview", to: "/sales/backorder-runs", duty: "sales.backorder.run" },
    { label: "Backorder runs", to: "/sales/backorder-processing", duty: "sales.order.read" },
    { label: "Product listings", to: "/sales/product-listings", duty: "sales.order.read" },
    { label: "Sales agreements", to: "/sales/agreements", duty: "sales.order.read" },
    { label: "Supplementary items", to: "/sales/supplementary-items", duty: "sales.order.read" },
    { label: "Product proposals", to: "/sales/product-proposals", duty: "sales.order.read" },
    { label: "Alternative items", to: "/sales/alternative-items", duty: "sales.order.read" },
    { label: "Allocation quotas", to: "/sales/allocation-quotas", duty: "sales.order.read" },
    { label: "Delivery terms", to: "/sales/delivery-terms", duty: "sales.order.read" },
    { label: "Intake errors", to: "/sales/intake-errors", duty: "sales.order.read" },
    { label: "Sales parameters", to: "/sales/parameters", duty: "sales.order.read" },
  ],
};

export default sales;
