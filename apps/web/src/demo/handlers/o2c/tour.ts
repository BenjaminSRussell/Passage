import type { TourStop } from "../../tour";

// Order to cash screens for the home page demo tour, in walkthrough order.
export const TOUR: TourStop[] = [
  { area: "Order to cash", label: "Sales orders", to: "/sales/orders", blurb: "Open, confirmed and shipped orders from six customers, one on credit hold." },
];
