import type { PassageModule } from "../registry";

const close: PassageModule = {
  id: "close",
  title: "Period close",
  routes: [{ path: "/close", element: null }],
  nav: [{ label: "Period close", to: "/close", duty: "period.read" }],
};

export default close;
