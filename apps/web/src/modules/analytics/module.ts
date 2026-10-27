import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ExplorerPage } from "./ExplorerPage";
import { MetricDetailPage, MetricsPage } from "./MetricsPage";
import { ReconciliationPage } from "./ReconciliationPage";

const analytics: PassageModule = {
  id: "analytics",
  title: "Analytics",
  description: "Governed metrics, the explorer and the daily reconciliation of metrics against the ledger.",
  routes: [
    { path: "/analytics", element: createElement(MetricsPage), handle: { title: "Governed metrics" } },
    { path: "/analytics/metrics", element: createElement(MetricsPage) },
    { path: "/analytics/metrics/:id", element: createElement(MetricDetailPage), handle: { title: "Governed metric" } },
    { path: "/analytics/explorer", element: createElement(ExplorerPage), handle: { title: "Explorer" } },
    { path: "/analytics/reconciliation", element: createElement(ReconciliationPage), handle: { title: "Metric reconciliation" } },
  ],
  nav: [
    { label: "Governed metrics", to: "/analytics/metrics", duty: "AnalyticsView" },
    { label: "Explorer", to: "/analytics/explorer", duty: "AnalyticsView" },
    { label: "Metric reconciliation", to: "/analytics/reconciliation", duty: "FinanceView" },
  ],
};

export default analytics;
