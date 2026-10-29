import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { idempotencyKey, json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import { findAlert, findTable } from "../../test-utils";
import type { Dashboard, Delivery, ExportJob, Metric, Plan, Schedule } from "./api/client";
import { validateDashboard } from "./DashboardsPage";
import { validateExport, validateExportFilter } from "./ExportsPage";
import { validateEdge, validateLineageSearch } from "./LineagePage";
import analytics from "./module";

// Slice 4 screens: dashboards (FR-46.8), explorer exports and the export log
// (FR-46.9, BR-46.7), schedule deliveries (FR-46.10) and lineage (FR-46.12).

useFakeServer();

const render = (path: string) => renderRoutes(analytics.routes, path);
const location = () => screen.getByTestId("location").textContent;
const USMF = "11111111-0000-4000-8000-000000000001";

const revenue: Metric = {
  metricId: "revenue",
  version: 1,
  name: "Revenue",
  formula: "-SUM(amount_reporting) WHERE account_category = 'REVENUE'",
  grain: "journal_line",
  allowedDimensions: ["legal_entity_id", "account_code"],
  reconciliationSource: "ledger.balance_by_account",
  nonFinancial: false,
  status: "PUBLISHED",
};
const cogs: Metric = { ...revenue, metricId: "cogs", name: "Cost of goods sold", formula: "SUM(amount_reporting) WHERE account_category = 'COGS'" };
const draftMetric: Metric = { ...revenue, metricId: "otif_pct", name: "OTIF", status: "DRAFT" };
const catalog = { items: [revenue, cogs, draftMetric] };

const finance: Dashboard = {
  dashboardId: "finance_overview",
  title: "Finance overview",
  tiles: [
    { title: "Revenue this month", metric: "revenue", period: "MTD" },
    { title: "COGS this year", metric: "cogs", period: "YTD" },
  ],
  refreshSeconds: 60,
  version: 3,
  updatedAt: "2026-10-28T08:00:00Z",
};

describe("dashboard rules", () => {
  it("mirrors domain.PrepareDashboard: id, title, refresh, tile count and published metrics only", () => {
    const ok = { dashboardId: "sales", title: "Sales", refreshSeconds: "60", tiles: [{ title: "Revenue", metric: "revenue", period: "MTD" as const }] };
    const metrics = [revenue, draftMetric];
    expect(validateDashboard(ok, metrics)).toEqual({});
    expect(validateDashboard({ ...ok, dashboardId: "Sales" }, metrics).dashboardId).toBeDefined();
    expect(validateDashboard({ ...ok, dashboardId: "s" }, metrics).dashboardId).toBeDefined();
    expect(validateDashboard({ ...ok, refreshSeconds: "14" }, metrics).refreshSeconds).toBeDefined();
    expect(validateDashboard({ ...ok, refreshSeconds: "601" }, metrics).refreshSeconds).toBeDefined();
    expect(validateDashboard({ ...ok, refreshSeconds: "15" }, metrics)).toEqual({});
    expect(validateDashboard({ ...ok, tiles: [] }, metrics).tiles).toBeDefined();
    expect(validateDashboard({ ...ok, tiles: Array.from({ length: 13 }, () => ok.tiles[0]) }, metrics).tiles).toBeDefined();
    // A draft metric is not governed yet, so a tile cannot show it.
    expect(validateDashboard({ ...ok, tiles: [{ title: "OTIF", metric: "otif_pct", period: "QTD" }] }, metrics)["tiles[0].metric"]).toBeDefined();
    expect(validateDashboard({ ...ok, tiles: [{ ...ok.tiles[0], title: "  " }] }, metrics)["tiles[0].title"]).toBeDefined();
  });
});

describe("dashboards", () => {
  it("lists dashboards and opens one", async () => {
    server.handler = () => json(200, { items: [finance] });
    const { container } = render("/analytics/dashboards");
    const table = await findTable({ name: "Dashboards" });
    const row = within(table).getByText("Finance overview").closest("tr") as HTMLElement;
    expect(within(row).getByText("finance_overview")).toBeInTheDocument();
    expect(within(row).getByText("2")).toBeInTheDocument();
    expect(within(row).getByText("2026-10-28 08:00 UTC")).toBeInTheDocument();
    expect(server.calls[0].url.pathname).toBe("/api/v1/analytics/dashboards");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(row).getByText("Finance overview"));
    await waitFor(() => expect(location()).toBe("/analytics/dashboards/finance_overview"));
  });

  it("shows the empty state, and the problem with a retry", async () => {
    server.handler = () => json(200, { items: [] });
    const first = render("/analytics/dashboards");
    expect(within(await findTable({ name: "Dashboards" })).getByText("No dashboards yet")).toBeInTheDocument();
    first.unmount();

    let attempt = 0;
    server.handler = () => (++attempt === 1 ? json(403, { code: "AUTH-0003", detail: "missing duty AnalyticsView" }) : json(200, { items: [finance] }));
    render("/analytics/dashboards");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("missing duty AnalyticsView (AUTH-0003)");
    fireEvent.click(within(alert.closest("div") as HTMLElement).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Finance overview")).toBeInTheDocument();
  });

  it("creates a dashboard without If-Match after checking the form, offering only published metrics", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/analytics/metrics") return json(200, catalog);
      if (init.method === "PUT") return json(201, { ...finance, dashboardId: "sales", title: "Sales", tiles: [{ title: "Revenue", metric: "revenue", period: "QTD" }], version: 1 });
      if (url.pathname === "/api/v1/analytics/dashboards/sales") return json(200, { ...finance, dashboardId: "sales", version: 1 });
      return undefined;
    };
    const { container } = render("/analytics/dashboards/new");
    const metric = await screen.findByLabelText("Metric");
    expect(within(metric).queryByRole("option", { name: "OTIF" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Create dashboard" }));
    expect(await screen.findByText(/Use 2 to 63 lowercase letters/)).toBeInTheDocument();
    expect(screen.getByText("Choose a published governed metric.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "PUT")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText(/^Dashboard id/), { target: { value: "sales" } });
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: " Sales " } });
    fireEvent.change(screen.getByLabelText("Tile title"), { target: { value: "Revenue" } });
    fireEvent.change(metric, { target: { value: "revenue" } });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "QTD" } });
    fireEvent.click(screen.getByRole("button", { name: "Create dashboard" }));
    await waitFor(() => expect(location()).toBe("/analytics/dashboards/sales"));

    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe("/api/v1/analytics/dashboards/sales");
    expect(new Headers(put?.init.headers).get("If-Match")).toBeNull();
    expect(put?.body).toEqual({ title: "Sales", refreshSeconds: 60, tiles: [{ title: "Revenue", metric: "revenue", period: "QTD" }] });
  });

  it("adds and removes tiles up to the cap of 12", async () => {
    server.handler = () => json(200, catalog);
    render("/analytics/dashboards/new");
    const add = await screen.findByRole("button", { name: "Add tile" });
    for (let i = 0; i < 11; i++) fireEvent.click(add);
    expect(screen.getAllByLabelText("Tile title")).toHaveLength(12);
    expect(add).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Remove tile 12" }));
    expect(screen.getAllByLabelText("Tile title")).toHaveLength(11);
    expect(add).not.toBeDisabled();
  });

  it("shows tiles with their governed metric and saves with If-Match of the loaded version", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/analytics/metrics") return json(200, catalog);
      if (init.method === "PUT") return json(200, { ...finance, title: "Finance", version: 4 });
      return json(200, finance);
    };
    const { container } = render("/analytics/dashboards/finance_overview");
    const tiles = await screen.findByLabelText("Tiles");
    expect(within(tiles).getByRole("heading", { name: "Revenue this month" })).toBeInTheDocument();
    expect(within(tiles).getByRole("link", { name: "Revenue" })).toHaveAttribute("href", "/analytics/metrics/revenue");
    expect(within(tiles).getByText("Year to date")).toBeInTheDocument();
    expect(within(tiles).getByText(revenue.formula)).toBeInTheDocument();
    expect(screen.getByLabelText(/^Dashboard id/)).toHaveAttribute("readonly");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Finance" } });
    fireEvent.click(screen.getByRole("button", { name: "Save dashboard" }));
    expect(await screen.findByText("Dashboard saved as version 4.")).toHaveAttribute("role", "status");
    const put = server.calls.find((c) => c.method === "PUT");
    expect(new Headers(put?.init.headers).get("If-Match")).toBe('"3"');
    expect(screen.getByRole("heading", { name: "Finance", level: 2 })).toBeInTheDocument();
  });

  it("explains a concurrent change (ANA-0113) and maps server field errors onto the tiles", async () => {
    let puts = 0;
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/analytics/metrics") return json(200, catalog);
      if (init.method === "PUT") {
        puts++;
        return puts === 1
          ? json(412, { code: "ANA-0113", detail: "dashboard finance_overview has changed" })
          : json(400, { code: "COM-0001", detail: "request failed validation", errors: { "tiles[1].period": "MTD, QTD, YTD or TRAILING_12M" } });
      }
      return json(200, finance);
    };
    render("/analytics/dashboards/finance_overview");
    fireEvent.click(await screen.findByRole("button", { name: "Save dashboard" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Someone else changed this dashboard");
    expect(alert).toHaveTextContent("(ANA-0113)");
    fireEvent.click(screen.getByRole("button", { name: "Save dashboard" }));
    expect(await screen.findByText("MTD, QTD, YTD or TRAILING_12M")).toBeInTheDocument();
  });

  it("shows not found for an unknown dashboard", async () => {
    server.handler = (url) => (url.pathname.endsWith("/metrics") ? json(200, catalog) : json(404, { code: "ANA-0112", detail: "dashboard nope not found" }));
    render("/analytics/dashboards/nope");
    expect(await screen.findByRole("heading", { name: "We couldn't find that dashboard" })).toBeInTheDocument();
  });
});

