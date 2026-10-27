import { createElement } from "react";
import { useNavigate, useParams } from "react-router-dom";
import type { PassageModule } from "../registry";
import { CreateProductionOrderPage } from "./CreateOrderPage";
import { ProductionOrderDetail } from "./OrderDetailPage";
import { ProductionOrdersPage } from "./OrdersPage";

const list = "/production";

function OrdersRoute() {
  const navigate = useNavigate();
  return createElement(ProductionOrdersPage, {
    onOpen: (order) => navigate(`${list}/${encodeURIComponent(order.id)}`),
    onCreate: () => navigate(`${list}/new`),
  });
}

function CreateRoute() {
  const navigate = useNavigate();
  return createElement(CreateProductionOrderPage, {
    onCreated: (order) => navigate(`${list}/${encodeURIComponent(order.id)}`),
    onCancel: () => navigate(list),
  });
}

function DetailRoute() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  return createElement(ProductionOrderDetail, { key: id, orderId: id, onBack: () => navigate(list) });
}

const production: PassageModule = {
  id: "production",
  title: "Production",
  description: "Production orders: create, estimate, release, start, report as finished and end, with planned cost.",
  routes: [
    { path: list, element: createElement(OrdersRoute) },
    { path: `${list}/new`, element: createElement(CreateRoute), handle: { title: "New production order" } },
    { path: `${list}/:id`, element: createElement(DetailRoute), handle: { title: "Production order" } },
  ],
  nav: [
    { label: "Production orders", to: list, duty: "production.order.read" },
    { label: "New production order", to: `${list}/new`, duty: "production.order.write" },
  ],
};

export default production;
