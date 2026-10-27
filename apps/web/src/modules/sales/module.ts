import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AgreementDetailPage } from "./AgreementDetailPage";
import { AgreementsPage, NewAgreementPage } from "./AgreementsPage";
import { AtpCheckPage } from "./AtpCheckPage";
import { BackorderRunPage } from "./BackorderRunPage";
import { ChangeOrderPage } from "./ChangeOrderPage";
import { CreditProfilePage, NewCreditProfilePage } from "./CreditProfileFormPage";
import { CreditProfilesPage } from "./CreditProfilesPage";
import { NewOrderPage } from "./NewOrderPage";
import { OrderDetailPage } from "./OrderDetailPage";
import { CreditHoldsPage, OrdersPage } from "./OrdersPage";
import { NewProductListingPage, ProductListingsPage } from "./ProductListingsPage";
import { ScheduleReleasePage } from "./ScheduleReleasePage";

const sales: PassageModule = {
  id: "sales",
  title: "Sales",
  description:
    "Enter, confirm, change and cancel sales orders, apply and release holds, maintain credit profiles, product listings and sales agreements, check available-to-promise, reconcile scheduling releases and preview backorder runs.",
  routes: [
    { path: "/sales", element: createElement(OrdersPage), handle: { title: "Sales orders" } },
    { path: "/sales/orders", element: createElement(OrdersPage) },
    { path: "/sales/orders/new", element: createElement(NewOrderPage), handle: { title: "New sales order" } },
    { path: "/sales/orders/:id", element: createElement(OrderDetailPage), handle: { title: "Sales order" } },
    { path: "/sales/orders/:id/change", element: createElement(ChangeOrderPage), handle: { title: "Change sales order" } },
    { path: "/sales/credit-holds", element: createElement(CreditHoldsPage) },
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
  ],
  nav: [
    { label: "Sales orders", to: "/sales/orders", duty: "sales.order.read" },
    { label: "New order", to: "/sales/orders/new", duty: "sales.order.write" },
    { label: "Credit release queue", to: "/sales/credit-holds", duty: "sales.credit.release" },
    { label: "Credit profiles", to: "/sales/credit-profiles", duty: "sales.order.read" },
    { label: "ATP check", to: "/sales/atp", duty: "sales.order.read" },
    { label: "Scheduling release check", to: "/sales/schedule-releases", duty: "sales.agreement.maintain" },
    { label: "Backorder run preview", to: "/sales/backorder-runs", duty: "sales.backorder.run" },
    { label: "Product listings", to: "/sales/product-listings", duty: "sales.order.read" },
    { label: "Sales agreements", to: "/sales/agreements", duty: "sales.order.read" },
  ],
};

export default sales;
