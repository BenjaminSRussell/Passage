import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AllocationQuotaDetailPage } from "./AllocationQuotaDetailPage";
import { AllocationQuotasPage, NewAllocationQuotaPage } from "./AllocationQuotasPage";
import { AgreementDetailPage } from "./AgreementDetailPage";
import { AgreementsPage, NewAgreementPage } from "./AgreementsPage";
import { AtpCheckPage } from "./AtpCheckPage";
import { BackordersPage } from "./BackordersPage";
import { BackorderRunPage } from "./BackorderRunPage";
import { ChangeOrderPage } from "./ChangeOrderPage";
import { CreditProfilePage, NewCreditProfilePage } from "./CreditProfileFormPage";
import { CreditProfilesPage } from "./CreditProfilesPage";
import { NewOrderPage } from "./NewOrderPage";
import { OrderDetailPage } from "./OrderDetailPage";
import { HoldQueuePage } from "./HoldQueuePage";
import { OrdersPage } from "./OrdersPage";
import { NewProductListingPage, ProductListingsPage } from "./ProductListingsPage";
import { NewProposalRulePage, ProposalRulesPage } from "./ProposalRulesPage";
import { PromiseLinePage } from "./PromiseLinePage";
import { QuotationDetailPage } from "./QuotationDetailPage";
import { NewQuotationPage, QuotationsPage } from "./QuotationsPage";
import { ScheduleReleasePage } from "./ScheduleReleasePage";
import { NewSupplementaryRulePage, SupplementaryRulesPage } from "./SupplementaryRulesPage";

const sales: PassageModule = {
  id: "sales",
  title: "Sales",
  description:
    "Quote and convert to orders, enter, confirm, change and cancel sales orders, apply holds and work the release queue, maintain credit profiles, product listings, supplementary items, product proposals, allocation quotas and sales agreements, check available-to-promise, reconcile scheduling releases and preview backorder runs.",
  routes: [
    { path: "/sales", element: createElement(OrdersPage), handle: { title: "Sales orders", duty: "sales.order.read" } },
    { path: "/sales/orders", element: createElement(OrdersPage) },
    { path: "/sales/orders/new", element: createElement(NewOrderPage), handle: { title: "New sales order" } },
    { path: "/sales/orders/:id", element: createElement(OrderDetailPage), handle: { title: "Sales order" } },
    { path: "/sales/orders/:id/change", element: createElement(ChangeOrderPage), handle: { title: "Change sales order" } },
    { path: "/sales/orders/:id/promise", element: createElement(PromiseLinePage), handle: { title: "Promise order line" } },
    { path: "/sales/backorders", element: createElement(BackordersPage), handle: { title: "Backorders" } },
    { path: "/sales/credit-holds", element: createElement(HoldQueuePage) },
    { path: "/sales/credit-profiles", element: createElement(CreditProfilesPage) },
    { path: "/sales/credit-profiles/new", element: createElement(NewCreditProfilePage), handle: { title: "New credit profile" } },
    { path: "/sales/credit-profiles/:legalEntityId/:customerId", element: createElement(CreditProfilePage), handle: { title: "Credit profile" } },
    { path: "/sales/atp", element: createElement(AtpCheckPage) },
    { path: "/sales/schedule-releases", element: createElement(ScheduleReleasePage), handle: { title: "Scheduling release check" } },
    { path: "/sales/backorder-runs", element: createElement(BackorderRunPage), handle: { title: "Backorder run preview" } },
    { path: "/sales/product-listings", element: createElement(ProductListingsPage) },
    { path: "/sales/product-listings/new", element: createElement(NewProductListingPage), handle: { title: "New listing entry" } },
    { path: "/sales/agreements", element: createElement(AgreementsPage) },
    { path: "/sales/agreements/new", element: createElement(NewAgreementPage), handle: { title: "New sales agreement" } },
    { path: "/sales/agreements/:id", element: createElement(AgreementDetailPage), handle: { title: "Sales agreement" } },
    { path: "/sales/quotations", element: createElement(QuotationsPage) },
    { path: "/sales/quotations/new", element: createElement(NewQuotationPage), handle: { title: "New sales quotation" } },
    { path: "/sales/quotations/:id", element: createElement(QuotationDetailPage), handle: { title: "Sales quotation" } },
    { path: "/sales/supplementary-items", element: createElement(SupplementaryRulesPage) },
    { path: "/sales/supplementary-items/new", element: createElement(NewSupplementaryRulePage), handle: { title: "New supplementary item rule" } },
    { path: "/sales/product-proposals", element: createElement(ProposalRulesPage) },
    { path: "/sales/allocation-quotas", element: createElement(AllocationQuotasPage) },
    { path: "/sales/allocation-quotas/new", element: createElement(NewAllocationQuotaPage), handle: { title: "New allocation quota" } },
    { path: "/sales/allocation-quotas/:id", element: createElement(AllocationQuotaDetailPage), handle: { title: "Allocation quota" } },
    { path: "/sales/product-proposals/new", element: createElement(NewProposalRulePage), handle: { title: "New product proposal rule" } },
  ],
  nav: [
    { label: "Sales orders", to: "/sales/orders", duty: "sales.order.read" },
    { label: "New order", to: "/sales/orders/new", duty: "sales.order.write" },
    { label: "Quotations", to: "/sales/quotations", duty: "sales.order.read" },
    { label: "Credit release queue", to: "/sales/credit-holds", duty: "sales.credit.release" },
    { label: "Credit profiles", to: "/sales/credit-profiles", duty: "sales.order.read" },
    { label: "ATP check", to: "/sales/atp", duty: "sales.order.read" },
    { label: "Scheduling release check", to: "/sales/schedule-releases", duty: "sales.agreement.maintain" },
    { label: "Backorders", to: "/sales/backorders", duty: "sales.order.read" },
    { label: "Backorder run preview", to: "/sales/backorder-runs", duty: "sales.backorder.run" },
    { label: "Product listings", to: "/sales/product-listings", duty: "sales.order.read" },
    { label: "Sales agreements", to: "/sales/agreements", duty: "sales.order.read" },
    { label: "Supplementary items", to: "/sales/supplementary-items", duty: "sales.order.read" },
    { label: "Product proposals", to: "/sales/product-proposals", duty: "sales.order.read" },
    { label: "Allocation quotas", to: "/sales/allocation-quotas", duty: "sales.order.read" },
  ],
};

export default sales;
