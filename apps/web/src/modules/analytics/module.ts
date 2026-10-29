import { createElement } from "react";
import type { PassageModule } from "../registry";
import { ArchivePage, ArchivePartitionPage } from "./ArchivePage";
import { DashboardCreatePage, DashboardDetailPage, DashboardsPage } from "./DashboardsPage";
import { DataQualityPage, DQRuleCreatePage, DQRuleDetailPage } from "./DataQualityPage";
import { ExplorerPage } from "./ExplorerPage";
import { ExportsPage } from "./ExportsPage";
import { LineagePage } from "./LineagePage";
import { MetricDetailPage, MetricsPage } from "./MetricsPage";
import { ReconciliationPage } from "./ReconciliationPage";
import { ScheduleCreatePage, ScheduleDetailPage, SchedulesPage } from "./SchedulesPage";

const analytics: PassageModule = {
  id: "analytics",
  title: "Analytics",
  description:
    "Governed metrics, dashboards, the explorer and its exports, the daily reconciliation of metrics against the ledger, data-quality rules, scheduled reports and their deliveries, catalog lineage and archive partitions.",
  routes: [
    { path: "/analytics", element: createElement(MetricsPage), handle: { title: "Governed metrics" } },
    { path: "/analytics/metrics", element: createElement(MetricsPage) },
    { path: "/analytics/metrics/:id", element: createElement(MetricDetailPage), handle: { title: "Governed metric" } },
    { path: "/analytics/dashboards", element: createElement(DashboardsPage), handle: { title: "Dashboards" } },
    { path: "/analytics/dashboards/new", element: createElement(DashboardCreatePage), handle: { title: "New dashboard" } },
    { path: "/analytics/dashboards/:id", element: createElement(DashboardDetailPage), handle: { title: "Dashboard" } },
    { path: "/analytics/explorer", element: createElement(ExplorerPage), handle: { title: "Explorer" } },
    { path: "/analytics/exports", element: createElement(ExportsPage), handle: { title: "Exports" } },
    { path: "/analytics/reconciliation", element: createElement(ReconciliationPage), handle: { title: "Metric reconciliation" } },
    { path: "/analytics/data-quality", element: createElement(DataQualityPage), handle: { title: "Data quality" } },
    { path: "/analytics/data-quality/new", element: createElement(DQRuleCreatePage), handle: { title: "New data-quality rule" } },
    { path: "/analytics/data-quality/:ruleId", element: createElement(DQRuleDetailPage), handle: { title: "Data-quality rule" } },
    { path: "/analytics/schedules", element: createElement(SchedulesPage), handle: { title: "Scheduled reports" } },
    { path: "/analytics/schedules/new", element: createElement(ScheduleCreatePage), handle: { title: "New schedule" } },
    { path: "/analytics/schedules/:id", element: createElement(ScheduleDetailPage), handle: { title: "Schedule" } },
    { path: "/analytics/lineage", element: createElement(LineagePage), handle: { title: "Lineage" } },
    { path: "/analytics/archive", element: createElement(ArchivePage), handle: { title: "Archive partitions" } },
    { path: "/analytics/archive/:id", element: createElement(ArchivePartitionPage), handle: { title: "Archive partition" } },
  ],
  nav: [
    { label: "Governed metrics", to: "/analytics/metrics", duty: "AnalyticsView" },
    { label: "Dashboards", to: "/analytics/dashboards", duty: "AnalyticsView" },
    { label: "Explorer", to: "/analytics/explorer", duty: "AnalyticsView" },
    { label: "Exports", to: "/analytics/exports", duty: "AnalyticsExport" },
    { label: "Metric reconciliation", to: "/analytics/reconciliation", duty: "FinanceView" },
    { label: "Data quality", to: "/analytics/data-quality", duty: "CatalogSteward" },
    { label: "Scheduled reports", to: "/analytics/schedules", duty: "AnalyticsAuthor" },
    { label: "Lineage", to: "/analytics/lineage", duty: "CatalogView" },
    { label: "Archive partitions", to: "/analytics/archive", duty: "ArchiveAdmin" },
  ],
};

export default analytics;
