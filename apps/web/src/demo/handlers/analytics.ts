// Analytics workspace dashboards (/api/v1/analytics/dashboards) and the governed metric catalogue their tiles are
// bound to. Tiles show the metric they name, so the metrics list is served too (the dashboard pages load it).
import type { Dashboard, Metric } from "../../modules/analytics/api/client";
import { reply, route } from "../backend";
import { at } from "../store";

const BASE = "/api/v1/analytics";

const metric = (metricId: string, name: string, formula: string, grain: Metric["grain"], allowedDimensions: string[], extra: Partial<Metric> = {}): Metric => ({
  metricId, version: 3, name, formula, grain, allowedDimensions, nonFinancial: false, status: "PUBLISHED", ...extra,
});

const METRICS: Metric[] = [
  metric("net_revenue", "Net revenue", "sum(credit - debit) where account_class = 'REVENUE'", "journal_line", ["legal_entity", "fiscal_period", "cost_center"], { reconciliationSource: "ledger.trial_balance" }),
  metric("gross_margin_pct", "Gross margin %", "(net_revenue - cogs) / net_revenue", "journal_line", ["legal_entity", "fiscal_period"], { reconciliationSource: "ledger.trial_balance" }),
  metric("cogs", "Cost of goods sold", "sum(debit - credit) where account_class = 'COGS'", "journal_line", ["legal_entity", "fiscal_period", "cost_center"]),
  metric("pump_units_shipped", "Pump units shipped", "sum(shipped_qty)", "sales_line", ["legal_entity", "product_family", "customer_group"], { nonFinancial: true }),
  metric("open_order_value", "Open order value", "sum(open_qty * unit_price)", "sales_line", ["legal_entity", "product_family", "customer_group"]),
  metric("on_time_delivery_pct", "On-time delivery %", "count(shipped_on_time) / count(shipped)", "sales_line", ["legal_entity", "customer_group"], { nonFinancial: true }),
  metric("machine_utilisation_pct", "Machine utilisation %", "sum(run_minutes) / sum(available_minutes)", "resource_shift", ["plant", "work_center", "shift"], { nonFinancial: true }),
  metric("scrap_rate_pct", "Scrap rate %", "sum(scrap_qty) / sum(produced_qty)", "resource_shift", ["plant", "work_center"], { nonFinancial: true }),
  metric("opex_run_rate", "Operating expense run rate", "sum(debit - credit) where account_class = 'OPEX'", "journal_line", ["legal_entity", "fiscal_period", "cost_center"], { status: "DRAFT", version: 1 }),
];

const SEED: Dashboard[] = [
  { dashboardId: "executive_overview", title: "Executive overview", refreshSeconds: 60, version: 5, updatedAt: at(-3), tiles: [
    { title: "Net revenue, year to date", metric: "net_revenue", period: "YTD" },
    { title: "Gross margin, quarter to date", metric: "gross_margin_pct", period: "QTD" },
    { title: "Pump units shipped this month", metric: "pump_units_shipped", period: "MTD" },
    { title: "Open order book", metric: "open_order_value", period: "MTD" },
  ] },
  { dashboardId: "plant_performance", title: "Plant performance", refreshSeconds: 30, version: 3, updatedAt: at(-9), tiles: [
    { title: "Machine utilisation, month to date", metric: "machine_utilisation_pct", period: "MTD" },
    { title: "Scrap rate, month to date", metric: "scrap_rate_pct", period: "MTD" },
    { title: "Units shipped, trailing 12 months", metric: "pump_units_shipped", period: "TRAILING_12M" },
  ] },
  { dashboardId: "sales_service_levels", title: "Sales and service levels", refreshSeconds: 120, version: 2, updatedAt: at(-21), tiles: [
    { title: "On-time delivery, quarter to date", metric: "on_time_delivery_pct", period: "QTD" },
    { title: "Net revenue, trailing 12 months", metric: "net_revenue", period: "TRAILING_12M" },
  ] },
  { dashboardId: "cost_watch", title: "Cost watch", refreshSeconds: 300, version: 1, updatedAt: at(-40), tiles: [
    { title: "Cost of goods sold, month to date", metric: "cogs", period: "MTD" },
    { title: "Cost of goods sold, year to date", metric: "cogs", period: "YTD" },
  ] },
];
const dashboards = new Map<string, Dashboard>(SEED.map((d) => [d.dashboardId, d]));

route("GET", `${BASE}/metrics`, () => ({ items: METRICS }));
route("GET", `${BASE}/dashboards`, () => ({ items: [...dashboards.values()] }));
route("GET", `${BASE}/dashboards/{id}`, ({ params }) => dashboards.get(params.id) ?? reply.problem(404, "ANA-0112", "No dashboard with that id"));
route("PUT", `${BASE}/dashboards/{id}`, ({ params, body }) => {
  const input = body as Pick<Dashboard, "title" | "tiles" | "refreshSeconds">;
  const current = dashboards.get(params.id);
  const next: Dashboard = { dashboardId: params.id, title: input.title, tiles: input.tiles, refreshSeconds: input.refreshSeconds, version: (current?.version ?? 0) + 1, updatedAt: at(0) };
  dashboards.set(params.id, next);
  return current ? next : reply.created(next);
});

// Current tile values (46-3), in the demo's net revenue and shipment figures. A metric without a value says why.
const TILE_VALUES: Record<string, { value: string | null; unit?: "REPORTING_CURRENCY" | "RATIO"; currency?: string; mixed?: boolean }> = {
  net_revenue: { value: "4182650.40", currency: "USD" },
  gross_margin_pct: { value: "0.3825", unit: "RATIO" },
  cogs: { value: null, mixed: true }, // the group books in USD and EUR; the tile never adds them
  pump_units_shipped: { value: "1286" },
  open_order_value: { value: "1934120.00", currency: "USD" },
  on_time_delivery_pct: { value: "0.947", unit: "RATIO" },
  machine_utilisation_pct: { value: "0.812", unit: "RATIO" },
  scrap_rate_pct: { value: "0.0213", unit: "RATIO" },
};
route("GET", `${BASE}/dashboards/{id}/values`, ({ params }) => {
  const d = dashboards.get(params.id);
  if (!d) return reply.problem(404, "ANA-0112", "No dashboard with that id");
  const to = at(0).slice(0, 10);
  return {
    dashboardId: d.dashboardId,
    title: d.title,
    refreshSeconds: d.refreshSeconds,
    computedAt: at(0),
    tiles: d.tiles.map((t) => {
      const v = TILE_VALUES[t.metric];
      return {
        title: t.title,
        metric: t.metric,
        metricName: METRICS.find((m) => m.metricId === t.metric)?.name ?? t.metric,
        period: t.period,
        from: `${to.slice(0, 4)}-01-01`,
        to,
        status: v?.mixed ? "MIXED_CURRENCY" : v ? "OK" : "NOT_MODELLED",
        value: v?.value ?? null,
        ...(v?.unit ? { unit: v.unit } : v ? { unit: "REPORTING_CURRENCY" } : {}),
        ...(v?.currency ? { currency: v.currency } : {}),
      };
    }),
  };
});
