import type { PassageModule } from "../registry";

const payables: PassageModule = {
  id: "payables",
  title: "Accounts payable",
  routes: [{ path: "/payables", element: null }],
  nav: [{ label: "Payment runs", to: "/payables", duty: "settlement.read" }],
};

export default payables;
