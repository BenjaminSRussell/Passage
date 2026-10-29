import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findAlert, findStatus, findTable } from "../../test-utils";
import type { ForecastModel, ForecastView, PlanVersion, Project, Transaction, TransferView, WbsNodeInput } from "./api/client";
import { forecastBody, type ForecastRowValues } from "./ForecastPages";
import projects from "./module";
import { nodeSummary } from "./TransferPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

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

function Where() {
  const location = useLocation();
  return <p>At {location.pathname}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {projects.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
}

const problem = (status: number, code: string, detail: string, errors?: Record<string, string>): Reply => ({
  status,
  body: { type: `https://passage.dev/problems/${code}`, title: "Error", status, code, detail, ...(errors ? { errors } : {}) },
});

const writes = () => calls.filter((c) => c.method !== "GET");

const MODEL = "0b1c2d3e-0000-4000-8000-0000000000f1";
const SIM = "0b1c2d3e-0000-4000-8000-0000000000a3";
const WORKING = "0b1c2d3e-0000-4000-8000-0000000000a1";
const BASELINE = "0b1c2d3e-0000-4000-8000-0000000000a2";

function model(overrides: Partial<ForecastModel> = {}): ForecastModel {
  return {
    id: MODEL,
    projectId: "ES-1001",
    name: "Q4 plan",
    currency: "USD",
    version: 1,
    lineCount: 2,
    totalCost: "4400.00",
    totalSales: "7200.00",
    createdBy: "pm-1",
    createdAt: "2026-10-01T09:00:00Z",
    updatedAt: "2026-10-02T09:00:00Z",
    ...overrides,
  };
}

function forecastView(version = 1): ForecastView {
  return {
    model: {
      ...model({ version }),
      lines: [
        { lineNo: 1, period: "2026-10", txnType: "HOUR", wbsCode: "1.1", quantity: "40.000", costAmount: "2000.00", salesAmount: "3600.00" },
        { lineNo: 2, period: "2026-11", txnType: "HOUR", quantity: "40.000", costAmount: "2400.00", salesAmount: "3600.00" },
      ],
    },
    comparison: [
      {
        period: "2026-10",
        txnType: "HOUR",
        forecastQuantity: "40.000",
        forecastCost: "2000.00",
        forecastSales: "3600.00",
        actualQuantity: "44.000",
        actualCost: "2200.00",
        actualSales: "3960.00",
        quantityVariance: "4.000",
        costVariance: "200.00",
        salesVariance: "360.00",
      },
      {
        period: "2026-11",
        txnType: "HOUR",
        forecastQuantity: "40.000",
        forecastCost: "2400.00",
        forecastSales: "3600.00",
        actualQuantity: "0.000",
        actualCost: "0.00",
        actualSales: "0.00",
        quantityVariance: "-40.000",
        costVariance: "-2400.00",
        salesVariance: "-3600.00",
      },
    ],
  };
}

const row = (overrides: Partial<ForecastRowValues> = {}): ForecastRowValues => ({
  key: "k",
  period: "2026-10",
  txnType: "HOUR",
  wbsCode: "",
  quantity: "8",
  costAmount: "400",
  salesAmount: "720",
  ...overrides,
});

describe("routes", () => {
  it("registers the forecast pages and the simulation transfer page", () => {
    const paths = projects.routes.map((r) => r.path);
    for (const path of ["/projects/:id/forecasts", "/projects/:id/forecasts/:modelId", "/projects/:id/plans/:planId/transfer"]) expect(paths).toContain(path);
  });
});

describe("forecastBody", () => {
  it("sends trimmed strings and leaves out an empty WBS code", () => {
    const { errors, input } = forecastBody("  Q4 plan ", [row({ wbsCode: " 1.1 " }), row({ period: "2026-11" })]);
    expect(errors).toEqual({});
    expect(input).toEqual({
      name: "Q4 plan",
      lines: [
        { period: "2026-10", txnType: "HOUR", wbsCode: "1.1", quantity: "8", costAmount: "400", salesAmount: "720" },
        { period: "2026-11", txnType: "HOUR", quantity: "8", costAmount: "400", salesAmount: "720" },
      ],
    });
  });

  it("refuses negative sales except on account, too many decimals on quantity, and repeated keys", () => {
    const { errors, input } = forecastBody("", [
      row({ salesAmount: "-10" }),
      row({ txnType: "ON_ACCOUNT", salesAmount: "-500" }),
      row({ period: "2026-12", quantity: "1.2345" }),
      row(),
      row({ period: "2026-13" }),
    ]);
    expect(input).toBeUndefined();
    expect(errors.name).toMatch(/name/);
    expect(errors["lines[0].salesAmount"]).toMatch(/on-account/);
    expect(errors["lines[1].salesAmount"]).toBeUndefined();
    expect(errors["lines[2].quantity"]).toMatch(/3 decimals/);
    expect(errors["lines[3].period"]).toMatch(/already/);
    expect(errors["lines[4].period"]).toMatch(/month/);
  });
});

describe("forecasts list", () => {
  it("lists models and opens one", async () => {
    handler = () => ({ status: 200, body: { items: [model()] } });
    const { container } = renderAt("/projects/ES-1001/forecasts");
    const table = await findTable({ name: "Forecast models" });
    expect(within(table).getByText("Q4 plan")).toBeTruthy();
    expect(within(table).getByText("4400.00 USD")).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
    handler = () => ({ status: 200, body: forecastView() });
    fireEvent.click(within(table).getByText("Q4 plan"));
    expect(await screen.findByRole("heading", { name: "Q4 plan for ES-1001" })).toBeTruthy();
  });

  it("shows the empty state, and an error with a retry", async () => {
    handler = () => problem(503, "unavailable", "Database unavailable");
    renderAt("/projects/ES-1001/forecasts");
    expect((await findAlert()).textContent).toContain("Database unavailable");
    handler = () => ({ status: 200, body: { items: [] } });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No forecast models yet")).toBeTruthy();
  });

  it("creates a model with an Idempotency-Key and goes to it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: { ...model(), lines: [] } };
      if (url.pathname.endsWith(`/forecasts/${MODEL}`)) return { status: 200, body: forecastView() };
      return { status: 200, body: { items: [] } };
    };
    renderAt("/projects/ES-1001/forecasts");
    await screen.findByText("No forecast models yet");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Q4 plan" } });
    fireEvent.change(screen.getByLabelText("Month of line 1"), { target: { value: "2026-10" } });
    fireEvent.change(screen.getByLabelText("Quantity of line 1"), { target: { value: "40" } });
    fireEvent.change(screen.getByLabelText("Cost of line 1"), { target: { value: "2000" } });
    fireEvent.change(screen.getByLabelText("Sales of line 1"), { target: { value: "3600" } });
    fireEvent.click(screen.getByRole("button", { name: "Add forecast model" }));
    expect(await screen.findByRole("heading", { name: "Q4 plan for ES-1001" })).toBeTruthy();
    const post = writes()[0];
    expect(post.url.pathname).toBe("/api/v1/projects/ES-1001/forecasts");
    expect(post.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(post.body).toEqual({ name: "Q4 plan", lines: [{ period: "2026-10", txnType: "HOUR", quantity: "40", costAmount: "2000", salesAmount: "3600" }] });
  });

  it("does not send an invalid form and shows the service's line errors on the inputs", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? problem(400, "validation", "request failed validation", { "lines[0].wbsCode": "must name a node of the project's WORKING plan" })
        : { status: 200, body: { items: [] } };
    renderAt("/projects/ES-1001/forecasts");
    await screen.findByText("No forecast models yet");
    fireEvent.click(screen.getByRole("button", { name: "Add forecast model" }));
    expect(await screen.findByText(/Enter a name/)).toBeTruthy();
    expect(writes()).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Q4" } });
    fireEvent.change(screen.getByLabelText("WBS code of line 1"), { target: { value: "9.9" } });
    fireEvent.click(screen.getByRole("button", { name: "Add forecast model" }));
    expect((await findAlert()).textContent).toContain("Line 1 wbsCode");
    expect(screen.getByLabelText("WBS code of line 1").getAttribute("aria-invalid")).toBe("true");
  });

  it("explains a project that is not visible", async () => {
    handler = () => problem(404, "not_found", "project not found");
    renderAt("/projects/NOPE/forecasts");
    expect(await screen.findByText("Project not found")).toBeTruthy();
  });
});

