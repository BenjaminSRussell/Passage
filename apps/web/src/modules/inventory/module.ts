import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AbcRunPage } from "./AbcRunPage";
import { AbcRunsPage } from "./AbcRunsPage";
import { BlockingsPage } from "./BlockingsPage";
import { ConsignmentOrderPage } from "./ConsignmentOrderPage";
import { ConsignmentOrdersPage } from "./ConsignmentOrdersPage";
import { ConsignmentStockPage } from "./ConsignmentStockPage";
import { CountPlansPage } from "./CountPlansPage";
import { CountSheetPage } from "./CountSheetPage";
import { CountVariancePage } from "./CountVariancePage";
import { DimensionCombinationsPage } from "./DimensionCombinationsPage";
import { CountSheetsPage } from "./CountSheetsPage";
import { CountTagsPage } from "./CountTagsPage";
import { InTransitPage } from "./InTransitPage";
import { JournalDetailPage } from "./JournalDetailPage";
import { JournalsPage } from "./JournalsPage";
import { NewBlockingPage } from "./NewBlockingPage";
import { NewConsignmentPage } from "./NewConsignmentPage";
import { NewCountPlanPage } from "./NewCountPlanPage";
import { NewJournalPage } from "./NewJournalPage";
import { NewAbcRunPage } from "./NewAbcRunPage";
import { NewReservationPage } from "./NewReservationPage";
import { NewTransferPage } from "./NewTransferPage";
import { NewReconciliationPage } from "./NewReconciliationPage";
import { NegativeOnHandPage } from "./NegativeOnHandPage";
import { OnHandPage } from "./OnHandPage";
import { ReconciliationPage } from "./ReconciliationPage";
import { ReconciliationsPage } from "./ReconciliationsPage";
import { ReservationsPage } from "./ReservationsPage";
import { StatusChangesPage } from "./StatusChangesPage";
import { TrackingTracePage } from "./TrackingTracePage";
import { TransactionPage } from "./TransactionPage";
import { TransactionsPage } from "./TransactionsPage";
import { TransferLinesPage } from "./TransferLinesPage";
import { TransferOrderPage } from "./TransferOrderPage";
import { TransferOrdersPage } from "./TransferOrdersPage";