const plan: Plan = {
  model: "fin_journal_line",
  grain: "journal_line",
  measures: ["revenue"],
  dimensions: [],
  from: "2026-01-01",
  to: "2026-03-31",
  filters: [{ member: "fin_journal_line.legal_entity_id", operator: "equals", values: [USMF] }],
};
const job: ExportJob = {
  exportId: "77777777-0000-4000-8000-000000000007",
  requestedBy: "22222222-0000-4000-8000-000000000002",
  queryHash: "a".repeat(64),
  plan,
  format: "XLSX",
  mode: "TEMPORAL_JOB",
  rowCount: 250000,
  linkTtlHours: 24,
  status: "QUEUED",
  createdAt: "2026-10-28T08:00:00Z",
  linkExpiresAt: "2026-10-29T08:00:00Z",
  deleteAt: "2026-10-29T08:00:00Z",
  reused: false,
};

describe("export rules", () => {
  it("mirrors the export policy: whole rows up to 1,000,000 and a 1 to 168 hour link", () => {
    expect(validateExport({ format: "XLSX", rowCount: "1000000", linkTtlHours: "168" })).toEqual({});
    expect(validateExport({ format: "XLSX", rowCount: "1000001", linkTtlHours: "24" }).rowCount).toMatch(/1,000,000/);
    expect(validateExport({ format: "CSV", rowCount: "1.5", linkTtlHours: "24" }).rowCount).toBeDefined();
    expect(validateExport({ format: "CSV", rowCount: "10", linkTtlHours: "0" }).linkTtlHours).toBeDefined();
    expect(validateExport({ format: "CSV", rowCount: "10", linkTtlHours: "169" }).linkTtlHours).toBeDefined();
    expect(validateExportFilter({ user: "", format: "", from: "2026-10-02", to: "2026-10-01" }).to).toMatch(/before/);
    expect(validateExportFilter({ user: "", format: "", from: "", to: "" })).toEqual({});
  });
});

