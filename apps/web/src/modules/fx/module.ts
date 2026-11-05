import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ConvertPage } from "./ConvertPage";
import { LookupPage } from "./LookupPage";
import { RateEntryPage } from "./RateEntryPage";
import { RatesPage } from "./RatesPage";
import { RateTypesPage } from "./RateTypesPage";

const fx: PassageModule = {
  id: "fx",
  title: "Exchange rates",
  description: "Stored exchange rates and their audit history, the rate that applies to a pair on a date, and amount conversion with minor-unit rounding.",
  routes: [
    { path: "/fx/rates", element: createElement(RatesPage), handle: { title: "Exchange rates" } },
    { path: "/fx/lookup", element: createElement(LookupPage), handle: { title: "Rate lookup" } },
    { path: "/fx/convert", element: createElement(ConvertPage), handle: { title: "Convert an amount" } },
    { path: "/fx/rate-entry", element: createElement(RateEntryPage), handle: { title: "Enter a rate" } },
    { path: "/fx/rate-types", element: createElement(RateTypesPage), handle: { title: "Rate types" } },
  ],
  nav: [
    { label: "Exchange rates", to: "/fx/rates", duty: "fx.rate.view" },
    { label: "Rate lookup", to: "/fx/lookup", duty: "fx.rate.view" },
    { label: "Convert an amount", to: "/fx/convert", duty: "fx.rate.view" },
    { label: "Enter a rate", to: "/fx/rate-entry", duty: "fx.rate.maintain" },
    { label: "Rate types", to: "/fx/rate-types", duty: "fx.rate.maintain" },
  ],
};

export default fx;
