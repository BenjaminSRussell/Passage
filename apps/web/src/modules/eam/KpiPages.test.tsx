import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { AssetCost, BacklogRow, PlanReview, RegularizationAlert } from "./api/client";
import { defaultWindow, validateFilter } from "./KpiPages";
import eam from "./module";

// Slice 5 KPI page: cost per asset and backlog (FR-44.13), emergency orders to regularize
// (BR-44.7, EAM-WO-003) and plan reviews due (BR-44.15).

const PUMP = "0195f0a0-0000-7000-8000-000000000001";
const WO = "0195f0a0-0000-7000-8000-0000000000d1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { url: URL }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ url });
      const reply = handler(url, init) ?? json(500, { code: "TEST-500", detail: `no fake for ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderKpis() {
  return render(
    <MemoryRouter initialEntries={["/eam/kpis"]}>
      <Routes>
        {eam.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const cost: AssetCost = {
  assetId: PUMP,
  assetNumber: "AST-1",
  name: "Feed pump",
  assetType: "PUMP",
  currency: "USD",
  workOrders: 2,
  partsCost: "350.00",
  laborCost: "460.00",
  total: "810.00",
};
const rows: BacklogRow[] = [
  { priority: 1, openCount: 1, overdueCount: 0 },
  { priority: 3, openCount: 3, overdueCount: 1, oldestDueAt: "2026-10-19T00:00:00Z" },
];
const alert: RegularizationAlert = {
  id: WO,
  number: "WO-12",
  assetId: PUMP,
  workOrderType: "EMERGENCY",
  status: "IN_PROGRESS",
  priority: 1,
  currency: "USD",
  downtimeMinutes: 0,
  partsCost: "0",
  laborCost: "0",
  warrantyCovered: false,
  version: 3,
  createdAt: "2026-10-20T06:00:00Z",
  code: "EAM-WO-003",
  detail: "Emergency work order WO-12 not regularized within 24 h",
};
const review: PlanReview = { assetId: PUMP, assetNumber: "AST-1", name: "Feed pump", criticality: 5 };

const path = (p: string) => (url: URL) => url.pathname === `/api/v1/eam${p}`;

function fakeAll(extra: Handler = () => undefined) {
  handler = (url, init) =>
    extra(url, init) ??
    (path("/kpis/cost")(url)
      ? json(200, { items: [cost], from: url.searchParams.get("from"), to: url.searchParams.get("to") })
      : path("/kpis/backlog")(url)
        ? json(200, { items: rows })
        : path("/work-orders/emergency-overdue")(url)
          ? json(200, { items: [alert], windowHours: Number(url.searchParams.get("hours")) })
          : path("/assets/plan-reviews")(url)
            ? json(200, { items: [review] })
            : undefined);
}

describe("KPI filter", () => {
  it("defaults to the 30 days up to today", () => {
    expect(defaultWindow(new Date("2026-10-21T15:00:00Z"))).toEqual({ from: "2026-09-21", to: "2026-10-21" });
  });

  it("includes the last day and upper-cases the asset type", () => {
    expect(validateFilter({ from: "2026-10-01", to: "2026-10-31", assetType: "pump" }).query).toEqual({
      from: "2026-10-01T00:00:00Z",
      to: "2026-11-01T00:00:00Z",
      assetType: "PUMP",
    });
  });

  it("refuses a reversed window and a bad asset type", () => {
    const { errors, query } = validateFilter({ from: "2026-10-31", to: "2026-10-01", assetType: "pump type" });
    expect(query).toBeUndefined();
    expect(errors.to).toMatch(/not be before/);
    expect(errors.assetType).toMatch(/asset type code/);
  });
});

describe("KPI page", () => {
  it("shows cost per asset, backlog, emergency orders and plan reviews", async () => {
    fakeAll();
    const { container } = renderKpis();
    const costs = await findTable({ name: "Cost per asset" });
    const row = within(costs).getAllByRole("row")[1];
    expect(row).toHaveTextContent("Feed pump");
    expect(row).toHaveTextContent("810.00 USD");
    expect(within(costs).getByRole("link", { name: "AST-1" })).toHaveAttribute("href", `/eam/assets/${PUMP}`);
    const backlogTable = await findTable({ name: "Backlog by priority" });
    expect(within(backlogTable).getAllByRole("row")[1]).toHaveTextContent("1 (emergency)");
    expect(within(backlogTable).getAllByRole("row")[2]).toHaveTextContent("2026-10-19");
    const alerts = await findTable({ name: "Emergency orders to regularize" });
    expect(within(alerts).getByRole("link", { name: "WO-12" })).toHaveAttribute("href", `/eam/work-orders/${WO}`);
    expect(alerts).toHaveTextContent("not regularized within 24 h (EAM-WO-003)");
    const reviews = await findTable({ name: "Plan reviews due" });
    expect(within(reviews).getAllByRole("row")[1]).toHaveTextContent("Never");
    expect(pageViolations(container)).toEqual([]);
  });

  it("reloads costs for a new window and asset type, and the alert for a new window", async () => {
    fakeAll();
    renderKpis();
    await findTable({ name: "Cost per asset" });
    fireEvent.change(screen.getByLabelText("First day"), { target: { value: "2026-10-01" } });
    fireEvent.change(screen.getByLabelText("Last day"), { target: { value: "2026-10-31" } });
    fireEvent.change(screen.getByLabelText(/Asset type/), { target: { value: "pump" } });
    fireEvent.click(screen.getByRole("button", { name: "Show costs" }));
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("assetType") === "PUMP")).toBe(true));
    const last = calls.filter((c) => c.url.pathname.endsWith("/kpis/cost")).pop()!;
    expect(last.url.searchParams.get("from")).toBe("2026-10-01T00:00:00Z");
    expect(last.url.searchParams.get("to")).toBe("2026-11-01T00:00:00Z");
    fireEvent.change(screen.getByLabelText(/Window in hours/), { target: { value: "99" } });
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    expect(await screen.findByText("Enter 1 to 72 hours.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Window in hours/), { target: { value: "48" } });
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("hours") === "48")).toBe(true));
  });

  it("shows empty sections and a failed section with a retry", async () => {
    let fail = true;
    fakeAll((url) =>
      path("/kpis/cost")(url)
        ? json(200, { items: [] })
        : path("/kpis/backlog")(url)
          ? json(200, { items: [] })
          : path("/work-orders/emergency-overdue")(url)
            ? json(200, { items: [], windowHours: 24 })
            : path("/assets/plan-reviews")(url)
              ? fail
                ? json(503, { code: "COM-0503", detail: "reports are down" })
                : json(200, { items: [] })
              : undefined,
    );
    renderKpis();
    expect(await screen.findByText("No completed work orders in this window")).toBeInTheDocument();
    expect(await screen.findByText("No open work orders")).toBeInTheDocument();
    expect(await screen.findByText("Nothing to regularize")).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("reports are down (COM-0503)");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No plan reviews due")).toBeInTheDocument();
  });
});
