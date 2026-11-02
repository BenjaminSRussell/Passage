import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { validateCountPlan } from "./NewCountPlanPage";
import inventory from "./module";

const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const PLAN_ID = "3d3d3d3d-0000-4000-8000-000000000011";
const SHEET_ID = "4e4e4e4e-0000-4000-8000-000000000012";
const LINE_ID = "5f5f5f5f-0000-4000-8000-000000000021";
const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body: unknown) => ({ status, body });

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const sheet = (overrides: Record<string, unknown> = {}, line: Record<string, unknown> = {}) => ({
  id: SHEET_ID,
  legalEntityId: ENTITY_ID,
  planId: PLAN_ID,
  planName: "Aisle A",
  number: "CNT-000123",
  status: "OPEN",
  blind: false,
  fourEyes: false,
  freezeStock: false,
  recountLines: 0,
  version: 3,
  lineCount: 1,
  countedCount: 1,
  createdAt: "2026-09-01T08:00:00Z",
  lines: [
    {
      id: LINE_ID,
      lineNo: 1,
      productId: PRODUCT_ID,
      dimensions: { site: "S1" },
      expectedQty: "50",
      countedQty: "46",
      postedVariance: null,
      recountRequested: false,
      recountRounds: 0,
      ...line,
    },
  ],
  ...overrides,
});

const base = { legalEntityId: ENTITY_ID, name: "Aisle A", warehouse: "", currency: "USD", unit: "EA", tolerancePct: "5", toleranceValue: "100", blind: false, freezeStock: false, fourEyes: false, locationFrom: "", locationTo: "", abcClass: "" as const, samplePct: "", minDaysSinceCount: "" };

describe("Count recount on movement and last-count age", () => {
  it("validates the age filter like the service", () => {
    expect(validateCountPlan({ ...base, minDaysSinceCount: "30" })).toEqual({});
    for (const bad of ["0", "3651", "2.5", "abc", "-1"]) {
      expect(validateCountPlan({ ...base, minDaysSinceCount: bad })).toEqual({ minDaysSinceCount: "Enter a whole number of days from 1 to 3650." });
    }
  });

  it("sends the age filter as a number", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, { id: PLAN_ID }) : json(200, { items: [] }));
    renderAt("/inventory/count-plans/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Aisle A" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "USD" } });
    fireEvent.change(screen.getByLabelText(/Not counted for/), { target: { value: "45" } });
    fireEvent.click(screen.getByRole("button", { name: "Create count plan" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === "POST")!.init.body as string)).toMatchObject({ minDaysSinceCount: 45 });
  });

  it("leaves the age filter out when it is empty", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, { id: PLAN_ID }) : json(200, { items: [] }));
    renderAt("/inventory/count-plans/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Aisle A" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "USD" } });
    fireEvent.click(screen.getByRole("button", { name: "Create count plan" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === "POST")!.init.body as string)).not.toHaveProperty("minDaysSinceCount");
  });

  it("rejects a bad age before calling the service", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/count-plans/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Aisle A" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "USD" } });
    fireEvent.change(screen.getByLabelText(/Not counted for/), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Create count plan" }));
    expect(await screen.findByText("Enter a whole number of days from 1 to 3650.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("lists the plan's age filter", async () => {
    handler = () =>
      json(200, {
        items: [{ id: PLAN_ID, legalEntityId: ENTITY_ID, name: "Aisle A", currency: "USD", unit: "EA", tolerancePct: "5", toleranceValue: "100", blind: false, freezeStock: false, fourEyes: false, minDaysSinceCount: 45, createdAt: "2026-09-01T08:00:00Z" }],
      });
    renderAt("/inventory/count-plans");
    const table = await screen.findByRole("table", { name: "Count plans" });
    expect(table).toHaveTextContent("Not counted for 45 days");
  });

  it("shows a line flagged because stock moved after it was counted", async () => {
    handler = () => json(200, sheet({ status: "OPEN", recountLines: 1 }, { recountRequested: true, recountSource: "MOVEMENT", recountReason: "Stock of this row moved after the line was counted; count it again", recountRounds: 1 }));
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    expect(await screen.findByText("Recount: stock moved after counting")).toBeInTheDocument();
    expect(screen.getByText(/Stock moved on some lines after they were counted/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Submit for review" })).toBeDisabled();
  });

  it("keeps the supervisor wording for a supervisor recount", async () => {
    handler = () => json(200, sheet({ status: "OPEN", recountLines: 1 }, { recountRequested: true, recountSource: "SUPERVISOR", recountReason: "shelf looked light", recountRounds: 1 }));
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    expect(await screen.findByText("Recount: shelf looked light")).toBeInTheDocument();
    expect(screen.queryByText(/Stock moved on some lines/)).toBeNull();
  });
});
