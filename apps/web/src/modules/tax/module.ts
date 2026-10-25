import type { PassageModule } from "../registry";

const tax: PassageModule = {
  id: "tax",
  title: "Tax",
  routes: [{ path: "/tax", element: null }],
  nav: [{ label: "Tax configuration", to: "/tax", duty: "Maintain tax codes" }],
};

export default tax;
