import { createElement } from "react";
import type { PassageModule } from "../registry";
import { CloseHome } from "./pages";

const close: PassageModule = {
  id: "close",
  title: "Period close",
  description: "Close your books on time with tasks, reviews and period locks.",
  routes: [{ path: "/close", element: createElement(CloseHome) }],
  nav: [{ label: "Period close", to: "/close", duty: "period.read" }],
};

export default close;
