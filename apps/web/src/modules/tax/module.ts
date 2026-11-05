import { createElement } from "react";
import type { PassageModule } from "../registry";
import { TaxHome } from "./pages";

const tax: PassageModule = {
  id: "tax",
  title: "Tax",
  description: "Tax codes, rates and the rules that apply them.",
  routes: [{ path: "/tax", element: createElement(TaxHome) }],
  nav: [{ label: "Tax configuration", to: "/tax", duty: "tax.code.maintain" }],
};

export default tax;
