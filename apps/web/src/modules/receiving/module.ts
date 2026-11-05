import { createElement } from "react";
import type { PassageModule } from "../registry";
import { NewArrivalPage } from "./NewArrivalPage";
import { ReceiptPage } from "./ReceiptPage";
import { ReceiptsPage } from "./ReceiptsPage";

const receiving: PassageModule = {
  id: "receiving",
  title: "Receiving",
  description: "Record deliveries at the dock, register what was received, post receipts to the ledger, and correct or cancel them while nothing is invoiced.",
  routes: [
    { path: "/receiving/receipts", element: createElement(ReceiptsPage), handle: { title: "Receipts" } },
    { path: "/receiving/receipts/new", element: createElement(NewArrivalPage), handle: { title: "Record arrival" } },
    { path: "/receiving/receipts/:id", element: createElement(ReceiptPage), handle: { title: "Receipt" } },
  ],
  nav: [
    { label: "Receipts", to: "/receiving/receipts", duty: "receiving.receipt.maintain" },
    { label: "Record arrival", to: "/receiving/receipts/new", duty: "receiving.receipt.maintain" },
  ],
};

export default receiving;
