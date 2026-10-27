import { createElement } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import type { PassageModule } from "../registry";
import type { ProductionOrder } from "./api/client";
import { AsBuiltInquiryPage } from "./AsBuiltInquiryPage";
import { CreateProductionOrderPage, type OrderValues } from "./CreateOrderPage";
import { ProductionOrderDetail } from "./OrderDetailPage";
import { ProductionOrdersPage } from "./OrdersPage";
import { PostingProfilesPage } from "./PostingProfilesPage";

const list = "/production";
const open = (id: string) => `${list}/${encodeURIComponent(id)}`;

/** The create form prefilled for a rework order of `original` (FR-21.20): the product itself is the first component. */
export function reworkValues(original: ProductionOrder): Partial<OrderValues> {
  return {
    rework: true,
    referenceType: "order",
    referenceId: original.id,
    productId: original.productId,
    siteId: original.siteId,
    warehouseId: original.warehouseId,
    currency: original.currency,
    itemGroup: original.itemGroup ?? "",
    lines: [{ lineNo: "10", itemId: original.productId, qtyPer: "1", scrapPct: "", unitCost: "" }],
  };
}

function OrdersRoute() {
  const navigate = useNavigate();
  return createElement(ProductionOrdersPage, {
    onOpen: (order) => navigate(open(order.id)),
    onCreate: () => navigate(`${list}/new`),
  });
}

function CreateRoute() {
  const navigate = useNavigate();
  const state = useLocation().state as { initial?: Partial<OrderValues> } | null;
  return createElement(CreateProductionOrderPage, {
    initial: state?.initial,
    onCreated: (order) => navigate(open(order.id)),
    onCancel: () => navigate(list),
  });
}

function DetailRoute() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  return createElement(ProductionOrderDetail, {
    key: id,
    orderId: id,
    onBack: () => navigate(list),
    onCreateRework: (order) => navigate(`${list}/new`, { state: { initial: reworkValues(order) } }),
    onOpenOrder: (orderId) => navigate(open(orderId)),
  });
}

function AsBuiltRoute() {
  const navigate = useNavigate();
  return createElement(AsBuiltInquiryPage, { onOpenOrder: (orderId) => navigate(open(orderId)) });
}

const production: PassageModule = {
  id: "production",
  title: "Production",
  description:
    "Production orders: create, estimate, release, start, post picking lists, route cards and reports as finished, end with settlement; rework orders, as-built genealogy and posting profiles.",
  routes: [
    { path: list, element: createElement(OrdersRoute) },
    { path: `${list}/new`, element: createElement(CreateRoute), handle: { title: "New production order" } },
    { path: `${list}/posting-profiles`, element: createElement(PostingProfilesPage), handle: { title: "Posting profiles" } },
    { path: `${list}/as-built`, element: createElement(AsBuiltRoute), handle: { title: "As-built and where-used" } },
    { path: `${list}/:id`, element: createElement(DetailRoute), handle: { title: "Production order" } },
  ],
  nav: [
    { label: "Production orders", to: list, duty: "production.order.read" },
    { label: "New production order", to: `${list}/new`, duty: "production.order.write" },
    { label: "As-built and where-used", to: `${list}/as-built`, duty: "production.order.read" },
    { label: "Posting profiles", to: `${list}/posting-profiles`, duty: "production.setup.read" },
  ],
};

export default production;