describe("forecast detail", () => {
  it("shows forecast vs actual with signed variances", async () => {
    handler = () => ({ status: 200, body: forecastView() });
    const { container } = renderAt(`/projects/ES-1001/forecasts/${MODEL}`);
    const table = await findTable({ name: "Forecast vs actual" });
    const rows = within(table).getAllByRole("row");
    expect(within(rows[1]).getByText("+200.00")).toBeTruthy();
    expect(within(rows[2]).getByText("-2400.00")).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("replaces the lines with If-Match and reads the comparison again", async () => {
    let version = 1;
    handler = (_url, init) => {
      if (init.method === "PUT") {
        version = 2;
        return { status: 200, body: { ...forecastView(2).model } };
      }
      return { status: 200, body: forecastView(version) };
    };
    renderAt(`/projects/ES-1001/forecasts/${MODEL}`);
    await findTable({ name: "Forecast vs actual" });
    fireEvent.click(screen.getByRole("button", { name: "Remove line 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Save forecast" }));
    expect((await findStatus()).textContent).toContain("version 2");
    const put = writes()[0];
    expect(put.method).toBe("PUT");
    expect(put.headers.get("If-Match")).toBe('"1"');
    expect((put.body as { lines: unknown[] }).lines).toEqual([{ period: "2026-10", txnType: "HOUR", wbsCode: "1.1", quantity: "40.000", costAmount: "2000.00", salesAmount: "3600.00" }]);
    expect(calls.filter((c) => c.method === "GET")).toHaveLength(2);
  });

  it("offers a reload when someone else changed the model", async () => {
    handler = (_url, init) => (init.method === "PUT" ? problem(412, "conflict_version", "stale") : { status: 200, body: forecastView() });
    renderAt(`/projects/ES-1001/forecasts/${MODEL}`);
    await findTable({ name: "Forecast vs actual" });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Q4 revised" } });
    fireEvent.click(screen.getByRole("button", { name: "Save forecast" }));
    expect((await findAlert()).textContent).toContain("Someone else changed this forecast");
    fireEvent.click(screen.getByRole("button", { name: "Reload forecast" }));
    await waitFor(() => expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("Q4 plan"));
  });

  it("explains a missing model", async () => {
    handler = () => problem(404, "not_found", "forecast model not found");
    renderAt(`/projects/ES-1001/forecasts/${MODEL}`);
    expect(await screen.findByText("Forecast model not found")).toBeTruthy();
  });
});

function plan(overrides: Partial<PlanVersion>): PlanVersion {
  return { id: WORKING, projectId: "ES-1001", kind: "WORKING", sequence: 1, locked: false, version: 3, createdAt: "2026-10-01T09:00:00Z", updatedAt: "2026-10-01T09:00:00Z", ...overrides };
}

const node = (wbsCode: string, name: string, durationDays = 2): WbsNodeInput => ({ wbsCode, parentWbsCode: "1", name, durationDays, isBillableMilestone: false });

function transferView(): TransferView {
  return {
    simulation: plan({ id: SIM, kind: "SIMULATION", sequence: 3, copiedFromVersionId: WORKING, version: 2 }),
    working: plan({ version: 5 }),
    applied: [{ wbsCode: "1.4", change: "CHANGED", working: node("1.4", "Test"), simulation: node("1.4", "Test", 4) }],
    conflicts: [{ wbsCode: "1.2", change: "CHANGED", working: node("1.2", "Design", 3), simulation: node("1.2", "Design", 5) }],
  };
}

describe("simulation transfer", () => {
  it("summarises a node side", () => {
    expect(nodeSummary(undefined)).toBe("Not in this plan");
    expect(nodeSummary({ ...node("1.1", "Kick-off", 1), effortHours: "8.00", constraintType: "SNET", constraintDate: "2026-11-02", isBillableMilestone: true })).toBe(
      "Kick-off, 1 day, 8.00 h, starts no earlier than 2026-11-02, billable milestone",
    );
  });

  it("needs a decision per conflict, then transfers with both versions and locks the simulation", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") {
        const v = transferView();
        return {
          status: 200,
          body: { ...v, simulation: { ...v.simulation, locked: true, version: 3, transferredAt: "2026-10-29T10:00:00Z" }, working: { ...v.working, version: 6 }, conflicts: [{ ...v.conflicts[0], resolution: "KEEP_WORKING" }] },
        };
      }
      return { status: 200, body: transferView() };
    };
    const { container } = renderAt(`/projects/ES-1001/plans/${SIM}/transfer`);
    const conflicts = await findTable({ name: "Conflicts" });
    expect(within(conflicts).getByText("Design, 3 days")).toBeTruthy();
    expect(within(await findTable({ name: "Changes to apply" })).getByText("Test, 4 days")).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
    const go = screen.getByRole("button", { name: "Transfer to working plan" }) as HTMLButtonElement;
    expect(go.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Decision for task 1.2"), { target: { value: "KEEP_WORKING" } });
    expect(go.disabled).toBe(false);
    fireEvent.click(go);
    expect((await findStatus()).textContent).toContain("working plan is at version 6");
    const post = writes()[0];
    expect(post.url.pathname).toBe(`/api/v1/projects/ES-1001/plans/${SIM}/transfer`);
    expect(post.headers.get("If-Match")).toBe('"2"');
    expect(post.body).toEqual({ resolutions: { "1.2": "KEEP_WORKING" }, workingVersion: 5 });
    expect(screen.queryByRole("button", { name: "Transfer to working plan" })).toBeNull();
    expect(screen.getByRole("link", { name: "Open working plan" }).getAttribute("href")).toBe(`/projects/ES-1001/plans/${WORKING}`);
  });

  it("offers a fresh comparison when the working plan moved", async () => {
    handler = (_url, init) => (init.method === "POST" ? problem(412, "conflict_version", "the WORKING plan changed since the diff was read") : { status: 200, body: transferView() });
    renderAt(`/projects/ES-1001/plans/${SIM}/transfer`);
    await findTable({ name: "Conflicts" });
    fireEvent.change(screen.getByLabelText("Decision for task 1.2"), { target: { value: "TAKE_SIMULATION" } });
    fireEvent.click(screen.getByRole("button", { name: "Transfer to working plan" }));
    expect((await findAlert()).textContent).toContain("WORKING plan changed");
    fireEvent.click(screen.getByRole("button", { name: "Compare again" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "GET")).toHaveLength(2));
    expect((screen.getByLabelText("Decision for task 1.2") as HTMLSelectElement).value).toBe("");
  });

  it("explains a version that cannot be transferred", async () => {
    handler = () => problem(422, "PROJ-PLAN-001", "only a SIMULATION plan version can be transferred into WORKING");
    renderAt(`/projects/ES-1001/plans/${WORKING}/transfer`);
    expect(await screen.findByText("This plan cannot be transferred")).toBeTruthy();
    expect(screen.getByText(/only a SIMULATION/)).toBeTruthy();
  });

  it("links an open simulation to its transfer and copies a simulation from the current baseline", async () => {
    const plans = [
      plan({}),
      plan({ id: BASELINE, kind: "BASELINE", sequence: 2, locked: true, copiedFromVersionId: WORKING }),
      plan({ id: "0b1c2d3e-0000-4000-8000-0000000000a9", kind: "BASELINE", sequence: 0, locked: true, supersededByVersionId: BASELINE }),
      plan({ id: SIM, kind: "SIMULATION", sequence: 3, locked: true, transferredAt: "2026-10-29T10:00:00Z" }),
    ];
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: plan({ id: SIM, kind: "SIMULATION", sequence: 4 }) } : { status: 200, body: { items: plans } });
    renderAt("/projects/ES-1001/plans");
    const table = await findTable({ name: "Plan versions" });
    expect(within(table).getByText("Transferred 2026-10-29")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save as baseline" })).toBeNull();
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: plan({ id: SIM, kind: "SIMULATION", sequence: 4 }) };
      if (url.pathname.endsWith("/wbs")) return { status: 200, body: { planVersion: plan({ id: SIM, kind: "SIMULATION", sequence: 4 }), nodes: [], dependencies: [] } };
      return { status: 200, body: { items: plans } };
    };
    fireEvent.click(screen.getByRole("button", { name: "New simulation from baseline" }));
    const link = await screen.findByRole("link", { name: "Transfer to working plan" });
    expect(link.getAttribute("href")).toBe(`/projects/ES-1001/plans/${SIM}/transfer`);
    expect(writes()[0].body).toEqual({ kind: "SIMULATION", copyFromVersionId: BASELINE });
  });
});

