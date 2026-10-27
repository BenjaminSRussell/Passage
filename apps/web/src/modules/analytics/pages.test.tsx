import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import { allowedDimensions, validateQuery } from "./ExplorerPage";
import type { Metric } from "./api/client";
import { dayBefore } from "./format";
import analytics from "./module";

const USMF = "11111111-0000-4000-8000-000000000001";
const DEMF = "22222222-0000-4000-8000-000000000002";

// The shell supplies the h1, landmarks and skip link; a page rendered alone is
// checked for everything else.
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const revenue: Metric = {
  metricId: "revenue",
  version: 1,
  name: "Revenue",
  formula: "-SUM(amount_reporting) WHERE account_category = 'REVENUE' AND status = 'POSTED'",
  grain: "journal_line",
  allowedDimensions: ["legal_entity_id", "fiscal_period", "account_code", "currency"],
  reconciliationSource: "ledger.balance_by_account",
  nonFinancial: false,
  status: "PUBLISHED",
};
const dso: Metric = {
  ...revenue,
  metricId: "dso_days",
  name: "Days sales outstanding",
  formula: "ar_open_balance_at(end) / NULLIF(revenue_trailing_90d, 0) * 90",
  allowedDimensions: ["legal_entity_id", "currency"],
  reconciliationSource: "subledger.ar_open_items",
};
const otif: Metric = {
  ...revenue,
  metricId: "otif_pct",
  name: "On time in full %",
  grain: "sales_line",
  allowedDimensions: ["legal_entity_id", "site_id", "customer_group"],
  reconciliationSource: undefined,
  nonFinancial: true,
};
const catalog = { items: [revenue, dso, otif] };

const reconRow = (le: string, status: "OK" | "FAILED" | "BLOCKED", delta: string, trusted = false) => ({
  metricId: "revenue",
  legalEntityId: le,
  date: "2026-10-26",
  currency: "USD",
  semanticValue: "1000.00",
  sourceValue: "1000.00",
  delta,
  tolerance: "0.005",
  status,
  consecutiveOkDays: trusted ? 30 : 0,
  trusted,
});

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body } = await handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {analytics.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("analytics module", () => {
  it("uses its folder name as id, routes under /analytics and gates nav by duty", () => {
    expect(analytics.id).toBe("analytics");
    expect(analytics.routes.every((r) => r.path?.startsWith("/analytics"))).toBe(true);
    expect(analytics.nav.every((n) => n.to.startsWith("/analytics"))).toBe(true);
    expect(Object.fromEntries(analytics.nav.map((n) => [n.to, n.duty]))).toEqual({
      "/analytics/metrics": "AnalyticsView",
      "/analytics/explorer": "AnalyticsView",
      "/analytics/reconciliation": "FinanceView",
    });
    // Every nav entry resolves to a route.
    for (const n of analytics.nav) expect(analytics.routes.some((r) => r.path === n.to)).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("analytics");
  });
});

describe("governed metrics list", () => {
  it("shows a loading table, then the published metrics, and opens a metric", async () => {
    handler = () => ({ status: 200, body: catalog });
    const { container } = renderAt("/analytics/metrics");
    expect(screen.getByRole("table", { name: "Governed metrics" })).toHaveAttribute("aria-busy", "true");
    const table = await findTable({ name: "Governed metrics" });
    await waitFor(() => expect(within(table).getByText("ledger.balance_by_account")).toBeInTheDocument());
    expect(within(table).getByText("Non-financial")).toBeInTheDocument();
    expect(within(table).getByText("Sales line")).toBeInTheDocument();
    expect(calls[0].url.pathname).toBe("/api/v1/analytics/metrics");
    expect(calls[0].headers.get("Accept")).toBe("application/json");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: "dso_days" }));
    expect(await screen.findByRole("heading", { name: "Days sales outstanding" })).toBeInTheDocument();
  });

  it("shows the empty state when nothing is published", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/analytics");
    expect(await screen.findByText("No published metrics yet")).toBeInTheDocument();
  });

  it("shows the problem code and retries", async () => {
    let attempt = 0;
    handler = () => (++attempt === 1 ? { status: 403, body: { code: "AUTH-0003", detail: "missing duty AnalyticsView" } } : { status: 200, body: catalog });
    renderAt("/analytics");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("missing duty AnalyticsView (AUTH-0003)");
    fireEvent.click(within(alert.closest("div") as HTMLElement).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "revenue" })).toBeInTheDocument();
  });

  it("refuses a response that breaks the contract instead of rendering it", async () => {
    handler = () => ({ status: 200, body: { items: [{ metricId: 1 }] } });
    renderAt("/analytics");
    expect(await findAlert()).toHaveTextContent("(CONTRACT)");
  });
});

