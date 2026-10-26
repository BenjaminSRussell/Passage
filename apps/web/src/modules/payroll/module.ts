import { createElement } from "react";
import type { PassageModule } from "../registry";
import { PayrollHome } from "./pages";

const payroll: PassageModule = {
  id: "payroll",
  title: "Payroll",
  description: "Pay cycles, pay statements and gross-to-net previews.",
  routes: [{ path: "/payroll", element: createElement(PayrollHome) }],
  nav: [{ label: "Pay statement preview", to: "/payroll", duty: "payroll.run.maintain" }],
};

export default payroll;