describe("explorer export", () => {
  it("exports the planned query with one Idempotency-Key that a retry reuses", async () => {
    let posts = 0;
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/analytics/metrics") return json(200, catalog);
      if (url.pathname === "/api/v1/analytics/query") return json(200, { plan });
      if (url.pathname === "/api/v1/analytics/exports" && init.method === "POST") {
        posts++;
        return posts === 1 ? json(503, { code: "HTTP-503", detail: "upstream unavailable" }) : json(202, job);
      }
      return undefined;
    };
    const { container } = render("/analytics/explorer?measure=revenue");
    await screen.findByRole("checkbox", { name: "Revenue" });
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-01-01" } });
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-03-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Build plan" }));
    const rows = await screen.findByLabelText(/^Rows/);
    // Editing the query after the plan was built does not change what is exported.
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-06-30" } });
    fireEvent.change(rows, { target: { value: "250000" } });
    expect(screen.getByText("Above 100,000 rows the export runs as a background job.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Export" }));
    expect(await findAlert()).toHaveTextContent("upstream unavailable (HTTP-503)");
    fireEvent.click(screen.getByRole("button", { name: "Export" }));
    await waitFor(() => expect(screen.getByText(/^Export queued: 250,000 rows as XLSX in a background job/)).toBeInTheDocument());

    const exports = server.calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/v1/analytics/exports");
    expect(exports).toHaveLength(2);
    expect(idempotencyKey(exports[0])).toBeTruthy();
    expect(idempotencyKey(exports[1])).toBe(idempotencyKey(exports[0]));
    expect(exports[0].body).toEqual({ measures: ["revenue"], dimensions: [], from: "2026-01-01", to: "2026-03-31", format: "XLSX", rowCount: 250000, linkTtlHours: 24 });
  });

  it("refuses more than 1,000,000 rows before calling and says when an identical export was reused", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/analytics/metrics") return json(200, catalog);
      if (url.pathname === "/api/v1/analytics/query") return json(200, { plan });
      if (init.method === "POST") return json(200, { ...job, format: "CSV", mode: "INLINE", rowCount: 900, reused: true });
      return undefined;
    };
    render("/analytics/explorer?measure=revenue");
    await screen.findByRole("checkbox", { name: "Revenue" });
    fireEvent.click(screen.getByRole("button", { name: "Build plan" }));
    fireEvent.change(await screen.findByLabelText(/^Rows/), { target: { value: "1000001" } });
    fireEvent.click(screen.getByRole("button", { name: "Export" }));
    expect(await screen.findByText(/Exports are limited to 1,000,000 rows/)).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname === "/api/v1/analytics/exports")).toBe(false);

    fireEvent.change(screen.getByLabelText(/^Rows/), { target: { value: "900" } });
    fireEvent.change(screen.getByLabelText("Format"), { target: { value: "CSV" } });
    fireEvent.click(screen.getByRole("button", { name: "Export" }));
    expect(await screen.findByText(/^An identical export from the last hour was returned: 900 rows/)).toHaveAttribute("role", "status");
  });

  it("offers no export when the caller has no data scope", async () => {
    server.handler = (url) =>
      url.pathname.endsWith("/metrics") ? json(200, catalog) : json(200, { plan: { ...plan, filters: [], reason: "NO_SCOPE" } });
    render("/analytics/explorer?measure=revenue");
    await screen.findByRole("checkbox", { name: "Revenue" });
    fireEvent.click(screen.getByRole("button", { name: "Build plan" }));
    expect(await screen.findByText("You have no data scope for this model")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Export" })).not.toBeInTheDocument();
  });
});

