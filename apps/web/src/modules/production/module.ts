import { createElement } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import type { PassageModule } from "../registry";
import type { ProductionOrder } from "./api/client";
import { AsBuiltInquiryPage } from "./AsBuiltInquiryPage";
import { CreateProductionOrderPage, type OrderValues } from "./CreateOrderPage";
import { HousekeepingPage } from "./HousekeepingPage";
import { JournalInquiryPage } from "./JournalInquiryPage";
import { KpiPage } from "./KpiPage";
import { OcmProfilesPage } from "./OcmProfilesPage";
import { ParametersPage } from "./ParametersPage";
import { OperationsOverviewPage } from "./OperationsOverviewPage";
import { OrderChangeDetailPage } from "./OrderChangeDetailPage";
import { OrderChangesPage } from "./OrderChangesPage";
import { ProductionOrderDetail } from "./OrderDetailPage";
import { ProductionOrdersPage } from "./OrdersPage";
import { PostingProfilesPage } from "./PostingProfilesPage";
import { SubcontractOperationsPage } from "./SubcontractOperationsPage";
import { VarianceReportPage } from "./VarianceReportPage";
import { WIPReportPage } from "./WIPReportPage";

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

function WIPRoute() {
  const navigate = useNavigate();
  return createElement(WIPReportPage, { onOpenOrder: (orderId) => navigate(open(orderId)) });
}

function VariancesRoute() {
  const navigate = useNavigate();
  return createElement(VarianceReportPage, { onOpenOrder: (orderId) => navigate(open(orderId)) });
}

function SubcontractRoute() {
  const navigate = useNavigate();
  return createElement(SubcontractOperationsPage, { onOpenOrder: (orderId) => navigate(open(orderId)) });
}

function OperationsRoute() {
  const navigate = useNavigate();
  return createElement(OperationsOverviewPage, { onOpenOrder: (orderId) => navigate(open(orderId)) });
}

function JournalsRoute() {
  const navigate = useNavigate();
  return createElement(JournalInquiryPage, { onOpenOrder: (orderId) => navigate(open(orderId)) });
}

function ChangesRoute() {
  const navigate = useNavigate();
  return createElement(OrderChangesPage, { onOpenChange: (change) => navigate(`${list}/order-changes/${encodeURIComponent(change.id)}`) });
}

function ChangeDetailRoute() {
  const { changeId = "" } = useParams();
  const navigate = useNavigate();
  return createElement(OrderChangeDetailPage, {
    key: changeId,
    changeId,
    onBack: () => navigate(`${list}/order-changes`),
    onOpenOrder: (orderId) => navigate(open(orderId)),
  });
}

const production: PassageModule = {
  id: "production",
  title: "Production",
  description:
    "Production orders: create, estimate, release, start, post picking lists, route cards and reports as finished, end with settlement; bulk status updates, split and merge, order-specific BOM and route edits; WIP balance and variance reports; subcontract operations with their vendor PO requests; the operations overview and the production journals across orders; the order KPIs (on-time %, cycle days, scrap %, overdue orders); order changes from engineering changes with preview, change cost, apply and dismiss; order housekeeping (cancel stale orders, end fully reported orders); rework orders, as-built genealogy, production parameters and posting profiles.",
  routes: [
    { path: list, element: createElement(OrdersRoute) },
    { path: `${list}/new`, element: createElement(CreateRoute), handle: { title: "New production order" } },
    { path: `${list}/housekeeping`, element: createElement(HousekeepingPage), handle: { title: "Order housekeeping" } },
    { path: `${list}/parameters`, element: createElement(ParametersPage), handle: { title: "Production parameters" } },
    { path: `${list}/posting-profiles`, element: createElement(PostingProfilesPage), handle: { title: "Posting profiles" } },
    { path: `${list}/as-built`, element: createElement(AsBuiltRoute), handle: { title: "As-built and where-used" } },
    { path: `${list}/wip-balances`, element: createElement(WIPRoute), handle: { title: "WIP balance by order" } },
    { path: `${list}/variances`, element: createElement(VariancesRoute), handle: { title: "Production variances" } },
    { path: `${list}/subcontract-operations`, element: createElement(SubcontractRoute), handle: { title: "Subcontract operations" } },
    { path: `${list}/operations`, element: createElement(OperationsRoute), handle: { title: "Operations overview" } },
    { path: `${list}/journals`, element: createElement(JournalsRoute), handle: { title: "Production journals" } },
    { path: `${list}/kpi`, element: createElement(KpiPage), handle: { title: "Production KPIs" } },
    { path: `${list}/order-changes`, element: createElement(ChangesRoute), handle: { title: "Order changes" } },
    { path: `${list}/order-changes/:changeId`, element: createElement(ChangeDetailRoute), handle: { title: "Order change" } },
    { path: `${list}/ocm-profiles`, element: createElement(OcmProfilesPage), handle: { title: "Order change profiles" } },
    { path: `${list}/:id`, element: createElement(DetailRoute), handle: { title: "Production order" } },
  ],
  nav: [
    { label: "Production orders", to: list, duty: "production.order.read" },
    { label: "New production order", to: `${list}/new`, duty: "production.order.write" },
    { label: "As-built and where-used", to: `${list}/as-built`, duty: "production.order.read" },
    { label: "WIP balance", to: `${list}/wip-balances`, duty: "production.cost.read" },
    { label: "Production variances", to: `${list}/variances`, duty: "production.cost.read" },
    { label: "Subcontract operations", to: `${list}/subcontract-operations`, duty: "production.order.read" },
    { label: "Operations overview", to: `${list}/operations`, duty: "production.order.read" },
    { label: "Production journals", to: `${list}/journals`, duty: "production.order.read" },
    { label: "Production KPIs", to: `${list}/kpi`, duty: "production.order.read" },
    { label: "Order changes", to: `${list}/order-changes`, duty: "production.order.read" },
    { label: "Order housekeeping", to: `${list}/housekeeping`, duty: "production.order.read" },
    { label: "Order change profiles", to: `${list}/ocm-profiles`, duty: "production.setup.read" },
    { label: "Production parameters", to: `${list}/parameters`, duty: "production.setup.read" },
    { label: "Posting profiles", to: `${list}/posting-profiles`, duty: "production.setup.read" },
  ],
};

export default production;
