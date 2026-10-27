import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ConvertPage } from "./ConvertPage";
import { LookupPage } from "./LookupPage";
import { RatesPage } from "./RatesPage";

const fx: PassageModule = {
  id: "fx",
  title: "Exchange rates",
  description: "Stored exchange rates and their audit history, the rate that applies to a pair on a date, and amount conversion with minor-unit rounding.",
  routes: [
    { path: "/fx/rates", element: createElement(RatesPage), handle: { title: "Exchange rates" } },
    { path: "/fx/lookup", element: createElement(LookupPage), handle: { title: "Rate lookup" } },
    { path: "/fx/convert", element: createElement(ConvertPage), handle: { title: "Convert an amount" } },
  ],
  nav: [
    { label: "Exchange rates", to: "/fx/rates", duty: "View exchange rates" },
    { label: "Rate lookup", to: "/fx/lookup", duty: "View exchange rates" },
    { label: "Convert an amount", to: "/fx/convert", duty: "View exchange rates" },
  ],
};

export default fx;