const inventory: PassageModule = {
  id: "inventory",
  title: "Inventory",
  description:
    "On-hand stock by any dimension, the journals that move it with their approval workflow, transactions with their status history and backward moves, reservations, blocking, cycle and tag counts, journal reversals, transfer orders, vendor consignment, ABC classification, the count variance, negative on-hand, batch and serial trace and dimension combination reports, and the daily inventory-to-ledger reconciliation.",
  routes: [
    { path: "/inventory", element: createElement(OnHandPage), handle: { title: "On-hand inquiry", duty: "inventory.read" } },
    { path: "/inventory/onhand", element: createElement(OnHandPage) },
    { path: "/inventory/journals", element: createElement(JournalsPage) },
    { path: "/inventory/journals/new", element: createElement(NewJournalPage) },
    { path: "/inventory/journals/:id", element: createElement(JournalDetailPage), handle: { title: "Journal details" } },
    { path: "/inventory/transactions", element: createElement(TransactionsPage) },
    { path: "/inventory/transactions/:id", element: createElement(TransactionPage), handle: { title: "Transaction" } },
    { path: "/inventory/status-changes", element: createElement(StatusChangesPage) },
    { path: "/inventory/reservations", element: createElement(ReservationsPage) },
    { path: "/inventory/reservations/new", element: createElement(NewReservationPage) },
    { path: "/inventory/blockings", element: createElement(BlockingsPage) },
    { path: "/inventory/blockings/new", element: createElement(NewBlockingPage) },
    { path: "/inventory/count-plans", element: createElement(CountPlansPage) },
    { path: "/inventory/count-plans/new", element: createElement(NewCountPlanPage) },
    { path: "/inventory/count-plans/:id/tags", element: createElement(CountTagsPage), handle: { title: "Count tags" } },
    { path: "/inventory/count-sheets", element: createElement(CountSheetsPage) },
    { path: "/inventory/count-sheets/:id", element: createElement(CountSheetPage), handle: { title: "Count sheet" } },
    { path: "/inventory/transfer-orders", element: createElement(TransferOrdersPage) },
    { path: "/inventory/transfer-orders/new", element: createElement(NewTransferPage) },
    { path: "/inventory/transfer-orders/:id", element: createElement(TransferOrderPage), handle: { title: "Transfer order" } },
    { path: "/inventory/transfer-lines", element: createElement(TransferLinesPage) },
    { path: "/inventory/in-transit", element: createElement(InTransitPage) },
    { path: "/inventory/abc-runs", element: createElement(AbcRunsPage) },
    { path: "/inventory/abc-runs/new", element: createElement(NewAbcRunPage) },
    { path: "/inventory/abc-runs/:id", element: createElement(AbcRunPage), handle: { title: "ABC run" } },
    { path: "/inventory/consignment-orders", element: createElement(ConsignmentOrdersPage) },
    { path: "/inventory/consignment-orders/new", element: createElement(NewConsignmentPage) },
    { path: "/inventory/consignment-orders/:id", element: createElement(ConsignmentOrderPage), handle: { title: "Consignment order" } },
    { path: "/inventory/consignment/stock", element: createElement(ConsignmentStockPage) },
    { path: "/inventory/reports/count-variance", element: createElement(CountVariancePage), handle: { title: "Count variance" } },
    { path: "/inventory/reports/negative-onhand", element: createElement(NegativeOnHandPage), handle: { title: "Negative on-hand" } },
    { path: "/inventory/reports/tracking-trace", element: createElement(TrackingTracePage), handle: { title: "Batch and serial trace" } },
    { path: "/inventory/reports/dimension-combinations", element: createElement(DimensionCombinationsPage), handle: { title: "Dimension combinations" } },
    { path: "/inventory/reconciliations", element: createElement(ReconciliationsPage) },
    { path: "/inventory/reconciliations/new", element: createElement(NewReconciliationPage) },
    { path: "/inventory/reconciliations/:id", element: createElement(ReconciliationPage), handle: { title: "Reconciliation run" } },
  ],
  nav: [
    { label: "On-hand", to: "/inventory/onhand", duty: "inventory.read" },
    { label: "Journals", to: "/inventory/journals", duty: "inventory.read" },
    { label: "New journal", to: "/inventory/journals/new", duty: "inventory.journal.write" },
    { label: "Transactions", to: "/inventory/transactions", duty: "inventory.read" },
    { label: "Status changes", to: "/inventory/status-changes", duty: "inventory.read" },
    { label: "Reservations", to: "/inventory/reservations", duty: "inventory.read" },
    { label: "Blocking", to: "/inventory/blockings", duty: "inventory.read" },
    { label: "Count plans", to: "/inventory/count-plans", duty: "inventory.read" },
    { label: "Count sheets", to: "/inventory/count-sheets", duty: "inventory.read" },
    { label: "Transfer orders", to: "/inventory/transfer-orders", duty: "inventory.read" },
    { label: "Transfer lines", to: "/inventory/transfer-lines", duty: "inventory.read" },
    { label: "Stock in transit", to: "/inventory/in-transit", duty: "inventory.read" },
    { label: "ABC classification", to: "/inventory/abc-runs", duty: "inventory.read" },
    { label: "Consignment orders", to: "/inventory/consignment-orders", duty: "inventory.read" },
    { label: "Consignment stock", to: "/inventory/consignment/stock", duty: "inventory.read" },
    { label: "Count variance", to: "/inventory/reports/count-variance", duty: "inventory.read" },
    { label: "Negative on-hand", to: "/inventory/reports/negative-onhand", duty: "inventory.read" },
    { label: "Batch and serial trace", to: "/inventory/reports/tracking-trace", duty: "inventory.read" },
    { label: "Dimension combinations", to: "/inventory/reports/dimension-combinations", duty: "inventory.read" },
    { label: "Reconciliation", to: "/inventory/reconciliations", duty: "inventory.read" },
  ],
};

export default inventory;