describe("export log", () => {
  it("lists exports and filters by user, format and day", async () => {
    server.handler = () => json(200, { items: [job] });
    const { container } = render("/analytics/exports");
    const table = await findTable({ name: "Export log" });
    const row = within(table).getByText("revenue").closest("tr") as HTMLElement;
    expect(within(row).getByText("250,000")).toBeInTheDocument();
    expect(within(row).getByText("Background job")).toBeInTheDocument();
    expect(within(row).getByText("Queued")).toBeInTheDocument();
    expect(within(row).getAllByText("2026-10-29 08:00 UTC")).toHaveLength(2);
    expect(server.calls[0].url.search).toBe("");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText(/^Requested by/), { target: { value: job.requestedBy } });
    fireEvent.change(screen.getByLabelText("Format"), { target: { value: "XLSX" } });
    fireEvent.change(screen.getByLabelText(/^From/), { target: { value: "2026-10-28" } });
    fireEvent.change(screen.getByLabelText(/^To/), { target: { value: "2026-10-27" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("The last day must not be before the first day.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(1);

    fireEvent.change(screen.getByLabelText(/^To/), { target: { value: "2026-10-28" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(server.calls).toHaveLength(2));
    expect(Object.fromEntries(server.calls[1].url.searchParams)).toEqual({ user: job.requestedBy, format: "XLSX", from: "2026-10-28", to: "2026-10-28" });
  });

  it("shows the empty state and the problem when the caller lacks AnalyticsExport", async () => {
    server.handler = () => json(200, { items: [] });
    const first = render("/analytics/exports");
    expect(within(await findTable({ name: "Export log" })).getByText("No exports found")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty AnalyticsExport" });
    render("/analytics/exports");
    expect(await findAlert()).toHaveTextContent("missing duty AnalyticsExport (AUTH-0003)");
  });
});

const SID = "66666666-0000-4000-8000-000000000006";
const RUN = "88888888-0000-4000-8000-000000000008";
const schedule: Schedule = {
  scheduleId: SID,
  name: "Weekly revenue",
  targetType: "DASHBOARD",
  targetId: "finance_overview",
  cron: "0 6 * * 1",
  timezone: "Europe/Berlin",
  format: "PDF",
  destination: { type: "SFTP", targets: ["sftp://files.example.com/reports"] },
  lastRunAt: "2026-10-26T05:21:00Z",
  nextRunAt: "2026-11-02T05:00:00Z",
  failureCount: 0,
  version: 1,
};
const attempt = (n: number, status: Delivery["status"], runAt: string, extra: Partial<Delivery> = {}): Delivery => ({
  deliveryId: `99999999-0000-4000-8000-00000000000${n}`,
  scheduleId: SID,
  runId: RUN,
  attempt: n,
  status,
  format: "PDF",
  destinationType: "SFTP",
  rowCount: 0,
  bytes: 0,
  runAt,
  ...extra,
});

describe("schedule deliveries", () => {
  it("lists attempts newest first and raises ANA-0108 when the run failed after its retries", async () => {
    server.handler = (url) =>
      url.pathname.endsWith("/deliveries")
        ? json(200, {
            items: [
              attempt(4, "FAILED", "2026-10-26T05:21:00Z", { error: "connection refused" }),
              attempt(1, "RETRY", "2026-10-26T05:00:00Z", { error: "connection refused", retryAt: "2026-10-26T05:01:00Z" }),
            ],
          })
        : json(200, schedule);
    const { container } = render(`/analytics/schedules/${SID}`);
    const table = await findTable({ name: "Deliveries" });
    const rows = within(table).getAllByRole("row");
    expect(within(rows[1]).getByText("Failed")).toBeInTheDocument();
    expect(within(rows[2]).getByText("Retrying")).toBeInTheDocument();
    expect(within(rows[2]).getByText("2026-10-26 05:01 UTC")).toBeInTheDocument();
    expect(screen.getByText("Schedule Weekly revenue failed after 3 attempts")).toBeInTheDocument();
    expect(screen.getByText(/connection refused Fix the destination/)).toHaveTextContent("(ANA-0108)");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "DELIVERED" } });
    await waitFor(() => expect(server.calls.at(-1)?.url.search).toBe("?status=DELIVERED"));
  });

  it("shows no deliveries before the first run", async () => {
    server.handler = (url) => (url.pathname.endsWith("/deliveries") ? json(200, { items: [] }) : json(200, { ...schedule, lastRunAt: undefined }));
    render(`/analytics/schedules/${SID}`);
    expect(within(await findTable({ name: "Deliveries" })).getByText("No deliveries yet")).toBeInTheDocument();
    expect(screen.queryByText(/failed after 3 attempts/)).not.toBeInTheDocument();
  });
});

describe("lineage rules", () => {
  it("checks asset names, depth and self loops like the service", () => {
    expect(validateLineageSearch("mart.fin_journal_line", "")).toEqual({});
    expect(validateLineageSearch("mart.fin_journal_line", "20")).toEqual({});
    expect(validateLineageSearch("mart.fin_journal_line", "21").depth).toBeDefined();
    expect(validateLineageSearch("Mart", "").asset).toBeDefined();
    expect(validateEdge("raw.ledger.journal_line", "core.journal_line")).toEqual({});
    expect(validateEdge("core.journal_line", "core.journal_line").target).toMatch(/cannot feed itself/);
    expect(validateEdge("bad", "core.journal_line").source).toBeDefined();
  });
});

describe("lineage", () => {
  it("shows upstream and downstream assets of the asset in the URL and walks to a neighbour", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/catalog/lineage/core.journal_line"
        ? json(200, { asset: "core.journal_line", depth: 20, upstream: [{ fqn: "raw.ledger.journal_line", distance: 1 }], downstream: [{ fqn: "mart.fin_journal_line", distance: 1 }, { fqn: "cube.revenue", distance: 2 }] })
        : json(200, { asset: "mart.fin_journal_line", depth: 20, upstream: [], downstream: [] });
    const { container } = render("/analytics/lineage?asset=core.journal_line");
    const up = await findTable({ name: "Upstream of core.journal_line" });
    expect(within(up).getByText("raw.ledger.journal_line")).toBeInTheDocument();
    const down = screen.getByRole("table", { name: "Downstream of core.journal_line" });
    expect(within(down).getAllByRole("row")).toHaveLength(3);
    expect(server.calls[0].url.search).toBe("");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(down).getByText("mart.fin_journal_line"));
    const next = await findTable({ name: "Upstream of mart.fin_journal_line" });
    expect(within(next).getByText("No upstream assets")).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.pathname).toBe("/api/v1/catalog/lineage/mart.fin_journal_line");
  });

  it("checks the search before calling and passes the depth", async () => {
    server.handler = () => json(200, { asset: "core.vendor", depth: 2, upstream: [], downstream: [] });
    render("/analytics/lineage");
    expect(await screen.findByText("Choose an asset")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Asset"), { target: { value: "Core Vendor" } });
    fireEvent.click(screen.getByRole("button", { name: "Show lineage" }));
    expect(await screen.findByText(/Use a dotted lowercase asset name such as mart/)).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Asset"), { target: { value: "core.vendor" } });
    fireEvent.change(screen.getByLabelText(/^Depth/), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Show lineage" }));
    await waitFor(() => expect(server.calls).toHaveLength(1));
    expect(server.calls[0].url.pathname).toBe("/api/v1/catalog/lineage/core.vendor");
    expect(server.calls[0].url.search).toBe("?depth=2");
  });

  it("records an edge and reloads the lineage; a repeated edge is reported as already recorded", async () => {
    let posts = 0;
    server.handler = (_url, init) => {
      if (init.method === "POST") return json(200, { added: ++posts === 1 ? 1 : 0 });
      return json(200, { asset: "core.vendor", depth: 20, upstream: posts > 0 ? [{ fqn: "raw.vendor.vendor", distance: 1 }] : [], downstream: [] });
    };
    render("/analytics/lineage?asset=core.vendor");
    await findTable({ name: "Upstream of core.vendor" });
    fireEvent.change(screen.getByLabelText(/^Source asset/), { target: { value: "raw.vendor.vendor" } });
    fireEvent.change(screen.getByLabelText(/^Target asset/), { target: { value: "core.vendor" } });
    fireEvent.click(screen.getByRole("button", { name: "Record edge" }));
    expect(await screen.findByText("Recorded: raw.vendor.vendor feeds core.vendor.")).toHaveAttribute("role", "status");
    expect(await screen.findByText("raw.vendor.vendor", { selector: "button" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/catalog/lineage/edges");
    expect(post?.body).toEqual({ edges: [{ source: "raw.vendor.vendor", target: "core.vendor" }] });

    fireEvent.click(screen.getByRole("button", { name: "Record edge" }));
    await waitFor(() => expect(screen.getByText("Already recorded: raw.vendor.vendor feeds core.vendor.")).toBeInTheDocument());
  });

  it("shows the problem when the caller may not record edges", async () => {
    server.handler = (_url, init) =>
      init.method === "POST" ? json(403, { code: "AUTH-0003", detail: "missing duty CatalogSteward" }) : json(200, { asset: "core.vendor", depth: 20, upstream: [], downstream: [] });
    render("/analytics/lineage?asset=core.vendor");
    await findTable({ name: "Upstream of core.vendor" });
    fireEvent.change(screen.getByLabelText(/^Source asset/), { target: { value: "raw.vendor.vendor" } });
    fireEvent.change(screen.getByLabelText(/^Target asset/), { target: { value: "core.vendor" } });
    fireEvent.click(screen.getByRole("button", { name: "Record edge" }));
    expect(await findAlert()).toHaveTextContent("missing duty CatalogSteward (AUTH-0003)");
  });
});