describe("metric detail", () => {
  it("shows the definition with formula, dimensions and reconciliation source", async () => {
    handler = () => ({ status: 200, body: catalog });
    const { container } = renderAt("/analytics/metrics/revenue");
    expect(screen.getByRole("status")).toHaveTextContent("Loading metric revenue");
    expect(await screen.findByRole("heading", { name: "Revenue" })).toBeInTheDocument();
    expect(screen.getByText(revenue.formula)).toBeInTheDocument();
    expect(screen.getByText("Fiscal period")).toBeInTheDocument();
    expect(screen.getByText("ledger.balance_by_account")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Explore revenue" })).toHaveAttribute("href", "/analytics/explorer?measure=revenue");
    expect(pageViolations(container)).toEqual([]);
  });

  it("says so when the metric id is unknown", async () => {
    handler = () => ({ status: 200, body: catalog });
    renderAt("/analytics/metrics/nope");
    expect(await screen.findByRole("heading", { name: "We couldn't find that metric" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to governed metrics" })).toHaveAttribute("href", "/analytics");
  });

  it("shows an error when the catalog cannot load", async () => {
    handler = () => ({ status: 503, body: {} });
    renderAt("/analytics/metrics/revenue");
    expect(await findAlert()).toHaveTextContent("(HTTP-503)");
  });
});

describe("explorer", () => {
  it("narrows dimensions to those every metric allows and checks the shape before calling", () => {
    expect(allowedDimensions([revenue, dso], ["revenue", "dso_days"])).toEqual(["legal_entity_id", "currency"]);
    expect(allowedDimensions([revenue], [])).toEqual([]);
    const ok = { measures: ["revenue"], dimensions: [], from: "2026-01-01", to: "2026-03-31" };
    expect(validateQuery(ok, [revenue])).toEqual({});
    expect(validateQuery({ ...ok, measures: [] }, [revenue]).measures).toBeDefined();
    expect(validateQuery({ ...ok, measures: ["revenue", "otif_pct"] }, [revenue, otif]).measures).toMatch(/one grain/);
    expect(validateQuery({ ...ok, to: "2025-12-31" }, [revenue]).to).toMatch(/before/);
    expect(validateQuery({ ...ok, from: "2020-01-01", to: "2025-01-02" }, [revenue]).to).toMatch(/5 years/);
    expect(validateQuery({ ...ok, from: "2020-01-01", to: "2025-01-01" }, [revenue]).to).toBeUndefined();
  });

  it("builds a plan with the legal-entity filter from the caller's scope", async () => {
    handler = (url) =>
      url.pathname.endsWith("/metrics")
        ? { status: 200, body: catalog }
        : {
            status: 200,
            body: {
              plan: {
                model: "fin_journal_line",
                grain: "journal_line",
                measures: ["revenue"],
                dimensions: ["account_code"],
                from: "2026-01-01",
                to: "2026-03-31",
                filters: [{ member: "fin_journal_line.legal_entity_id", operator: "equals", values: [USMF] }],
              },
            },
          };
    const { container } = renderAt("/analytics/explorer?measure=revenue");
    expect(await screen.findByRole("checkbox", { name: "Revenue" })).toBeChecked();
    fireEvent.click(screen.getByRole("checkbox", { name: "Account code" }));
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-01-01" } });
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-03-31" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Build plan" }));

    const filters = await findTable({ name: "Scope filters" });
    expect(within(filters).getByText(USMF)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/analytics/query");
    expect(post?.headers.get("Content-Type")).toBe("application/json");
    expect(post?.body).toEqual({ measures: ["revenue"], dimensions: ["account_code"], from: "2026-01-01", to: "2026-03-31" });
  });

  it("drops a dimension the new metric set does not allow", async () => {
    handler = () => ({ status: 200, body: catalog });
    renderAt("/analytics/explorer?measure=revenue");
    fireEvent.click(await screen.findByRole("checkbox", { name: "Account code" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Days sales outstanding" }));
    expect(screen.queryByRole("checkbox", { name: "Account code" })).not.toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Currency" })).not.toBeChecked();
  });

  it("explains NO_SCOPE as an empty result, not an error (BR-46.9)", async () => {
    handler = (url) =>
      url.pathname.endsWith("/metrics")
        ? { status: 200, body: catalog }
        : {
            status: 200,
            body: { plan: { model: "fin_journal_line", grain: "journal_line", measures: ["revenue"], dimensions: [], from: "2026-01-01", to: "2026-01-31", filters: [], reason: "NO_SCOPE" } },
          };
    renderAt("/analytics/explorer?measure=revenue");
    fireEvent.click(await screen.findByRole("button", { name: "Build plan" }));
    expect(await screen.findByRole("heading", { name: "You have no data scope for this model" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("does not call the service while the form is invalid, and shows service field errors", async () => {
    handler = (url) =>
      url.pathname.endsWith("/metrics")
        ? { status: 200, body: catalog }
        : { status: 403, body: { code: "ANA-0103", detail: "model fin_journal_line has no security mapping", errors: { measures: "not mapped" } } };
    renderAt("/analytics/explorer");
    fireEvent.click(await screen.findByRole("button", { name: "Build plan" }));
    expect(await screen.findByText("Choose at least one metric.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    fireEvent.click(screen.getByRole("checkbox", { name: "Revenue" }));
    fireEvent.click(screen.getByRole("button", { name: "Build plan" }));
    expect(await screen.findByText("not mapped")).toBeInTheDocument();
    expect(screen.getByText(/no security mapping \(ANA-0103\)/)).toBeInTheDocument();
  });

  it("shows the empty state when no metric is published", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/analytics/explorer");
    expect(await screen.findByRole("heading", { name: "No published metrics yet" })).toBeInTheDocument();
  });
});

describe("metric reconciliation", () => {
  it("defaults to yesterday and shows the failure banner with the delta", async () => {
    handler = () => ({
      status: 200,
      body: { date: "2026-10-26", failedCount: 1, items: [reconRow(USMF, "OK", "0", true), reconRow(DEMF, "FAILED", "-0.01")] },
    });
    const { container } = renderAt("/analytics/reconciliation");
    const table = await findTable({ name: "Reconciliation results" });
    await waitFor(() => expect(within(table).getByText("-0.01")).toBeInTheDocument());
    expect(within(table).getByText("Trusted")).toBeInTheDocument();
    expect(within(table).getByText("Failed")).toBeInTheDocument();
    expect(screen.getByText("1 of 2 results need attention on 2026-10-26")).toBeInTheDocument();
    expect(screen.getByText(/Metric revenue failed reconciliation on 2026-10-26: delta -0.01 USD/)).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Reconciliation summary" })).toHaveTextContent("Outside tolerance 1");
    const today = new Date(Date.now() - new Date().getTimezoneOffset() * 60_000).toISOString().slice(0, 10);
    expect(calls[0].url.pathname).toBe("/api/v1/analytics/reconciliation");
    expect(calls[0].url.searchParams.get("date")).toBe(dayBefore(today));
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by day and metric and shows the empty state", async () => {
    handler = (url) => ({ status: 200, body: { date: url.searchParams.get("date"), failedCount: 0, items: [] } });
    renderAt("/analytics/reconciliation");
    expect(await screen.findByText("No results for this day")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Day"), { target: { value: "2026-09-30" } });
    fireEvent.change(screen.getByLabelText("Metric"), { target: { value: "cogs" } });
    fireEvent.click(screen.getByRole("button", { name: "Show results" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("metric")).toBe("cogs"));
    expect(calls.at(-1)?.url.searchParams.get("date")).toBe("2026-09-30");
    expect(screen.queryByText(/need attention/)).not.toBeInTheDocument();
  });

  it("rejects a malformed metric id without calling the service", async () => {
    handler = () => ({ status: 200, body: { date: "2026-10-26", failedCount: 0, items: [] } });
    renderAt("/analytics/reconciliation");
    await screen.findByText("No results for this day");
    const before = calls.length;
    fireEvent.change(screen.getByLabelText("Metric"), { target: { value: "Revenue!" } });
    fireEvent.click(screen.getByRole("button", { name: "Show results" }));
    expect(await screen.findByText("Use a metric id such as revenue or gross_margin.")).toBeInTheDocument();
    expect(calls.length).toBe(before);
  });

  it("shows the problem when the caller lacks FinanceView", async () => {
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "missing duty FinanceView" } });
    renderAt("/analytics/reconciliation");
    expect(await findAlert()).toHaveTextContent("missing duty FinanceView (AUTH-0003)");
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});
