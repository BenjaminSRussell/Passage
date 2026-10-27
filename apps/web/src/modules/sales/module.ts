import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AtpCheckPage } from "./AtpCheckPage";
import { CreditProfilePage, NewCreditProfilePage } from "./CreditProfileFormPage";
import { CreditProfilesPage } from "./CreditProfilesPage";
import { NewOrderPage } from "./NewOrderPage";
import { OrderDetailPage } from "./OrderDetailPage";
import { CreditHoldsPage, OrdersPage } from "./OrdersPage";

const sales: PassageModule = {
  id: "sales",
  title: "Sales",
  description: "Enter and confirm sales orders, release credit holds, maintain credit profiles and check available-to-promise.",
  routes: [
    { path: "/sales", element: createElement(OrdersPage), handle: { title: "Sales orders" } },
    { path: "/sales/orders", element: createElement(OrdersPage) },
    { path: "/sales/orders/new", element: createElement(NewOrderPage), handle: { title: "New sales order" } },
    { path: "/sales/orders/:id", element: createElement(OrderDetailPage), handle: { title: "Sales order" } },
    { path: "/sales/credit-holds", element: createElement(CreditHoldsPage) },
    { path: "/sales/credit-profiles", element: createElement(CreditProfilesPage) },
    { path: "/sales/credit-profiles/new", element: createElement(NewCreditProfilePage), handle: { title: "New credit profile" } },
    { path: "/sales/credit-profiles/:legalEntityId/:customerId", element: createElement(CreditProfilePage), handle: { title: "Credit profile" } },
    { path: "/sales/atp", element: createElement(AtpCheckPage) },
  ],
  nav: [
    { label: "Sales orders", to: "/sales/orders", duty: "sales.order.read" },
    { label: "New order", to: "/sales/orders/new", duty: "sales.order.write" },
    { label: "Credit release queue", to: "/sales/credit-holds", duty: "sales.credit.release" },
    { label: "Credit profiles", to: "/sales/credit-profiles", duty: "sales.order.read" },
    { label: "ATP check", to: "/sales/atp", duty: "sales.order.read" },
  ],
};

export default sales;
