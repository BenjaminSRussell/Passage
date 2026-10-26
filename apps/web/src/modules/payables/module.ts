import { createElement } from "react";
import type { PassageModule } from "../registry";
import { PayablesHome } from "./pages";

const payables: PassageModule = {
  id: "payables",
  title: "Accounts payable",
  description: "Plan payment runs and pay vendors on schedule.",
  routes: [{ path: "/payables", element: createElement(PayablesHome) }],
  nav: [{ label: "Payment runs", to: "/payables", duty: "settlement.read" }],
};

export default payables;