const project: Project = {
  id: "ES-1001",
  legalEntityId: "0b1c2d3e-0000-4000-8000-000000000101",
  projectGroupId: "ENG",
  projectType: "TIME_MATERIAL",
  currency: "USD",
  contractValue: "0.00",
  stage: "IN_PROCESS",
  startDate: "2026-09-01",
  version: 2,
  createdAt: "2026-09-01T09:00:00Z",
  updatedAt: "2026-09-01T09:00:00Z",
};

function txn(n: number): Transaction {
  return {
    id: `0b1c2d3e-0000-4000-8000-${String(n).padStart(12, "0")}`,
    projectId: "ES-1001",
    txnType: "FEE",
    categoryId: `FEE-${n}`,
    lineProperty: "CHARGEABLE",
    txnDate: "2026-09-02",
    quantity: "1.000",
    costPrice: "0.000000",
    salesPrice: "100.000000",
    currency: "USD",
    costAmount: "0.00",
    salesAmount: "100.00",
    sourceDocType: "MANUAL",
    description: `Fee ${n}`,
    createdBy: "acct-1",
    createdAt: "2026-09-02T09:00:00Z",
  };
}

describe("paged lists", () => {
  it("loads the next page of transactions from nextCursor and stops at the last page", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/transactions")) {
        return url.searchParams.get("cursor") === "c1" ? { status: 200, body: { items: [txn(2)] } } : { status: 200, body: { items: [txn(1)], nextCursor: "c1" } };
      }
      if (url.pathname.endsWith("/categories")) return { status: 200, body: { items: [] } };
      return { status: 200, body: project };
    };
    renderAt("/projects/ES-1001/transactions");
    await screen.findByText("FEE-1");
    fireEvent.click(screen.getByRole("button", { name: "Load more transactions" }));
    expect(await screen.findByText("FEE-2")).toBeTruthy();
    expect(screen.getByText("FEE-1")).toBeTruthy();
    expect(calls.find((c) => c.url.searchParams.get("cursor"))?.url.pathname).toBe("/api/v1/projects/ES-1001/transactions");
    expect(screen.queryByRole("button", { name: "Load more transactions" })).toBeNull();
  });

  it("keeps the loaded rows and shows the failure when a later page fails", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/invoice-proposals") || url.pathname === "/api/v1/projects/invoice-proposals") {
        return url.searchParams.get("cursor") ? problem(400, "validation", "cursor is invalid") : { status: 200, body: { items: [], nextCursor: "c1" } };
      }
      return { status: 200, body: project };
    };
    renderAt("/projects/ES-1001/invoice-proposals");
    fireEvent.click(await screen.findByRole("button", { name: "Load more proposals" }));
    expect((await findAlert()).textContent).toContain("Could not load more proposals");
    const cursorCall = calls.find((c) => c.url.searchParams.get("cursor"));
    expect(cursorCall?.url.searchParams.get("projectId")).toBe("ES-1001");
  });
});
