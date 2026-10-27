import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AbcRunPage } from "./AbcRunPage";
import { AbcRunsPage } from "./AbcRunsPage";
import { BlockingsPage } from "./BlockingsPage";
import { CountPlansPage } from "./CountPlansPage";
import { CountSheetPage } from "./CountSheetPage";
import { CountSheetsPage } from "./CountSheetsPage";
import { InTransitPage } from "./InTransitPage";
import { JournalDetailPage } from "./JournalDetailPage";
import { JournalsPage } from "./JournalsPage";
import { NewBlockingPage } from "./NewBlockingPage";
import { NewCountPlanPage } from "./NewCountPlanPage";
import { NewJournalPage } from "./NewJournalPage";
import { NewAbcRunPage } from "./NewAbcRunPage";
import { NewReservationPage } from "./NewReservationPage";
import { NewTransferPage } from "./NewTransferPage";
import { OnHandPage } from "./OnHandPage";
import { ReservationsPage } from "./ReservationsPage";
import { TransactionsPage } from "./TransactionsPage";
import { TransferOrderPage } from "./TransferOrderPage";
import { TransferOrdersPage } from "./TransferOrdersPage";

const inventory: PassageModule = {
  id: "inventory",
  title: "Inventory",
  description:
    "On-hand stock by any dimension, the journals and transactions that move it, reservations, blocking, cycle counts, transfer orders and ABC classification.",
  routes: [
    { path: "/inventory", element: createElement(OnHandPage), handle: { title: "On-hand inquiry" } },
    { path: "/inventory/onhand", element: createElement(OnHandPage) },
    { path: "/inventory/journals", element: createElement(JournalsPage) },
    { path: "/inventory/journals/new", element: createElement(NewJournalPage) },
    { path: "/inventory/journals/:id", element: createElement(JournalDetailPage), handle: { title: "Journal details" } },
    { path: "/inventory/transactions", element: createElement(TransactionsPage) },
    { path: "/inventory/reservations", element: createElement(ReservationsPage) },
    { path: "/inventory/reservations/new", element: createElement(NewReservationPage) },
    { path: "/inventory/blockings", element: createElement(BlockingsPage) },
    { path: "/inventory/blockings/new", element: createElement(NewBlockingPage) },
    { path: "/inventory/count-plans", element: createElement(CountPlansPage) },
    { path: "/inventory/count-plans/new", element: createElement(NewCountPlanPage) },
    { path: "/inventory/count-sheets", element: createElement(CountSheetsPage) },
    { path: "/inventory/count-sheets/:id", element: createElement(CountSheetPage), handle: { title: "Count sheet" } },
    { path: "/inventory/transfer-orders", element: createElement(TransferOrdersPage) },
    { path: "/inventory/transfer-orders/new", element: createElement(NewTransferPage) },
    { path: "/inventory/transfer-orders/:id", element: createElement(TransferOrderPage), handle: { title: "Transfer order" } },
    { path: "/inventory/in-transit", element: createElement(InTransitPage) },
    { path: "/inventory/abc-runs", element: createElement(AbcRunsPage) },
    { path: "/inventory/abc-runs/new", element: createElement(NewAbcRunPage) },
    { path: "/inventory/abc-runs/:id", element: createElement(AbcRunPage), handle: { title: "ABC run" } },
  ],
  nav: [
    { label: "On-hand", to: "/inventory/onhand", duty: "inventory.read" },
    { label: "Journals", to: "/inventory/journals", duty: "inventory.read" },
    { label: "New journal", to: "/inventory/journals/new", duty: "inventory.journal.write" },
    { label: "Transactions", to: "/inventory/transactions", duty: "inventory.read" },
    { label: "Reservations", to: "/inventory/reservations", duty: "inventory.read" },
    { label: "Blocking", to: "/inventory/blockings", duty: "inventory.read" },
    { label: "Count plans", to: "/inventory/count-plans", duty: "inventory.read" },
    { label: "Count sheets", to: "/inventory/count-sheets", duty: "inventory.read" },
    { label: "Transfer orders", to: "/inventory/transfer-orders", duty: "inventory.read" },
    { label: "Stock in transit", to: "/inventory/in-transit", duty: "inventory.read" },
    { label: "ABC classification", to: "/inventory/abc-runs", duty: "inventory.read" },
  ],
};

export default inventory;
